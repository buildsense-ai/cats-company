// Package server implements Cats Company message-related API handlers.
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const structuredMentionAllBots = "all"

// MessageHandler handles message-related API requests.
type MessageHandler struct {
	db  store.Store
	hub *Hub
}

// NewMessageHandler creates a new MessageHandler.
func NewMessageHandler(db store.Store, hub *Hub) *MessageHandler {
	return &MessageHandler{db: db, hub: hub}
}

// SendMessageRequest is the JSON body for sending a message.
type SendMessageRequest struct {
	TopicID       string                 `json:"topic_id"`
	ClientMsgID   string                 `json:"client_msg_id,omitempty"`
	Content       json.RawMessage        `json:"content,omitempty"`
	ContentBlocks []types.ContentBlock   `json:"content_blocks,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	MsgType       string                 `json:"msg_type,omitempty"`
	Type          string                 `json:"type,omitempty"`
	Mode          string                 `json:"mode,omitempty"`
	Role          string                 `json:"role,omitempty"`
	ReplyTo       int                    `json:"reply_to,omitempty"`
	Mentions      []string               `json:"mentions,omitempty"`
}

type normalizedMessagePayload struct {
	StoredContent       string
	DisplayContent      interface{}
	StoredType          string
	DisplayType         string
	ExplicitDisplayType bool
	ClientMsgID         string
	ContentBlocks       []types.ContentBlock
	Metadata            map[string]interface{}
	Mode                string
	Role                string
	Mentions            []string
}

type savedMessageResult struct {
	ID        int64
	Duplicate bool
}

// HandleSendMessage handles POST /api/messages/send
func (h *MessageHandler) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.TopicID = strings.TrimSpace(req.TopicID)

	payload, err := normalizeMessageRequest(&req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if h.hub != nil {
		if code, text := h.hub.validateMessagePublish(uid, h.accountTypeForUID(uid), req.TopicID, true); code != 0 {
			writeJSON(w, code, map[string]string{"error": text})
			return
		}
		payload.Metadata = h.hub.canonicalizeArtifactMessageMetadata(r.Context(), uid, req.TopicID, payload.Metadata)
	} else {
		payload.Metadata = metadataWithoutArtifactContext(payload.Metadata)
	}

	if isTransientRuntimePayload(payload) {
		h.fanoutMessage(uid, req.TopicID, req.ReplyTo, payload, 0)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id":       0,
			"seq_id":   0,
			"topic_id": req.TopicID,
			"from_uid": uid,
			"msg_type": payload.StoredType,
			"type":     payload.DisplayType,
			"metadata": payload.Metadata,
		})
		return
	}

	if isTaskStatusPayload(payload) {
		if !canPublishTaskStatus(h.accountTypeForUID(uid)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "task_status requires a bot or service account"})
			return
		}
		status, err := h.handleTaskStatus(uid, req.TopicID, payload)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id":          0,
			"seq_id":      0,
			"topic_id":    req.TopicID,
			"from_uid":    uid,
			"msg_type":    payload.StoredType,
			"type":        payload.DisplayType,
			"metadata":    payload.Metadata,
			"task_status": status,
		})
		return
	}

	if !isGroupTopic(req.TopicID) {
		// Ensure p2p topic exists before saving.
		h.db.CreateTopic(req.TopicID, "p2p", uid)
	}

	result, err := saveNormalizedMessage(h.db, req.TopicID, uid, req.ReplyTo, payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to send"})
		return
	}

	resp := map[string]interface{}{
		"id":       result.ID,
		"seq_id":   result.ID,
		"topic_id": req.TopicID,
		"from_uid": uid,
		"msg_type": payload.StoredType,
		"type":     payload.DisplayType,
		"reply_to": req.ReplyTo,
	}
	if payload.ClientMsgID != "" {
		resp["client_msg_id"] = payload.ClientMsgID
		resp["duplicate"] = result.Duplicate
	}
	if responseMetadata := artifactMetadataForRecipient(payload.Metadata, uid); responseMetadata != nil {
		resp["metadata"] = responseMetadata
	}
	if len(payload.ContentBlocks) > 0 {
		resp["content_blocks"] = payload.ContentBlocks
		resp["mode"] = payload.Mode
		resp["role"] = payload.Role
	}
	if payload.DisplayContent != nil && payload.DisplayContent != "" {
		resp["content"] = payload.DisplayContent
	}

	if !result.Duplicate {
		h.fanoutMessage(uid, req.TopicID, req.ReplyTo, payload, result.ID)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *MessageHandler) accountTypeForUID(uid int64) types.AccountType {
	if h == nil || h.db == nil {
		return types.AccountHuman
	}
	user, err := h.db.GetUser(uid)
	if err != nil || user == nil || user.AccountType == "" {
		return types.AccountHuman
	}
	return user.AccountType
}

func (h *MessageHandler) fanoutMessage(uid int64, topicID string, replyTo int, payload *normalizedMessagePayload, msgID int64) {
	if h == nil || h.hub == nil {
		return
	}
	h.hub.fanoutNormalizedMessage(uid, topicID, replyTo, payload, msgID, nil)
}

func saveNormalizedMessage(db store.MessageStore, topicID string, uid int64, replyTo int, payload *normalizedMessagePayload) (*savedMessageResult, error) {
	if metadataStore, ok := db.(store.MessageMetadataStore); ok {
		id, duplicate, err := metadataStore.SaveMessageWithMetadata(topicID, uid, payload.StoredContent, payload.ContentBlocks, payload.Mode, payload.Role, payload.StoredType, int64(replyTo), payload.ClientMsgID, payload.Metadata)
		if err != nil {
			return nil, err
		}
		return &savedMessageResult{ID: id, Duplicate: duplicate}, nil
	}
	if _, hasArtifactContext := payload.Metadata[artifactContextMetadataKey]; hasArtifactContext {
		return nil, errors.New("message store does not support metadata persistence")
	}

	if payload.ClientMsgID != "" {
		id, duplicate, err := db.SaveMessageIdempotent(topicID, uid, payload.StoredContent, payload.ContentBlocks, payload.Mode, payload.Role, payload.StoredType, int64(replyTo), payload.ClientMsgID)
		if err != nil {
			return nil, err
		}
		return &savedMessageResult{ID: id, Duplicate: duplicate}, nil
	}

	var (
		id  int64
		err error
	)
	if len(payload.ContentBlocks) > 0 {
		mode := payload.Mode
		if mode == "" {
			mode = "code"
		}
		id, err = db.SaveMessageWithBlocks(topicID, uid, payload.StoredContent, payload.ContentBlocks, mode, payload.Role, payload.StoredType)
	} else if replyTo > 0 {
		id, err = db.SaveMessageWithReply(topicID, uid, payload.StoredContent, payload.StoredType, int64(replyTo))
	} else {
		id, err = db.SaveMessage(topicID, uid, payload.StoredContent, payload.StoredType)
	}
	if err != nil {
		return nil, err
	}
	return &savedMessageResult{ID: id}, nil
}

func (h *Hub) fanoutNormalizedMessage(uid int64, topicID string, replyTo int, payload *normalizedMessagePayload, msgID int64, exclude *Client) {
	if h == nil || payload == nil {
		return
	}
	dataMsg := h.messageForRecipient(uid, 0, topicID, replyTo, payload, msgID)
	if dataMsg == nil {
		return
	}
	if h.isTaskStatusPublisher(uid) {
		h.agentPush.observeWorkingMessage(uid, dataMsg)
	}

	if isGroupTopic(topicID) {
		groupID := extractGroupID(topicID)
		if groupID == 0 {
			return
		}
		mentions := payload.Mentions
		trustedChannelTrigger := trustedChannelBindingDeliveryMetadata(payload.Metadata) && metadataBool(payload.Metadata, "channel_native_group_triggered")
		dataMsg.Data.Mentions = mentions
		if !h.isChannelManagedGroup(groupID) {
			h.SendToUserExcept(uid, h.messageForRecipient(uid, uid, topicID, replyTo, payload, msgID), exclude)
		}
		h.broadcastToGroupWithMentions(groupID, dataMsg, uid, mentions, uid, trustedChannelTrigger)
		h.forwardChannelGroupBotReply(uid, topicID, payload, msgID)
		return
	}

	peerUID := extractPeerUID(topicID, uid)
	if peerUID == 0 {
		return
	}
	if !channelMetadataHasSource(payload.Metadata) {
		h.clearChannelInboundReplyRoute(topicID, uid, peerUID)
	}

	h.SendToUserExcept(uid, h.messageForRecipient(uid, uid, topicID, replyTo, payload, msgID), exclude)
	peerMessage := h.messageForRecipient(uid, peerUID, topicID, replyTo, payload, msgID)
	h.SendToUser(peerUID, peerMessage)
	if shouldNotifyOfflineForMessage(peerMessage) {
		h.notifyOfflineUserForMessage(peerUID, uid, peerMessage, h.isTaskStatusPublisher(uid))
	}
	h.forwardChannelBotReply(uid, peerUID, topicID, payload, msgID)

	if senderClient := h.getClient(uid); senderClient != nil && senderClient.accountType == types.AccountBot {
		h.botStats.RecordSent(uid, topicID)
	}
	if peerClient := h.getClient(peerUID); peerClient != nil && peerClient.accountType == types.AccountBot {
		h.botStats.RecordRecv(peerUID)
	}
}

func (h *Hub) fanoutNormalizedGroupMessageToHumans(uid int64, topicID string, payload *normalizedMessagePayload, msgID int64) {
	if h == nil || payload == nil || !isGroupTopic(topicID) {
		return
	}
	groupID := extractGroupID(topicID)
	if groupID == 0 {
		return
	}
	if h.isChannelManagedGroup(groupID) {
		return
	}
	msg := h.messageForRecipient(uid, 0, topicID, 0, payload, msgID)
	if msg == nil {
		return
	}
	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		log.Printf("fanout group record-only message: failed to get members for group %d: %v", groupID, err)
		return
	}
	for _, member := range members {
		if member == nil || member.UserID == uid {
			continue
		}
		isBot, err := h.db.IsUserBot(member.UserID)
		if err != nil {
			log.Printf("fanout group record-only message: failed to classify member %d: %v", member.UserID, err)
			continue
		}
		if isBot {
			continue
		}
		h.SendToUser(member.UserID, h.messageForRecipient(uid, member.UserID, topicID, 0, payload, msgID))
	}
}

func (h *Hub) messageForRecipient(uid int64, recipientUID int64, topicID string, replyTo int, payload *normalizedMessagePayload, msgID int64) *ServerMessage {
	if payload == nil {
		return nil
	}
	sourceMetadata := payload.Metadata
	suppressPushNotification := channelMetadataHasSource(sourceMetadata)
	nativeChannelGroup := firstMetadataInt64(sourceMetadata, "channel_native_group_binding_id") > 0
	publicMetadata := withoutInternalChannelBindingDeliveryMetadata(payload.Metadata)
	if recipientUID > 0 {
		publicMetadata = artifactMetadataForRecipient(publicMetadata, recipientUID)
	}
	metadata := withCatscoIdentityMetadata(publicMetadata, h.buildCatscoIdentityMetadata(uid, recipientUID, topicID, msgID, normalizeContentText(payload.DisplayContent), catscoIdentityMetadataOptions{OmitDeviceAccess: nativeChannelGroup, SourceMetadata: sourceMetadata}))
	if !nativeChannelGroup {
		metadata = withXiaobaRuntimeMetadata(metadata, h.buildXiaobaRuntimeMetadata(uid, recipientUID, topicID))
	}
	return &ServerMessage{
		Data: &MsgServerData{
			Topic:         topicID,
			From:          formatUID(uid),
			SeqID:         int(msgID),
			Content:       payload.DisplayContent,
			Type:          payload.DisplayType,
			MsgType:       payload.StoredType,
			Metadata:      metadata,
			ContentBlocks: payload.ContentBlocks,
			Mode:          payload.Mode,
			Role:          payload.Role,
			ReplyTo:       replyTo,
		},
		suppressPushNotification: suppressPushNotification,
	}
}

func (h *Hub) historyMessageDataForRecipient(recipientUID int64, message *types.Message, identityUsers ...map[int64]*types.User) *MsgServerData {
	if message == nil {
		return nil
	}
	var users map[int64]*types.User
	if len(identityUsers) > 0 {
		users = identityUsers[0]
	}
	displayContent := decodeStoredContent(message.Content)
	storedMetadata := artifactMetadataForRecipient(message.Metadata, recipientUID)
	return &MsgServerData{
		Topic:         message.TopicID,
		From:          formatUID(message.FromUID),
		SeqID:         int(message.ID),
		Content:       displayContent,
		Type:          inferDisplayTypeFromStoredMessage(message.MsgType, message.Content, message.ContentBlocks),
		MsgType:       message.MsgType,
		Metadata:      withCatscoIdentityMetadata(storedMetadata, h.buildCatscoIdentityMetadata(message.FromUID, recipientUID, message.TopicID, message.ID, normalizeContentText(displayContent), catscoIdentityMetadataOptions{OmitDeviceAccess: true, Replay: true, IdentityUsers: users})),
		ContentBlocks: message.ContentBlocks,
		Mode:          message.Mode,
		Role:          message.Role,
	}
}

func (h *Hub) historyAPIMessageForRecipient(recipientUID int64, message *types.Message, identityUsers ...map[int64]*types.User) map[string]interface{} {
	if message == nil {
		return nil
	}
	data := h.historyMessageDataForRecipient(recipientUID, message, identityUsers...)
	if data == nil {
		return nil
	}
	out := map[string]interface{}{
		"id":         message.ID,
		"seq_id":     message.ID,
		"topic_id":   message.TopicID,
		"from_uid":   message.FromUID,
		"from":       data.From,
		"content":    data.Content,
		"type":       data.Type,
		"msg_type":   data.MsgType,
		"metadata":   data.Metadata,
		"created_at": message.CreatedAt,
	}
	if len(data.ContentBlocks) > 0 {
		out["content_blocks"] = data.ContentBlocks
	}
	if data.Mode != "" {
		out["mode"] = data.Mode
	}
	if data.Role != "" {
		out["role"] = data.Role
	}
	return out
}

func withCatscoIdentityMetadata(metadata map[string]interface{}, identity map[string]interface{}) map[string]interface{} {
	if metadata == nil && identity == nil {
		return nil
	}
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		next[key] = value
	}
	if identity != nil {
		next["catsco_identity"] = identity
	}
	return next
}

func withXiaobaRuntimeMetadata(metadata map[string]interface{}, runtime map[string]interface{}) map[string]interface{} {
	if runtime == nil {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		next[key] = value
	}
	next["xiaoba_runtime"] = runtime
	return next
}

func (h *Hub) buildXiaobaRuntimeMetadata(actorUID int64, recipientUID int64, topicID string) map[string]interface{} {
	if h == nil || h.userDevices == nil || recipientUID <= 0 || !h.isBotUser(recipientUID) {
		return nil
	}
	users := h.xiaobaRuntimeRelevantHumanUsers(actorUID, recipientUID, topicID)
	if len(users) == 0 {
		log.Printf("[xiaoba_runtime] no relevant human users: topic=%s actor=%s recipient=%s", topicID, formatUID(actorUID), formatUID(recipientUID))
		return nil
	}
	devices := make([]map[string]interface{}, 0, len(users))
	for _, user := range users {
		if user == nil || user.AccountType == types.AccountBot || user.AccountType == types.AccountService {
			continue
		}
		device, ok := h.xiaobaRuntimeReadyDevice(user.ID)
		if !ok {
			log.Printf("[xiaoba_runtime] no ready device: topic=%s actor=%s recipient=%s user=%s username=%s display=%s", topicID, formatUID(actorUID), formatUID(recipientUID), formatUID(user.ID), user.Username, user.DisplayName)
			continue
		}
		userName := firstNonEmpty(user.DisplayName, user.Username, formatUID(user.ID))
		label := firstNonEmpty(device.DisplayName, userName+" 的电脑", formatUID(user.ID)+" 的电脑")
		log.Printf("[xiaoba_runtime] ready device: topic=%s actor=%s recipient=%s user=%s username=%s label=%q device_id=%s body_id=%s install_id=%s os=%s active=%t route_connected=%t routable=%t", topicID, formatUID(actorUID), formatUID(recipientUID), formatUID(user.ID), userName, label, device.DeviceID, device.BodyID, device.InstallationID, normalizeDeviceOS(device.OS), device.Active, device.RouteConnected, device.Routable)
		devices = append(devices, map[string]interface{}{
			"userId":   formatUID(user.ID),
			"userName": userName,
			"deviceId": device.DeviceID,
			"label":    label,
			"os":       normalizeDeviceOS(device.OS),
		})
	}
	if len(devices) == 0 {
		log.Printf("[xiaoba_runtime] no devices injected: topic=%s actor=%s recipient=%s relevant_users=%d", topicID, formatUID(actorUID), formatUID(recipientUID), len(users))
		return nil
	}
	log.Printf("[xiaoba_runtime] inject devices: topic=%s actor=%s recipient=%s count=%d devices=%#v", topicID, formatUID(actorUID), formatUID(recipientUID), len(devices), devices)
	return map[string]interface{}{
		"schema":  "xiaoba.runtime.v1",
		"devices": devices,
	}
}

func (h *Hub) xiaobaRuntimeRelevantHumanUsers(actorUID int64, recipientUID int64, topicID string) []*types.User {
	if h == nil || h.db == nil {
		return nil
	}
	seen := make(map[int64]bool)
	addUser := func(uid int64, out *[]*types.User) {
		if uid <= 0 || uid == recipientUID || seen[uid] {
			return
		}
		seen[uid] = true
		user, err := h.db.GetUser(uid)
		if err != nil || user == nil {
			return
		}
		if user.AccountType == types.AccountBot || user.AccountType == types.AccountService {
			return
		}
		*out = append(*out, user)
	}

	users := make([]*types.User, 0, 4)
	if isGroupTopic(topicID) {
		groupID := extractGroupID(topicID)
		if groupID > 0 {
			members, err := h.db.GetGroupMembers(groupID)
			if err == nil {
				for _, member := range members {
					if member == nil || member.UserID <= 0 || member.UserID == recipientUID || seen[member.UserID] || member.IsBot {
						continue
					}
					seen[member.UserID] = true
					users = append(users, &types.User{
						ID:          member.UserID,
						Username:    member.Username,
						DisplayName: member.DisplayName,
						AccountType: types.AccountHuman,
					})
				}
			}
		}
		return users
	}

	addUser(actorUID, &users)
	peerUID := extractPeerUID(topicID, actorUID)
	addUser(peerUID, &users)
	return users
}

func (h *Hub) xiaobaRuntimeReadyDevice(ownerUID int64) (UserDevice, bool) {
	if h == nil || ownerUID <= 0 {
		return UserDevice{}, false
	}
	routable, _ := h.userDeviceRouteCandidates(ownerUID)
	if len(routable) == 0 {
		return UserDevice{}, false
	}
	return routable[0], true
}

func (h *Hub) isBotUser(uid int64) bool {
	if h == nil || uid <= 0 {
		return false
	}
	if client := h.getClient(uid); client != nil && client.accountType == types.AccountBot {
		return true
	}
	if h.db != nil {
		user, err := h.db.GetUser(uid)
		return err == nil && user != nil && user.AccountType == types.AccountBot
	}
	return false
}

// isTaskStatusPublisher identifies automated accounts whose visible messages
// are coordinated with task status before sending a background push. Keep this
// narrower than isBotUser: group routing still treats only bot accounts as
// agents, while service accounts are explicitly allowed to publish task state.
func (h *Hub) isTaskStatusPublisher(uid int64) bool {
	if h == nil || uid <= 0 {
		return false
	}
	if client := h.getClient(uid); client != nil && canPublishTaskStatus(client.accountType) {
		return true
	}
	if h.db != nil {
		user, err := h.db.GetUser(uid)
		return err == nil && user != nil && canPublishTaskStatus(user.AccountType)
	}
	return false
}

type catscoIdentityMetadataOptions struct {
	OmitDeviceAccess bool
	Replay           bool
	SourceMetadata   map[string]interface{}
	IdentityUsers    map[int64]*types.User
}

func (h *Hub) buildCatscoIdentityMetadata(actorUID int64, recipientUID int64, topicID string, msgID int64, messageText string, options ...catscoIdentityMetadataOptions) map[string]interface{} {
	opts := catscoIdentityMetadataOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	topicType := topicTypeForID(topicID)
	identity := map[string]interface{}{
		"schema_version": 1,
		"actor": map[string]interface{}{
			"user_id": formatUID(actorUID),
		},
		"topic": map[string]interface{}{
			"topic_id": topicID,
			"type":     topicType,
		},
		"permissions": map[string]interface{}{
			"source": "server_canonical_message",
		},
	}
	if opts.Replay {
		permissions := identity["permissions"].(map[string]interface{})
		permissions["replay"] = true
		permissions["device_access"] = "non_executable_history"
	}
	if msgID > 0 {
		identity["topic"].(map[string]interface{})["channel_seq"] = msgID
	}
	if opts.IdentityUsers != nil {
		if actor := opts.IdentityUsers[actorUID]; actor != nil {
			actorMap := identity["actor"].(map[string]interface{})
			accountType := actor.AccountType
			if accountType == "" {
				accountType = types.AccountHuman
			}
			actorMap["account_type"] = accountType
			actorMap["is_bot"] = accountType == types.AccountBot
			if actor.DisplayName != "" {
				actorMap["display_name"] = actor.DisplayName
			}
			if actor.Username != "" {
				actorMap["username"] = actor.Username
			}
		}
	} else if h != nil && h.db != nil {
		if actor, err := h.db.GetUser(actorUID); err == nil && actor != nil {
			actorMap := identity["actor"].(map[string]interface{})
			accountType := actor.AccountType
			if accountType == "" {
				accountType = types.AccountHuman
			}
			actorMap["account_type"] = accountType
			actorMap["is_bot"] = accountType == types.AccountBot
			if actor.DisplayName != "" {
				actorMap["display_name"] = actor.DisplayName
			}
			if actor.Username != "" {
				actorMap["username"] = actor.Username
			}
		}
	}
	if recipientUID <= 0 {
		return identity
	}
	agent := map[string]interface{}{
		"agent_id": formatUID(recipientUID),
	}
	if opts.IdentityUsers != nil {
		if user := opts.IdentityUsers[recipientUID]; user != nil {
			if user.DisplayName != "" {
				agent["display_name"] = user.DisplayName
			}
			if user.Username != "" {
				agent["username"] = user.Username
			}
		}
	} else if h != nil && h.db != nil {
		if user, err := h.db.GetUser(recipientUID); err == nil && user != nil {
			if user.DisplayName != "" {
				agent["display_name"] = user.DisplayName
			}
			if user.Username != "" {
				agent["username"] = user.Username
			}
		}
	}
	if client := h.getClient(recipientUID); client != nil && client.accountType == types.AccountBot {
		if client.bodyID != "" {
			agent["body_id"] = client.bodyID
		}
		if client.displayName != "" {
			agent["display_name"] = client.displayName
		}
		if !opts.OmitDeviceAccess && h != nil && h.userDevices != nil {
			deviceOwnerUID, deviceOwnerSource := h.deviceAccessOwnerUID(actorUID, recipientUID, opts.SourceMetadata)
			if deviceOwnerUID > 0 {
				permissions := identity["permissions"].(map[string]interface{})
				permissions["device_owner_user_id"] = formatUID(deviceOwnerUID)
				permissions["device_owner_source"] = deviceOwnerSource
			}
			routableDevices, unavailableDevices := h.userDeviceRouteCandidates(deviceOwnerUID)
			deviceContext := h.userDevices.turnContextForOwnerDevices(actorUID, deviceOwnerUID, topicID, topicType, recipientUID, client.bodyID, messageText, routableDevices, unavailableDevices)
			if len(deviceContext.Grants) > 0 {
				identity["device_grants"] = deviceContext.Grants
			}
			if deviceContext.Selection != nil {
				identity["device_selection"] = deviceContext.Selection
			}
		}
	}
	identity["agent"] = agent
	return identity
}

func (h *Hub) deviceAccessOwnerUID(actorUID, agentUID int64, sourceMetadata ...map[string]interface{}) (int64, string) {
	if h == nil || actorUID <= 0 || agentUID <= 0 {
		return actorUID, "actor"
	}
	if h.db != nil {
		if bindings, ok := h.db.(store.ChannelAgentBindingStore); ok {
			var metadata map[string]interface{}
			if len(sourceMetadata) > 0 {
				metadata = sourceMetadata[0]
			}
			if query, scoped := channelAgentBindingQueryFromMessageMetadata(metadata, actorUID, agentUID); scoped {
				binding, err := bindings.ResolveChannelAgentBinding(query)
				if err != nil || binding == nil {
					return 0, "channel_binding_not_found"
				}
				if err := validateDeliverableChannelBinding(h.db, binding); err != nil {
					return 0, "channel_identity_unapproved"
				}
				if binding.CanonicalUID > 0 {
					if binding.CanonicalUID == actorUID {
						return actorUID, "actor"
					}
					return binding.CanonicalUID, "channel_identity_link"
				}
				return 0, "channel_identity_unlinked"
			}
			if channelBindingMetadataHintsPresent(metadata) || h.actorLooksLikeChannelIdentity(actorUID) {
				if existing, err := bindings.ResolveChannelAgentBindingForActorAny(actorUID, agentUID); err == nil && existing != nil {
					return 0, "channel_device_context_missing"
				}
			}
		}
	}
	return actorUID, "actor"
}

func (h *Hub) actorLooksLikeChannelIdentity(actorUID int64) bool {
	if h == nil || h.db == nil || actorUID <= 0 {
		return false
	}
	user, err := h.db.GetUser(actorUID)
	if err != nil || user == nil {
		return false
	}
	username := strings.TrimSpace(user.Username)
	return strings.HasPrefix(username, "ch_weixin_") || strings.HasPrefix(username, "ch_weixin_clawbot_") || strings.HasPrefix(username, "ch_feishu_")
}

func channelAgentBindingQueryFromMessageMetadata(metadata map[string]interface{}, actorUID, agentUID int64) (types.ChannelAgentBindingQuery, bool) {
	if !trustedChannelBindingDeliveryMetadata(metadata) {
		return types.ChannelAgentBindingQuery{}, false
	}
	return channelAgentBindingQueryFromMetadata(metadata, actorUID, agentUID)
}

func channelAgentBindingQueryFromInboundMetadata(metadata map[string]interface{}, actorUID, agentUID int64) (types.ChannelAgentBindingQuery, bool) {
	return channelAgentBindingQueryFromMetadata(metadata, actorUID, agentUID)
}

func channelAgentBindingQueryFromMetadata(metadata map[string]interface{}, actorUID, agentUID int64) (types.ChannelAgentBindingQuery, bool) {
	channel := normalizeChannel(firstMetadataString(metadata, "source_channel", "channel"))
	channelUserID := firstMetadataString(metadata, "channel_user_id")
	if channel == "" || channelUserID == "" {
		return types.ChannelAgentBindingQuery{}, false
	}
	bindingActorUID := firstMetadataInt64(metadata, "channel_actor_uid")
	if bindingActorUID <= 0 {
		bindingActorUID = actorUID
	}
	return types.ChannelAgentBindingQuery{
		Channel:                 channel,
		ChannelAppID:            firstMetadataString(metadata, "channel_app_id"),
		ChannelUserID:           channelUserID,
		ChannelConversationID:   firstMetadataString(metadata, "channel_conversation_id"),
		ChannelConversationType: normalizeConversationType(firstMetadataString(metadata, "channel_conversation_type")),
		AgentUID:                agentUID,
		ActorUID:                bindingActorUID,
	}, true
}

func trustedChannelBindingDeliveryMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	_, ok := metadata[channelBindingDeliveryTrustMetadataKey].(channelBindingDeliveryTrustToken)
	return ok
}

func channelBindingMetadataHintsPresent(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	if normalizeChannel(firstMetadataString(metadata, "source_channel", "channel")) != "" {
		return true
	}
	if firstMetadataString(metadata, "channel_app_id", "channel_user_id", "channel_conversation_id", "channel_conversation_type") != "" {
		return true
	}
	if firstMetadataInt64(metadata, "channel_agent_binding_id", "channel_actor_uid", "channel_canonical_uid") > 0 {
		return true
	}
	if _, ok := metadata["channel_device_access_enabled"]; ok {
		return true
	}
	return false
}

func withoutInternalChannelBindingDeliveryMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	if _, ok := metadata[channelBindingDeliveryTrustMetadataKey]; !ok {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata)-1)
	for key, value := range metadata {
		if key == channelBindingDeliveryTrustMetadataKey {
			continue
		}
		next[key] = value
	}
	return next
}

func topicTypeForID(topicID string) string {
	if isGroupTopic(topicID) {
		return "group"
	}
	return "p2p"
}

const (
	defaultAgentContextHistoryLimit = 100
	maxAgentContextHistoryLimit     = 200
)

type latestMessagesBeforeStore interface {
	GetLatestMessagesBefore(topicID string, beforeID int64, limit int) ([]*types.Message, error)
}

type usersByIDsStore interface {
	GetUsersByIDs(ids []int64) (map[int64]*types.User, error)
}

// HandleGetMessages handles GET /api/messages?topic_id=xxx&limit=50&offset=0
func (h *MessageHandler) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	topicID := r.URL.Query().Get("topic_id")
	if topicID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic_id required"})
		return
	}
	if aroundID, aroundLimit, present := parseAroundRequest(r); present {
		if aroundID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "around_id must be positive and limit must be between 1 and 100"})
			return
		}
		h.getMessagesAround(w, r, uid, topicID, aroundID, aroundLimit)
		return
	}
	if h.hub != nil {
		if code, text := h.hub.validateTopicReadAccess(uid, h.accountTypeForUID(uid), topicID); code != 0 {
			writeJSON(w, code, map[string]string{"error": text})
			return
		}
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	latest := r.URL.Query().Get("latest") == "1" || r.URL.Query().Get("latest") == "true"
	agentContext := r.URL.Query().Get("agent_context") == "1" || r.URL.Query().Get("agent_context") == "true"
	beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before_id"), 10, 64)

	if agentContext {
		if h.hub == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent context history unavailable"})
			return
		}
		if h.accountTypeForUID(uid) != types.AccountBot {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "agent context history requires bot credentials"})
			return
		}
		if limit <= 0 {
			limit = defaultAgentContextHistoryLimit
		}
		if limit > maxAgentContextHistoryLimit {
			limit = maxAgentContextHistoryLimit
		}
		rawMsgs, hasMore, nextBeforeID, err := h.loadAgentContextHistory(topicID, beforeID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load agent context history"})
			return
		}
		identityUsers := h.loadHistoryUsers(uid, rawMsgs)
		msgs := make([]map[string]interface{}, 0, len(rawMsgs))
		for _, message := range rawMsgs {
			if formatted := h.hub.agentContextHistoryAPIMessageForRecipient(uid, message, identityUsers); formatted != nil {
				msgs = append(msgs, formatted)
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"messages":       msgs,
			"topic_id":       topicID,
			"agent_uid":      uid,
			"has_more":       hasMore,
			"next_before_id": nextBeforeID,
		})
		return
	}

	if limit <= 0 {
		limit = 50
	}

	var rawMsgs []*types.Message
	var err error
	if latest {
		queryLimit := limit + 1
		if beforeID > 0 {
			if storeWithCursor, ok := h.db.(latestMessagesBeforeStore); ok {
				rawMsgs, err = storeWithCursor.GetLatestMessagesBefore(topicID, beforeID, queryLimit)
			} else {
				rawMsgs, err = h.db.GetLatestMessages(topicID, queryLimit, offset)
			}
		} else {
			rawMsgs, err = h.db.GetLatestMessages(topicID, queryLimit, offset)
		}
	} else {
		rawMsgs, err = h.db.GetMessages(topicID, limit, offset)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load messages"})
		return
	}
	hasMore := latest && len(rawMsgs) > limit
	if hasMore {
		rawMsgs = rawMsgs[len(rawMsgs)-limit:]
	}
	nextBeforeID := int64(0)
	if latest && len(rawMsgs) > 0 {
		nextBeforeID = rawMsgs[0].ID
	}

	msgs := make([]map[string]interface{}, 0, len(rawMsgs))
	if h.hub != nil {
		identityUsers := h.loadHistoryUsers(uid, rawMsgs)
		for _, message := range rawMsgs {
			if formatted := h.hub.historyAPIMessageForRecipient(uid, message, identityUsers); formatted != nil {
				msgs = append(msgs, formatted)
			}
		}
	} else {
		for _, message := range rawMsgs {
			msgs = append(msgs, map[string]interface{}{
				"id":         message.ID,
				"seq_id":     message.ID,
				"topic_id":   message.TopicID,
				"from_uid":   message.FromUID,
				"content":    decodeStoredContent(message.Content),
				"type":       inferDisplayTypeFromStoredMessage(message.MsgType, message.Content, message.ContentBlocks),
				"msg_type":   message.MsgType,
				"created_at": message.CreatedAt,
			})
		}
	}

	response := map[string]interface{}{"messages": msgs}
	if latest {
		response["has_more"] = hasMore
		response["next_before_id"] = nextBeforeID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *MessageHandler) loadAgentContextHistory(topicID string, beforeID int64, limit int) ([]*types.Message, bool, int64, error) {
	queryLimit := limit + 1
	var (
		messages []*types.Message
		err      error
	)
	if beforeID > 0 {
		if storeWithCursor, ok := h.db.(latestMessagesBeforeStore); ok {
			messages, err = storeWithCursor.GetLatestMessagesBefore(topicID, beforeID, queryLimit)
		} else {
			return nil, false, 0, errors.New("message store does not support stable before cursor")
		}
	} else {
		messages, err = h.db.GetLatestMessages(topicID, queryLimit, 0)
	}
	if err != nil {
		return nil, false, 0, err
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[len(messages)-limit:]
	}
	nextBeforeID := int64(0)
	if len(messages) > 0 {
		nextBeforeID = messages[0].ID
	}
	return messages, hasMore, nextBeforeID, nil
}

func (h *MessageHandler) loadHistoryUsers(recipientUID int64, messages []*types.Message) map[int64]*types.User {
	uids := map[int64]struct{}{recipientUID: {}}
	for _, message := range messages {
		if message != nil && message.FromUID > 0 {
			uids[message.FromUID] = struct{}{}
		}
	}

	ids := make([]int64, 0, len(uids))
	for uid := range uids {
		if uid > 0 {
			ids = append(ids, uid)
		}
	}
	if batchStore, ok := h.db.(usersByIDsStore); ok {
		if users, err := batchStore.GetUsersByIDs(ids); err == nil {
			return users
		}
	}

	users := make(map[int64]*types.User, len(ids))
	for _, uid := range ids {
		if user, err := h.db.GetUser(uid); err == nil && user != nil {
			users[uid] = user
		}
	}
	return users
}

func (h *Hub) agentContextHistoryAPIMessageForRecipient(agentUID int64, message *types.Message, identityUsers map[int64]*types.User) map[string]interface{} {
	formatted := h.historyAPIMessageForRecipient(agentUID, message, identityUsers)
	if formatted == nil || message == nil {
		return nil
	}

	displayType, _ := formatted["type"].(string)
	role := "user"
	eligible := isDurableAgentContextMessage(message, displayType)
	reason := "participant_message"
	if message.FromUID == agentUID {
		role = "assistant"
		reason = "current_agent_message"
	} else if sender := identityUsers[message.FromUID]; sender == nil {
		eligible = false
		reason = "sender_classification_failed"
	} else if sender.AccountType == types.AccountBot {
		role = "other_agent"
		eligible = false
		reason = "other_agent_message"
	}

	mentions := structuredMentionsFromMessage(formatted)
	if eligible && isGroupTopic(message.TopicID) && role == "user" && len(mentions) > 0 {
		agentID := formatUID(agentUID)
		if containsString(mentions, structuredMentionAllBots) {
			reason = "group_message_targets_all_agents"
		} else if !containsString(mentions, agentID) {
			eligible = false
			reason = "group_message_targets_another_member"
		} else {
			reason = "group_message_targets_agent"
		}
	}

	formatted["agent_uid"] = agentUID
	formatted["agent_id"] = formatUID(agentUID)
	formatted["context_role"] = role
	formatted["context_eligible"] = eligible
	formatted["context_reason"] = reason
	if len(mentions) > 0 {
		formatted["mentions"] = mentions
	}
	return formatted
}

func isDurableAgentContextMessage(message *types.Message, displayType string) bool {
	if message == nil {
		return false
	}
	switch displayType {
	case "text", "image", "voice", "file":
	default:
		return false
	}
	if isInternalAgentWorkingMessage(displayType, decodeStoredContent(message.Content), message.ContentBlocks) {
		return false
	}
	for _, block := range message.ContentBlocks {
		if isInternalAgentContentBlock(block.Type) {
			return false
		}
	}
	return true
}

func structuredMentionsFromMessage(message map[string]interface{}) []string {
	if message == nil {
		return nil
	}
	values := make([]string, 0)
	switch blocks := message["content_blocks"].(type) {
	case []types.ContentBlock:
		for _, block := range blocks {
			values = append(values, mentionValues(block.Payload["mentions"])...)
		}
	case []interface{}:
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]interface{})
			payload, _ := block["payload"].(map[string]interface{})
			values = append(values, mentionValues(payload["mentions"])...)
		}
	}
	if len(values) == 0 {
		metadata, _ := message["metadata"].(map[string]interface{})
		values = append(values, mentionValues(metadata["mentions"])...)
	}
	return normalizeStructuredMentions(values)
}

func mentionValues(raw interface{}) []string {
	switch values := raw.(type) {
	case []string:
		return values
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func withStructuredMentionBlocks(blocks []types.ContentBlock, displayContent interface{}, mentions []string) []types.ContentBlock {
	if len(mentions) == 0 {
		return blocks
	}
	result := append([]types.ContentBlock(nil), blocks...)
	for index := range result {
		if result[index].Type != "text" {
			continue
		}
		payload := make(map[string]interface{}, len(result[index].Payload)+1)
		for key, value := range result[index].Payload {
			payload[key] = value
		}
		payload["mentions"] = append([]string(nil), mentions...)
		result[index].Payload = payload
		return result
	}
	text := normalizeContentText(displayContent)
	if text == "" {
		return result
	}
	return append([]types.ContentBlock{{
		Type: "text",
		Text: text,
		Payload: map[string]interface{}{
			"mentions": append([]string(nil), mentions...),
		},
	}}, result...)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeMessageRequest(req *SendMessageRequest) (*normalizedMessagePayload, error) {
	if req == nil || strings.TrimSpace(req.TopicID) == "" {
		return nil, errors.New("topic_id required")
	}

	storedContent, displayContent := normalizeRawContent(req.Content)
	explicitDisplayType := stripMessageNullBytes(firstNonEmpty(strings.TrimSpace(req.Type), strings.TrimSpace(req.MsgType)))
	displayType := explicitDisplayType
	if displayType == "" {
		displayType = inferDisplayTypeFromContent(displayContent)
	}
	if displayType == "" {
		displayType = "text"
	}

	blocks := sanitizeContentBlocks(req.ContentBlocks)
	metadata := sanitizeMessageMap(req.Metadata)
	mentions := normalizeStructuredMentions(req.Mentions)
	if len(mentions) > 0 {
		if metadata == nil {
			metadata = make(map[string]interface{})
		}
		metadata["mentions"] = mentions
	}
	mode := stripMessageNullBytes(strings.TrimSpace(req.Mode))
	role := stripMessageNullBytes(strings.TrimSpace(req.Role))
	if len(blocks) == 0 && isStructuredDisplayType(displayType) {
		blocks = buildStructuredContentBlocks(displayType, displayContent, metadata)
		if mode == "" {
			mode = "code"
		}
		if role == "" {
			role = "assistant"
		}
	}
	blocks = withStructuredMentionBlocks(blocks, displayContent, mentions)

	if storedContent == "" && len(blocks) == 0 {
		return nil, errors.New("topic_id and content/content_blocks required")
	}

	return &normalizedMessagePayload{
		StoredContent:       storedContent,
		DisplayContent:      displayContent,
		StoredType:          normalizeStoredMsgType(displayType),
		DisplayType:         displayType,
		ExplicitDisplayType: explicitDisplayType != "",
		ClientMsgID:         normalizeClientMsgID(req.ClientMsgID, metadata),
		ContentBlocks:       blocks,
		Metadata:            metadata,
		Mode:                mode,
		Role:                role,
		Mentions:            mentions,
	}, nil
}

func isCanonicalMentionTarget(value string) bool {
	if value == structuredMentionAllBots {
		return true
	}
	if !strings.HasPrefix(value, "usr") || len(value) == len("usr") {
		return false
	}
	for _, char := range value[len("usr"):] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func normalizeStructuredMentions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	mentions := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		uid := strings.TrimSpace(stripMessageNullBytes(value))
		if !isCanonicalMentionTarget(uid) {
			continue
		}
		if _, exists := seen[uid]; exists {
			continue
		}
		seen[uid] = struct{}{}
		mentions = append(mentions, uid)
	}
	return mentions
}

func normalizeClientMsgID(raw string, metadata map[string]interface{}) string {
	value := strings.TrimSpace(stripMessageNullBytes(raw))
	if value == "" {
		value = firstMetadataString(metadata, "client_msg_id", "clientMessageId", "client_message_id")
	}
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}

func normalizeRawContent(raw json.RawMessage) (string, interface{}) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", ""
	}

	var parsed interface{}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		switch value := parsed.(type) {
		case string:
			sanitized := stripMessageNullBytes(value)
			return sanitized, sanitized
		case nil:
			return "", ""
		default:
			return stripMessageNullBytes(trimmed), sanitizeMessageValue(value)
		}
	}

	sanitized := stripMessageNullBytes(trimmed)
	return sanitized, sanitized
}

func decodeStoredContent(content string) interface{} {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		return parsed
	}

	return content
}

func inferDisplayTypeFromStoredMessage(msgType, content string, blocks []types.ContentBlock) string {
	if msgType != "" && msgType != "text" {
		return msgType
	}
	if inferred := inferDisplayTypeFromContent(decodeStoredContent(content)); inferred != "" {
		return inferred
	}
	if len(blocks) > 0 {
		return "text"
	}
	return "text"
}

func inferDisplayTypeFromContent(content interface{}) string {
	if rich, ok := content.(map[string]interface{}); ok {
		if value, ok := rich["type"].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func normalizeStoredMsgType(displayType string) string {
	switch displayType {
	case "image", "voice", "file":
		return displayType
	default:
		return "text"
	}
}

func isTransientRuntimePayload(payload *normalizedMessagePayload) bool {
	if payload == nil {
		return false
	}
	return payload.DisplayType == "runtime_plan"
}

func isStructuredDisplayType(displayType string) bool {
	switch displayType {
	case "thinking", "tool_use", "tool_result":
		return true
	default:
		return false
	}
}

func buildStructuredContentBlocks(displayType string, content interface{}, metadata map[string]interface{}) []types.ContentBlock {
	text := normalizeContentText(content)
	switch displayType {
	case "thinking":
		return []types.ContentBlock{{Type: "thinking", Thinking: text}}
	case "tool_use":
		return []types.ContentBlock{{
			Type:  "tool_use",
			ID:    firstMetadataString(metadata, "id", "tool_call_id", "tool_use_id"),
			Name:  text,
			Input: metadataMap(metadata, "input"),
		}}
	case "tool_result":
		return []types.ContentBlock{{
			Type:      "tool_result",
			ToolUseID: firstMetadataString(metadata, "tool_use_id", "id", "tool_call_id"),
			Content:   text,
			IsError:   metadataBool(metadata, "is_error"),
		}}
	default:
		return nil
	}
}

func normalizeContentText(content interface{}) string {
	switch value := content.(type) {
	case string:
		return stripMessageNullBytes(value)
	case map[string]interface{}:
		if text, ok := value["text"].(string); ok {
			return stripMessageNullBytes(text)
		}
		bytes, _ := json.Marshal(value)
		return stripMessageNullBytes(string(bytes))
	default:
		bytes, _ := json.Marshal(value)
		return stripMessageNullBytes(string(bytes))
	}
}

func stripMessageNullBytes(value string) string {
	if strings.IndexByte(value, 0) < 0 {
		return value
	}
	return strings.ReplaceAll(value, "\x00", "")
}

func sanitizeMessageValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case string:
		return stripMessageNullBytes(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = sanitizeMessageValue(item)
		}
		return out
	case map[string]interface{}:
		return sanitizeMessageMap(typed)
	default:
		return value
	}
}

func sanitizeMessageMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[stripMessageNullBytes(key)] = sanitizeMessageValue(value)
	}
	return out
}

func sanitizeContentBlocks(blocks []types.ContentBlock) []types.ContentBlock {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]types.ContentBlock, len(blocks))
	for i, block := range blocks {
		out[i] = block
		out[i].Type = stripMessageNullBytes(block.Type)
		out[i].Text = stripMessageNullBytes(block.Text)
		out[i].Thinking = stripMessageNullBytes(block.Thinking)
		out[i].Payload = sanitizeMessageMap(block.Payload)
		out[i].ID = stripMessageNullBytes(block.ID)
		out[i].Name = stripMessageNullBytes(block.Name)
		out[i].Input = sanitizeMessageMap(block.Input)
		out[i].ToolUseID = stripMessageNullBytes(block.ToolUseID)
		out[i].Content = stripMessageNullBytes(block.Content)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstMetadataString(metadata map[string]interface{}, keys ...string) string {
	if metadata == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstMetadataInt64(metadata map[string]interface{}, keys ...string) int64 {
	if metadata == nil {
		return 0
	}
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case int64:
			if typed > 0 {
				return typed
			}
		case int:
			if typed > 0 {
				return int64(typed)
			}
		case int32:
			if typed > 0 {
				return int64(typed)
			}
		case float64:
			if typed > 0 {
				return int64(typed)
			}
		case json.Number:
			parsed, err := typed.Int64()
			if err == nil && parsed > 0 {
				return parsed
			}
		case string:
			parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
			if err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func metadataMap(metadata map[string]interface{}, key string) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	if value, ok := metadata[key].(map[string]interface{}); ok {
		return value
	}
	return nil
}

func metadataBool(metadata map[string]interface{}, key string) bool {
	if metadata == nil {
		return false
	}
	if value, ok := metadata[key].(bool); ok {
		return value
	}
	return false
}

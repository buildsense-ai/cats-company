// Package server implements Cats Company message-related API handlers.
package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
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
	TopicID                string                 `json:"topic_id"`
	ClientMsgID            string                 `json:"client_msg_id,omitempty"`
	Content                json.RawMessage        `json:"content,omitempty"`
	ContentBlocks          []types.ContentBlock   `json:"content_blocks,omitempty"`
	Metadata               map[string]interface{} `json:"metadata,omitempty"`
	MsgType                string                 `json:"msg_type,omitempty"`
	Type                   string                 `json:"type,omitempty"`
	Mode                   string                 `json:"mode,omitempty"`
	Role                   string                 `json:"role,omitempty"`
	ReplyTo                int                    `json:"reply_to,omitempty"`
	Mentions               []string               `json:"mentions,omitempty"`
	fileAnnotationsTrusted bool
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
	ArtifactContextRef  *artifactContextDeliveryRef
	ArtifactTaskRef     *artifactTaskDeliveryRef
	// FileAnnotationsRef carries the validated canonical file_annotations
	// document through fanout; the trusted boundary marker is never durable.
	FileAnnotationsRef *fileAnnotationsDocument
	Mode               string
	Role               string
	Mentions           []string
}

type savedMessageResult struct {
	ID        int64
	Duplicate bool
}

// HandleSendMessage handles POST /api/messages/send
func (h *MessageHandler) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	h.sendMessage(w, r, req, nil)
}

// sendMessage is the ordinary HTTP ingestion service. A bound Artifact submit
// supplies only a final lifecycle gate; normalization, access, metadata,
// persistence, idempotency and recipient fanout remain the same implementation.
// The optional gate holds its release lock only across the persistence boundary.
func (h *MessageHandler) sendMessage(w http.ResponseWriter, r *http.Request, req SendMessageRequest, beforeSave func() (func(), bool)) {
	uid := UIDFromContext(r.Context())
	req.TopicID = strings.TrimSpace(req.TopicID)
	if code, text := sessionForkPublishError(h.db, req.TopicID); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return
	}
	if hasFileAnnotationsMetadata(req.Metadata) && !req.fileAnnotationsTrusted {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_annotations require the dedicated annotation endpoint"})
		return
	}

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
		candidateMetadata := payload.Metadata
		payload.Metadata, payload.ArtifactContextRef = h.hub.extractArtifactContextDelivery(uid, req.TopicID, candidateMetadata)
		_, payload.ArtifactTaskRef, err = h.hub.extractArtifactTaskDelivery(uid, req.TopicID, payload.ClientMsgID, candidateMetadata)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errArtifactTaskDeliveryPending) {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		sawGatewayAnnotationsIngress := hasGatewayAnnotationsMetadata(payload.Metadata)
		payload.Metadata, err = h.hub.validateGatewayAnnotationsMetadata(uid, req.TopicID, payload.Metadata)
		if err != nil {
			// A rejected annotated message must never strand a task delivery
			// lease reserved by the earlier extraction step.
			if payload.ArtifactTaskRef != nil {
				h.hub.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		payload.Metadata, payload.FileAnnotationsRef, err = h.hub.validateFileAnnotationsMetadata(uid, req.TopicID, payload.Metadata, req.fileAnnotationsTrusted)
		if err != nil {
			if payload.ArtifactTaskRef != nil {
				h.hub.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if payload.FileAnnotationsRef != nil && (isTransientRuntimePayload(payload) || isTaskStatusPayload(payload)) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_annotations require a persisted visible message"})
			return
		}
		if sawGatewayAnnotationsIngress && (isTransientRuntimePayload(payload) || isTaskStatusPayload(payload)) {
			if payload.ArtifactTaskRef != nil {
				h.hub.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "gateway_annotations require a persisted visible message",
			})
			return
		}
	} else {
		payload.Metadata = metadataWithoutArtifactContext(payload.Metadata)
		payload.Metadata = metadataWithoutGatewayAnnotationContext(payload.Metadata)
		payload.Metadata = metadataWithoutGatewayAnnotations(payload.Metadata)
		payload.Metadata = metadataWithoutFileAnnotations(payload.Metadata)
	}
	if payload.ArtifactTaskRef != nil && (isTransientRuntimePayload(payload) || isTaskStatusPayload(payload)) {
		h.hub.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Artifact task references require a persisted visible message",
		})
		return
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
	if payload.ArtifactTaskRef != nil && !payload.ArtifactTaskRef.AlreadyDelivered &&
		!h.hub.IsOnline(payload.ArtifactTaskRef.AgentUID) {
		h.hub.artifactTasks.failDelivery(
			payload.ArtifactTaskRef,
			"agent_offline",
			"Artifact task Agent is not connected",
		)
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Artifact task Agent is offline",
			"code":  "artifact_task_agent_offline",
		})
		return
	}

	var release func()
	if beforeSave != nil {
		var valid bool
		release, valid = beforeSave()
		if !valid {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "artifact_open_binding_invalid", "code": "artifact_open_binding_invalid"})
			return
		}
	}
	if !isGroupTopic(req.TopicID) {
		// Ensure p2p topic exists before saving.
		h.db.CreateTopic(req.TopicID, "p2p", uid)
	}

	result, err := saveNormalizedMessage(h.db, req.TopicID, uid, req.ReplyTo, payload)
	if release != nil {
		release()
	}
	if err != nil {
		if payload.ArtifactTaskRef != nil {
			h.hub.artifactTasks.releaseDelivery(payload.ArtifactTaskRef)
		}
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
	if responseMetadata := metadataWithoutArtifactContext(payload.Metadata); responseMetadata != nil {
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

	if payload.ArtifactTaskRef != nil {
		if failure := h.hub.deliverPersistedArtifactTaskMessage(
			uid, req.TopicID, req.ReplyTo, payload, result, nil,
		); failure != nil {
			writeJSON(w, failure.HTTPStatus, map[string]string{
				"error": failure.PublicMessage,
				"code":  failure.PublicCode,
			})
			return
		}
	} else if !result.Duplicate {
		h.fanoutMessage(uid, req.TopicID, req.ReplyTo, payload, result.ID)
	}
	writeJSON(w, http.StatusOK, resp)
}

type artifactTaskMessageDeliveryFailure struct {
	HTTPStatus    int
	PublicCode    string
	PublicMessage string
}

// deliverPersistedArtifactTaskMessage owns the post-save delivery decision for
// HTTP and WebSocket ingress. A recovered lease may replay the same persisted
// message; the Agent deduplicates the stable Artifact task ref before running.
func (h *Hub) deliverPersistedArtifactTaskMessage(
	uid int64,
	topicID string,
	replyTo int,
	payload *normalizedMessagePayload,
	result *savedMessageResult,
	exclude *Client,
) *artifactTaskMessageDeliveryFailure {
	if h == nil || payload == nil || payload.ArtifactTaskRef == nil || result == nil {
		return &artifactTaskMessageDeliveryFailure{
			HTTPStatus: http.StatusInternalServerError, PublicCode: "artifact_task_delivery_failed",
			PublicMessage: "Artifact task turn delivery failed",
		}
	}
	delivery := payload.ArtifactTaskRef
	if result.Duplicate {
		if delivery.AlreadyDelivered {
			return nil
		}
		if !delivery.Recovered {
			h.artifactTasks.failDelivery(
				delivery,
				"turn_delivery_conflict",
				"Artifact task message identity was already used without a recoverable delivery claim",
			)
			return &artifactTaskMessageDeliveryFailure{
				HTTPStatus: http.StatusConflict, PublicCode: "artifact_task_delivery_conflict",
				PublicMessage: "Artifact task delivery could not be reconciled",
			}
		}
	}
	deliveryPayload := payload
	if result.Duplicate && delivery.Recovered {
		persisted, err := h.persistedArtifactTaskReplayPayload(uid, topicID, result.ID, delivery)
		if err != nil {
			return &artifactTaskMessageDeliveryFailure{
				HTTPStatus: http.StatusServiceUnavailable, PublicCode: "artifact_task_delivery_recovery_failed",
				PublicMessage: "Artifact task delivery could not be recovered",
			}
		}
		deliveryPayload = persisted
	}
	if h.fanoutNormalizedMessage(uid, topicID, replyTo, deliveryPayload, result.ID, exclude) {
		return nil
	}
	h.artifactTasks.failDelivery(
		delivery,
		"turn_delivery_failed",
		"Artifact task turn could not be delivered to the online Agent",
	)
	return &artifactTaskMessageDeliveryFailure{
		HTTPStatus: http.StatusServiceUnavailable, PublicCode: "artifact_task_delivery_failed",
		PublicMessage: "Artifact task turn delivery failed",
	}
}

func (h *Hub) persistedArtifactTaskReplayPayload(
	uid int64,
	topicID string,
	messageID int64,
	delivery *artifactTaskDeliveryRef,
) (*normalizedMessagePayload, error) {
	if h == nil || h.db == nil || uid <= 0 || topicID == "" || messageID <= 0 || delivery == nil {
		return nil, errors.New("persisted Artifact task message is unavailable")
	}
	messages, err := h.db.GetMessagesSince(topicID, messageID-1, 1)
	if err != nil {
		return nil, err
	}
	var message *types.Message
	for _, candidate := range messages {
		if candidate != nil && candidate.ID == messageID && candidate.TopicID == topicID && candidate.FromUID == uid {
			message = candidate
			break
		}
	}
	if message == nil {
		return nil, errors.New("persisted Artifact task message was not found")
	}
	metadata := metadataWithoutGatewayAnnotationContext(metadataWithoutArtifactContext(message.Metadata))
	mentions := structuredMentionsFromMessage(map[string]interface{}{
		"content_blocks": message.ContentBlocks,
		"metadata":       metadata,
	})
	return &normalizedMessagePayload{
		StoredContent:   message.Content,
		DisplayContent:  decodeStoredContent(message.Content),
		StoredType:      message.MsgType,
		DisplayType:     inferDisplayTypeFromStoredMessage(message.MsgType, message.Content, message.ContentBlocks),
		ClientMsgID:     delivery.ClientMessageID,
		ContentBlocks:   message.ContentBlocks,
		Metadata:        metadata,
		Mode:            message.Mode,
		Role:            message.Role,
		Mentions:        mentions,
		ArtifactTaskRef: delivery,
	}, nil
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

func (h *MessageHandler) fanoutMessage(uid int64, topicID string, replyTo int, payload *normalizedMessagePayload, msgID int64) bool {
	if h == nil || h.hub == nil {
		return false
	}
	return h.hub.fanoutNormalizedMessage(uid, topicID, replyTo, payload, msgID, nil)
}

func saveNormalizedMessage(db store.MessageStore, topicID string, uid int64, replyTo int, payload *normalizedMessagePayload) (*savedMessageResult, error) {
	// Artifact context is a short-lived delivery capability, never durable
	// message metadata. Scrub again at the persistence boundary so alternate
	// ingestion paths and legacy clients cannot reintroduce it.
	payload.Metadata = metadataWithoutArtifactContext(payload.Metadata)
	// The device directory is rebuilt per recipient on read. Persisting a
	// client copy would replay a forged rollout signal to later readers.
	payload.Metadata = metadataWithoutXiaobaRuntime(payload.Metadata)
	if metadataStore, ok := db.(store.MessageMetadataStore); ok {
		id, duplicate, err := metadataStore.SaveMessageWithMetadata(topicID, uid, payload.StoredContent, payload.ContentBlocks, payload.Mode, payload.Role, payload.StoredType, int64(replyTo), payload.ClientMsgID, payload.Metadata)
		if err != nil {
			return nil, err
		}
		return &savedMessageResult{ID: id, Duplicate: duplicate}, nil
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

func (h *Hub) fanoutNormalizedMessage(uid int64, topicID string, replyTo int, payload *normalizedMessagePayload, msgID int64, exclude *Client) bool {
	if h == nil || payload == nil {
		return false
	}
	dataMsg := h.messageForRecipient(uid, 0, topicID, replyTo, payload, msgID)
	if dataMsg == nil {
		return false
	}
	if h.isTaskStatusPublisher(uid) {
		h.agentPush.resetVisibleTailAtWorkingBoundary(uid, dataMsg)
	}

	if isGroupTopic(topicID) {
		groupID := extractGroupID(topicID)
		if groupID == 0 {
			return false
		}
		mentions := payload.Mentions
		trustedChannelTrigger := trustedChannelBindingDeliveryMetadata(payload.Metadata) && metadataBool(payload.Metadata, "channel_native_group_triggered")
		dataMsg.Data.Mentions = mentions
		if !h.isChannelManagedGroup(groupID) {
			h.SendToUserExcept(uid, h.messageForRecipient(uid, uid, topicID, replyTo, payload, msgID), exclude)
		}
		// Recipient 0 is only a group-routing template. Preserve the reserved
		// delivery candidate for per-Agent validation inside the broadcaster.
		dataMsg.artifactTaskRef = payload.ArtifactTaskRef
		taskDelivered := h.broadcastToGroupWithMentions(groupID, dataMsg, uid, mentions, uid, trustedChannelTrigger)
		h.forwardChannelGroupBotReply(uid, topicID, payload, msgID)
		return taskDelivered
	}

	peerUID := extractPeerUID(topicID, uid)
	if peerUID == 0 {
		return false
	}
	if !channelMetadataHasSource(payload.Metadata) {
		h.clearChannelInboundReplyRoute(topicID, uid, peerUID)
	}

	h.SendToUserExcept(uid, h.messageForRecipient(uid, uid, topicID, replyTo, payload, msgID), exclude)
	peerMessage := h.messageForRecipient(uid, peerUID, topicID, replyTo, payload, msgID)
	taskDelivered := false
	if payload.ArtifactTaskRef != nil && payload.ArtifactTaskRef.AgentUID == peerUID {
		// A task candidate that lost validation must never degrade into an
		// ordinary visible Agent turn.
		if peerMessage == nil || peerMessage.artifactTaskRef == nil {
			return false
		}
		if peerMessage.artifactTaskRef.AlreadyDelivered {
			taskDelivered = true
		} else if h.sendToUserExceptConfirmed(peerUID, peerMessage, nil) > 0 {
			taskDelivered = h.artifactTasks.confirmDelivery(peerMessage.artifactTaskRef)
		}
	} else {
		h.SendToUser(peerUID, peerMessage)
	}
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
	return taskDelivered
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
	publicMetadata := metadataWithoutXiaobaRuntime(metadataWithoutArtifactContext(withoutInternalChannelBindingDeliveryMetadata(payload.Metadata)))
	explicitTargetsEnabled := xiaobaRuntimeV2EnabledForClient(h.getClient(recipientUID))
	metadata := withCatscoIdentityMetadata(publicMetadata, h.buildCatscoIdentityMetadata(uid, recipientUID, topicID, msgID, normalizeContentText(payload.DisplayContent), catscoIdentityMetadataOptions{OmitDeviceAccess: nativeChannelGroup, SourceMetadata: sourceMetadata, ExplicitTargetsEnabled: &explicitTargetsEnabled}))
	if !nativeChannelGroup {
		metadata = withXiaobaRuntimeMetadata(metadata, h.buildXiaobaRuntimeMetadataWithExplicitTargets(uid, recipientUID, topicID, explicitTargetsEnabled, sourceMetadata))
	}
	metadata = withArtifactContextDeliveryRef(
		metadata,
		h.validatedArtifactContextDeliveryRef(uid, topicID, payload.ArtifactContextRef, recipientUID),
		recipientUID,
	)
	validatedTaskDelivery := h.validatedArtifactTaskDeliveryRef(
		uid,
		topicID,
		payload.ArtifactTaskRef,
		recipientUID,
	)
	metadata = withArtifactTaskDeliveryRef(
		metadata,
		validatedTaskDelivery,
		recipientUID,
	)
	metadata = withGatewayAnnotationAgentContext(
		metadata,
		h.gatewayAnnotationAgentContext(uid, recipientUID, topicID, payload.Metadata),
		recipientUID,
	)
	metadata = h.fileAnnotationsMetadataForRecipient(uid, recipientUID, topicID, metadata)
	// Agent-side readability: the target Agent's fanout copy carries the
	// validated annotations through both real consumer channels (blocks for
	// the live parse merge, content for the history/cloud reader) while
	// human copies and persistence keep the payload untouched.
	contentBlocks := payload.ContentBlocks
	dataContent := payload.DisplayContent
	if modelText := h.gatewayAnnotationModelTextForPayload(uid, recipientUID, topicID, payload); modelText != "" {
		dataContent, contentBlocks = withGatewayAnnotationAgentDelivery(contentBlocks, payload.DisplayContent, modelText, payload.DisplayType, payload.StoredType)
	}
	if modelText := h.fileAnnotationModelText(uid, recipientUID, topicID, payload.Metadata); modelText != "" {
		dataContent, contentBlocks = withGatewayAnnotationAgentDelivery(contentBlocks, dataContent, modelText, payload.DisplayType, payload.StoredType)
	}
	metadata = withSkillConnectorMetadata(metadata, h.buildShimoSkillConnectorMetadata(uid, recipientUID, topicID, msgID))
	return &ServerMessage{
		Data: &MsgServerData{
			Topic:         topicID,
			From:          formatUID(uid),
			SeqID:         int(msgID),
			Content:       dataContent,
			Type:          payload.DisplayType,
			MsgType:       payload.StoredType,
			Metadata:      metadata,
			ContentBlocks: contentBlocks,
			Mode:          payload.Mode,
			Role:          payload.Role,
			ReplyTo:       replyTo,
		},
		suppressPushNotification: suppressPushNotification,
		artifactContextRef:       payload.ArtifactContextRef,
		artifactTaskRef:          validatedTaskDelivery,
	}
}

// DeliverShimoLoginResume sends a trusted, transient continuation prompt only
// to the original virtual employee. It is not saved and therefore never
// appears as a synthetic user message in conversation history.
func (h *Hub) DeliverShimoLoginResume(resume ShimoLoginResume) bool {
	if h == nil || resume.AgentUID <= 0 || resume.ActorUID <= 0 || resume.MessageID <= 0 || !h.shimoResumeAllowed(resume) || !h.isBotUser(resume.AgentUID) {
		return false
	}
	clients := h.getClients(resume.AgentUID)
	hasRuntime := false
	for _, client := range clients {
		if client != nil && client.deviceConnector == nil && client.accountType == types.AccountBot {
			hasRuntime = true
			break
		}
	}
	if !hasRuntime {
		return false
	}
	payload := &normalizedMessagePayload{
		StoredContent:  "石墨账号已连接成功。请继续执行刚才因等待石墨登录而中断的任务；先检查连接状态，再读取原任务中的石墨链接。",
		DisplayContent: "石墨账号已连接成功。请继续执行刚才因等待石墨登录而中断的任务；先检查连接状态，再读取原任务中的石墨链接。",
		StoredType:     "text",
		DisplayType:    "text",
		Metadata: map[string]interface{}{
			"catsco_transient":          true,
			"catsco_skill_login_resume": true,
			"catsco_event":              "provider_connection_ready",
			"provider":                  "shimo",
		},
	}
	message := h.messageForRecipient(resume.ActorUID, resume.AgentUID, resume.TopicID, 0, payload, resume.MessageID)
	if message == nil || message.Data == nil {
		return false
	}
	message.Data.SeqID = 0
	// Group chats only let a virtual employee act on a structured @mention, and a
	// resume prompt is exactly "this employee was addressed". Without the mention
	// the client drops the delivery as an unrelated group message. One-to-one
	// chats have no such gate, so they keep the plain envelope.
	if isGroupTopic(resume.TopicID) {
		message.Data.Mentions = []string{formatUID(resume.AgentUID)}
	}
	h.SendToUser(resume.AgentUID, message)
	return true
}

// DeliverServiceBotPrompt 把「另一个 bot 想请你处理这件事」的请求推给目标 bot。
//
// 这是 bot-as-tool 的投递半边：平台已经把消息落库（调用方可见、bot 侧有历史），
// 这里只负责让在线的 bot 立刻收到。返回 false = 本节点上没有它的连接，
// 调用方据此如实失败（而不是让 run 卡到一个超时）。
//
// 只有平台内部的 runner 会调它：权限（是不是自己的 bot / 有没有开放分享）
// 已经在嵌入之前判过，这里不再重复鉴权。
func (h *Hub) DeliverServiceBotPrompt(callerUID, botUID int64, topicID, text string, msgID int64) bool {
	if h == nil || callerUID <= 0 || botUID <= 0 || msgID <= 0 {
		return false
	}
	if strings.TrimSpace(topicID) == "" || strings.TrimSpace(text) == "" {
		return false
	}
	if len(h.getClients(botUID)) == 0 {
		return false
	}
	payload := &normalizedMessagePayload{
		StoredContent:  text,
		DisplayContent: text,
		StoredType:     "text",
		DisplayType:    "text",
		Metadata: map[string]interface{}{
			// 让 bot 侧能分辨「这是另一个 Agent 通过工具调用发来的」，
			// 从而按「有人派活」而不是「主人闲聊」来处理。
			"catsco_bot_call":   true,
			"catsco_caller_uid": callerUID,
		},
	}
	message := h.messageForRecipient(callerUID, botUID, topicID, 0, payload, msgID)
	if message == nil || message.Data == nil {
		return false
	}
	message.Data.SeqID = int(msgID)
	h.SendToUser(botUID, message)
	return true
}

// shimoResumeAllowed authorizes a trusted Shimo login resume for the topic it
// names. A one-to-one topic has to be the conversation between exactly that
// person and that virtual employee. A group topic has to still contain both of
// them, so somebody who was removed from the group, or a virtual employee that
// is no longer in it, cannot have work resumed there.
func (h *Hub) shimoResumeAllowed(resume ShimoLoginResume) bool {
	if isGroupTopic(resume.TopicID) {
		groupID := extractGroupID(resume.TopicID)
		if groupID <= 0 || h.db == nil {
			return false
		}
		actorIsMember, err := h.db.IsGroupMember(groupID, resume.ActorUID)
		if err != nil || !actorIsMember {
			return false
		}
		agentIsMember, err := h.db.IsGroupMember(groupID, resume.AgentUID)
		if err != nil || !agentIsMember {
			return false
		}
		return true
	}
	return extractPeerUID(resume.TopicID, resume.ActorUID) == resume.AgentUID
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
	// The stored annotation context key is fanout-only; a value that somehow
	// reached the store must never be replayed to readers as server output.
	// Runtime directories are live-only. Drop any persisted copy (including
	// rows written before the key was scrubbed), rather than replaying it.
	storedMetadata := metadataWithoutXiaobaRuntime(metadataWithoutFileAnnotationContext(metadataWithoutGatewayAnnotationContext(metadataWithoutArtifactContext(message.Metadata))))
	// Offline agent readers rebuild their session from history (XiaoBa
	// cloud-session-restore), which ignores metadata and prefers a non-empty
	// string content over content_blocks. The authorized target Agent's read
	// copy therefore carries the annotation context in the content itself;
	// human readers and the stored value stay untouched.
	contentBlocks := message.ContentBlocks
	readContent := displayContent
	envelopeDisplayType := inferDisplayTypeFromStoredMessage(message.MsgType, message.Content, message.ContentBlocks)
	if modelText := h.gatewayAnnotationHistoryModelText(message, recipientUID); modelText != "" {
		readContent, contentBlocks = withGatewayAnnotationAgentDelivery(message.ContentBlocks, displayContent, modelText, envelopeDisplayType, message.MsgType)
		displayContent = readContent
	}
	if modelText := h.fileAnnotationModelText(message.FromUID, recipientUID, message.TopicID, storedMetadata); modelText != "" {
		displayContent, contentBlocks = withGatewayAnnotationAgentDelivery(contentBlocks, displayContent, modelText, envelopeDisplayType, message.MsgType)
	}
	storedMetadata = h.fileAnnotationsMetadataForRecipient(message.FromUID, recipientUID, message.TopicID, storedMetadata)
	return &MsgServerData{
		Topic:         message.TopicID,
		From:          formatUID(message.FromUID),
		SeqID:         int(message.ID),
		ReplyTo:       int(message.ReplyTo),
		Content:       displayContent,
		Type:          envelopeDisplayType,
		MsgType:       message.MsgType,
		Metadata:      withCatscoIdentityMetadata(storedMetadata, h.buildCatscoIdentityMetadata(message.FromUID, recipientUID, message.TopicID, message.ID, normalizeContentText(displayContent), catscoIdentityMetadataOptions{OmitDeviceAccess: true, Replay: true, IdentityUsers: users})),
		ContentBlocks: contentBlocks,
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
	if data.ReplyTo != 0 {
		out["reply_to"] = data.ReplyTo
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
	if metadata == nil && runtime == nil {
		return nil
	}
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		if key == "xiaoba_runtime" {
			continue
		}
		next[key] = value
	}
	if runtime != nil {
		next["xiaoba_runtime"] = runtime
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

// xiaoba_runtime is server-generated routing metadata. It is removed from
// client-authored inbound messages so a sender cannot forge a rollout signal.
func metadataWithoutXiaobaRuntime(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	if _, present := metadata["xiaoba_runtime"]; !present {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key != "xiaoba_runtime" {
			next[key] = value
		}
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

func withSkillConnectorMetadata(metadata map[string]interface{}, connectors map[string]interface{}) map[string]interface{} {
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		if key == "catsco_skill_connectors" {
			continue
		}
		next[key] = value
	}
	if connectors != nil {
		next["catsco_skill_connectors"] = connectors
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

func (h *Hub) buildXiaobaRuntimeMetadata(actorUID int64, recipientUID int64, topicID string, sourceMetadata ...map[string]interface{}) map[string]interface{} {
	return h.buildXiaobaRuntimeMetadataWithExplicitTargets(actorUID, recipientUID, topicID, xiaobaRuntimeV2EnabledForBot(recipientUID), sourceMetadata...)
}

func (h *Hub) buildXiaobaRuntimeMetadataWithExplicitTargets(actorUID int64, recipientUID int64, topicID string, explicitTargetsEnabled bool, sourceMetadata ...map[string]interface{}) map[string]interface{} {
	if h == nil || h.db == nil || h.userDevices == nil || actorUID <= 0 || recipientUID <= 0 || !h.isBotUser(recipientUID) {
		return nil
	}
	// A directory is not permission to operate another group member's computer.
	// Use the same canonical owner resolution as scoped device grants.
	ownerUID, _ := h.deviceAccessOwnerUID(actorUID, recipientUID, sourceMetadata...)
	if ownerUID <= 0 {
		return nil
	}
	user, err := h.db.GetUser(ownerUID)
	if err != nil || user == nil || user.AccountType == types.AccountBot || user.AccountType == types.AccountService {
		return nil
	}
	known, _ := h.classifyUserDevices(ownerUID, humanToolTargetDevices(h.userDevices.list(ownerUID)))
	// Stable ordering makes heartbeat changes purely availability updates.
	sort.Slice(known, func(i, j int) bool { return known[i].DeviceID < known[j].DeviceID })
	devices := make([]map[string]interface{}, 0, len(known))
	userName := firstNonEmpty(user.DisplayName, user.Username, formatUID(ownerUID))
	for _, device := range known {
		status := "unavailable"
		if device.Routable {
			status = "ready"
		}
		devices = append(devices, map[string]interface{}{
			"userId":   formatUID(ownerUID),
			"userName": userName,
			"deviceId": device.DeviceID,
			"label":    firstNonEmpty(device.DisplayName, userName+" 的电脑"),
			"os":       normalizeDeviceOS(device.OS),
			"status":   status,
		})
	}
	if !explicitTargetsEnabled {
		// v1 consumers assume every entry is ready. During the compatibility
		// phase, only advertise routable devices and keep the v1 wire shape.
		ready := make([]map[string]interface{}, 0, len(devices))
		for _, device := range devices {
			if device["status"] == "ready" {
				ready = append(ready, device)
			}
		}
		return map[string]interface{}{
			"schema":  "xiaoba.runtime.v1",
			"devices": ready,
		}
	}
	return map[string]interface{}{
		"schema":      "xiaoba.runtime.v2",
		"ownerUserId": formatUID(ownerUID),
		"devices":     devices,
	}
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
	// ExplicitTargetsEnabled is supplied only by a live delivery path after
	// checking the authenticated Bot connection's handshake capability. Nil
	// retains a gate-only, fail-open fallback for focused registry tests, not
	// live delivery. TestLiveDeviceMetadataCallersRequireNegotiation prevents
	// production callers from omitting this decision (except inert replay).
	ExplicitTargetsEnabled *bool
}

func (h *Hub) buildCatscoIdentityMetadata(actorUID int64, recipientUID int64, topicID string, msgID int64, messageText string, options ...catscoIdentityMetadataOptions) map[string]interface{} {
	opts := catscoIdentityMetadataOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	explicitTargetsEnabled := xiaobaRuntimeV2EnabledForBot(recipientUID)
	if opts.ExplicitTargetsEnabled != nil {
		explicitTargetsEnabled = *opts.ExplicitTargetsEnabled
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
			knownDevices, unavailableDevices := h.classifyUserDevices(deviceOwnerUID, humanToolTargetDevices(h.userDevices.list(deviceOwnerUID)))
			deviceContext := h.userDevices.turnContextForOwnerDevicesWithExplicitTargets(actorUID, deviceOwnerUID, topicID, topicType, recipientUID, client.bodyID, routableDevices(knownDevices), unavailableDevices, explicitTargetsEnabled)
			if len(deviceContext.Grants) > 0 {
				identity["device_grants"] = deviceContext.Grants
			}
			if len(deviceContext.ExplicitGrants) > 0 {
				identity["device_target_authorizations"] = map[string]interface{}{
					"schema": "catsco.device-targets.v1",
					"grants": deviceContext.ExplicitGrants,
				}
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
		mentionNames := h.mentionNamesForAgentContext(topicID, identityUsers)
		msgs := make([]map[string]interface{}, 0, len(rawMsgs))
		for _, message := range rawMsgs {
			if formatted := h.hub.agentContextHistoryAPIMessageForRecipient(uid, message, identityUsers, mentionNames); formatted != nil {
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

func (h *Hub) agentContextHistoryAPIMessageForRecipient(agentUID int64, message *types.Message, identityUsers map[int64]*types.User, mentionNames map[string]string) map[string]interface{} {
	formatted := h.historyAPIMessageForRecipient(agentUID, message, identityUsers)
	if formatted == nil || message == nil {
		return nil
	}

	displayType, _ := formatted["type"].(string)
	role := "user"
	durable := isDurableAgentContextMessage(message, displayType)
	eligible := durable
	reason := "participant_message"
	// Whether this is a peer's runtime traffic with nothing worth reading. See
	// isInternalAgentWorkingMessage for the predicate's full semantics — it is
	// wider than "internal blocks with no readable block", because a working
	// display type short-circuits the block scan.
	//
	// What this must NOT use is `durable`: it is false for any message that
	// merely *contains* an internal block, so a peer's final answer wrapped as
	// `thinking + assistant_text` counts as not-durable. Using it here would
	// label that answer `other_agent_working`, and readers drop working traffic
	// — silently losing the peer's actual reply.
	peerWorking := isInternalAgentWorkingMessage(displayType, decodeStoredContent(message.Content), message.ContentBlocks)
	if message.FromUID == agentUID {
		role = "assistant"
		reason = "current_agent_message"
	} else if sender := identityUsers[message.FromUID]; sender == nil {
		eligible = false
		reason = "sender_classification_failed"
	} else if sender.AccountType == types.AccountBot {
		role = "other_agent"
		eligible = false
		if peerWorking {
			reason = "other_agent_working"
		} else {
			reason = "other_agent_message"
		}
	}

	// A person's client resolves @usr<id> against its own member list, so it
	// renders a name. A cloud Agent only ever sees the raw token, and peers end
	// up guessing who was addressed from their own session file names. Rewrite
	// the tokens to names on the Agent's read copy; the stored value and human
	// readers keep the raw token they already resolve themselves. The
	// structured `mentions` array stays raw so activation matching is unchanged.
	withMentionNamesForAgentCopy(formatted, mentionNames)

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

	// File/image attachments store only a server-relative /uploads path. A cloud
	// Agent rebuilds its session from this history and can only fetch by
	// absolute address, so the authorized Agent's read copy carries the public
	// origin. The stored value and human readers keep the relative path.
	if modelText := h.fileAddressModelText(message); modelText != "" {
		blocks, _ := formatted["content_blocks"].([]types.ContentBlock)
		content, nextBlocks := withGatewayAnnotationAgentDelivery(blocks, formatted["content"], modelText, displayType, message.MsgType)
		formatted["content"] = content
		if len(nextBlocks) > 0 {
			formatted["content_blocks"] = nextBlocks
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

// fileAddressModelText returns the model-facing absolute address of a file or
// image attachment so a cloud Agent can read it straight from this server with
// read_file. It returns "" for anything that is not a stored file/image
// attachment or when no public origin is configured.
func (h *Hub) fileAddressModelText(message *types.Message) string {
	if message == nil {
		return ""
	}
	rich, ok := decodeStoredContent(message.Content).(map[string]interface{})
	if !ok {
		return ""
	}
	richType := strings.TrimSpace(jsStringOf(rich["type"]))
	if richType != "file" && richType != "image" {
		return ""
	}
	payload, ok := rich["payload"].(map[string]interface{})
	if !ok {
		payload = rich
	}
	rawURL := strings.TrimSpace(jsStringOf(payload["url"]))
	if rawURL == "" {
		return ""
	}
	absolute := rawURL
	if strings.HasPrefix(rawURL, "/") {
		resolved, err := requestAbsoluteURL(rawURL)
		if err != nil {
			return ""
		}
		absolute = resolved
	}
	name := strings.TrimSpace(jsStringOf(payload["name"]))
	if name == "" {
		name = strings.TrimSpace(jsStringOf(payload["file_name"]))
	}
	label := "[附件地址]"
	if name != "" {
		label += " " + name
	}
	return label + "\n" + absolute + "\n读取方式: 如需查看该附件，调用 read_file，file_path 使用上面的地址。"
}

func isDurableAgentContextMessage(message *types.Message, displayType string) bool {
	if message == nil {
		return false
	}
	if !isDurableAgentContextDisplayType(displayType) {
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

// mentionNamesForAgentContext builds the `usr<id>` → display-name map used to
// rewrite `@` tokens on an Agent's history read copy.
//
// The member list, not the page's senders, is the right source: a member who
// has not spoken on this page is exactly who a peer cannot identify. Non-group
// topics have no member list and no `@` addressing to resolve, so they get
// nothing and the rewrite is a no-op there.
func (h *MessageHandler) mentionNamesForAgentContext(topicID string, identityUsers map[int64]*types.User) map[string]string {
	groupID := extractGroupID(topicID)
	if groupID <= 0 || h == nil || h.db == nil {
		return nil
	}
	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		// A failed lookup and an empty group are different problems: the first
		// silently degrades `@usr<id>` back to the raw token this rewrite exists
		// to remove, and without a line here there is nothing to debug with.
		log.Printf("[mention_names] group %d member lookup failed, @ tokens stay raw: %v", groupID, err)
		return nil
	}
	if len(members) == 0 {
		return nil
	}
	names := make(map[string]string, len(members)+1)
	for _, member := range members {
		if member == nil || member.UserID <= 0 {
			continue
		}
		if name := strings.TrimSpace(firstNonEmpty(member.DisplayName, member.Username)); name != "" {
			names[formatUID(member.UserID)] = name
		}
	}
	// A page sender missing from the member list (left the group, or a store
	// without joined fields) still gets a name when identityUsers has one.
	for uid, user := range identityUsers {
		if uid <= 0 || user == nil {
			continue
		}
		key := formatUID(uid)
		if _, exists := names[key]; exists {
			continue
		}
		if name := strings.TrimSpace(firstNonEmpty(user.DisplayName, user.Username)); name != "" {
			names[key] = name
		}
	}
	if len(names) == 0 {
		return nil
	}
	names[structuredMentionAllBots] = "所有人"
	return names
}

// withMentionNamesForAgentCopy rewrites raw `@usr<id>` tokens into `@<name>` on
// the Agent's read copy only.
//
// A person's client already resolves the token against its own member list, so
// the raw form never reaches a human. A cloud Agent has no member list and only
// sees the token, so peers end up identifying each other by scraping their own
// session file names — on a live group, both Agents guessed who `@usr365` was
// that way and said so in the room. The structured `mentions` array stays raw
// (activation matching compares it against `usr<id>`), and the stored value and
// human copies are untouched.
//
// `names` maps `usr<id>` (and the `all` sentinel) to a display name. It is built
// once per history page by the caller: building it per message would repeat the
// same work for every row, and a name map keyed off `identityUsers` alone would
// miss every member who has not spoken on this page — exactly the case the
// rewrite exists for.
func withMentionNamesForAgentCopy(formatted map[string]interface{}, names map[string]string) {
	if formatted == nil || len(names) == 0 {
		return
	}

	if content, ok := formatted["content"].(string); ok && content != "" {
		formatted["content"] = replaceMentionTokens(content, names)
	}
	switch blocks := formatted["content_blocks"].(type) {
	case []types.ContentBlock:
		next := make([]types.ContentBlock, len(blocks))
		copy(next, blocks)
		for index := range next {
			if next[index].Type != "text" || next[index].Text == "" {
				continue
			}
			next[index].Text = replaceMentionTokens(next[index].Text, names)
		}
		formatted["content_blocks"] = next
	case []interface{}:
		next := make([]interface{}, len(blocks))
		copy(next, blocks)
		for index, rawBlock := range next {
			block, ok := rawBlock.(map[string]interface{})
			if !ok {
				continue
			}
			text, ok := block["text"].(string)
			if !ok || text == "" {
				continue
			}
			replaced := make(map[string]interface{}, len(block))
			for key, value := range block {
				replaced[key] = value
			}
			replaced["text"] = replaceMentionTokens(text, names)
			next[index] = replaced
		}
		formatted["content_blocks"] = next
	}
}

// replaceMentionTokens swaps whole `@usr<id>` / `@all` tokens for their names.
// Matching is boundary-aware so an id that is a prefix of a longer one (usr36
// inside usr365) cannot be rewritten in its place.
func replaceMentionTokens(text string, names map[string]string) string {
	if text == "" || !strings.Contains(text, "@") {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for index := 0; index < len(text); {
		if text[index] != '@' {
			builder.WriteByte(text[index])
			index++
			continue
		}
		end := index + 1
		for end < len(text) && isMentionTokenChar(text[end]) {
			end++
		}
		token := text[index+1 : end]
		if name, ok := names[token]; ok {
			builder.WriteString("@" + name)
		} else {
			builder.WriteString(text[index:end])
		}
		index = end
	}
	return builder.String()
}

func isMentionTokenChar(char byte) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
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
	// Single ingestion choke point for HTTP, WebSocket and channel paths. The
	// device directory is server-generated and recipient-gated, so a client
	// copy must not reach persistence or fanout as if the server issued it.
	metadata = metadataWithoutXiaobaRuntime(metadata)
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

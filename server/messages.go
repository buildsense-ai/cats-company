// Package server implements Cats Company message-related API handlers.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

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
}

type normalizedMessagePayload struct {
	StoredContent  string
	DisplayContent interface{}
	StoredType     string
	DisplayType    string
	ClientMsgID    string
	ContentBlocks  []types.ContentBlock
	Metadata       map[string]interface{}
	Mode           string
	Role           string
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
	if payload.Metadata != nil {
		resp["metadata"] = payload.Metadata
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

	if isGroupTopic(topicID) {
		groupID := extractGroupID(topicID)
		if groupID == 0 {
			return
		}
		mentions := parseMentions(payload.DisplayContent)
		dataMsg.Data.Mentions = mentions
		h.SendToUserExcept(uid, dataMsg, exclude)
		h.broadcastToGroupWithMentions(groupID, dataMsg, uid, mentions, uid)
		return
	}

	peerUID := extractPeerUID(topicID, uid)
	if peerUID == 0 {
		return
	}

	h.SendToUserExcept(uid, dataMsg, exclude)
	h.SendToUser(peerUID, h.messageForRecipient(uid, peerUID, topicID, replyTo, payload, msgID))

	if senderClient := h.getClient(uid); senderClient != nil && senderClient.accountType == types.AccountBot {
		h.botStats.RecordSent(uid, topicID)
	}
	if peerClient := h.getClient(peerUID); peerClient != nil && peerClient.accountType == types.AccountBot {
		h.botStats.RecordRecv(peerUID)
	}
}

func (h *Hub) messageForRecipient(uid int64, recipientUID int64, topicID string, replyTo int, payload *normalizedMessagePayload, msgID int64) *ServerMessage {
	if payload == nil {
		return nil
	}
	metadata := withCatscoIdentityMetadata(payload.Metadata, h.buildCatscoIdentityMetadata(uid, recipientUID, topicID, msgID))
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
	}
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

func (h *Hub) buildCatscoIdentityMetadata(actorUID int64, recipientUID int64, topicID string, msgID int64) map[string]interface{} {
	identity := map[string]interface{}{
		"schema_version": 1,
		"actor": map[string]interface{}{
			"user_id": formatUID(actorUID),
		},
		"topic": map[string]interface{}{
			"topic_id": topicID,
			"type":     topicTypeForID(topicID),
		},
		"permissions": map[string]interface{}{
			"source": "server_canonical_message",
		},
	}
	if msgID > 0 {
		identity["topic"].(map[string]interface{})["channel_seq"] = msgID
	}
	if h != nil && h.db != nil {
		if actor, err := h.db.GetUser(actorUID); err == nil && actor != nil {
			actorMap := identity["actor"].(map[string]interface{})
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
	if h != nil && h.db != nil {
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
	}
	identity["agent"] = agent
	return identity
}

func topicTypeForID(topicID string) string {
	if isGroupTopic(topicID) {
		return "group"
	}
	return "p2p"
}

// HandleGetMessages handles GET /api/messages?topic_id=xxx&limit=50&offset=0
func (h *MessageHandler) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	_ = UIDFromContext(r.Context())

	topicID := r.URL.Query().Get("topic_id")
	if topicID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic_id required"})
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	latest := r.URL.Query().Get("latest") == "1" || r.URL.Query().Get("latest") == "true"

	var msgs interface{}
	var err error
	if latest {
		msgs, err = h.db.GetLatestMessages(topicID, limit, offset)
	} else {
		msgs, err = h.db.GetMessages(topicID, limit, offset)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load messages"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": msgs})
}

func normalizeMessageRequest(req *SendMessageRequest) (*normalizedMessagePayload, error) {
	if req == nil || strings.TrimSpace(req.TopicID) == "" {
		return nil, errors.New("topic_id required")
	}

	storedContent, displayContent := normalizeRawContent(req.Content)
	displayType := firstNonEmpty(strings.TrimSpace(req.Type), strings.TrimSpace(req.MsgType))
	if displayType == "" {
		displayType = inferDisplayTypeFromContent(displayContent)
	}
	if displayType == "" {
		displayType = "text"
	}

	blocks := req.ContentBlocks
	mode := strings.TrimSpace(req.Mode)
	role := strings.TrimSpace(req.Role)
	if len(blocks) == 0 && isStructuredDisplayType(displayType) {
		blocks = buildStructuredContentBlocks(displayType, displayContent, req.Metadata)
		if mode == "" {
			mode = "code"
		}
		if role == "" {
			role = "assistant"
		}
	}

	if storedContent == "" && len(blocks) == 0 {
		return nil, errors.New("topic_id and content/content_blocks required")
	}

	return &normalizedMessagePayload{
		StoredContent:  storedContent,
		DisplayContent: displayContent,
		StoredType:     normalizeStoredMsgType(displayType),
		DisplayType:    displayType,
		ClientMsgID:    normalizeClientMsgID(req.ClientMsgID, req.Metadata),
		ContentBlocks:  blocks,
		Metadata:       req.Metadata,
		Mode:           mode,
		Role:           role,
	}, nil
}

func normalizeClientMsgID(raw string, metadata map[string]interface{}) string {
	value := strings.TrimSpace(raw)
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
			return value, value
		case nil:
			return "", ""
		default:
			return trimmed, value
		}
	}

	return trimmed, trimmed
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
	return payload.DisplayType == "runtime_plan" && metadataBool(payload.Metadata, "transient")
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
		return value
	case map[string]interface{}:
		if text, ok := value["text"].(string); ok {
			return text
		}
		bytes, _ := json.Marshal(value)
		return string(bytes)
	default:
		bytes, _ := json.Marshal(value)
		return string(bytes)
	}
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

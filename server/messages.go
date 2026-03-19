// Package server implements Cats Company message-related API handlers.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/db/mysql"
	"github.com/openchat/openchat/server/store/types"
)

// MessageHandler handles message-related API requests.
type MessageHandler struct {
	db  *mysql.Adapter
	hub *Hub
}

// NewMessageHandler creates a new MessageHandler.
func NewMessageHandler(db *mysql.Adapter, hub *Hub) *MessageHandler {
	return &MessageHandler{db: db, hub: hub}
}

// SendMessageRequest is the JSON body for sending a message.
type SendMessageRequest struct {
	TopicID       string                `json:"topic_id"`
	Content       string                `json:"content,omitempty"`
	ContentBlocks []types.ContentBlock  `json:"content_blocks,omitempty"`
	MsgType       string                `json:"msg_type,omitempty"`
	Mode          string                `json:"mode,omitempty"`
	Role          string                `json:"role,omitempty"`
}

// HandleSendMessage handles POST /api/messages/send
func (h *MessageHandler) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	if (req.Content == "" && len(req.ContentBlocks) == 0) || req.TopicID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic_id and content/content_blocks required"})
		return
	}

	msgType := req.MsgType
	if msgType == "" {
		msgType = "text"
	}

	// Ensure topic exists
	h.db.CreateTopic(req.TopicID, "p2p", uid)

	var id int64
	var err error

	// Use new API if content_blocks provided
	if len(req.ContentBlocks) > 0 {
		mode := req.Mode
		if mode == "" {
			mode = "code"
		}
		id, err = h.db.SaveMessageWithBlocks(req.TopicID, uid, req.Content, req.ContentBlocks, mode, req.Role, msgType)
	} else {
		id, err = h.db.SaveMessage(req.TopicID, uid, req.Content, msgType)
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to send"})
		return
	}

	resp := map[string]interface{}{
		"id":       id,
		"topic_id": req.TopicID,
		"from_uid": uid,
		"msg_type": msgType,
	}
	if len(req.ContentBlocks) > 0 {
		resp["content_blocks"] = req.ContentBlocks
		resp["mode"] = req.Mode
		resp["role"] = req.Role
	} else {
		resp["content"] = req.Content
	}

	writeJSON(w, http.StatusOK, resp)

	// Broadcast via WebSocket to online users in this topic
	if h.hub != nil {
		dataMsg := &ServerMessage{
			Data: &MsgServerData{
				Topic:         req.TopicID,
				From:          fmt.Sprintf("usr%d", uid),
				SeqID:         int(id),
				Content:       req.Content,
				ContentBlocks: req.ContentBlocks,
				Mode:          req.Mode,
				Role:          req.Role,
			},
		}
		// For p2p topics, send to both users
		if strings.HasPrefix(req.TopicID, "p2p") {
			h.hub.SendToUser(uid, dataMsg) // sender's other devices
			peerUID := extractPeerUID(req.TopicID, uid)
			if peerUID > 0 {
				h.hub.SendToUser(peerUID, dataMsg)
			}
		}
	}
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

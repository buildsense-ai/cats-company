// Package server implements the Cats Company friends system HTTP/WebSocket handlers.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// FriendHandler handles friend-related API requests.
type FriendHandler struct {
	db store.Store
}

// NewFriendHandler creates a new FriendHandler.
func NewFriendHandler(db store.Store) *FriendHandler {
	return &FriendHandler{db: db}
}

// FriendActionRequest is the JSON body for friend actions.
type FriendActionRequest struct {
	UserID   int64  `json:"user_id"`
	AgentUID int64  `json:"agent_uid,omitempty"`
	Message  string `json:"message,omitempty"`
}

// HandleSendRequest handles POST /api/friends/request
func (h *FriendHandler) HandleSendRequest(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req FriendActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.UserID == uid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot add yourself"})
		return
	}

	// Check if already friends
	already, err := h.db.AreFriends(uid, req.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if already {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already friends"})
		return
	}

	// Check if blocked
	blocked, err := h.db.IsBlocked(req.UserID, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if blocked {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "user not found"})
		return
	}

	id, err := h.db.CreateFriendRequest(uid, req.UserID, req.Message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to send request"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "status": "pending"})
}

// HandleAcceptRequest handles POST /api/friends/accept
func (h *FriendHandler) HandleAcceptRequest(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req FriendActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	targetUID, err := h.friendActionTargetUID(uid, req.AgentUID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	if err := h.db.AcceptFriendRequest(req.UserID, targetUID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to accept"})
		return
	}
	if user, err := h.db.GetUser(targetUID); err == nil && user != nil && user.AccountType == types.AccountBot {
		if bindings, ok := h.db.(store.ChannelAgentBindingStore); ok {
			if _, err := bindings.ActivateChannelAgentBindingsForCanonicalUser(req.UserID, targetUID, uid); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to activate channel binding"})
				return
			}
			if _, err := bindings.ApproveChannelAgentAccessRequestsForActor(req.UserID, targetUID, uid); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to activate channel access"})
				return
			}
		}
	}

	// Create P2P topic for the new friends
	topicID := p2pTopicID(targetUID, req.UserID)
	// Topic creation would be handled by the topic manager
	_ = topicID

	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// HandleRejectRequest handles POST /api/friends/reject
func (h *FriendHandler) HandleRejectRequest(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req FriendActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	targetUID, err := h.friendActionTargetUID(uid, req.AgentUID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	if err := h.db.RejectFriendRequest(req.UserID, targetUID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reject"})
		return
	}
	if user, err := h.db.GetUser(targetUID); err == nil && user != nil && user.AccountType == types.AccountBot {
		if bindings, ok := h.db.(store.ChannelAgentBindingStore); ok {
			if err := bindings.RejectChannelAgentBindingsForCanonicalUser(req.UserID, targetUID, uid); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reject channel binding"})
				return
			}
			if err := bindings.RejectChannelAgentAccessRequestsForActor(req.UserID, targetUID, uid); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reject channel access"})
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

// HandleBlock handles POST /api/friends/block
func (h *FriendHandler) HandleBlock(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	var req FriendActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := h.db.BlockUser(uid, req.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to block"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "blocked"})
}

// HandleRemoveFriend handles DELETE /api/friends/:id
func (h *FriendHandler) HandleRemoveFriend(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	friendID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
		return
	}

	if err := h.db.RemoveFriend(uid, friendID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to remove"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// HandleGetFriends handles GET /api/friends
func (h *FriendHandler) HandleGetFriends(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())

	friends, err := h.db.GetFriends(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get friends"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"friends": friends})
}

// HandleGetPendingRequests handles GET /api/friends/pending
func (h *FriendHandler) HandleGetPendingRequests(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	targetUID := uid
	if agentUID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("agent_uid")), 10, 64); err == nil && agentUID > 0 {
		resolvedUID, resolveErr := h.friendActionTargetUID(uid, agentUID)
		if resolveErr != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": resolveErr.Error()})
			return
		}
		targetUID = resolvedUID
	}

	requests, err := h.db.GetPendingRequests(targetUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get requests"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"requests": requests})
}

func (h *FriendHandler) friendActionTargetUID(currentUID, agentUID int64) (int64, error) {
	if agentUID <= 0 || agentUID == currentUID {
		return currentUID, nil
	}
	agent, err := h.db.GetUser(agentUID)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		return 0, fmt.Errorf("agent not available")
	}
	ownerUID, err := h.db.GetBotOwner(agentUID)
	if err != nil || ownerUID != currentUID {
		return 0, fmt.Errorf("agent is not owned by current user")
	}
	return agentUID, nil
}

// HandleSearchUsers handles GET /api/users/search?q=xxx
func (h *FriendHandler) HandleSearchUsers(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query too short"})
		return
	}

	users, err := h.db.SearchUsers(query, 20)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search failed"})
		return
	}

	filtered := users[:0]
	for _, user := range users {
		if user.ID != uid {
			filtered = append(filtered, user)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"users": filtered})
}

// p2pTopicID generates a deterministic topic ID for a P2P conversation.
func p2pTopicID(uid1, uid2 int64) string {
	if uid1 > uid2 {
		uid1, uid2 = uid2, uid1
	}
	return fmt.Sprintf("p2p_%d_%d", uid1, uid2)
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// WriteJSONPublic is the exported version of writeJSON for use outside the package.
func WriteJSONPublic(w http.ResponseWriter, status int, data interface{}) {
	writeJSON(w, status, data)
}

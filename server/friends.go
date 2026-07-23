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
	db  store.Store
	hub *Hub
}

// NewFriendHandler creates a new FriendHandler.
func NewFriendHandler(db store.Store, hubs ...*Hub) *FriendHandler {
	var hub *Hub
	if len(hubs) > 0 {
		hub = hubs[0]
	}
	return &FriendHandler{db: db, hub: hub}
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
	if status, err := h.validateFriendRequestTarget(uid, req.UserID); err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
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

	h.notifyFriendEvent("request", uid, req.UserID, req.Message, h.friendEventRecipients(req.UserID)...)
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "status": "pending"})
}

func (h *FriendHandler) friendEventRecipients(targetUID int64) []int64 {
	recipients := []int64{targetUID}
	if h.db == nil {
		return recipients
	}

	user, err := h.db.GetUser(targetUID)
	if err != nil || user == nil || user.AccountType != types.AccountBot {
		return recipients
	}
	if ownerUID, ownerErr := h.db.GetBotOwner(targetUID); ownerErr == nil && ownerUID > 0 {
		recipients = append(recipients, ownerUID)
	}
	return recipients
}

func (h *FriendHandler) notifyFriendEvent(action string, fromUID, toUID int64, message string, recipients ...int64) {
	if h.hub == nil {
		return
	}

	event := &ServerMessage{Friend: &MsgServerFriend{
		Action: action,
		From:   fromUID,
		To:     toUID,
		Msg:    message,
	}}
	sent := make(map[int64]struct{}, len(recipients))
	for _, recipientUID := range recipients {
		if recipientUID <= 0 {
			continue
		}
		if _, exists := sent[recipientUID]; exists {
			continue
		}
		sent[recipientUID] = struct{}{}
		h.hub.SendToUser(recipientUID, event)
	}
}

func (h *FriendHandler) validateFriendRequestTarget(actorUID, targetUID int64) (int, error) {
	user, err := h.db.GetUser(targetUID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to check friend target")
	}
	if user == nil || user.State != 0 {
		return http.StatusForbidden, fmt.Errorf("user not found")
	}
	if user.AccountType != types.AccountBot {
		return 0, nil
	}
	ownerUID, err := h.db.GetBotOwner(targetUID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to check agent owner")
	}
	if ownerUID == actorUID {
		return 0, nil
	}
	config, err := h.db.GetBotConfig(targetUID)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to check agent visibility")
	}
	if config == nil {
		return 0, nil
	}
	if config.Visibility == types.BotPrivate {
		return http.StatusForbidden, fmt.Errorf("agent is private")
	}
	return 0, nil
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

	recipients := append(h.friendEventRecipients(targetUID), req.UserID)
	h.notifyFriendEvent("accepted", targetUID, req.UserID, "", recipients...)
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

	recipients := append(h.friendEventRecipients(targetUID), req.UserID)
	h.notifyFriendEvent("rejected", targetUID, req.UserID, "", recipients...)
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

	// Keep the privacy-sensitive block action local to the actor. The target and
	// an Agent owner's session only need a relationship refresh.
	h.notifyFriendEvent("blocked", uid, req.UserID, "", uid)
	h.notifyFriendEvent("removed", uid, req.UserID, "", h.friendEventRecipients(req.UserID)...)
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

	recipients := append(h.friendEventRecipients(friendID), uid)
	h.notifyFriendEvent("removed", uid, friendID, "", recipients...)
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
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "name"
	}

	if mode != "name" && mode != "uid" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid search mode"})
		return
	}
	if mode == "uid" && !isNumericQuery(query) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "uid must be numeric"})
		return
	}
	if mode == "name" && len(query) < 2 {
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
		if user.ID == uid {
			continue
		}
		switch mode {
		case "uid":
			if !matchesUserUIDQuery(user, query) {
				continue
			}
		default:
			if !matchesUserNameQuery(user, query) {
				continue
			}
		}
		filtered = append(filtered, user)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"users": filtered})
}

func matchesUserUIDQuery(user *types.User, query string) bool {
	if user == nil {
		return false
	}
	uid, err := strconv.ParseInt(query, 10, 64)
	if err != nil {
		return false
	}
	return user.ID == uid
}

func matchesUserNameQuery(user *types.User, query string) bool {
	if user == nil {
		return false
	}
	needle := strings.ToLower(query)
	return strings.Contains(strings.ToLower(user.Username), needle) ||
		strings.Contains(strings.ToLower(user.DisplayName), needle)
}

func isNumericQuery(query string) bool {
	if query == "" {
		return false
	}
	for _, ch := range query {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
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

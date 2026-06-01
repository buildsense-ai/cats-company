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

// AgentHandler serves organization-style virtual employee discovery and access.
type AgentHandler struct {
	db store.Store
}

// NewAgentHandler creates an AgentHandler.
func NewAgentHandler(db store.Store) *AgentHandler {
	return &AgentHandler{db: db}
}

// HandleList handles GET /api/agents.
func (h *AgentHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	agents, err := h.db.ListAccessibleAgents(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list agents"})
		return
	}
	if agents == nil {
		agents = []*types.AgentRosterItem{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"agents": agents})
}

type openAgentRequest struct {
	AgentUID      int64  `json:"agent_uid"`
	AgentID       int64  `json:"agent_id"`
	AgentUsername string `json:"agent_username"`
	Username      string `json:"username"`
}

// HandleOpen handles POST /api/agents/open.
func (h *AgentHandler) HandleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	var req openAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	agentUID, err := h.resolveOpenAgentUID(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	agent, err := h.db.GetAccessibleAgent(agentUID, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check agent access"})
		return
	}
	if agent == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "agent is not available to this user"})
		return
	}
	if !agent.CanChat {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":  "agent is not usable for this user",
			"agent":  agent,
			"status": agent.Status,
		})
		return
	}

	topicID := p2pTopicID(uid, agentUID)
	if err := h.db.CreateTopic(topicID, "p2p", uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to open conversation"})
		return
	}
	agent.TopicID = topicID

	actor, _ := h.db.GetUser(uid)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent":           agent,
		"topic_id":        topicID,
		"catsco_identity": buildCatsCoIdentity(actor, agent, topicID),
	})
}

type agentAccessInviteRequest struct {
	UserUID        int64  `json:"user_uid"`
	UserID         int64  `json:"user_id"`
	TargetUserID   int64  `json:"target_user_id"`
	Username       string `json:"username"`
	TargetUsername string `json:"target_username"`
	Permission     string `json:"permission"`
	Status         string `json:"status"`
}

type agentAccessUpdateRequest struct {
	Permission string `json:"permission"`
	Status     string `json:"status"`
}

type acceptAgentInviteRequest struct {
	AgentUID      int64  `json:"agent_uid"`
	AgentID       int64  `json:"agent_id"`
	AgentUsername string `json:"agent_username"`
	Username      string `json:"username"`
}

// HandleAcceptInvite handles POST /api/agents/access/accept.
func (h *AgentHandler) HandleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	var req acceptAgentInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	agentUID, err := h.resolveAcceptAgentUID(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	access, err := h.db.AcceptAgentInvite(agentUID, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to accept invite"})
		return
	}
	if access == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invite not found"})
		return
	}

	agent, _ := h.db.GetAccessibleAgent(agentUID, uid)
	writeJSON(w, http.StatusOK, map[string]interface{}{"access": access, "agent": agent})
}

// HandleAgentSubroute handles /api/agents/{agent_uid}/access...
func (h *AgentHandler) HandleAgentSubroute(w http.ResponseWriter, r *http.Request) {
	agentUID, parts, ok := parseAgentSubroute(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	if !h.ensureCanManageAgent(w, r, agentUID) {
		return
	}

	if len(parts) == 1 && parts[0] == "access" {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handleListAgentAccess(w, agentUID)
		return
	}

	if len(parts) == 2 && parts[0] == "access" && parts[1] == "invite" {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handleInviteAgentAccess(w, r, agentUID)
		return
	}

	if len(parts) == 2 && parts[0] == "access" {
		accessID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || accessID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid access id"})
			return
		}
		switch r.Method {
		case http.MethodPatch:
			h.handleUpdateAgentAccess(w, r, agentUID, accessID)
		case http.MethodDelete:
			h.handleRevokeAgentAccess(w, agentUID, accessID)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
		return
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func (h *AgentHandler) handleListAgentAccess(w http.ResponseWriter, agentUID int64) {
	records, err := h.db.ListAgentAccess(agentUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list access"})
		return
	}
	if records == nil {
		records = []*types.AgentAccess{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"access": records})
}

func (h *AgentHandler) handleInviteAgentAccess(w http.ResponseWriter, r *http.Request, agentUID int64) {
	var req agentAccessInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	user, err := h.resolveTargetUser(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if user.AccountType != types.AccountHuman {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target must be a human user"})
		return
	}
	if user.ID == UIDFromContext(r.Context()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner already manages this agent"})
		return
	}

	permission, ok := normalizeAgentPermission(req.Permission)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid permission"})
		return
	}
	status, ok := normalizeAgentAccessStatus(req.Status)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid status"})
		return
	}

	access, err := h.db.UpsertAgentAccess(agentUID, user.ID, UIDFromContext(r.Context()), permission, status, "admin_invite")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to invite user"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"access": access})
}

func (h *AgentHandler) handleUpdateAgentAccess(w http.ResponseWriter, r *http.Request, agentUID, accessID int64) {
	var req agentAccessUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	permission, ok := normalizeAgentPermission(req.Permission)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid permission"})
		return
	}
	status, ok := normalizeAgentAccessStatus(req.Status)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid status"})
		return
	}

	access, err := h.db.UpdateAgentAccess(accessID, agentUID, permission, status)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update access"})
		return
	}
	if access == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "access not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"access": access})
}

func (h *AgentHandler) handleRevokeAgentAccess(w http.ResponseWriter, agentUID, accessID int64) {
	if err := h.db.RevokeAgentAccess(accessID, agentUID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke access"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (h *AgentHandler) resolveTargetUser(req agentAccessInviteRequest) (*types.User, error) {
	userID := firstPositiveInt64(req.TargetUserID, req.UserUID, req.UserID)
	if userID > 0 {
		return h.db.GetUser(userID)
	}

	username := firstNonBlank(req.TargetUsername, req.Username)
	if username == "" {
		return nil, fmt.Errorf("target user id or username required")
	}
	return h.db.GetUserByUsername(username)
}

func (h *AgentHandler) resolveOpenAgentUID(req openAgentRequest) (int64, error) {
	agentUID := firstPositiveInt64(req.AgentUID, req.AgentID)
	if agentUID > 0 {
		return agentUID, nil
	}
	return h.resolveAgentUIDByUsername(firstNonBlank(req.AgentUsername, req.Username))
}

func (h *AgentHandler) resolveAcceptAgentUID(req acceptAgentInviteRequest) (int64, error) {
	agentUID := firstPositiveInt64(req.AgentUID, req.AgentID)
	if agentUID > 0 {
		return agentUID, nil
	}
	return h.resolveAgentUIDByUsername(firstNonBlank(req.AgentUsername, req.Username))
}

func (h *AgentHandler) resolveAgentUIDByUsername(username string) (int64, error) {
	if username == "" {
		return 0, fmt.Errorf("agent uid or username required")
	}
	user, err := h.db.GetUserByUsername(username)
	if err != nil {
		return 0, fmt.Errorf("failed to find agent")
	}
	if user == nil || user.AccountType != types.AccountBot {
		return 0, fmt.Errorf("agent not found")
	}
	return user.ID, nil
}

func (h *AgentHandler) ensureCanManageAgent(w http.ResponseWriter, r *http.Request, agentUID int64) bool {
	uid := UIDFromContext(r.Context())
	ownerID, err := h.db.GetBotOwner(agentUID)
	if err != nil || ownerID == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return false
	}
	if ownerID != uid {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not allowed to manage this agent"})
		return false
	}
	return true
}

func parseAgentSubroute(path string) (int64, []string, bool) {
	rest := strings.TrimPrefix(path, "/api/agents/")
	if rest == path || rest == "" {
		return 0, nil, false
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		return 0, nil, false
	}
	agentUID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || agentUID <= 0 {
		return 0, nil, false
	}
	return agentUID, parts[1:], true
}

func normalizeAgentPermission(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "":
		return string(types.AgentPermissionUse), true
	case string(types.AgentPermissionView), string(types.AgentPermissionUse), string(types.AgentPermissionManage):
		return strings.TrimSpace(value), true
	default:
		return "", false
	}
}

func normalizeAgentAccessStatus(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "", "pending", "invited":
		return string(types.AgentAccessPending), true
	case string(types.AgentAccessPending), string(types.AgentAccessActive), string(types.AgentAccessBlocked), string(types.AgentAccessRevoked):
		return strings.TrimSpace(value), true
	default:
		return "", false
	}
}

func buildCatsCoIdentity(actor *types.User, agent *types.AgentRosterItem, topicID string) map[string]interface{} {
	actorName := ""
	actorID := int64(0)
	if actor != nil {
		actorID = actor.ID
		actorName = displayNameOrUsername(actor.DisplayName, actor.Username)
	}
	return map[string]interface{}{
		"actor": map[string]interface{}{
			"user_id":      actorID,
			"display_name": actorName,
		},
		"agent": map[string]interface{}{
			"agent_id":      agent.ID,
			"display_name":  displayNameOrUsername(agent.DisplayName, agent.Username),
			"relation":      agent.Source,
			"access_status": agent.Status,
			"permission":    agent.Permission,
		},
		"topic": map[string]interface{}{
			"topic_id": topicID,
			"type":     "p2p",
		},
		"permissions": map[string]interface{}{
			"can_chat":   agent.CanChat,
			"can_manage": agent.CanManage,
			"source":     agent.Source,
		},
	}
}

func prepareAgentP2PMessage(db store.Store, uid int64, topicID string, payload *normalizedMessagePayload) (int, string) {
	if db == nil || payload == nil || isGroupTopic(topicID) {
		return 0, ""
	}

	peerUID := extractPeerUID(topicID, uid)
	if peerUID == 0 {
		return http.StatusBadRequest, "invalid p2p topic"
	}

	selfIsBot, err := db.IsUserBot(uid)
	if err != nil {
		return http.StatusBadRequest, "sender not found"
	}
	peerIsBot, err := db.IsUserBot(peerUID)
	if err != nil {
		return http.StatusBadRequest, "peer not found"
	}
	if selfIsBot && peerIsBot {
		return 0, ""
	}
	if !selfIsBot && !peerIsBot {
		return 0, ""
	}

	if peerIsBot {
		agent, err := db.GetAccessibleAgent(peerUID, uid)
		if err != nil {
			return http.StatusInternalServerError, "failed to check agent access"
		}
		if agent == nil || !agent.CanChat {
			return http.StatusForbidden, "agent is not usable for this user"
		}
		actor, _ := db.GetUser(uid)
		payload.Metadata = mergeMessageMetadata(payload.Metadata, map[string]interface{}{
			"catsco_identity": buildCatsCoIdentity(actor, agent, topicID),
		})
		return 0, ""
	}

	if selfIsBot {
		agent, err := db.GetAccessibleAgent(uid, peerUID)
		if err != nil {
			return http.StatusInternalServerError, "failed to check agent access"
		}
		if agent == nil || !agent.CanChat {
			return http.StatusForbidden, "agent is not usable for this user"
		}
	}

	return 0, ""
}

func ensureAgentP2PTopicAccess(db store.Store, uid int64, topicID string) (int, string) {
	if db == nil || isGroupTopic(topicID) {
		return 0, ""
	}

	peerUID := extractPeerUID(topicID, uid)
	if peerUID == 0 {
		return http.StatusBadRequest, "invalid p2p topic"
	}

	selfIsBot, err := db.IsUserBot(uid)
	if err != nil {
		return http.StatusBadRequest, "sender not found"
	}
	peerIsBot, err := db.IsUserBot(peerUID)
	if err != nil {
		return http.StatusBadRequest, "peer not found"
	}
	if !selfIsBot && !peerIsBot {
		return 0, ""
	}
	if selfIsBot && peerIsBot {
		return 0, ""
	}

	agentUID := peerUID
	humanUID := uid
	if selfIsBot {
		agentUID = uid
		humanUID = peerUID
	}
	agent, err := db.GetAccessibleAgent(agentUID, humanUID)
	if err != nil {
		return http.StatusInternalServerError, "failed to check agent access"
	}
	if agent == nil || !agent.CanChat {
		return http.StatusForbidden, "agent is not usable for this user"
	}
	return 0, ""
}

func mergeMessageMetadata(existing, patch map[string]interface{}) map[string]interface{} {
	if len(existing) == 0 {
		existing = map[string]interface{}{}
	}
	for key, value := range patch {
		existing[key] = value
	}
	return existing
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

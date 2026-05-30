package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// AgentHandler exposes the user-facing virtual employee roster.
type AgentHandler struct {
	db  store.Store
	hub *Hub
}

// NewAgentHandler creates an AgentHandler.
func NewAgentHandler(db store.Store, hub *Hub) *AgentHandler {
	return &AgentHandler{db: db, hub: hub}
}

// AgentSummary is the lightweight roster item used by the WebApp.
type AgentSummary struct {
	ID               int64  `json:"id"`
	UID              int64  `json:"uid"`
	Username         string `json:"username"`
	DisplayName      string `json:"display_name"`
	AvatarURL        string `json:"avatar_url,omitempty"`
	Relation         string `json:"relation"`
	TopicID          string `json:"topic_id"`
	IsBot            bool   `json:"is_bot"`
	IsOnline         bool   `json:"is_online"`
	Visibility       string `json:"visibility,omitempty"`
	DeploymentStatus string `json:"deployment_status,omitempty"`
}

type openAgentRequest struct {
	AgentUID int64 `json:"agent_uid"`
}

// HandleListAgents handles GET /api/agents.
func (h *AgentHandler) HandleListAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	agents, err := h.visibleAgents(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list agents"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"agents": agents})
}

// HandleOpenAgent handles POST /api/agents/open.
func (h *AgentHandler) HandleOpenAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req openAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	agent, status, err := h.accessibleAgent(uid, req.AgentUID)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	if err := h.db.CreateTopic(agent.TopicID, "p2p", uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to open agent chat"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"agent": agent, "topic": agent.TopicID})
}

func (h *AgentHandler) visibleAgents(uid int64) ([]AgentSummary, error) {
	seen := make(map[int64]struct{})
	agents := make([]AgentSummary, 0)

	ownedBots, err := h.db.ListBotsByOwner(uid)
	if err != nil {
		return nil, err
	}
	for _, bot := range ownedBots {
		agent, ok := h.agentFromBotMap(uid, bot, "owner")
		if !ok {
			continue
		}
		seen[agent.UID] = struct{}{}
		agents = append(agents, agent)
	}

	friends, err := h.db.GetFriends(uid)
	if err != nil {
		return nil, err
	}
	for _, friend := range friends {
		if friend == nil {
			continue
		}
		if _, ok := seen[friend.ID]; ok {
			continue
		}
		if friend.AccountType != types.AccountBot && !friend.BotDisclose {
			continue
		}
		agent := h.agentFromUser(uid, friend, "friend")
		seen[agent.UID] = struct{}{}
		agents = append(agents, agent)
	}

	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Relation != agents[j].Relation {
			return agents[i].Relation == "owner"
		}
		return agents[i].DisplayName < agents[j].DisplayName
	})
	return agents, nil
}

func (h *AgentHandler) accessibleAgent(uid, agentUID int64) (AgentSummary, int, error) {
	user, relation, status, err := accessibleAgentUser(h.db, uid, agentUID)
	if err != nil {
		return AgentSummary{}, status, err
	}

	return h.agentFromUser(uid, user, relation), 0, nil
}

func accessibleAgentUser(db store.Store, uid, agentUID int64) (*types.User, string, int, error) {
	if agentUID <= 0 {
		return nil, "", http.StatusBadRequest, errInvalidAgentUID{}
	}
	if db == nil {
		return nil, "", http.StatusInternalServerError, errAgentAccessCheck{}
	}

	user, err := db.GetUser(agentUID)
	if err != nil || user == nil {
		return nil, "", http.StatusNotFound, errAgentNotFound{}
	}
	if user.AccountType != types.AccountBot {
		return nil, "", http.StatusBadRequest, errNotAgent{}
	}

	relation := ""
	if ownerUID, err := db.GetBotOwner(agentUID); err == nil && ownerUID == uid {
		relation = "owner"
	}
	if relation == "" {
		areFriends, err := db.AreFriends(uid, agentUID)
		if err != nil {
			return nil, "", http.StatusInternalServerError, errAgentAccessCheck{}
		}
		if areFriends {
			relation = "friend"
		}
	}
	if relation == "" {
		return nil, "", http.StatusForbidden, errAgentForbidden{}
	}
	return user, relation, 0, nil
}

func validateAgentP2PMessageAccess(db store.Store, uid int64, accountType types.AccountType, peerUID int64) (int, string) {
	if db == nil || uid <= 0 || peerUID <= 0 || accountType == types.AccountBot {
		return 0, ""
	}
	peer, err := db.GetUser(peerUID)
	if err != nil || peer == nil || peer.AccountType != types.AccountBot {
		return 0, ""
	}
	if _, _, status, err := accessibleAgentUser(db, uid, peerUID); err != nil {
		return status, err.Error()
	}
	return 0, ""
}

func (h *AgentHandler) agentFromBotMap(viewerUID int64, bot map[string]interface{}, relation string) (AgentSummary, bool) {
	uid := mapID(bot["id"])
	if uid <= 0 {
		return AgentSummary{}, false
	}
	displayName := mapString(bot["display_name"])
	if displayName == "" {
		displayName = mapString(bot["username"])
	}
	agent := AgentSummary{
		ID:               uid,
		UID:              uid,
		Username:         mapString(bot["username"]),
		DisplayName:      displayName,
		AvatarURL:        mapString(bot["avatar_url"]),
		Relation:         relation,
		TopicID:          p2pTopicID(viewerUID, uid),
		IsBot:            true,
		IsOnline:         h.hub != nil && h.hub.IsOnline(uid),
		Visibility:       mapString(bot["visibility"]),
		DeploymentStatus: mapString(bot["deployment_status"]),
	}
	return agent, true
}

func (h *AgentHandler) agentFromUser(viewerUID int64, user *types.User, relation string) AgentSummary {
	displayName := displayNameOrUsername(user.DisplayName, user.Username)
	return AgentSummary{
		ID:          user.ID,
		UID:         user.ID,
		Username:    user.Username,
		DisplayName: displayName,
		AvatarURL:   user.AvatarURL,
		Relation:    relation,
		TopicID:     p2pTopicID(viewerUID, user.ID),
		IsBot:       true,
		IsOnline:    h.hub != nil && h.hub.IsOnline(user.ID),
	}
}

func mapString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}

type errInvalidAgentUID struct{}

func (errInvalidAgentUID) Error() string { return "invalid agent_uid" }

type errAgentNotFound struct{}

func (errAgentNotFound) Error() string { return "agent not found" }

type errNotAgent struct{}

func (errNotAgent) Error() string { return "user is not an agent" }

type errAgentForbidden struct{}

func (errAgentForbidden) Error() string { return "agent is not available to this user" }

type errAgentAccessCheck struct{}

func (errAgentAccessCheck) Error() string { return "failed to check agent access" }

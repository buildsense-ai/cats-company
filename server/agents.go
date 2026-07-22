package server

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// AgentHandler exposes the user-facing virtual employee roster.
type AgentHandler struct {
	db                        store.Store
	hub                       *Hub
	relayAdmin                *RelayAdminClient
	deviceModelStatusResolver func(uid int64, bodyID string) (DeviceModelStatus, bool)
	cloudArtifactAgentUIDs    map[int64]struct{}
	quotaMu                   sync.Mutex
	quotaCache                map[string]agentQuotaCacheEntry
}

const agentQuotaCacheTTL = 30 * time.Second

type agentQuotaResponse struct {
	Configured bool               `json:"configured"`
	Shared     bool               `json:"shared"`
	Summary    *agentQuotaSummary `json:"summary,omitempty"`
}

type agentQuotaSummary struct {
	Source           string  `json:"source,omitempty"`
	Model            string  `json:"model"`
	ReasoningEffort  string  `json:"reasoning_effort,omitempty"`
	RemainingPercent float64 `json:"remaining_percent"`
	Status           string  `json:"status"`
	ResetDuration    string  `json:"reset_duration,omitempty"`
}

type agentQuotaCacheEntry struct {
	response  agentQuotaResponse
	expiresAt time.Time
}

type botModelConfigReader interface {
	GetBotModelConfig(botUID int64) (*types.BotModelConfig, error)
}

// NewAgentHandler creates an AgentHandler.
func NewAgentHandler(db store.Store, hub *Hub) *AgentHandler {
	return &AgentHandler{
		db:                     db,
		hub:                    hub,
		cloudArtifactAgentUIDs: parseAgentUIDSet(os.Getenv("CATSCO_CLOUD_ARTIFACT_AGENT_UIDS")),
		quotaCache:             make(map[string]agentQuotaCacheEntry),
	}
}

// SetRelayUsageDependencies enables the friend-visible, sanitized agent quota summary.
func (h *AgentHandler) SetRelayUsageDependencies(admin *RelayAdminClient, resolver func(uid int64, bodyID string) (DeviceModelStatus, bool)) {
	if h == nil {
		return
	}
	h.relayAdmin = admin
	h.deviceModelStatusResolver = resolver
}

// AgentSummary is the lightweight roster item used by the WebApp.
type AgentSummary struct {
	ID                    int64  `json:"id"`
	UID                   int64  `json:"uid"`
	Username              string `json:"username"`
	DisplayName           string `json:"display_name"`
	AvatarURL             string `json:"avatar_url,omitempty"`
	Relation              string `json:"relation"`
	TopicID               string `json:"topic_id"`
	IsBot                 bool   `json:"is_bot"`
	IsOnline              bool   `json:"is_online"`
	Visibility            string `json:"visibility,omitempty"`
	DeploymentStatus      string `json:"deployment_status,omitempty"`
	CloudArtifactsEnabled bool   `json:"cloud_artifacts_enabled,omitempty"`
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

// HandleAgentQuota handles GET /api/agents/quota?uid=<agent uid>.
// It deliberately exposes only the active model and remaining percentage.
func (h *AgentHandler) HandleAgentQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	viewerUID := UIDFromContext(r.Context())
	if viewerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	agentUID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("uid")), 10, 64)
	if err != nil || agentUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent uid"})
		return
	}
	if _, _, status, err := accessibleAgentUser(h.db, viewerUID, agentUID); err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	ownerUID, err := h.db.GetBotOwner(agentUID)
	if err != nil || ownerUID <= 0 {
		writeJSON(w, http.StatusOK, agentQuotaResponse{Configured: false, Shared: true})
		return
	}
	deviceStatus, ok := h.resolveAgentModelStatus(agentUID, ownerUID)
	if !ok {
		writeJSON(w, http.StatusOK, agentQuotaResponse{Configured: false, Shared: true})
		return
	}

	source := strings.ToLower(strings.TrimSpace(deviceStatus.Source))
	model := strings.TrimSpace(deviceStatus.Model)
	if source == "custom" || normalizeRelayModelName(model) == "custom" || strings.EqualFold(model, "自定义模型") {
		customModel := model
		if customModel == "" || normalizeRelayModelName(customModel) == "custom" || strings.EqualFold(customModel, "自定义模型") {
			customModel = "自定义模型"
		}
		writeJSON(w, http.StatusOK, agentQuotaResponse{
			Configured: true,
			Shared:     false,
			Summary: &agentQuotaSummary{
				Source:          "custom",
				Model:           customModel,
				ReasoningEffort: deviceStatus.ReasoningEffort,
				Status:          "custom",
			},
		})
		return
	}
	if h.relayAdmin == nil || model == "" {
		writeJSON(w, http.StatusOK, agentQuotaResponse{Configured: false, Shared: true})
		return
	}

	cacheKey := strconv.FormatInt(ownerUID, 10) + ":" + normalizeRelayModelName(model) + ":" + strings.ToLower(strings.TrimSpace(deviceStatus.ReasoningEffort))
	if cached, ok := h.cachedAgentQuota(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	usage, err := fetchRelayUsageForUID(r.Context(), h.relayAdmin, ownerUID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "relay admin request failed"})
		return
	}
	response := sanitizeAgentQuota(buildRelayUsageResponse(usage, model))
	if response.Summary != nil {
		// A relay budget may cover multiple allowed models. Keep its quota values,
		// but expose the model actually applied by this bot rather than the
		// canonical model name of the shared billing bucket.
		response.Summary.Model = model
		response.Summary.ReasoningEffort = deviceStatus.ReasoningEffort
	}
	h.storeAgentQuota(cacheKey, response)
	writeJSON(w, http.StatusOK, response)
}

func (h *AgentHandler) resolveAgentModelStatus(agentUID, ownerUID int64) (DeviceModelStatus, bool) {
	if models, ok := h.db.(botModelConfigReader); ok {
		if config, err := models.GetBotModelConfig(agentUID); err == nil {
			if status, applied := appliedBotModelStatus(config); applied {
				return status, true
			}
		}
	}
	if h.deviceModelStatusResolver == nil {
		return DeviceModelStatus{}, false
	}
	bodyID, _ := h.db.GetBotBodyID(agentUID)
	return h.deviceModelStatusResolver(ownerUID, strings.TrimSpace(bodyID))
}

func appliedBotModelStatus(config *types.BotModelConfig) (DeviceModelStatus, bool) {
	if config == nil {
		return DeviceModelStatus{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(config.AppliedKind))
	model := strings.TrimSpace(config.AppliedModelID)
	if kind == "" && model != "" {
		kind = botModelKindCatalog
	}
	switch kind {
	case botModelKindCatalog:
		if model == "" {
			return DeviceModelStatus{}, false
		}
		return DeviceModelStatus{Source: "relay", Model: model, ReasoningEffort: strings.TrimSpace(config.AppliedReasoning)}, true
	case botModelKindCustom:
		if model == "" || normalizeRelayModelName(model) == "custom" || strings.EqualFold(model, "自定义模型") {
			model = "自定义模型"
		}
		return DeviceModelStatus{Source: "custom", Model: model, ReasoningEffort: strings.TrimSpace(config.AppliedReasoning)}, true
	default:
		return DeviceModelStatus{}, false
	}
}

func sanitizeAgentQuota(usage relayUsageResponse) agentQuotaResponse {
	response := agentQuotaResponse{Configured: usage.Configured, Shared: true}
	if usage.Summary == nil {
		return response
	}
	remaining := 100 - usage.Summary.Percent
	if remaining < 0 {
		remaining = 0
	} else if remaining > 100 {
		remaining = 100
	}
	response.Summary = &agentQuotaSummary{
		Source:           usage.Summary.Source,
		Model:            usage.Summary.Model,
		RemainingPercent: remaining,
		Status:           usage.Summary.Status,
		ResetDuration:    usage.Summary.ResetDuration,
	}
	return response
}

func (h *AgentHandler) cachedAgentQuota(key string) (agentQuotaResponse, bool) {
	h.quotaMu.Lock()
	defer h.quotaMu.Unlock()
	entry, ok := h.quotaCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(h.quotaCache, key)
		return agentQuotaResponse{}, false
	}
	return entry.response, true
}

func (h *AgentHandler) storeAgentQuota(key string, response agentQuotaResponse) {
	h.quotaMu.Lock()
	defer h.quotaMu.Unlock()
	if h.quotaCache == nil {
		h.quotaCache = make(map[string]agentQuotaCacheEntry)
	}
	h.quotaCache[key] = agentQuotaCacheEntry{response: response, expiresAt: time.Now().Add(agentQuotaCacheTTL)}
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
		ID:                    uid,
		UID:                   uid,
		Username:              mapString(bot["username"]),
		DisplayName:           displayName,
		AvatarURL:             mapString(bot["avatar_url"]),
		Relation:              relation,
		TopicID:               p2pTopicID(viewerUID, uid),
		IsBot:                 true,
		IsOnline:              h.agentRuntimeOnline(uid),
		Visibility:            mapString(bot["visibility"]),
		DeploymentStatus:      mapString(bot["deployment_status"]),
		CloudArtifactsEnabled: h.cloudArtifactsEnabled(uid),
	}
	return agent, true
}

func (h *AgentHandler) agentFromUser(viewerUID int64, user *types.User, relation string) AgentSummary {
	displayName := displayNameOrUsername(user.DisplayName, user.Username)
	return AgentSummary{
		ID:                    user.ID,
		UID:                   user.ID,
		Username:              user.Username,
		DisplayName:           displayName,
		AvatarURL:             user.AvatarURL,
		Relation:              relation,
		TopicID:               p2pTopicID(viewerUID, user.ID),
		IsBot:                 true,
		IsOnline:              h.agentRuntimeOnline(user.ID),
		CloudArtifactsEnabled: h.cloudArtifactsEnabled(user.ID),
	}
}

func (h *AgentHandler) cloudArtifactsEnabled(uid int64) bool {
	if h == nil || uid <= 0 {
		return false
	}
	_, ok := h.cloudArtifactAgentUIDs[uid]
	return ok
}

func parseAgentUIDSet(value string) map[int64]struct{} {
	uids := make(map[int64]struct{})
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		field = strings.TrimSpace(field)
		if len(field) >= 3 && strings.EqualFold(field[:3], "usr") {
			field = field[3:]
		}
		uid, err := strconv.ParseInt(field, 10, 64)
		if err == nil && uid > 0 {
			uids[uid] = struct{}{}
		}
	}
	return uids
}

func (h *AgentHandler) agentRuntimeOnline(uid int64) bool {
	if h == nil || h.hub == nil {
		return false
	}
	return h.hub.BotBodyStatus(uid).Active
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

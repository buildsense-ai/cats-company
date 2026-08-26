// Package server implements Cats Company bot management REST API.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// BotHandler handles bot management API requests.
type BotHandler struct {
	db                               store.Store
	hub                              *Hub
	runtimeActivationAckScopeAllowed func(int64) bool
}

// NewBotHandler creates a new BotHandler.
func NewBotHandler(db store.Store) *BotHandler {
	return &BotHandler{db: db}
}

func (h *BotHandler) SetHub(hub *Hub) {
	h.hub = hub
}

func (h *BotHandler) SetRuntimeActivationAckScopeAllowed(allowed func(int64) bool) {
	h.runtimeActivationAckScopeAllowed = allowed
}

// HandleBotsRouter routes /api/bots by HTTP method.
func (h *BotHandler) HandleBotsRouter(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.HandleListMyBots(w, r)
	case http.MethodPost:
		h.HandleCreateBot(w, r)
	case http.MethodPatch:
		h.HandleUpdateBot(w, r)
	case http.MethodDelete:
		h.HandleDeleteBot(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// BotRegisterRequest is the JSON body for bot registration.
type BotRegisterRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Model       string `json:"model,omitempty"`
	APIEndpoint string `json:"api_endpoint,omitempty"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
}

var supportedBotRoles = map[string]struct{}{
	"code_review": {},
	"debugging":   {},
	"writing":     {},
	"research":    {},
	"general":     {},
}

func normalizeBotRole(value string) (string, bool) {
	role := strings.TrimSpace(value)
	if role == "" {
		return "general", true
	}
	_, ok := supportedBotRoles[role]
	return role, ok
}

// HandleRegisterBot handles POST /api/admin/bots - register a new bot account.
func (h *BotHandler) HandleRegisterBot(w http.ResponseWriter, r *http.Request) {
	var req BotRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	if len(req.Username) < 3 || len(req.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username min 3, password min 6"})
		return
	}

	existing, err := h.db.GetUserByUsername(req.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "username taken"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	user := &types.User{
		Username:    req.Username,
		DisplayName: req.DisplayName,
		AccountType: types.AccountBot,
		PassHash:    hash,
	}

	uid, err := h.db.CreateUser(user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "registration failed"})
		return
	}

	// Save bot config
	if err := h.db.SaveBotConfig(uid, req.APIEndpoint, req.Model); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config save failed"})
		return
	}

	// Generate and store API key
	apiKey := GenerateAPIKey(uid)
	if err := h.db.SaveAPIKey(uid, apiKey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "api key save failed"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"uid":      uid,
		"username": req.Username,
		"type":     "bot",
		"api_key":  apiKey,
	})
}

// HandleListBots handles GET /api/admin/bots
func (h *BotHandler) HandleListBots(w http.ResponseWriter, r *http.Request) {
	bots, err := h.db.ListBots()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list bots"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"bots": bots})
}

// HandleToggleBot handles POST /api/admin/bots/:id/toggle
func (h *BotHandler) HandleToggleBot(w http.ResponseWriter, r *http.Request) {
	uidStr := r.URL.Query().Get("uid")
	uid, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	if err := h.db.ToggleBotEnabled(uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "toggle failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "toggled"})
}

// HandleRotateAPIKey handles POST /api/admin/bots/rotate-key?uid=xxx
// Generates a new API key for the specified bot, invalidating the old one.
func (h *BotHandler) HandleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	uid, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	// Verify the bot exists
	_, err = h.db.GetBotConfig(uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}

	// Generate and save new API key
	apiKey := GenerateAPIKey(uid)
	if err := h.db.SaveAPIKey(uid, apiKey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rotate failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":     uid,
		"api_key": apiKey,
	})
}

// HandleBotDebugLog handles GET /api/admin/bots/debug?uid=xxx&limit=50
// Returns recent messages sent by the specified bot for debugging.
func (h *BotHandler) HandleBotDebugLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	if uidStr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "uid parameter required"})
		return
	}
	uid, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 200 {
		limit = 200
	}

	msgs, err := h.db.GetBotDebugMessages(uid, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to fetch debug messages"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":      uid,
		"count":    len(msgs),
		"messages": msgs,
	})
}

// createBotResult holds the result of bot account creation.
type createBotResult struct {
	UID      int64
	Username string
	APIKey   string
}

// createBotAccount is the shared logic for creating a bot account with owner.
func (h *BotHandler) createBotAccount(ownerUID int64, req BotRegisterRequest) (*createBotResult, int, error) {
	if len(req.Username) < 3 {
		return nil, http.StatusBadRequest, fmt.Errorf("username min 3 chars")
	}
	role, validRole := normalizeBotRole(req.Role)
	if !validRole {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid assistant role")
	}
	description := strings.TrimSpace(req.Description)
	if len([]rune(description)) > 500 {
		return nil, http.StatusBadRequest, fmt.Errorf("description max 500 chars")
	}

	existing, err := h.db.GetUserByUsername(req.Username)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("database error")
	}
	if existing != nil {
		return nil, http.StatusConflict, fmt.Errorf("username taken")
	}

	randPass := GenerateAPIKey(0)
	hash, err := bcrypt.GenerateFromPassword([]byte(randPass), bcrypt.DefaultCost)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("internal error")
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Username
	}

	user := &types.User{
		Username:    req.Username,
		DisplayName: displayName,
		AccountType: types.AccountBot,
		PassHash:    hash,
	}

	uid, err := h.db.CreateUser(user)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("registration failed")
	}

	if err := h.db.SaveBotConfigWithOwner(uid, ownerUID, req.APIEndpoint, req.Model); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("config save failed")
	}
	if profiles, ok := h.db.(store.BotProfileStore); ok {
		if err := profiles.UpdateBotProfile(uid, &role, &description); err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("profile save failed")
		}
	}

	apiKey := GenerateAPIKey(uid)
	if err := h.db.SaveAPIKey(uid, apiKey); err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("api key save failed")
	}

	return &createBotResult{UID: uid, Username: req.Username, APIKey: apiKey}, 0, nil
}

// HandleCreateBot handles POST /api/bots — authenticated user creates a bot they own.
func (h *BotHandler) HandleCreateBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req BotRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	result, status, err := h.createBotAccount(ownerUID, req)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	role, _ := normalizeBotRole(req.Role)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"uid":         result.UID,
		"username":    result.Username,
		"type":        "bot",
		"owner_id":    ownerUID,
		"api_key":     result.APIKey,
		"role":        role,
		"description": strings.TrimSpace(req.Description),
	})
}

// HandleListMyBots handles GET /api/bots — list bots owned by or added by the authenticated user.
func (h *BotHandler) HandleListMyBots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	ownedBots, err := h.db.ListBotsByOwner(ownerUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list bots"})
		return
	}
	bots := make([]map[string]interface{}, 0, len(ownedBots))
	seen := make(map[int64]struct{})
	for _, bot := range ownedBots {
		if bot == nil {
			continue
		}
		botUID := mapInt64(bot["id"])
		if botUID > 0 {
			bot["uid"] = botUID
			bot["owner_id"] = ownerUID
			seen[botUID] = struct{}{}
		}
		bot["relation"] = "owner"
		bot["is_owner"] = true
		bot["is_bot"] = true
		bots = append(bots, bot)
	}

	friends, err := h.db.GetFriends(ownerUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list bot friends"})
		return
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
		bot := map[string]interface{}{
			"id":           friend.ID,
			"uid":          friend.ID,
			"username":     friend.Username,
			"display_name": displayNameOrUsername(friend.DisplayName, friend.Username),
			"avatar_url":   friend.AvatarURL,
			"state":        friend.State,
			"relation":     "friend",
			"is_owner":     false,
			"is_bot":       true,
			"visibility":   "friend",
		}
		if botOwnerUID, err := h.db.GetBotOwner(friend.ID); err == nil && botOwnerUID > 0 {
			bot["owner_id"] = botOwnerUID
		}
		seen[friend.ID] = struct{}{}
		bots = append(bots, bot)
	}

	sort.SliceStable(bots, func(i, j int) bool {
		leftRelation, _ := bots[i]["relation"].(string)
		rightRelation, _ := bots[j]["relation"].(string)
		if leftRelation != rightRelation {
			return leftRelation == "owner"
		}
		leftName, _ := bots[i]["display_name"].(string)
		rightName, _ := bots[j]["display_name"].(string)
		if leftName == rightName {
			return mapInt64(bots[i]["id"]) < mapInt64(bots[j]["id"])
		}
		return leftName < rightName
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{"bots": bots})
}

// HandleDeleteBot handles DELETE /api/bots?uid=xxx — owner deletes their bot.
func (h *BotHandler) HandleDeleteBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	// Verify ownership
	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	if err := h.db.DeleteBot(botUID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// HandleSetBotVisibility handles PATCH /api/bots/visibility?uid=xxx&v=public|private
func (h *BotHandler) HandleSetBotVisibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	vis := r.URL.Query().Get("v")
	if vis != "public" && vis != "private" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "v must be public or private"})
		return
	}

	// Verify ownership
	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	if err := h.db.SetBotVisibility(botUID, vis); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":        botUID,
		"visibility": vis,
	})
}

// HandleSetBotSkillsVisibility handles
// PATCH /api/bots/skills-visibility?uid=xxx&v=owner|authorized|public.
func (h *BotHandler) HandleSetBotSkillsVisibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	botUID, err := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	visibility := types.BotSkillsVisibility(r.URL.Query().Get("v"))
	if !validBotSkillsVisibility(visibility) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "v must be owner, authorized, or public"})
		return
	}

	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	visibilityStore, ok := h.db.(store.BotSkillsVisibilityStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "skills visibility is unavailable"})
		return
	}
	if err := visibilityStore.SetBotSkillsVisibility(botUID, string(visibility)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":               botUID,
		"skills_visibility": visibility,
	})
}

func mapInt64(value interface{}) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		parsed, _ := v.Int64()
		return parsed
	default:
		return 0
	}
}

// HandleGetBotAPIKey handles GET /api/bots/api-key?uid=xxx.
// Only the owner can read a bot API key for later copy/configuration.
func (h *BotHandler) HandleGetBotAPIKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	apiKey, err := h.db.GetBotAPIKey(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load api key"})
		return
	}
	if apiKey == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "api key not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":     botUID,
		"api_key": apiKey,
	})
}

// HandleGetBotBodyStatus handles GET /api/bots/body-status?uid=xxx.
// It exposes only the bound/active body id for the bot owner; connection ids
// and API keys stay server-internal.
func (h *BotHandler) HandleGetBotBodyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil || botUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	boundBodyID, err := h.db.GetBotBodyID(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read bot body binding"})
		return
	}

	status := BotBodyStatus{BotUID: botUID, State: "offline", Active: false, BodyID: boundBodyID, Bound: boundBodyID != ""}
	if boundBodyID == "" {
		status.State = "unbound"
	}
	if h.hub != nil {
		status.RuntimeMode = h.hub.RuntimeMode()
		status.RouteState = h.hub.RuntimeRouteState()
		activeStatus := h.hub.BotBodyStatus(botUID)
		if activeStatus.Active {
			status = activeStatus
			status.Bound = status.Bound || boundBodyID != ""
		}
	}
	writeJSON(w, http.StatusOK, status)
}

var globalBotStats *BotStats

// SetBotStats sets the global bot stats reference for the API.
func SetBotStats(bs *BotStats) {
	globalBotStats = bs
}

// HandleBotStats handles GET /api/admin/bots/stats
func (h *BotHandler) HandleBotStats(w http.ResponseWriter, r *http.Request) {
	if globalBotStats == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "stats not available"})
		return
	}
	uidStr := r.URL.Query().Get("uid")
	if uidStr != "" {
		uid, err := strconv.ParseInt(uidStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
			return
		}
		writeJSON(w, http.StatusOK, globalBotStats.GetBotStats(uid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"bots": globalBotStats.GetStats()})
}

// HandleUpdateBotAvatar handles POST /api/bots/avatar
func (h *BotHandler) HandleUpdateBotAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	// Verify ownership
	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil || actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}

	avatarURL := r.FormValue("avatar_url")
	if avatarURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "avatar_url required"})
		return
	}

	// Update avatar
	if err := h.db.UpdateUserAvatar(botUID, avatarURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"avatar_url": avatarURL})
}

// HandleUpdateBot handles PATCH /api/bots?uid=xxx — update bot profile
func (h *BotHandler) HandleUpdateBot(w http.ResponseWriter, r *http.Request) {
	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil || actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	var req struct {
		DisplayName           string  `json:"display_name"`
		AvatarURL             string  `json:"avatar_url"`
		Role                  *string `json:"role"`
		Description           *string `json:"description"`
		ArtifactUploadEnabled *bool   `json:"artifact_upload_enabled"`
		SkillMutationMode     *string `json:"skill_mutation_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var skillMutationMode types.BotSkillMutationMode
	var skillMutationPolicies store.BotSkillMutationPolicyStore
	if req.SkillMutationMode != nil {
		var ok bool
		skillMutationMode, ok = types.ParseBotSkillMutationMode(*req.SkillMutationMode)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skill mutation mode"})
			return
		}
		skillMutationPolicies, ok = h.db.(store.BotSkillMutationPolicyStore)
		if !ok {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "skill mutation policy is unavailable"})
			return
		}
	}

	if req.DisplayName != "" {
		if err := h.db.UpdateUserDisplayName(botUID, req.DisplayName); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
	}
	if req.AvatarURL != "" {
		if err := h.db.UpdateUserAvatar(botUID, req.AvatarURL); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
	}
	if req.Role != nil || req.Description != nil {
		role := req.Role
		if role != nil {
			normalized, ok := normalizeBotRole(*role)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid assistant role"})
				return
			}
			role = &normalized
		}
		description := req.Description
		if description != nil {
			trimmed := strings.TrimSpace(*description)
			if len([]rune(trimmed)) > 500 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "description max 500 chars"})
				return
			}
			description = &trimmed
		}
		if profiles, ok := h.db.(store.BotProfileStore); ok {
			if err := profiles.UpdateBotProfile(botUID, role, description); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
				return
			}
		}
	}
	if req.ArtifactUploadEnabled != nil {
		policies, ok := h.db.(store.BotArtifactPolicyStore)
		if !ok {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "artifact upload policy is unavailable"})
			return
		}
		if err := policies.UpdateBotArtifactUploadPolicy(botUID, *req.ArtifactUploadEnabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
	}
	if req.SkillMutationMode != nil {
		if err := skillMutationPolicies.UpdateBotSkillMutationMode(botUID, skillMutationMode); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// HandleGetBotFriends handles GET/DELETE /api/bots/friends?uid=xxx.
func (h *BotHandler) HandleGetBotFriends(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGetBotFriends(w, r)
	case http.MethodDelete:
		h.handleRemoveBotFriend(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *BotHandler) handleGetBotFriends(w http.ResponseWriter, r *http.Request) {
	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil || actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}

	friends, err := h.db.GetFriends(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get friends"})
		return
	}
	filtered := friends[:0]
	for _, friend := range friends {
		if friend == nil || friend.ID == ownerUID {
			continue
		}
		filtered = append(filtered, friend)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"friends": filtered})
}

func (h *BotHandler) handleRemoveBotFriend(w http.ResponseWriter, r *http.Request) {
	ownerUID := UIDFromContext(r.Context())
	if ownerUID == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	uidStr := r.URL.Query().Get("uid")
	botUID, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil || botUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}

	userIDStr := r.URL.Query().Get("user_id")
	userUID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil || userUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
		return
	}

	actualOwner, err := h.db.GetBotOwner(botUID)
	if err != nil || actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}
	if userUID == ownerUID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner access cannot be removed"})
		return
	}

	if bindings, ok := h.db.(store.ChannelAgentBindingStore); ok {
		if err := bindings.RejectChannelAgentBindingsForCanonicalUser(userUID, botUID, ownerUID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke channel binding"})
			return
		}
		if err := bindings.RejectChannelAgentAccessRequestsForActor(userUID, botUID, ownerUID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke channel access"})
			return
		}
	}
	if err := h.db.RemoveFriend(botUID, userUID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to remove friend"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "removed",
		"uid":      botUID,
		"user_id":  userUID,
		"owner_id": ownerUID,
	})
}

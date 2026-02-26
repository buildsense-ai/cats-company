// Package server implements Cats Company bot management REST API.
package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"golang.org/x/crypto/bcrypt"

	"github.com/openchat/openchat/server/db/mysql"
	"github.com/openchat/openchat/server/store/types"
)

// BotHandler handles bot management API requests.
type BotHandler struct {
	db *mysql.Adapter
}

// NewBotHandler creates a new BotHandler.
func NewBotHandler(db *mysql.Adapter) *BotHandler {
	return &BotHandler{db: db}
}

// HandleBotsRouter routes /api/bots by HTTP method.
func (h *BotHandler) HandleBotsRouter(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.HandleListMyBots(w, r)
	case http.MethodPost:
		h.HandleCreateBot(w, r)
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

	if len(req.Username) < 3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username min 3 chars"})
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

	// Bot accounts use a random password (not user-facing)
	randPass := GenerateAPIKey(0) // just need random bytes
	hash, err := bcrypt.GenerateFromPassword([]byte(randPass), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "registration failed"})
		return
	}

	if err := h.db.SaveBotConfigWithOwner(uid, ownerUID, req.APIEndpoint, req.Model); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config save failed"})
		return
	}

	apiKey := GenerateAPIKey(uid)
	if err := h.db.SaveAPIKey(uid, apiKey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "api key save failed"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"uid":      uid,
		"username": req.Username,
		"type":     "bot",
		"owner_id": ownerUID,
		"api_key":  apiKey,
	})
}

// HandleListMyBots handles GET /api/bots — list bots owned by the authenticated user.
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

	bots, err := h.db.ListBotsByOwner(ownerUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list bots"})
		return
	}
	if bots == nil {
		bots = []map[string]interface{}{}
	}
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

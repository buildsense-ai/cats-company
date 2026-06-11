package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// ChannelAgentBindingHandler owns QR/link entries and external channel bindings.
type ChannelAgentBindingHandler struct {
	db  store.Store
	hub *Hub
}

// NewChannelAgentBindingHandler creates the channel binding handler.
func NewChannelAgentBindingHandler(db store.Store, hub *Hub) *ChannelAgentBindingHandler {
	return &ChannelAgentBindingHandler{db: db, hub: hub}
}

type channelAgentEntryRequest struct {
	AgentUID     int64  `json:"agent_uid"`
	Channel      string `json:"channel"`
	ChannelAppID string `json:"channel_app_id,omitempty"`
}

type channelAgentConfirmRequest struct {
	SceneKey                string `json:"scene_key"`
	Channel                 string `json:"channel,omitempty"`
	ChannelAppID            string `json:"channel_app_id,omitempty"`
	ChannelUserID           string `json:"channel_user_id"`
	ChannelConversationID   string `json:"channel_conversation_id,omitempty"`
	ChannelConversationType string `json:"channel_conversation_type,omitempty"`
	ConfirmToken            string `json:"confirm_token,omitempty"`
}

type channelAgentLinkRequest struct {
	BindingID int64  `json:"binding_id"`
	LinkToken string `json:"link_token"`
}

type channelAgentLinkTokenPayload struct {
	BindingID int64 `json:"binding_id"`
	ActorUID  int64 `json:"actor_uid"`
	AgentUID  int64 `json:"agent_uid"`
	ExpiresAt int64 `json:"expires_at"`
}

type channelAgentEntryResponse struct {
	*types.ChannelAgentEntry
	EntryURL     string `json:"entry_url"`
	ChannelQRURL string `json:"channel_qr_url,omitempty"`
}

// HandleAgentEntries handles authenticated owner entry management.
func (h *ChannelAgentBindingHandler) HandleAgentEntries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleListAgentEntries(w, r)
	case http.MethodPost:
		h.handleCreateAgentEntry(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// HandleAgentEntryByID handles POST /api/agent-entries/{id}/regenerate.
func (h *ChannelAgentBindingHandler) HandleAgentEntryByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/agent-entries/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "regenerate" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid entry id"})
		return
	}
	bindings, ok := h.bindingStore(w)
	if !ok {
		return
	}
	uid := UIDFromContext(r.Context())
	entry, err := bindings.RegenerateChannelAgentEntry(id, uid, mustGenerateSceneKey())
	if err != nil || entry == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entry": h.entryResponse(r, entry),
	})
}

// HandleAgentEntryPreview handles unauthenticated QR landing previews.
func (h *ChannelAgentBindingHandler) HandleAgentEntryPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	bindings, ok := h.bindingStore(w)
	if !ok {
		return
	}
	sceneKey := strings.TrimSpace(r.URL.Query().Get("scene_key"))
	if sceneKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing scene_key"})
		return
	}
	entry, err := bindings.GetChannelAgentEntryBySceneKey(sceneKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to preview entry"})
		return
	}
	if entry == nil || entry.Status != "active" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
		return
	}

	agent, err := h.db.GetUser(entry.AgentUID)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not available"})
		return
	}
	owner, _ := h.db.GetUser(entry.OwnerUID)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entry": h.entryResponse(r, entry),
		"agent": map[string]interface{}{
			"uid":          agent.ID,
			"username":     agent.Username,
			"display_name": displayNameOrUsername(agent.DisplayName, agent.Username),
			"avatar_url":   agent.AvatarURL,
			"is_online":    h.hub != nil && h.hub.IsOnline(agent.ID),
		},
		"owner": map[string]interface{}{
			"uid":          entry.OwnerUID,
			"display_name": displayNameOrUsername(userDisplayName(owner), userUsername(owner)),
		},
	})
}

// HandleConfirmChannelAgentBinding binds an external channel identity to an entry.
func (h *ChannelAgentBindingHandler) HandleConfirmChannelAgentBinding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	bindings, ok := h.bindingStore(w)
	if !ok {
		return
	}
	var req channelAgentConfirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if !authorizedChannelConfirm(r, req.ConfirmToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	req.SceneKey = strings.TrimSpace(req.SceneKey)
	req.ChannelUserID = strings.TrimSpace(req.ChannelUserID)
	if req.SceneKey == "" || req.ChannelUserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing scene_key or channel_user_id"})
		return
	}
	entry, err := bindings.GetChannelAgentEntryBySceneKey(req.SceneKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to bind entry"})
		return
	}
	if entry == nil || entry.Status != "active" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
		return
	}
	channel := normalizeChannel(req.Channel)
	if channel == "" {
		channel = entry.Channel
	}
	if channel != entry.Channel {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel does not match entry"})
		return
	}
	requestedAppID := strings.TrimSpace(req.ChannelAppID)
	entryAppID := strings.TrimSpace(entry.ChannelAppID)
	if entryAppID != "" && requestedAppID != "" && requestedAppID != entryAppID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel app does not match entry"})
		return
	}
	channelAppID := requestedAppID
	if channelAppID == "" {
		channelAppID = entryAppID
	}
	agent, _, err := h.requireAgentOwner(entry.OwnerUID, entry.AgentUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not available"})
		return
	}

	binding, err := bindings.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 channel,
		ChannelAppID:            channelAppID,
		ChannelUserID:           req.ChannelUserID,
		ChannelConversationID:   strings.TrimSpace(req.ChannelConversationID),
		ChannelConversationType: normalizeConversationType(req.ChannelConversationType),
		OwnerUID:                entry.OwnerUID,
		AgentUID:                entry.AgentUID,
		EntryID:                 entry.ID,
		Status:                  "active",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save binding"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          "bound",
		"binding":         binding,
		"device_link_url": channelBindingDeviceLinkURL(r, binding),
		"agent": map[string]interface{}{
			"uid":          agent.ID,
			"username":     agent.Username,
			"display_name": displayNameOrUsername(agent.DisplayName, agent.Username),
			"is_online":    h.hub != nil && h.hub.IsOnline(agent.ID),
		},
	})
}

// HandleResolveChannelAgentBinding resolves a channel identity for adapters.
func (h *ChannelAgentBindingHandler) HandleResolveChannelAgentBinding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !authorizedChannelResolve(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	bindings, ok := h.bindingStore(w)
	if !ok {
		return
	}
	query := types.ChannelAgentBindingQuery{
		Channel:                 normalizeChannel(r.URL.Query().Get("channel")),
		ChannelAppID:            strings.TrimSpace(r.URL.Query().Get("channel_app_id")),
		ChannelUserID:           strings.TrimSpace(r.URL.Query().Get("channel_user_id")),
		ChannelConversationID:   strings.TrimSpace(r.URL.Query().Get("channel_conversation_id")),
		ChannelConversationType: normalizeConversationType(r.URL.Query().Get("channel_conversation_type")),
	}
	if query.Channel == "" || query.ChannelUserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing channel or channel_user_id"})
		return
	}
	binding, err := bindings.ResolveChannelAgentBinding(query)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to resolve binding"})
		return
	}
	if binding == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"bound": false})
		return
	}
	agent, err := h.db.GetUser(binding.AgentUID)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"bound": false, "error": "agent unavailable"})
		return
	}
	bodyID, _ := h.db.GetBotBodyID(binding.AgentUID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"bound":            true,
		"binding":          binding,
		"agent_uid":        binding.AgentUID,
		"agent_id":         fmt.Sprintf("usr%d", binding.AgentUID),
		"agent_body_id":    bodyID,
		"owner_uid":        binding.OwnerUID,
		"canonical_uid":    binding.CanonicalUID,
		"device_link_url":  channelBindingDeviceLinkURL(r, binding),
		"device_linked":    binding.CanonicalUID > 0,
		"device_owner_uid": binding.CanonicalUID,
		"agent": map[string]interface{}{
			"uid":          agent.ID,
			"username":     agent.Username,
			"display_name": displayNameOrUsername(agent.DisplayName, agent.Username),
			"avatar_url":   agent.AvatarURL,
			"is_online":    h.hub != nil && h.hub.IsOnline(agent.ID),
		},
		"identity_trust":  "server_canonical",
		"identity_source": "channel_agent_binding",
	})
}

// HandleLinkChannelAgentBindingUser links a channel actor to the currently
// authenticated CatsCo user so device grants can target that user's devices
// while preserving the channel actor as the task initiator.
func (h *ChannelAgentBindingHandler) HandleLinkChannelAgentBindingUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	bindings, ok := h.bindingStore(w)
	if !ok {
		return
	}
	var req channelAgentLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	payload, err := verifyChannelBindingLinkToken(req.LinkToken)
	if err != nil || payload == nil || payload.BindingID != req.BindingID {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired link token"})
		return
	}
	canonicalUID := UIDFromContext(r.Context())
	if canonicalUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
		return
	}
	canonicalUser, err := h.db.GetUser(canonicalUID)
	if err != nil || canonicalUser == nil || canonicalUser.AccountType != types.AccountHuman || canonicalUser.State != 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only active human CatsCo users can link channel device authorization"})
		return
	}
	binding, err := bindings.LinkChannelAgentBindingCanonicalUser(payload.BindingID, payload.ActorUID, payload.AgentUID, canonicalUID)
	if err != nil {
		if errors.Is(err, store.ErrChannelAgentBindingAlreadyLinked) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "channel identity already linked to another CatsCo user"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to link channel identity"})
		return
	}
	if binding == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "binding not found or expired"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":           "linked",
		"binding":          binding,
		"actor_uid":        binding.ActorUID,
		"agent_uid":        binding.AgentUID,
		"canonical_uid":    binding.CanonicalUID,
		"device_owner_uid": binding.CanonicalUID,
		"device_linked":    binding.CanonicalUID > 0,
	})
}

func (h *ChannelAgentBindingHandler) handleListAgentEntries(w http.ResponseWriter, r *http.Request) {
	bindings, ok := h.bindingStore(w)
	if !ok {
		return
	}
	uid := UIDFromContext(r.Context())
	agentUID, err := strconv.ParseInt(r.URL.Query().Get("agent_uid"), 10, 64)
	if err != nil || agentUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent_uid"})
		return
	}
	if _, status, err := h.requireAgentOwner(uid, agentUID); err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	entries, err := bindings.ListChannelAgentEntries(uid, agentUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list entries"})
		return
	}
	resp := make([]channelAgentEntryResponse, 0, len(entries))
	for _, entry := range entries {
		resp = append(resp, h.entryResponse(r, entry))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": resp})
}

func (h *ChannelAgentBindingHandler) handleCreateAgentEntry(w http.ResponseWriter, r *http.Request) {
	bindings, ok := h.bindingStore(w)
	if !ok {
		return
	}
	uid := UIDFromContext(r.Context())
	var req channelAgentEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	channel := normalizeChannel(req.Channel)
	if channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported channel"})
		return
	}
	if _, status, err := h.requireAgentOwner(uid, req.AgentUID); err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	entry, err := bindings.EnsureChannelAgentEntry(&types.ChannelAgentEntry{
		SceneKey:     mustGenerateSceneKey(),
		Channel:      channel,
		ChannelAppID: canonicalEntryChannelAppID(channel, req.ChannelAppID),
		OwnerUID:     uid,
		AgentUID:     req.AgentUID,
		Status:       "active",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create entry"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entry": h.entryResponse(r, entry)})
}

func (h *ChannelAgentBindingHandler) requireAgentOwner(ownerUID, agentUID int64) (*types.User, int, error) {
	if ownerUID <= 0 || agentUID <= 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid agent_uid")
	}
	user, err := h.db.GetUser(agentUID)
	if err != nil || user == nil {
		return nil, http.StatusNotFound, fmt.Errorf("agent not found")
	}
	if user.AccountType != types.AccountBot {
		return nil, http.StatusBadRequest, fmt.Errorf("user is not an agent")
	}
	if user.State != 0 {
		return nil, http.StatusNotFound, fmt.Errorf("agent not available")
	}
	actualOwner, err := h.db.GetBotOwner(agentUID)
	if err != nil || actualOwner != ownerUID {
		return nil, http.StatusForbidden, fmt.Errorf("agent is not owned by current user")
	}
	return user, 0, nil
}

func (h *ChannelAgentBindingHandler) bindingStore(w http.ResponseWriter) (store.ChannelAgentBindingStore, bool) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "channel agent binding store not configured"})
		return nil, false
	}
	return bindings, true
}

func (h *ChannelAgentBindingHandler) entryResponse(r *http.Request, entry *types.ChannelAgentEntry) channelAgentEntryResponse {
	resp := channelAgentEntryResponse{
		ChannelAgentEntry: entry,
		EntryURL:          entryURL(r, entry.SceneKey),
	}
	if entry != nil && entry.Channel == "weixin" && weixinQRCodeConfiguredFromEnv() && entry.ChannelAppID == configuredWeixinAppID() {
		resp.ChannelQRURL = publicBaseURL(r) + weixinQRCodePath(entry.SceneKey)
	}
	return resp
}

func configuredWeixinAppID() string {
	return strings.TrimSpace(firstEnv("CATSCO_WEIXIN_APP_ID", "CATSCO_WECHAT_APP_ID", "WEIXIN_APP_ID", "WECHAT_APP_ID"))
}

func canonicalEntryChannelAppID(channel, requested string) string {
	if normalizeChannel(channel) == "feishu" {
		if appID := strings.TrimSpace(firstEnv("CATSCO_FEISHU_APP_ID", "FEISHU_APP_ID")); appID != "" {
			return appID
		}
	}
	if normalizeChannel(channel) == "weixin" {
		if appID := configuredWeixinAppID(); appID != "" {
			return appID
		}
	}
	return strings.TrimSpace(requested)
}

func normalizeChannel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "weixin", "wechat":
		return "weixin"
	case "feishu", "lark":
		return "feishu"
	default:
		return ""
	}
}

func normalizeConversationType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "group":
		return "group"
	default:
		return "p2p"
	}
}

func mustGenerateSceneKey() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func entryURL(r *http.Request, sceneKey string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL")), "/")
	if base == "" && r != nil {
		proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
		if proto == "" {
			if r.TLS != nil {
				proto = "https"
			} else {
				proto = "http"
			}
		}
		host := r.Host
		if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
			host = forwardedHost
		}
		base = proto + "://" + host
	}
	if base == "" {
		base = "https://app.catsco.cc"
	}
	return base + "/e/" + sceneKey
}

func channelBindingDeviceLinkURL(r *http.Request, binding *types.ChannelAgentBinding) string {
	if binding == nil || binding.ID <= 0 || binding.ActorUID <= 0 || binding.AgentUID <= 0 {
		return ""
	}
	token, err := signChannelBindingLinkToken(channelAgentLinkTokenPayload{
		BindingID: binding.ID,
		ActorUID:  binding.ActorUID,
		AgentUID:  binding.AgentUID,
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
	})
	if err != nil || token == "" {
		return ""
	}
	return publicBaseURL(r) + "/channel-device-link?binding_id=" + strconv.FormatInt(binding.ID, 10) + "&link_token=" + token
}

func signChannelBindingLinkToken(payload channelAgentLinkTokenPayload) (string, error) {
	secret := channelBindingLinkSecret()
	if secret == "" || payload.BindingID <= 0 || payload.ActorUID <= 0 || payload.AgentUID <= 0 || payload.ExpiresAt <= 0 {
		return "", fmt.Errorf("invalid channel binding link token")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadB64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadB64 + "." + sig, nil
}

func verifyChannelBindingLinkToken(raw string) (*channelAgentLinkTokenPayload, error) {
	secret := channelBindingLinkSecret()
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if secret == "" || len(parts) != 2 {
		return nil, fmt.Errorf("invalid channel binding link token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return nil, fmt.Errorf("invalid channel binding link token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var out channelAgentLinkTokenPayload
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	if out.BindingID <= 0 || out.ActorUID <= 0 || out.AgentUID <= 0 || time.Now().Unix() > out.ExpiresAt {
		return nil, fmt.Errorf("expired channel binding link token")
	}
	return &out, nil
}

func channelBindingLinkSecret() string {
	if secret := strings.TrimSpace(firstEnv(
		"CATSCO_CHANNEL_BINDING_LINK_SECRET",
		"CATSCO_CHANNEL_BINDING_TOKEN",
		"CATSCO_FEISHU_APP_SECRET",
		"FEISHU_APP_SECRET",
		"CATSCO_WEIXIN_APP_SECRET",
		"CATSCO_WECHAT_APP_SECRET",
		"WEIXIN_APP_SECRET",
		"WECHAT_APP_SECRET",
	)); secret != "" {
		return secret
	}
	if !isProductionLikeEnv() {
		return "catsco-dev-channel-binding-link-secret"
	}
	return ""
}

func authorizedChannelResolve(r *http.Request) bool {
	required := strings.TrimSpace(os.Getenv("CATSCO_CHANNEL_BINDING_TOKEN"))
	if required == "" {
		return !isProductionLikeEnv()
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") && constantTimeStringEqual(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")), required) {
		return true
	}
	return false
}

func authorizedChannelConfirm(r *http.Request, payloadToken string) bool {
	required := strings.TrimSpace(os.Getenv("CATSCO_CHANNEL_BINDING_TOKEN"))
	if required == "" {
		return !isProductionLikeEnv()
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") && constantTimeStringEqual(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")), required) {
		return true
	}
	if constantTimeStringEqual(strings.TrimSpace(payloadToken), required) {
		return true
	}
	return constantTimeStringEqual(strings.TrimSpace(r.URL.Query().Get("confirm_token")), required)
}

func constantTimeStringEqual(got, expected string) bool {
	if got == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func userDisplayName(user *types.User) string {
	if user == nil {
		return ""
	}
	return user.DisplayName
}

func userUsername(user *types.User) string {
	if user == nil {
		return ""
	}
	return user.Username
}

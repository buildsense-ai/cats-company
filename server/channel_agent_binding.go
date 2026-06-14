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
	"net/url"
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
	AccessMode   string `json:"access_mode,omitempty"`
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
	EntryURL          string                   `json:"entry_url"`
	ChannelQRURL      string                   `json:"channel_qr_url,omitempty"`
	FeishuOAuthURL    string                   `json:"feishu_oauth_url,omitempty"`
	FeishuEntryStatus *feishuEntryConfigStatus `json:"feishu_entry_status,omitempty"`
	QRValue           string                   `json:"qr_value,omitempty"`
	QRKind            string                   `json:"qr_kind,omitempty"`
}

type feishuEntryConfigStatus struct {
	Ready                      bool     `json:"ready"`
	Status                     string   `json:"status"`
	Reasons                    []string `json:"reasons,omitempty"`
	AppIDConfigured            bool     `json:"app_id_configured"`
	AppSecretConfigured        bool     `json:"app_secret_configured"`
	EntryAppIDMatches          bool     `json:"entry_app_id_matches"`
	NativeTemplateConfigured   bool     `json:"native_template_configured"`
	NativeTemplateCarriesScene bool     `json:"native_template_carries_scene"`
	NativeURLValid             bool     `json:"native_url_valid"`
	NativeShortURL             string   `json:"native_short_url,omitempty"`
	NativeURL                  string   `json:"native_url,omitempty"`
	LandingURL                 string   `json:"landing_url,omitempty"`
	OAuthURL                   string   `json:"oauth_url,omitempty"`
	OAuthCallbackURL           string   `json:"oauth_callback_url,omitempty"`
	EventsCallbackURL          string   `json:"events_callback_url,omitempty"`
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
	current, err := bindings.GetChannelAgentEntryByID(id)
	if err != nil || current == nil || current.OwnerUID != uid || current.Status != "active" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found"})
		return
	}
	entry, err := bindings.RegenerateChannelAgentEntry(id, uid, mustGenerateSceneKey(), channelEntryAppIDForRegeneration(current))
	if err != nil || entry == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entry": h.entryResponse(r, entry),
	})
}

func channelEntryAppIDForRegeneration(entry *types.ChannelAgentEntry) string {
	if entry == nil {
		return ""
	}
	switch entry.Channel {
	case "feishu":
		if appID := configuredFeishuAppID(); appID != "" {
			return appID
		}
	case "weixin":
		if appID := configuredWeixinAppID(); appID != "" {
			return appID
		}
	}
	return entry.ChannelAppID
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

	actorUID, err := h.ensureChannelActor(channel, channelAppID, req.ChannelUserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create channel actor"})
		return
	}
	binding, accessRequest, err := bindOrRequestChannelAgentAccess(
		h.db,
		bindings,
		entry,
		actorUID,
		channel,
		channelAppID,
		req.ChannelUserID,
		strings.TrimSpace(req.ChannelConversationID),
		normalizeConversationType(req.ChannelConversationType),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save binding"})
		return
	}
	if binding != nil {
		if _, err := bindings.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
			Channel:                 channel,
			ChannelAppID:            channelAppID,
			ChannelUserID:           strings.TrimSpace(req.ChannelUserID),
			ChannelConversationID:   strings.TrimSpace(req.ChannelConversationID),
			ChannelConversationType: normalizeConversationType(req.ChannelConversationType),
			ActorUID:                actorUID,
			AgentUID:                binding.AgentUID,
			Source:                  "entry_confirm",
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save channel route"})
			return
		}
	}
	if accessRequest != nil && binding == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":         "pending_approval",
			"access_request": accessRequest,
			"agent": map[string]interface{}{
				"uid":          agent.ID,
				"username":     agent.Username,
				"display_name": displayNameOrUsername(agent.DisplayName, agent.Username),
				"is_online":    h.hub != nil && h.hub.IsOnline(agent.ID),
			},
		})
		return
	}
	if channelBindingNeedsCatsCoLogin(binding) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":           "needs_catsco_login",
			"binding":          binding,
			"account_link_url": channelBindingDeviceLinkURL(r, binding),
			"agent": map[string]interface{}{
				"uid":          agent.ID,
				"username":     agent.Username,
				"display_name": displayNameOrUsername(agent.DisplayName, agent.Username),
				"is_online":    h.hub != nil && h.hub.IsOnline(agent.ID),
			},
		})
		return
	}
	if channelBindingRejected(binding) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":  "rejected",
			"binding": binding,
			"agent": map[string]interface{}{
				"uid":          agent.ID,
				"username":     agent.Username,
				"display_name": displayNameOrUsername(agent.DisplayName, agent.Username),
				"is_online":    h.hub != nil && h.hub.IsOnline(agent.ID),
			},
		})
		return
	}
	if pending, err := channelBindingPendingFriendApproval(h.db, binding); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check channel access"})
		return
	} else if pending {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":  "pending_approval",
			"binding": binding,
			"agent": map[string]interface{}{
				"uid":          agent.ID,
				"username":     agent.Username,
				"display_name": displayNameOrUsername(agent.DisplayName, agent.Username),
				"is_online":    h.hub != nil && h.hub.IsOnline(agent.ID),
			},
		})
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
		AgentUID:                parseOptionalInt64(r.URL.Query().Get("agent_uid")),
		ActorUID:                parseOptionalInt64(r.URL.Query().Get("actor_uid")),
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
		if access, accessErr := bindings.ResolveChannelAgentAccessRequest(query); accessErr == nil && access != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"bound":          false,
				"status":         access.Status,
				"access_request": access,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"bound": false})
		return
	}
	agent, err := h.db.GetUser(binding.AgentUID)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"bound": false, "error": "agent unavailable"})
		return
	}
	if channelBindingNeedsCatsCoLogin(binding) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bound":            false,
			"status":           "needs_catsco_login",
			"binding":          binding,
			"agent_uid":        binding.AgentUID,
			"owner_uid":        binding.OwnerUID,
			"account_link_url": channelBindingDeviceLinkURL(r, binding),
			"device_link_url":  channelBindingDeviceLinkURL(r, binding),
			"device_linked":    false,
		})
		return
	}
	if channelBindingRejected(binding) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bound":         false,
			"status":        "rejected",
			"binding":       binding,
			"agent_uid":     binding.AgentUID,
			"owner_uid":     binding.OwnerUID,
			"canonical_uid": binding.CanonicalUID,
		})
		return
	}
	if pending, err := channelBindingPendingFriendApproval(h.db, binding); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check channel access"})
		return
	} else if pending {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bound":         false,
			"status":        "pending_approval",
			"binding":       binding,
			"agent_uid":     binding.AgentUID,
			"owner_uid":     binding.OwnerUID,
			"canonical_uid": binding.CanonicalUID,
		})
		return
	}
	if err := validateDeliverableChannelBinding(h.db, binding); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"bound":         false,
			"status":        "not_allowed",
			"binding":       binding,
			"agent_uid":     binding.AgentUID,
			"owner_uid":     binding.OwnerUID,
			"canonical_uid": binding.CanonicalUID,
		})
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
	if channelBindingRejected(binding) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":           "rejected",
			"binding":          binding,
			"actor_uid":        binding.ActorUID,
			"agent_uid":        binding.AgentUID,
			"canonical_uid":    binding.CanonicalUID,
			"device_owner_uid": binding.CanonicalUID,
			"device_linked":    false,
		})
		return
	}
	publicAccess, err := channelBindingEntryAllowsPublicAccess(h.db, binding)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check channel access"})
		return
	}
	status := "linked"
	if pending, err := channelBindingPendingFriendApproval(h.db, binding); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check channel access"})
		return
	} else if pending {
		if _, err := h.db.CreateFriendRequest(canonicalUID, binding.AgentUID, channelAgentAccessFriendMessage(binding.Channel)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to request virtual employee access"})
			return
		}
		if updater, ok := h.db.(store.ChannelAgentBindingStore); ok {
			if refreshed, err := updater.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
				Channel:                 binding.Channel,
				ChannelAppID:            binding.ChannelAppID,
				ChannelUserID:           binding.ChannelUserID,
				ChannelConversationID:   binding.ChannelConversationID,
				ChannelConversationType: binding.ChannelConversationType,
				ActorUID:                binding.ActorUID,
				CanonicalUID:            binding.CanonicalUID,
				OwnerUID:                binding.OwnerUID,
				AgentUID:                binding.AgentUID,
				EntryID:                 binding.EntryID,
				Status:                  types.ChannelAgentBindingPendingApproval,
			}); err == nil && refreshed != nil {
				binding = refreshed
			}
		}
		status = "pending_approval"
	} else if activator, ok := h.db.(store.ChannelAgentBindingStore); ok {
		if publicAccess {
			if refreshed, err := activator.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
				Channel:                 binding.Channel,
				ChannelAppID:            binding.ChannelAppID,
				ChannelUserID:           binding.ChannelUserID,
				ChannelConversationID:   binding.ChannelConversationID,
				ChannelConversationType: binding.ChannelConversationType,
				ActorUID:                binding.ActorUID,
				CanonicalUID:            binding.CanonicalUID,
				OwnerUID:                binding.OwnerUID,
				AgentUID:                binding.AgentUID,
				EntryID:                 binding.EntryID,
				Status:                  types.ChannelAgentBindingActive,
			}); err == nil && refreshed != nil {
				binding = refreshed
			} else if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to activate channel access"})
				return
			}
		} else {
			if _, err := activator.ActivateChannelAgentBindingsForCanonicalUser(canonicalUID, binding.AgentUID, canonicalUID); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to activate channel access"})
				return
			}
			if refreshed, err := activator.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
				Channel:                 binding.Channel,
				ChannelAppID:            binding.ChannelAppID,
				ChannelUserID:           binding.ChannelUserID,
				ChannelConversationID:   binding.ChannelConversationID,
				ChannelConversationType: binding.ChannelConversationType,
			}); err == nil && refreshed != nil {
				binding = refreshed
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":           status,
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

func (h *ChannelAgentBindingHandler) HandleListAgentChannelBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
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
	rows, err := bindings.ListChannelAgentBindingsForAgent(uid, agentUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list channel bindings"})
		return
	}
	items := make([]map[string]interface{}, 0, len(rows))
	for _, binding := range rows {
		accessMode := types.ChannelAgentAccessApprovalRequired
		if binding.EntryID > 0 {
			if entry, err := bindings.GetChannelAgentEntryByID(binding.EntryID); err == nil && entry != nil {
				accessMode = types.NormalizeChannelAgentAccessMode(entry.AccessMode)
			}
		}
		item := map[string]interface{}{
			"binding":       binding,
			"status":        channelBindingManagementStatus(binding),
			"channel":       binding.Channel,
			"channel_user":  binding.ChannelUserID,
			"access_mode":   accessMode,
			"canonical_uid": binding.CanonicalUID,
			"updated_at":    binding.UpdatedAt,
		}
		if binding.CanonicalUID > 0 {
			if user, err := h.db.GetUser(binding.CanonicalUID); err == nil && user != nil {
				item["user"] = map[string]interface{}{
					"uid":          user.ID,
					"username":     user.Username,
					"display_name": displayNameOrUsername(user.DisplayName, user.Username),
					"avatar_url":   user.AvatarURL,
				}
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"bindings": items})
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
		AccessMode:   normalizeChannelAgentAccessModeForCreate(req.AccessMode),
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

func channelBindingManagementStatus(binding *types.ChannelAgentBinding) string {
	if binding == nil {
		return ""
	}
	switch binding.Status {
	case types.ChannelAgentBindingActive:
		return "approved"
	case types.ChannelAgentBindingPendingApproval:
		return "pending"
	case types.ChannelAgentBindingPendingLogin:
		return "needs_login"
	case types.ChannelAgentBindingRejected:
		return "rejected"
	default:
		return binding.Status
	}
}

func (h *ChannelAgentBindingHandler) ensureChannelActor(channel, appID, channelUserID string) (int64, error) {
	username := channelActorUsername(channel, appID, channelUserID)
	if user, err := h.db.GetUserByUsername(username); err == nil && user != nil {
		return user.ID, nil
	} else if err != nil {
		return 0, err
	}
	displayName := "Channel User"
	switch normalizeChannel(channel) {
	case "feishu":
		displayName = "Feishu User"
	case "weixin":
		displayName = "Weixin User"
	}
	uid, err := h.db.CreateUser(&types.User{
		Username:    username,
		DisplayName: displayName,
		AccountType: types.AccountHuman,
		PassHash:    []byte("external-channel-account"),
		State:       0,
	})
	if err != nil {
		if user, lookupErr := h.db.GetUserByUsername(username); lookupErr == nil && user != nil {
			return user.ID, nil
		}
		return 0, err
	}
	return uid, nil
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
		QRValue:           entryURL(r, entry.SceneKey),
		QRKind:            "web_entry",
	}
	if entry != nil && entry.Channel == "weixin" && weixinQRCodeConfiguredFromEnv() && entry.ChannelAppID == configuredWeixinAppID() {
		resp.ChannelQRURL = publicBaseURL(r) + weixinQRCodePath(entry.SceneKey)
		resp.QRValue = resp.ChannelQRURL
		resp.QRKind = "weixin_official_qr"
	}
	if entry != nil && entry.Channel == "feishu" {
		resp.QRValue = ""
		resp.QRKind = "feishu_native_unconfigured"
		feishuStatus := buildFeishuEntryConfigStatus(r, entry)
		resp.FeishuEntryStatus = feishuStatus
		appID := configuredFeishuAppID()
		resp.FeishuOAuthURL = feishuOAuthStartURL(r, entry.SceneKey)
		if feishuStatus.Ready {
			resp.ChannelQRURL = feishuStatus.NativeShortURL
			resp.QRValue = resp.ChannelQRURL
			resp.QRKind = "feishu_native_entry"
		}
		if appID == "" {
			resp.FeishuOAuthURL = ""
		}
	}
	return resp
}

func configuredFeishuAppID() string {
	return strings.TrimSpace(firstEnv("CATSCO_FEISHU_APP_ID", "FEISHU_APP_ID"))
}

func configuredFeishuAppSecret() string {
	return strings.TrimSpace(firstEnv("CATSCO_FEISHU_APP_SECRET", "FEISHU_APP_SECRET"))
}

func configuredFeishuEventVerificationToken() string {
	return strings.TrimSpace(firstEnv("CATSCO_FEISHU_EVENT_VERIFICATION_TOKEN", "FEISHU_EVENT_VERIFICATION_TOKEN"))
}

func configuredFeishuEntryURLTemplate() string {
	return strings.TrimSpace(firstEnv(
		"CATSCO_FEISHU_ENTRY_URL_TEMPLATE",
		"CATSCO_FEISHU_NATIVE_ENTRY_URL_TEMPLATE",
		"FEISHU_ENTRY_URL_TEMPLATE",
	))
}

func buildFeishuEntryConfigStatus(r *http.Request, entry *types.ChannelAgentEntry) *feishuEntryConfigStatus {
	status := &feishuEntryConfigStatus{
		Status:                   "unconfigured",
		LandingURL:               entryURL(r, safeEntrySceneKey(entry)),
		OAuthURL:                 feishuOAuthStartURL(r, safeEntrySceneKey(entry)),
		OAuthCallbackURL:         publicBaseURL(r) + "/api/channel-agent-bindings/oauth/feishu/callback",
		EventsCallbackURL:        publicBaseURL(r) + "/api/channels/feishu/events",
		NativeTemplateConfigured: configuredFeishuEntryURLTemplate() != "",
	}
	if entry == nil {
		status.Reasons = append(status.Reasons, "入口不存在")
		return status
	}
	appID := configuredFeishuAppID()
	status.AppIDConfigured = appID != ""
	status.AppSecretConfigured = configuredFeishuAppSecret() != ""
	status.EntryAppIDMatches = feishuEntryMatchesAppID(entry, appID)
	status.NativeTemplateCarriesScene = feishuEntryTemplateCarriesScene(configuredFeishuEntryURLTemplate())
	if appID == "" {
		status.Status = "missing_app_id"
		status.Reasons = append(status.Reasons, "缺少 CATSCO_FEISHU_APP_ID")
	}
	if !status.AppSecretConfigured {
		if status.Status == "unconfigured" {
			status.Status = "missing_app_secret"
		}
		status.Reasons = append(status.Reasons, "缺少 CATSCO_FEISHU_APP_SECRET，飞书 OAuth 无法确认用户身份")
	}
	if !status.EntryAppIDMatches {
		if status.Status == "unconfigured" {
			status.Status = "app_mismatch"
		}
		status.Reasons = append(status.Reasons, "入口所属飞书 AppID 与当前服务配置不一致，请重新生成飞书入口码")
	}
	if !status.NativeTemplateConfigured {
		if status.Status == "unconfigured" {
			status.Status = "missing_native_template"
		}
		status.Reasons = append(status.Reasons, "缺少 CATSCO_FEISHU_ENTRY_URL_TEMPLATE，无法生成飞书原生应用入口")
	}
	if status.NativeTemplateConfigured && !status.NativeTemplateCarriesScene {
		if status.Status == "unconfigured" {
			status.Status = "native_template_missing_scene"
		}
		status.Reasons = append(status.Reasons, "飞书原生入口模板必须包含 {landing_url_encoded}、{oauth_url_encoded} 或 {scene_key}，否则扫码后无法知道要添加哪个虚拟员工")
	}
	if isProductionLikeEnv() && configuredFeishuEventVerificationToken() == "" {
		if status.Status == "unconfigured" {
			status.Status = "missing_event_token"
		}
		status.Reasons = append(status.Reasons, "生产环境缺少 CATSCO_FEISHU_EVENT_VERIFICATION_TOKEN，飞书消息回调应 fail-closed")
	}
	nativeURL := feishuNativeEntryURL(r, entry)
	status.NativeURL = nativeURL
	status.NativeURLValid = nativeURL == "" || isUsableRedirectURL(nativeURL)
	if nativeURL != "" && !status.NativeURLValid {
		if status.Status == "unconfigured" {
			status.Status = "invalid_native_url"
		}
		status.Reasons = append(status.Reasons, "CATSCO_FEISHU_ENTRY_URL_TEMPLATE 渲染后不是有效入口 URL")
	}
	if len(status.Reasons) == 0 {
		status.Ready = true
		status.Status = "ready"
		status.NativeShortURL = feishuNativeEntryShortURL(r, entry.SceneKey)
	}
	return status
}

func feishuEntryMatchesAppID(entry *types.ChannelAgentEntry, appID string) bool {
	if entry == nil {
		return false
	}
	return strings.TrimSpace(entry.ChannelAppID) != "" && strings.TrimSpace(entry.ChannelAppID) == strings.TrimSpace(appID)
}

func safeEntrySceneKey(entry *types.ChannelAgentEntry) string {
	if entry == nil {
		return ""
	}
	return entry.SceneKey
}

func feishuNativeEntryURL(r *http.Request, entry *types.ChannelAgentEntry) string {
	template := configuredFeishuEntryURLTemplate()
	if template == "" || entry == nil {
		return ""
	}
	entryURLValue := entryURL(r, entry.SceneKey)
	oauthURLValue := feishuOAuthStartURL(r, entry.SceneKey)
	shortURLValue := feishuNativeEntryShortURL(r, entry.SceneKey)
	appID := strings.TrimSpace(entry.ChannelAppID)
	if appID == "" {
		appID = configuredFeishuAppID()
	}
	replacer := strings.NewReplacer(
		"{scene_key}", entry.SceneKey,
		"{scene_key_encoded}", url.QueryEscape(entry.SceneKey),
		"{app_id}", appID,
		"{app_id_encoded}", url.QueryEscape(appID),
		"{agent_uid}", strconv.FormatInt(entry.AgentUID, 10),
		"{owner_uid}", strconv.FormatInt(entry.OwnerUID, 10),
		"{entry_url}", entryURLValue,
		"{entry_url_encoded}", url.QueryEscape(entryURLValue),
		"{landing_url}", entryURLValue,
		"{landing_url_encoded}", url.QueryEscape(entryURLValue),
		"{oauth_url}", oauthURLValue,
		"{oauth_url_encoded}", url.QueryEscape(oauthURLValue),
		"{native_short_url}", shortURLValue,
		"{native_short_url_encoded}", url.QueryEscape(shortURLValue),
	)
	return strings.TrimSpace(replacer.Replace(template))
}

func feishuEntryTemplateCarriesScene(template string) bool {
	template = strings.TrimSpace(template)
	if template == "" {
		return false
	}
	for _, marker := range []string{
		"{scene_key}",
		"{scene_key_encoded}",
		"{entry_url}",
		"{entry_url_encoded}",
		"{landing_url}",
		"{landing_url_encoded}",
		"{oauth_url}",
		"{oauth_url_encoded}",
	} {
		if strings.Contains(template, marker) {
			return true
		}
	}
	return false
}

func isUsableRedirectURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.Scheme == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "http", "lark", "feishu":
		return true
	default:
		return false
	}
}

func feishuNativeEntryShortURL(r *http.Request, sceneKey string) string {
	return publicBaseURL(r) + "/api/fn/" + url.PathEscape(sceneKey)
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

func normalizeChannelAgentAccessModeForCreate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return types.ChannelAgentAccessApprovalRequired
	}
	return types.NormalizeChannelAgentAccessMode(value)
}

func bindOrRequestChannelAgentAccess(
	db store.Store,
	bindings store.ChannelAgentBindingStore,
	entry *types.ChannelAgentEntry,
	actorUID int64,
	channel string,
	channelAppID string,
	channelUserID string,
	conversationID string,
	conversationType string,
) (*types.ChannelAgentBinding, *types.ChannelAgentAccessRequest, error) {
	if db == nil || bindings == nil || entry == nil || actorUID <= 0 || strings.TrimSpace(channelUserID) == "" {
		return nil, nil, fmt.Errorf("invalid channel agent access")
	}
	channel = normalizeChannel(channel)
	if channel == "" {
		channel = entry.Channel
	}
	conversationID = strings.TrimSpace(conversationID)
	conversationType = normalizeConversationType(conversationType)
	query := types.ChannelAgentBindingQuery{
		Channel:                 channel,
		ChannelAppID:            strings.TrimSpace(channelAppID),
		ChannelUserID:           strings.TrimSpace(channelUserID),
		ChannelConversationID:   conversationID,
		ChannelConversationType: conversationType,
		AgentUID:                entry.AgentUID,
	}
	binding, err := bindings.ResolveChannelAgentBinding(query)
	if err != nil {
		return nil, nil, err
	}
	if binding != nil && binding.Status == types.ChannelAgentBindingRejected {
		return binding, nil, nil
	}
	if channelBindingTargetsEntry(binding, entry) {
		return binding, nil, nil
	}
	status := types.ChannelAgentBindingPendingLogin
	canonicalUID := int64(0)
	if binding != nil && binding.CanonicalUID > 0 {
		canonicalUID = binding.CanonicalUID
	} else if conversationID != "" {
		baseBinding, err := bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
			Channel:                 channel,
			ChannelAppID:            strings.TrimSpace(channelAppID),
			ChannelUserID:           strings.TrimSpace(channelUserID),
			ChannelConversationType: conversationType,
			AgentUID:                entry.AgentUID,
		})
		if err != nil {
			return nil, nil, err
		}
		if baseBinding != nil && baseBinding.CanonicalUID > 0 {
			canonicalUID = baseBinding.CanonicalUID
		}
	}
	if canonicalUID == 0 {
		identityBinding, err := bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
			Channel:                 channel,
			ChannelAppID:            strings.TrimSpace(channelAppID),
			ChannelUserID:           strings.TrimSpace(channelUserID),
			ChannelConversationID:   conversationID,
			ChannelConversationType: conversationType,
		})
		if err != nil {
			return nil, nil, err
		}
		if identityBinding != nil && identityBinding.CanonicalUID > 0 {
			canonicalUID = identityBinding.CanonicalUID
		}
	}
	if canonicalUID == 0 && conversationID != "" {
		identityBinding, err := bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
			Channel:                 channel,
			ChannelAppID:            strings.TrimSpace(channelAppID),
			ChannelUserID:           strings.TrimSpace(channelUserID),
			ChannelConversationType: conversationType,
		})
		if err != nil {
			return nil, nil, err
		}
		if identityBinding != nil && identityBinding.CanonicalUID > 0 {
			canonicalUID = identityBinding.CanonicalUID
		}
	}
	if canonicalUID > 0 {
		nextStatus, createFriendRequest, err := channelBindingStatusForEntryCanonicalUser(db, entry, canonicalUID)
		if err != nil {
			return nil, nil, err
		}
		status = nextStatus
		if createFriendRequest {
			if _, err := db.CreateFriendRequest(canonicalUID, entry.AgentUID, channelAgentAccessFriendMessage(channel)); err != nil {
				return nil, nil, err
			}
		}
	}
	binding, err = bindings.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 channel,
		ChannelAppID:            strings.TrimSpace(channelAppID),
		ChannelUserID:           strings.TrimSpace(channelUserID),
		ChannelConversationID:   conversationID,
		ChannelConversationType: conversationType,
		ActorUID:                actorUID,
		CanonicalUID:            canonicalUID,
		OwnerUID:                entry.OwnerUID,
		AgentUID:                entry.AgentUID,
		EntryID:                 entry.ID,
		Status:                  status,
	})
	return binding, nil, err
}

func channelBindingTargetsEntry(binding *types.ChannelAgentBinding, entry *types.ChannelAgentEntry) bool {
	if binding == nil || entry == nil {
		return false
	}
	if binding.AgentUID != entry.AgentUID || binding.OwnerUID != entry.OwnerUID {
		return false
	}
	if entry.ID > 0 && binding.EntryID > 0 && binding.EntryID != entry.ID {
		return false
	}
	if entry.Channel != "" && normalizeChannel(binding.Channel) != normalizeChannel(entry.Channel) {
		return false
	}
	if strings.TrimSpace(entry.ChannelAppID) != "" && strings.TrimSpace(binding.ChannelAppID) != strings.TrimSpace(entry.ChannelAppID) {
		return false
	}
	return true
}

func channelBindingStatusForCanonicalUser(db store.Store, canonicalUID, agentUID int64) (string, bool, error) {
	if db == nil || canonicalUID <= 0 || agentUID <= 0 {
		return types.ChannelAgentBindingPendingLogin, false, nil
	}
	ownerUID, err := db.GetBotOwner(agentUID)
	if err != nil {
		return "", false, err
	}
	if ownerUID == canonicalUID {
		return types.ChannelAgentBindingActive, false, nil
	}
	allowed, err := db.AreFriends(canonicalUID, agentUID)
	if err != nil {
		return "", false, err
	}
	if allowed {
		return types.ChannelAgentBindingActive, false, nil
	}
	return types.ChannelAgentBindingPendingApproval, true, nil
}

func channelBindingStatusForEntryCanonicalUser(db store.Store, entry *types.ChannelAgentEntry, canonicalUID int64) (string, bool, error) {
	if entry != nil && types.NormalizeChannelAgentAccessMode(entry.AccessMode) == types.ChannelAgentAccessPublic {
		if canonicalUID > 0 {
			return types.ChannelAgentBindingActive, false, nil
		}
		return types.ChannelAgentBindingPendingLogin, false, nil
	}
	if entry == nil {
		return types.ChannelAgentBindingPendingLogin, false, nil
	}
	return channelBindingStatusForCanonicalUser(db, canonicalUID, entry.AgentUID)
}

func channelAgentAccessFriendMessage(channel string) string {
	switch normalizeChannel(channel) {
	case "feishu":
		return "通过飞书扫码申请添加该虚拟员工"
	case "weixin":
		return "通过微信扫码申请添加该虚拟员工"
	default:
		return "通过入口码申请添加该虚拟员工"
	}
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

func parseOptionalInt64(value string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if n < 0 {
		return 0
	}
	return n
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

func channelBindingDeviceLinkGuidance(db store.Store, r *http.Request, binding *types.ChannelAgentBinding) string {
	if binding == nil || binding.CanonicalUID > 0 {
		return ""
	}
	link := channelBindingDeviceLinkURL(r, binding)
	if link == "" {
		return "你还没有登录绑定 CatsCo 账号。请联系管理员检查账号绑定链接配置。"
	}
	if ok, err := channelBindingEntryAllowsPublicAccess(db, binding); err == nil && ok {
		return "请先登录 CatsCo 账号完成身份验证。验证后就可以继续和该虚拟员工对话；如果后续需要操作你的电脑，也只会使用你自己账号名下授权的设备：\n" + link
	}
	return "请先登录 CatsCo 账号并申请添加该虚拟员工。通过后，我才能在这里继续为你服务；如果后续需要操作你的电脑，也只会使用你自己账号名下授权的设备：\n" + link
}

func channelBindingNeedsCatsCoLogin(binding *types.ChannelAgentBinding) bool {
	return binding != nil && (binding.CanonicalUID <= 0 || binding.Status == types.ChannelAgentBindingPendingLogin)
}

func channelBindingRejected(binding *types.ChannelAgentBinding) bool {
	return binding != nil && binding.Status == types.ChannelAgentBindingRejected
}

func resolveDeliverableChannelBinding(db store.Store, actorUID, agentUID int64) (*types.ChannelAgentBinding, error) {
	if db == nil || actorUID <= 0 || agentUID <= 0 {
		return nil, errors.New("invalid channel binding scope")
	}
	bindings, ok := db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	binding, err := bindings.ResolveChannelAgentBindingForActorAny(actorUID, agentUID)
	if err != nil {
		return nil, err
	}
	if err := validateDeliverableChannelBinding(db, binding); err != nil {
		return nil, err
	}
	return binding, nil
}

func validateDeliverableChannelBinding(db store.Store, binding *types.ChannelAgentBinding) error {
	if db == nil || binding == nil {
		return errors.New("channel binding not approved")
	}
	if binding.Status != types.ChannelAgentBindingActive {
		return fmt.Errorf("channel binding is %s", binding.Status)
	}
	if binding.CanonicalUID <= 0 {
		return errors.New("channel binding is not linked to a CatsCo user")
	}
	canonical, err := db.GetUser(binding.CanonicalUID)
	if err != nil {
		return err
	}
	if canonical == nil || canonical.AccountType != types.AccountHuman || canonical.State != 0 {
		return errors.New("channel binding user is not an active human account")
	}
	if ownerUID, err := db.GetBotOwner(binding.AgentUID); err == nil && ownerUID == binding.CanonicalUID {
		return nil
	} else if err != nil {
		return err
	}
	if ok, err := channelBindingEntryAllowsPublicAccess(db, binding); err != nil {
		return err
	} else if ok {
		return nil
	}
	allowed, err := db.AreFriends(binding.CanonicalUID, binding.AgentUID)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("channel binding is waiting for virtual employee approval")
	}
	return nil
}

func channelBindingPendingFriendApproval(db store.Store, binding *types.ChannelAgentBinding) (bool, error) {
	if db == nil || binding == nil || binding.CanonicalUID <= 0 || binding.AgentUID <= 0 {
		return false, nil
	}
	if ownerUID, err := db.GetBotOwner(binding.AgentUID); err == nil && ownerUID == binding.CanonicalUID {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if ok, err := channelBindingEntryAllowsPublicAccess(db, binding); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	allowed, err := db.AreFriends(binding.CanonicalUID, binding.AgentUID)
	if err != nil {
		return false, err
	}
	if allowed {
		return false, nil
	}
	return !allowed, nil
}

func channelBindingEntryAllowsPublicAccess(db store.Store, binding *types.ChannelAgentBinding) (bool, error) {
	if db == nil || binding == nil || binding.EntryID <= 0 {
		return false, nil
	}
	bindings, ok := db.(store.ChannelAgentBindingStore)
	if !ok {
		return false, nil
	}
	entry, err := bindings.GetChannelAgentEntryByID(binding.EntryID)
	if err != nil {
		return false, err
	}
	if entry == nil || entry.Status != "active" {
		return false, nil
	}
	if entry.OwnerUID != binding.OwnerUID || entry.AgentUID != binding.AgentUID {
		return false, nil
	}
	if normalizeChannel(entry.Channel) != normalizeChannel(binding.Channel) {
		return false, nil
	}
	if strings.TrimSpace(entry.ChannelAppID) != "" && strings.TrimSpace(entry.ChannelAppID) != strings.TrimSpace(binding.ChannelAppID) {
		return false, nil
	}
	return types.NormalizeChannelAgentAccessMode(entry.AccessMode) == types.ChannelAgentAccessPublic, nil
}

func isChannelDeviceLinkRequest(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	keywords := []string{
		"设备授权",
		"授权设备",
		"绑定设备",
		"连接设备",
		"连接电脑",
		"绑定电脑",
		"操作电脑",
		"操作我的电脑",
		"本地电脑",
		"电脑文件",
		"本地文件",
		"device link",
		"device auth",
		"link device",
	}
	for _, keyword := range keywords {
		if strings.Contains(normalized, keyword) {
			return true
		}
	}
	return false
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

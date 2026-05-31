package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const agentChannelWeixin = "weixin"

// WeixinChannelProvider talks to the Weixin iLink QR authorization API.
type WeixinChannelProvider interface {
	RequestQRCode(ctx context.Context) (map[string]interface{}, error)
	RequestQRCodeStatus(ctx context.Context, qrcode string) (map[string]interface{}, error)
}

type weixinHTTPProvider struct {
	baseURL string
	client  *http.Client
}

func newWeixinHTTPProvider() *weixinHTTPProvider {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("WEIXIN_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://ilinkai.weixin.qq.com"
	}
	return &weixinHTTPProvider{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 12 * time.Second},
	}
}

func (p *weixinHTTPProvider) RequestQRCode(ctx context.Context) (map[string]interface{}, error) {
	return p.getJSON(ctx, "/ilink/bot/get_bot_qrcode?bot_type=3")
}

func (p *weixinHTTPProvider) RequestQRCodeStatus(ctx context.Context, qrcode string) (map[string]interface{}, error) {
	return p.getJSON(ctx, "/ilink/bot/get_qrcode_status?qrcode="+url.QueryEscape(qrcode))
}

func (p *weixinHTTPProvider) getJSON(ctx context.Context, path string) (map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("weixin qrcode api returned %d", resp.StatusCode)
	}
	return data, nil
}

type agentChannelRequest struct {
	AgentUID int64 `json:"agent_uid"`
}

// SetWeixinChannelProvider replaces the provider used by tests.
func (h *AgentHandler) SetWeixinChannelProvider(provider WeixinChannelProvider) {
	if provider != nil {
		h.weixinProvider = provider
	}
}

// HandleAgentChannels handles GET/DELETE /api/agents/channels.
func (h *AgentHandler) HandleAgentChannels(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	agentUID := parseInt64(r.URL.Query().Get("agent_uid"))
	if agentUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_uid required"})
		return
	}
	agent, status, err := h.ownerAgent(uid, agentUID)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	switch r.Method {
	case http.MethodGet:
		binding, err := h.db.GetAgentChannelBinding(agentUID, agentChannelWeixin)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read agent channels"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"agent":     agent,
			"agent_uid": agentUID,
			"channels": map[string]interface{}{
				agentChannelWeixin: sanitizeAgentChannelBinding(binding),
			},
		})
	case http.MethodDelete:
		channel := strings.TrimSpace(r.URL.Query().Get("channel"))
		if channel == "" {
			channel = agentChannelWeixin
		}
		if channel != agentChannelWeixin {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported channel"})
			return
		}
		if err := h.db.DeleteAgentChannelBinding(agentUID, channel); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete channel binding"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "agent": agent, "agent_uid": agentUID})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// HandleWeixinChannelQRCode handles POST /api/agents/channels/weixin/qrcode.
func (h *AgentHandler) HandleWeixinChannelQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req agentChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	agent, status, err := h.ownerAgent(uid, req.AgentUID)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	data, err := h.weixinChannelProvider().RequestQRCode(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to request weixin qrcode"})
		return
	}
	writeJSON(w, http.StatusOK, withAgentChannelContext(data, agent))
}

// HandleWeixinChannelQRCodeStatus handles GET /api/agents/channels/weixin/qrcode-status.
func (h *AgentHandler) HandleWeixinChannelQRCodeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	agentUID := parseInt64(r.URL.Query().Get("agent_uid"))
	qrcode := strings.TrimSpace(r.URL.Query().Get("qrcode"))
	if agentUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_uid required"})
		return
	}
	if qrcode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "qrcode required"})
		return
	}
	agent, status, err := h.ownerAgent(uid, agentUID)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	data, err := h.weixinChannelProvider().RequestQRCodeStatus(r.Context(), qrcode)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to check weixin qrcode"})
		return
	}

	token := strings.TrimSpace(mapString(data["bot_token"]))
	safeData := cloneMapWithout(data, "bot_token")
	if strings.TrimSpace(mapString(data["status"])) == "confirmed" && token != "" {
		binding := &types.AgentChannelBinding{
			AgentUID:    agent.UID,
			Channel:     agentChannelWeixin,
			Status:      "configured",
			SecretToken: token,
			TokenHash:   channelTokenSHA256Hex(token),
			TokenLast4:  last4(token),
			BoundByUID:  uid,
			Metadata: map[string]interface{}{
				"agent_uid":       agent.UID,
				"agent_username":  agent.Username,
				"agent_name":      agent.DisplayName,
				"source":          "catsco_webapp",
				"transport_owner": "platform_pending_body_sync",
			},
		}
		if err := h.db.UpsertAgentChannelBinding(binding); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save weixin channel binding"})
			return
		}
		safeData["token_saved"] = true
		safeData["binding"] = sanitizeAgentChannelBinding(binding)
	}
	writeJSON(w, http.StatusOK, withAgentChannelContext(safeData, agent))
}

func (h *AgentHandler) ownerAgent(uid, agentUID int64) (AgentSummary, int, error) {
	agent, status, err := h.accessibleAgent(uid, agentUID)
	if err != nil {
		return AgentSummary{}, status, err
	}
	if agent.Relation != "owner" {
		return AgentSummary{}, http.StatusForbidden, errAgentForbidden{}
	}
	return agent, 0, nil
}

func (h *AgentHandler) weixinChannelProvider() WeixinChannelProvider {
	if h.weixinProvider != nil {
		return h.weixinProvider
	}
	h.weixinProvider = newWeixinHTTPProvider()
	return h.weixinProvider
}

func sanitizeAgentChannelBinding(binding *types.AgentChannelBinding) map[string]interface{} {
	if binding == nil {
		return map[string]interface{}{
			"channel":    agentChannelWeixin,
			"configured": false,
			"status":     "not_configured",
		}
	}
	return map[string]interface{}{
		"agent_uid":    binding.AgentUID,
		"channel":      binding.Channel,
		"configured":   binding.Status == "configured" && binding.TokenHash != "",
		"status":       binding.Status,
		"has_token":    binding.TokenHash != "",
		"token_last4":  binding.TokenLast4,
		"bound_by_uid": binding.BoundByUID,
		"created_at":   binding.CreatedAt,
		"updated_at":   binding.UpdatedAt,
	}
}

func withAgentChannelContext(data map[string]interface{}, agent AgentSummary) map[string]interface{} {
	out := cloneMapWithout(data, "bot_token")
	out["agent_uid"] = agent.UID
	out["agent"] = agent
	return out
}

func cloneMapWithout(in map[string]interface{}, omitted ...string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	omit := make(map[string]struct{}, len(omitted))
	for _, key := range omitted {
		omit[key] = struct{}{}
	}
	for key, value := range in {
		if _, skip := omit[key]; skip {
			continue
		}
		out[key] = value
	}
	return out
}

func channelTokenSHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func last4(value string) string {
	if len(value) <= 4 {
		return value
	}
	return value[len(value)-4:]
}

package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultFeishuAuthorizeURL = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"
	defaultFeishuAPIBase      = "https://open.feishu.cn"
	feishuOAuthStateTTL       = 10 * time.Minute
)

// FeishuChannelConfig contains the cloud Feishu app settings.
type FeishuChannelConfig struct {
	AppID                  string
	AppSecret              string
	OAuthRedirectURI       string
	OAuthAuthorizeURL      string
	APIBaseURL             string
	OAuthStateSecret       string
	EventVerificationToken string
}

// FeishuUserIdentity is the canonical identity returned by Feishu OAuth.
type FeishuUserIdentity struct {
	OpenID    string
	UserID    string
	UnionID   string
	Name      string
	AvatarURL string
}

type feishuAPI interface {
	AppID() string
	ExchangeOAuthCode(ctx context.Context, code string, redirectURI string) (*FeishuUserIdentity, error)
	SendTextMessage(ctx context.Context, receiveIDType string, receiveID string, text string) error
}

// FeishuChannelHandler owns Feishu OAuth binding and event callbacks.
type FeishuChannelHandler struct {
	db     store.Store
	hub    *Hub
	config FeishuChannelConfig
	api    feishuAPI
}

// NewFeishuChannelHandlerFromEnv creates the Feishu cloud channel handler.
func NewFeishuChannelHandlerFromEnv(db store.Store, hub *Hub) *FeishuChannelHandler {
	cfg := feishuConfigFromEnv()
	return NewFeishuChannelHandler(db, hub, cfg, newFeishuAPIClient(cfg))
}

func NewFeishuChannelHandler(db store.Store, hub *Hub, cfg FeishuChannelConfig, api feishuAPI) *FeishuChannelHandler {
	if strings.TrimSpace(cfg.OAuthAuthorizeURL) == "" {
		cfg.OAuthAuthorizeURL = defaultFeishuAuthorizeURL
	}
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = defaultFeishuAPIBase
	}
	if api == nil {
		api = newFeishuAPIClient(cfg)
	}
	return &FeishuChannelHandler{db: db, hub: hub, config: cfg, api: api}
}

func (h *FeishuChannelHandler) InstallOutboundDispatcher() {
	if h == nil || h.hub == nil {
		return
	}
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	if h.hub.channelOut == nil {
		h.hub.channelOut = NewChannelOutboundDispatcher(h.db, h.api, h.effectiveAppID(""))
		return
	}
	h.hub.channelOut.WithFeishu(h.api, h.effectiveAppID(""))
}

func feishuConfigFromEnv() FeishuChannelConfig {
	return FeishuChannelConfig{
		AppID:                  firstEnv("CATSCO_FEISHU_APP_ID", "FEISHU_APP_ID"),
		AppSecret:              firstEnv("CATSCO_FEISHU_APP_SECRET", "FEISHU_APP_SECRET"),
		OAuthRedirectURI:       firstEnv("CATSCO_FEISHU_OAUTH_REDIRECT_URI", "FEISHU_OAUTH_REDIRECT_URI"),
		OAuthAuthorizeURL:      firstEnv("CATSCO_FEISHU_OAUTH_AUTHORIZE_URL", "FEISHU_OAUTH_AUTHORIZE_URL"),
		APIBaseURL:             firstEnv("CATSCO_FEISHU_API_BASE_URL", "FEISHU_API_BASE_URL"),
		OAuthStateSecret:       firstEnv("CATSCO_FEISHU_OAUTH_STATE_SECRET", "FEISHU_OAUTH_STATE_SECRET"),
		EventVerificationToken: firstEnv("CATSCO_FEISHU_EVENT_VERIFICATION_TOKEN", "FEISHU_EVENT_VERIFICATION_TOKEN"),
	}
}

// HandleOAuthStart redirects an entry scan to Feishu OAuth.
func (h *FeishuChannelHandler) HandleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if strings.TrimSpace(h.config.AppID) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "feishu app is not configured"})
		return
	}
	sceneKey := strings.TrimSpace(r.URL.Query().Get("scene_key"))
	if sceneKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing scene_key"})
		return
	}
	entry, err := h.activeFeishuEntry(sceneKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load entry"})
		return
	}
	if entry == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
		return
	}
	state, err := h.signOAuthState(feishuOAuthState{
		SceneKey:  entry.SceneKey,
		ExpiresAt: time.Now().Add(feishuOAuthStateTTL).Unix(),
		Nonce:     mustGenerateSceneKey(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create oauth state"})
		return
	}
	redirectURI := h.oauthRedirectURI(r)
	authURL, err := url.Parse(h.config.OAuthAuthorizeURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid feishu authorize url"})
		return
	}
	q := authURL.Query()
	q.Set("app_id", h.config.AppID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	authURL.RawQuery = q.Encode()
	http.Redirect(w, r, authURL.String(), http.StatusFound)
}

// HandleOAuthCallback binds the Feishu OAuth identity to the scanned entry.
func (h *FeishuChannelHandler) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("error")) != "" {
		writeHTML(w, http.StatusBadRequest, oauthResultHTML("绑定失败", "飞书授权未完成，请重新扫码进入虚拟员工。"))
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	rawState := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || rawState == "" {
		writeHTML(w, http.StatusBadRequest, oauthResultHTML("绑定失败", "飞书回调缺少授权信息，请重新扫码。"))
		return
	}
	state, err := h.verifyOAuthState(rawState)
	if err != nil {
		writeHTML(w, http.StatusBadRequest, oauthResultHTML("绑定失败", "授权状态已失效，请重新扫码。"))
		return
	}
	entry, err := h.activeFeishuEntry(state.SceneKey)
	if err != nil {
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "读取虚拟员工入口失败。"))
		return
	}
	if entry == nil {
		writeHTML(w, http.StatusNotFound, oauthResultHTML("入口不可用", "这个虚拟员工入口不存在或已失效。"))
		return
	}
	identity, err := h.api.ExchangeOAuthCode(r.Context(), code, h.oauthRedirectURI(r))
	if err != nil {
		log.Printf("feishu oauth exchange failed: %v", err)
		writeHTML(w, http.StatusBadGateway, oauthResultHTML("绑定失败", "飞书身份校验失败，请稍后重试。"))
		return
	}
	if identity == nil {
		writeHTML(w, http.StatusBadGateway, oauthResultHTML("绑定失败", "飞书没有返回可绑定的用户身份。"))
		return
	}
	channelUserID := strings.TrimSpace(identity.OpenID)
	if channelUserID == "" {
		channelUserID = strings.TrimSpace(identity.UserID)
	}
	if channelUserID == "" {
		writeHTML(w, http.StatusBadGateway, oauthResultHTML("绑定失败", "飞书没有返回可绑定的用户身份。"))
		return
	}
	actorUID, err := h.ensureChannelActor("feishu", h.effectiveAppID(""), channelUserID, identity)
	if err != nil {
		log.Printf("ensure feishu actor failed: %v", err)
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "创建用户身份失败，请稍后重试。"))
		return
	}
	binding, err := h.bindFeishuIdentity(entry, actorUID, channelUserID, "", "p2p")
	if err != nil {
		log.Printf("bind feishu identity failed: %v", err)
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "保存虚拟员工绑定失败，请稍后重试。"))
		return
	}
	if err := h.db.CreateTopic(p2pTopicID(actorUID, entry.AgentUID), "p2p", actorUID); err != nil {
		log.Printf("create feishu agent topic failed: %v", err)
	}
	agent, _ := h.db.GetUser(entry.AgentUID)
	name := "该虚拟员工"
	if agent != nil {
		name = displayNameOrUsername(agent.DisplayName, agent.Username)
	}
	message := fmt.Sprintf("你已进入「%s」，可以回到飞书聊天框直接提问。", name)
	if link := channelBindingDeviceLinkURL(r, binding); link != "" {
		message += " 如需让我使用你的电脑文件，请登录 CatsCo 完成设备授权：" + link
	}
	writeHTML(w, http.StatusOK, oauthResultHTML("绑定完成", message))
}

// HandleEvents receives Feishu URL verification and message events.
func (h *FeishuChannelHandler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var env feishuEventEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if env.Encrypt != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "encrypted feishu events are not enabled"})
		return
	}
	if !h.verifyEventAppID(&env) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid app id"})
		return
	}
	if !h.verifyEventToken(&env) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid event token"})
		return
	}
	if env.isURLVerification() {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": env.Challenge})
		return
	}
	if strings.TrimSpace(h.config.EventVerificationToken) == "" && isProductionLikeEnv() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "feishu event token is required in production"})
		return
	}
	if env.Header.EventType != "im.message.receive_v1" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "ignored": true})
		return
	}
	if err := h.handleMessageEvent(r.Context(), &env); err != nil {
		log.Printf("feishu message event failed: %v", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *FeishuChannelHandler) handleMessageEvent(ctx context.Context, env *feishuEventEnvelope) error {
	var event feishuMessageEvent
	if err := json.Unmarshal(env.Event, &event); err != nil {
		return fmt.Errorf("decode message event: %w", err)
	}
	if event.Sender.SenderType != "" && event.Sender.SenderType != "user" {
		return nil
	}
	channelUserID := firstNonEmpty(strings.TrimSpace(event.Sender.SenderID.OpenID), strings.TrimSpace(event.Sender.SenderID.UserID))
	if channelUserID == "" {
		return errors.New("message event missing sender id")
	}
	chatType := normalizeFeishuChatType(event.Message.ChatType)
	if chatType == "group" {
		return h.replyToFeishu(ctx, "chat_id", event.Message.ChatID, "群聊虚拟员工绑定暂未启用，请先在私聊中扫码绑定。")
	}
	if event.Message.MessageType != "" && event.Message.MessageType != "text" {
		return h.replyToFeishu(ctx, "open_id", channelUserID, "当前飞书云端入口先支持文本消息，文件和图片能力会在后续版本接入。")
	}
	text := extractFeishuText(event.Message.Content)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	appID := h.effectiveAppID(env.Header.AppID)
	binding, err := h.resolveFeishuBinding(appID, channelUserID, event.Message.ChatID, chatType)
	if err != nil {
		return err
	}
	if binding == nil {
		return h.replyToFeishu(ctx, "open_id", channelUserID, "请先扫描虚拟员工入口二维码完成绑定，然后再回到飞书聊天框提问。")
	}
	identity := &FeishuUserIdentity{
		OpenID:  event.Sender.SenderID.OpenID,
		UserID:  event.Sender.SenderID.UserID,
		UnionID: event.Sender.SenderID.UnionID,
	}
	actorUID := binding.ActorUID
	if actorUID <= 0 {
		actorUID, err = h.ensureChannelActor("feishu", appID, channelUserID, identity)
		if err != nil {
			return err
		}
		binding, err = h.bindFeishuIdentity(bindingAsEntry(binding), actorUID, channelUserID, binding.ChannelConversationID, binding.ChannelConversationType)
		if err != nil {
			return err
		}
	}
	return h.deliverInboundTextToAgent(actorUID, binding.AgentUID, text, "feishu:"+event.Message.MessageID, map[string]interface{}{
		"source_channel":                 "feishu",
		"channel_app_id":                 appID,
		"channel_user_id":                channelUserID,
		"channel_conversation_id":        event.Message.ChatID,
		"channel_conversation_type":      chatType,
		"channel_message_id":             event.Message.MessageID,
		"channel_identity_source":        "feishu.event",
		"channel_identity_trust":         "feishu_event_callback",
		"channel_agent_binding_entry_id": binding.EntryID,
	})
}

func (h *FeishuChannelHandler) deliverInboundTextToAgent(actorUID, agentUID int64, text, clientMsgID string, metadata map[string]interface{}) error {
	return deliverInboundChannelTextToAgent(h.db, h.hub, actorUID, agentUID, text, clientMsgID, "feishu", metadata)
}

func (h *FeishuChannelHandler) resolveFeishuBinding(appID, channelUserID, conversationID, conversationType string) (*types.ChannelAgentBinding, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	return bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 "feishu",
		ChannelAppID:            appID,
		ChannelUserID:           channelUserID,
		ChannelConversationID:   conversationID,
		ChannelConversationType: conversationType,
	})
}

func (h *FeishuChannelHandler) activeFeishuEntry(sceneKey string) (*types.ChannelAgentEntry, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	entry, err := bindings.GetChannelAgentEntryBySceneKey(sceneKey)
	if err != nil || entry == nil {
		return entry, err
	}
	if entry.Status != "active" || entry.Channel != "feishu" {
		return nil, nil
	}
	return entry, nil
}

func (h *FeishuChannelHandler) bindFeishuIdentity(entry *types.ChannelAgentEntry, actorUID int64, channelUserID, conversationID, conversationType string) (*types.ChannelAgentBinding, error) {
	if entry == nil {
		return nil, errors.New("missing channel entry")
	}
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	if conversationType == "" {
		conversationType = "p2p"
	}
	return bindings.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "feishu",
		ChannelAppID:            h.effectiveAppID(""),
		ChannelUserID:           channelUserID,
		ChannelConversationID:   strings.TrimSpace(conversationID),
		ChannelConversationType: normalizeConversationType(conversationType),
		ActorUID:                actorUID,
		OwnerUID:                entry.OwnerUID,
		AgentUID:                entry.AgentUID,
		EntryID:                 entry.ID,
		Status:                  "active",
	})
}

func bindingAsEntry(binding *types.ChannelAgentBinding) *types.ChannelAgentEntry {
	if binding == nil {
		return nil
	}
	return &types.ChannelAgentEntry{
		ID:           binding.EntryID,
		Channel:      binding.Channel,
		ChannelAppID: binding.ChannelAppID,
		OwnerUID:     binding.OwnerUID,
		AgentUID:     binding.AgentUID,
		Status:       "active",
	}
}

func (h *FeishuChannelHandler) ensureChannelActor(channel, appID, channelUserID string, identity *FeishuUserIdentity) (int64, error) {
	username := channelActorUsername(channel, appID, channelUserID)
	if user, err := h.db.GetUserByUsername(username); err == nil && user != nil {
		return user.ID, nil
	} else if err != nil {
		return 0, err
	}
	displayName := ""
	avatarURL := ""
	if identity != nil {
		displayName = strings.TrimSpace(identity.Name)
		avatarURL = strings.TrimSpace(identity.AvatarURL)
	}
	if displayName == "" {
		displayName = "Feishu User"
	}
	uid, err := h.db.CreateUser(&types.User{
		Username:    username,
		DisplayName: displayName,
		AvatarURL:   avatarURL,
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

func channelActorUsername(channel, appID, channelUserID string) string {
	sum := sha256.Sum256([]byte(channel + "\x00" + appID + "\x00" + channelUserID))
	return "ch_" + channel + "_" + hex.EncodeToString(sum[:])[:24]
}

func (h *FeishuChannelHandler) oauthRedirectURI(r *http.Request) string {
	if uri := strings.TrimSpace(h.config.OAuthRedirectURI); uri != "" {
		return uri
	}
	return publicBaseURL(r) + "/api/channel-agent-bindings/oauth/feishu/callback"
}

func publicBaseURL(r *http.Request) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL")), "/")
	if base != "" {
		return base
	}
	if r == nil {
		return "https://app.catsco.cc"
	}
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
	if host == "" {
		return "https://app.catsco.cc"
	}
	return proto + "://" + host
}

func (h *FeishuChannelHandler) effectiveAppID(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	if h.api != nil {
		if appID := strings.TrimSpace(h.api.AppID()); appID != "" {
			return appID
		}
	}
	return strings.TrimSpace(h.config.AppID)
}

func (h *FeishuChannelHandler) replyToFeishu(ctx context.Context, receiveIDType, receiveID, text string) error {
	if h.api == nil || strings.TrimSpace(receiveID) == "" {
		return nil
	}
	return h.api.SendTextMessage(ctx, receiveIDType, receiveID, text)
}

type feishuOAuthState struct {
	SceneKey  string `json:"scene_key"`
	ExpiresAt int64  `json:"expires_at"`
	Nonce     string `json:"nonce"`
}

func (h *FeishuChannelHandler) signOAuthState(state feishuOAuthState) (string, error) {
	secret := h.oauthStateSecret()
	if secret == "" {
		return "", errors.New("missing oauth state secret")
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadB64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadB64 + "." + sig, nil
}

func (h *FeishuChannelHandler) verifyOAuthState(raw string) (*feishuOAuthState, error) {
	secret := h.oauthStateSecret()
	parts := strings.Split(raw, ".")
	if secret == "" || len(parts) != 2 {
		return nil, errors.New("invalid state")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return nil, errors.New("invalid state signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var state feishuOAuthState
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, err
	}
	if state.SceneKey == "" || time.Now().Unix() > state.ExpiresAt {
		return nil, errors.New("expired state")
	}
	return &state, nil
}

func (h *FeishuChannelHandler) oauthStateSecret() string {
	if secret := strings.TrimSpace(h.config.OAuthStateSecret); secret != "" {
		return secret
	}
	return strings.TrimSpace(h.config.AppSecret)
}

type feishuEventEnvelope struct {
	Schema    string          `json:"schema"`
	Challenge string          `json:"challenge"`
	Token     string          `json:"token"`
	Type      string          `json:"type"`
	Encrypt   string          `json:"encrypt"`
	Header    feishuEventHead `json:"header"`
	Event     json.RawMessage `json:"event"`
}

type feishuEventHead struct {
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	AppID     string `json:"app_id"`
	TenantKey string `json:"tenant_key"`
	Token     string `json:"token"`
}

func (e *feishuEventEnvelope) isURLVerification() bool {
	return strings.TrimSpace(e.Challenge) != "" &&
		(e.Type == "url_verification" || e.Header.EventType == "url_verification" || e.Header.EventType == "")
}

func (h *FeishuChannelHandler) verifyEventToken(env *feishuEventEnvelope) bool {
	required := strings.TrimSpace(h.config.EventVerificationToken)
	if required == "" {
		return true
	}
	got := strings.TrimSpace(env.Header.Token)
	if got == "" {
		got = strings.TrimSpace(env.Token)
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(required)) == 1
}

func (h *FeishuChannelHandler) verifyEventAppID(env *feishuEventEnvelope) bool {
	required := strings.TrimSpace(h.config.AppID)
	if required == "" || strings.TrimSpace(env.Header.AppID) == "" {
		return true
	}
	return env.Header.AppID == required
}

type feishuMessageEvent struct {
	Sender struct {
		SenderID struct {
			OpenID  string `json:"open_id"`
			UserID  string `json:"user_id"`
			UnionID string `json:"union_id"`
		} `json:"sender_id"`
		SenderType string `json:"sender_type"`
		TenantKey  string `json:"tenant_key"`
	} `json:"sender"`
	Message struct {
		MessageID   string `json:"message_id"`
		ChatID      string `json:"chat_id"`
		ChatType    string `json:"chat_type"`
		MessageType string `json:"message_type"`
		Content     string `json:"content"`
	} `json:"message"`
}

func normalizeFeishuChatType(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "group") {
		return "group"
	}
	return "p2p"
}

func extractFeishuText(content string) string {
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err == nil && parsed.Text != "" {
		return parsed.Text
	}
	return strings.TrimSpace(content)
}

type feishuAPIClient struct {
	config FeishuChannelConfig
	http   *http.Client
	mu     sync.Mutex
	token  string
	expiry time.Time
}

func newFeishuAPIClient(cfg FeishuChannelConfig) *feishuAPIClient {
	return &feishuAPIClient{
		config: cfg,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *feishuAPIClient) AppID() string {
	return strings.TrimSpace(c.config.AppID)
}

func (c *feishuAPIClient) ExchangeOAuthCode(ctx context.Context, code string, redirectURI string) (*FeishuUserIdentity, error) {
	if c == nil || strings.TrimSpace(c.config.AppID) == "" || strings.TrimSpace(c.config.AppSecret) == "" {
		return nil, errors.New("feishu oauth is not configured")
	}
	tokenURL := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultFeishuAPIBase), "/") + "/open-apis/authen/v2/oauth/token"
	payload := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     c.config.AppID,
		"client_secret": c.config.AppSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
	}
	var tokenResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken     string `json:"access_token"`
			UserAccessToken string `json:"user_access_token"`
		} `json:"data"`
		AccessToken     string `json:"access_token"`
		UserAccessToken string `json:"user_access_token"`
	}
	if err := c.postJSON(ctx, tokenURL, "", payload, &tokenResp); err != nil {
		return nil, err
	}
	if tokenResp.Code != 0 {
		return nil, fmt.Errorf("feishu oauth token error: %s", tokenResp.Msg)
	}
	userToken := firstNonEmpty(tokenResp.Data.AccessToken, tokenResp.Data.UserAccessToken, tokenResp.AccessToken, tokenResp.UserAccessToken)
	if userToken == "" {
		return nil, errors.New("feishu oauth token response missing access_token")
	}
	infoURL := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultFeishuAPIBase), "/") + "/open-apis/authen/v1/user_info"
	var infoResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
			OpenID    string `json:"open_id"`
			UnionID   string `json:"union_id"`
			UserID    string `json:"user_id"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, infoURL, "Bearer "+userToken, &infoResp); err != nil {
		return nil, err
	}
	if infoResp.Code != 0 {
		return nil, fmt.Errorf("feishu user info error: %s", infoResp.Msg)
	}
	return &FeishuUserIdentity{
		OpenID:    infoResp.Data.OpenID,
		UserID:    infoResp.Data.UserID,
		UnionID:   infoResp.Data.UnionID,
		Name:      infoResp.Data.Name,
		AvatarURL: infoResp.Data.AvatarURL,
	}, nil
}

func (c *feishuAPIClient) SendTextMessage(ctx context.Context, receiveIDType string, receiveID string, text string) error {
	if c == nil || strings.TrimSpace(c.config.AppID) == "" || strings.TrimSpace(c.config.AppSecret) == "" {
		return errors.New("feishu app is not configured")
	}
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return err
	}
	contentBytes, _ := json.Marshal(map[string]string{"text": text})
	sendURL := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultFeishuAPIBase), "/") + "/open-apis/im/v1/messages?receive_id_type=" + url.QueryEscape(receiveIDType)
	payload := map[string]string{
		"receive_id": receiveID,
		"msg_type":   "text",
		"content":    string(contentBytes),
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := c.postJSON(ctx, sendURL, "Bearer "+token, payload, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu send message error: %s", resp.Msg)
	}
	return nil
}

func (c *feishuAPIClient) tenantAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.expiry.Add(-time.Minute)) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	tokenURL := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultFeishuAPIBase), "/") + "/open-apis/auth/v3/tenant_access_token/internal"
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		AppAccessToken    string `json:"app_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := c.postJSON(ctx, tokenURL, "", map[string]string{
		"app_id":     c.config.AppID,
		"app_secret": c.config.AppSecret,
	}, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("feishu tenant token error: %s", resp.Msg)
	}
	token := firstNonEmpty(resp.TenantAccessToken, resp.AppAccessToken)
	if token == "" {
		return "", errors.New("feishu tenant token response missing token")
	}
	expires := resp.Expire
	if expires <= 0 {
		expires = 3600
	}
	c.mu.Lock()
	c.token = token
	c.expiry = time.Now().Add(time.Duration(expires) * time.Second)
	c.mu.Unlock()
	return token, nil
}

func (c *feishuAPIClient) postJSON(ctx context.Context, endpoint string, auth string, payload interface{}, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return c.doJSON(req, out)
}

func (c *feishuAPIClient) getJSON(ctx context.Context, endpoint string, auth string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return c.doJSON(req, out)
}

func (c *feishuAPIClient) doJSON(req *http.Request, out interface{}) error {
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("feishu http %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode feishu response: %w", err)
	}
	return nil
}

// ChannelOutboundDispatcher forwards CatsCo bot replies back to external chats.
type ChannelOutboundDispatcher struct {
	db          store.Store
	feishu      feishuAPI
	feishuAppID string
	weixin      weixinAPI
	weixinAppID string
}

func NewChannelOutboundDispatcher(db store.Store, feishu feishuAPI, appID string) *ChannelOutboundDispatcher {
	return (&ChannelOutboundDispatcher{db: db}).WithFeishu(feishu, appID)
}

func (d *ChannelOutboundDispatcher) WithFeishu(feishu feishuAPI, appID string) *ChannelOutboundDispatcher {
	if d == nil {
		return nil
	}
	d.feishu = feishu
	d.feishuAppID = strings.TrimSpace(appID)
	return d
}

func (d *ChannelOutboundDispatcher) WithWeixin(weixin weixinAPI, appID string) *ChannelOutboundDispatcher {
	if d == nil {
		return nil
	}
	d.weixin = weixin
	d.weixinAppID = strings.TrimSpace(appID)
	return d
}

func (h *Hub) SetChannelOutboundDispatcher(dispatcher *ChannelOutboundDispatcher) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.channelOut = dispatcher
	h.mu.Unlock()
}

func (h *Hub) forwardChannelBotReply(senderUID int64, peerUID int64, topicID string, payload *normalizedMessagePayload, msgID int64) {
	if h == nil || payload == nil || msgID <= 0 || peerUID <= 0 || senderUID <= 0 {
		return
	}
	if payload.StoredType != "text" || isTransientRuntimePayload(payload) {
		return
	}
	user, err := h.db.GetUser(senderUID)
	if err != nil || user == nil || user.AccountType != types.AccountBot {
		return
	}
	h.mu.RLock()
	dispatcher := h.channelOut
	h.mu.RUnlock()
	if dispatcher == nil {
		return
	}
	text := normalizeContentText(payload.DisplayContent)
	if strings.TrimSpace(text) == "" {
		return
	}
	go dispatcher.ForwardBotReply(context.Background(), peerUID, senderUID, topicID, text)
}

func (d *ChannelOutboundDispatcher) ForwardBotReply(ctx context.Context, actorUID int64, agentUID int64, topicID string, text string) error {
	if d == nil || d.db == nil {
		return nil
	}
	bindings, ok := d.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil
	}
	if d.feishu != nil {
		binding, err := bindings.ResolveChannelAgentBindingForActor("feishu", d.feishuAppID, actorUID, agentUID)
		if err != nil {
			return err
		}
		if binding != nil {
			receiveIDType := "open_id"
			receiveID := binding.ChannelUserID
			if binding.ChannelConversationType == "group" && binding.ChannelConversationID != "" {
				receiveIDType = "chat_id"
				receiveID = binding.ChannelConversationID
			}
			if receiveID == "" {
				return nil
			}
			if err := d.feishu.SendTextMessage(ctx, receiveIDType, receiveID, text); err != nil {
				log.Printf("feishu outbound reply failed topic=%s actor=%d agent=%d: %v", topicID, actorUID, agentUID, err)
				return err
			}
			return nil
		}
	}
	if d.weixin != nil {
		binding, err := bindings.ResolveChannelAgentBindingForActor("weixin", d.weixinAppID, actorUID, agentUID)
		if err != nil {
			return err
		}
		if binding != nil && binding.ChannelUserID != "" {
			if err := d.weixin.SendTextMessage(ctx, binding.ChannelUserID, text); err != nil {
				log.Printf("weixin outbound reply failed topic=%s actor=%d agent=%d: %v", topicID, actorUID, agentUID, err)
				return err
			}
		}
	}
	return nil
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func oauthResultHTML(title, message string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` +
		html.EscapeString(title) +
		`</title><style>body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f6f7fb;color:#111827;display:flex;min-height:100vh;align-items:center;justify-content:center;padding:24px}.card{width:100%;max-width:420px;background:#fff;border:1px solid #e5e7eb;border-radius:14px;padding:28px;text-align:center;box-shadow:0 20px 50px rgba(15,23,42,.08)}h1{font-size:22px;margin:0 0 12px}p{font-size:15px;line-height:1.7;color:#64748b;margin:0}</style></head><body><div class="card"><h1>` +
		html.EscapeString(title) +
		`</h1><p>` +
		html.EscapeString(message) +
		`</p></div></body></html>`
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

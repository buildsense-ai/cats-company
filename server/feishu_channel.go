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
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	DownloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (*channelMediaDownload, error)
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
	if strings.HasPrefix(sceneKey, "g.") {
		link, _, err := resolveChannelGroupMobileLink(h.db, sceneKey, "feishu", h.effectiveAppID(""), false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load group entry"})
			return
		}
		if link == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
			return
		}
	} else {
		entry, _, err := h.resolveFeishuEntryScene(sceneKey, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load entry"})
			return
		}
		if entry == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
			return
		}
		if !feishuEntryMatchesAppID(entry, h.effectiveAppID("")) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
			return
		}
	}
	state, err := h.signOAuthState(feishuOAuthState{
		SceneKey:  sceneKey,
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

// HandleOAuthShortLink keeps Feishu entry QR payloads short enough for the
// built-in QR renderer, then delegates to the normal OAuth start endpoint.
func (h *FeishuChannelHandler) HandleOAuthShortLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sceneKey := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/f/"), "/")
	if sceneKey == "" || strings.Contains(sceneKey, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing scene_key"})
		return
	}
	http.Redirect(w, r, feishuOAuthStartURL(r, sceneKey), http.StatusFound)
}

// HandleNativeEntryShortLink keeps legacy Feishu native-entry QR payloads
// usable by redirecting them through the OAuth binding flow.
func (h *FeishuChannelHandler) HandleNativeEntryShortLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sceneKey := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/fn/"), "/")
	if sceneKey == "" || strings.Contains(sceneKey, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing scene_key"})
		return
	}
	if strings.TrimSpace(h.config.AppID) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "feishu app is not configured"})
		return
	}
	var entry *types.ChannelAgentEntry
	if strings.HasPrefix(sceneKey, "g.") {
		link, _, err := resolveChannelGroupMobileLink(h.db, sceneKey, "feishu", h.effectiveAppID(""), false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load group entry"})
			return
		}
		if link == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
			return
		}
		entry = groupFeishuPseudoEntry(link)
	} else {
		var err error
		entry, _, err = h.resolveFeishuEntryScene(sceneKey, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load entry"})
			return
		}
		if entry == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
			return
		}
		if !feishuEntryMatchesAppID(entry, h.effectiveAppID("")) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
			return
		}
	}
	http.Redirect(w, r, feishuOAuthStartURL(r, sceneKey), http.StatusFound)
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
	if strings.HasPrefix(strings.TrimSpace(state.SceneKey), "g.") {
		h.handleGroupOAuthCallback(w, r, state, code)
		return
	}
	isMobileIdentityLink := strings.HasPrefix(strings.TrimSpace(state.SceneKey), "m.")
	entry, canonicalUIDHint, err := h.resolveFeishuEntryScene(state.SceneKey, false)
	if err != nil {
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "读取虚拟员工入口失败。"))
		return
	}
	if entry == nil {
		writeHTML(w, http.StatusNotFound, oauthResultHTML("入口不可用", "这个虚拟员工入口不存在或已失效。"))
		return
	}
	if !feishuEntryMatchesAppID(entry, h.effectiveAppID("")) {
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
	binding, accessRequest, err := h.bindOrRequestFeishuIdentityWithCanonical(entry, actorUID, channelUserID, "", "p2p", canonicalUIDHint)
	if err != nil {
		log.Printf("bind feishu identity failed: %v", err)
		if errors.Is(err, store.ErrChannelAgentBindingAlreadyLinked) {
			writeHTML(w, http.StatusConflict, oauthResultHTML("绑定失败", "这个飞书身份已经绑定到另一个 CatsCo 账号。请使用原 CatsCo 账号生成移动端二维码，或先解绑后再绑定。"))
			return
		}
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "保存虚拟员工绑定失败，请稍后重试。"))
		return
	}
	if isMobileIdentityLink && canonicalUIDHint > 0 {
		if _, _, err := h.resolveFeishuEntryScene(state.SceneKey, true); err != nil {
			log.Printf("consume feishu mobile link failed: %v", err)
		}
	}
	if binding != nil {
		if _, err := h.upsertFeishuRoute(h.effectiveAppID(""), channelUserID, "", "p2p", actorUID, binding.AgentUID, "oauth"); err != nil {
			log.Printf("select feishu oauth route failed: %v", err)
		}
	}
	agent, _ := h.db.GetUser(entry.AgentUID)
	name := "该虚拟员工"
	if agent != nil {
		name = displayNameOrUsername(agent.DisplayName, agent.Username)
	}
	if accessRequest != nil && binding == nil {
		writeHTML(w, http.StatusOK, oauthResultHTML("申请已提交", fmt.Sprintf("已向「%s」发送好友申请。管理员通过后，你就可以回到飞书聊天框提问；如果需要使用你的电脑文件，可以发送「设备授权」获取绑定链接。", name)))
		return
	}
	if channelBindingNeedsCatsCoLogin(binding) {
		if ok, err := channelBindingEntryAllowsPublicAccess(h.db, binding); err == nil && ok {
			writeHTML(w, http.StatusOK, oauthResultHTML("需要登录 CatsCo", fmt.Sprintf("你已通过飞书确认身份。请继续登录 CatsCo 账号完成验证；验证后，你就可以回到飞书和「%s」对话。\n\n%s", name, channelBindingDeviceLinkGuidance(h.db, r, binding))))
		} else {
			writeHTML(w, http.StatusOK, oauthResultHTML("需要登录 CatsCo", fmt.Sprintf("你已通过飞书确认身份。请继续登录 CatsCo 账号并申请添加「%s」；管理员通过后，你就可以回到飞书聊天框提问。\n\n%s", name, channelBindingDeviceLinkGuidance(h.db, r, binding))))
		}
		return
	}
	if channelBindingRejected(binding) {
		writeHTML(w, http.StatusOK, oauthResultHTML("申请未通过", fmt.Sprintf("你添加「%s」的申请暂未通过，请联系虚拟员工管理员。", name)))
		return
	}
	if pending, err := channelBindingPendingFriendApproval(h.db, binding); err != nil {
		log.Printf("check feishu channel access failed: %v", err)
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "检查虚拟员工好友关系失败，请稍后重试。"))
		return
	} else if pending {
		writeHTML(w, http.StatusOK, oauthResultHTML("申请已提交", fmt.Sprintf("已向「%s」发送好友申请。管理员通过后，你就可以回到飞书聊天框提问。", name)))
		return
	}
	conversationUID := channelBindingConversationActorUID(binding, actorUID)
	if err := h.db.CreateTopic(p2pTopicID(conversationUID, entry.AgentUID), "p2p", conversationUID); err != nil {
		log.Printf("create feishu agent topic failed: %v", err)
	}
	writeHTML(w, http.StatusOK, oauthResultHTML("绑定完成", fmt.Sprintf("你已进入「%s」，可以回到飞书聊天框直接提问。", name)))
}

func (h *FeishuChannelHandler) handleGroupOAuthCallback(w http.ResponseWriter, r *http.Request, state *feishuOAuthState, code string) {
	link, group, err := resolveChannelGroupMobileLink(h.db, state.SceneKey, "feishu", h.effectiveAppID(""), false)
	if err != nil {
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "读取群聊移动端入口失败。"))
		return
	}
	if link == nil {
		writeHTML(w, http.StatusNotFound, oauthResultHTML("入口不可用", "这个群聊移动端入口不存在或已失效。"))
		return
	}
	identity, err := h.api.ExchangeOAuthCode(r.Context(), code, h.oauthRedirectURI(r))
	if err != nil {
		log.Printf("feishu group oauth exchange failed: %v", err)
		writeHTML(w, http.StatusBadGateway, oauthResultHTML("绑定失败", "飞书身份校验失败，请稍后重试。"))
		return
	}
	if identity == nil {
		writeHTML(w, http.StatusBadGateway, oauthResultHTML("绑定失败", "飞书没有返回可绑定的用户身份。"))
		return
	}
	channelUserID := firstNonEmpty(strings.TrimSpace(identity.OpenID), strings.TrimSpace(identity.UserID))
	if channelUserID == "" {
		writeHTML(w, http.StatusBadGateway, oauthResultHTML("绑定失败", "飞书没有返回可绑定的用户身份。"))
		return
	}
	actorUID, err := h.ensureChannelActor("feishu", h.effectiveAppID(""), channelUserID, identity)
	if err != nil {
		log.Printf("ensure feishu group actor failed: %v", err)
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "创建飞书用户身份失败，请稍后重试。"))
		return
	}
	if _, err := h.upsertFeishuGroupBinding(link, actorUID, channelUserID, "", "p2p"); err != nil {
		log.Printf("bind feishu group identity failed: %v", err)
		writeHTML(w, http.StatusInternalServerError, oauthResultHTML("绑定失败", "保存群聊移动端绑定失败，请稍后重试。"))
		return
	}
	if _, _, err := resolveChannelGroupMobileLink(h.db, state.SceneKey, "feishu", h.effectiveAppID(""), true); err != nil {
		log.Printf("consume feishu group mobile link failed: %v", err)
	}
	if err := h.db.CreateTopic(link.TopicID, "group", link.CanonicalUID); err != nil {
		log.Printf("create feishu group mobile topic failed: %v", err)
	}
	groupName := "这个群聊"
	if group != nil && strings.TrimSpace(group.Name) != "" {
		groupName = strings.TrimSpace(group.Name)
	}
	writeHTML(w, http.StatusOK, oauthResultHTML("已进入群聊", fmt.Sprintf("已进入「%s」。你现在可以回到飞书聊天框直接发消息，CatsCo 会把消息同步到这个群。", groupName)))
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
	replyIDType, replyID := feishuReplyTarget(channelUserID, event.Message.ChatID, chatType)
	messageType := strings.ToLower(strings.TrimSpace(event.Message.MessageType))
	if messageType == "" {
		messageType = "text"
	}
	var text string
	var media *feishuInboundMedia
	switch messageType {
	case "text":
		text = extractFeishuText(event.Message.Content)
	case "image", "file":
		var err error
		media, err = extractFeishuInboundMedia(messageType, event.Message.Content)
		if err != nil {
			log.Printf("decode feishu media content failed: %v", err)
			return h.replyToFeishu(ctx, replyIDType, replyID, "未能读取飞书图片或文件，请稍后重试。")
		}
		text = media.Text
	default:
		return h.replyToFeishu(ctx, replyIDType, replyID, "当前飞书入口暂不支持这种消息类型，请发送文本、图片或文件。")
	}
	if strings.TrimSpace(text) == "" && media == nil {
		return nil
	}
	appID := h.effectiveAppID(env.Header.AppID)
	identity := &FeishuUserIdentity{
		OpenID:  event.Sender.SenderID.OpenID,
		UserID:  event.Sender.SenderID.UserID,
		UnionID: event.Sender.SenderID.UnionID,
	}
	actorUID, err := h.ensureChannelActor("feishu", appID, channelUserID, identity)
	if err != nil {
		return err
	}
	if groupBinding, err := h.resolveFeishuGroupBinding(appID, channelUserID, event.Message.ChatID, chatType, actorUID); err != nil {
		return err
	} else if groupBinding != nil {
		groupText := strings.TrimSpace(stripFeishuLeadingMentions(text))
		if groupText == "" {
			return nil
		}
		return deliverInboundChannelTextToGroup(h.db, h.hub, groupBinding.CanonicalUID, groupBinding, groupText, "feishu-group:"+event.Message.MessageID, "feishu", map[string]interface{}{
			"source_channel":            "feishu",
			"channel_app_id":            appID,
			"channel_user_id":           channelUserID,
			"channel_actor_uid":         actorUID,
			"channel_conversation_id":   event.Message.ChatID,
			"channel_conversation_type": chatType,
			"channel_message_id":        event.Message.MessageID,
			"channel_identity_source":   "feishu.event",
			"channel_identity_trust":    "feishu_event_callback",
			"channel_group_binding_id":  groupBinding.ID,
		})
	}

	cmd := parseFeishuGatewayCommand(text)
	groupTriggered := cmd.Trigger || feishuEventMentionsBot(&event, text)
	if chatType == "group" && !groupTriggered {
		return nil
	}
	text = stripFeishuLeadingMentions(text)
	if chatType == "group" && cmd.Kind == "" && media == nil && strings.TrimSpace(text) == "" {
		return nil
	}
	groupCoordinatorMode := chatType == "group" && cmd.Kind == "" && media == nil
	if chatType == "group" && cmd.Kind == "" && media != nil {
		return h.replyToFeishu(ctx, replyIDType, replyID, "群聊里的图片或文件请先切换到目标虚拟员工后，在私聊中发送，避免附件进入错误会话。")
	}

	switch cmd.Kind {
	case "help":
		return h.replyToFeishu(ctx, replyIDType, replyID, feishuGatewayHelpText())
	case "list":
		return h.replyToFeishu(ctx, replyIDType, replyID, h.formatFeishuRosterReply(appID))
	case "current":
		return h.replyToFeishuSafely(ctx, channelUserID, event.Message.ChatID, chatType, h.formatFeishuCurrentReply(appID, channelUserID, event.Message.ChatID, chatType, actorUID))
	case "chat_id":
		return h.replyToFeishu(ctx, replyIDType, replyID, h.formatFeishuChatIDReply(event.Message.ChatID, chatType))
	case "bind":
		return h.replyToFeishuSafely(ctx, channelUserID, event.Message.ChatID, chatType, h.formatFeishuAccountBindingReply(appID, channelUserID, event.Message.ChatID, chatType, actorUID))
	case "device":
		return h.replyToFeishuSafely(ctx, channelUserID, event.Message.ChatID, chatType, h.formatFeishuDeviceBindingReply(appID, channelUserID, event.Message.ChatID, chatType, actorUID))
	case "select":
		msg, err := h.selectFeishuAgent(appID, channelUserID, event.Message.ChatID, chatType, actorUID, cmd.Target)
		if err != nil {
			return err
		}
		return h.replyToFeishuSafely(ctx, channelUserID, event.Message.ChatID, chatType, msg)
	}

	binding, err := h.resolveCurrentFeishuBinding(appID, channelUserID, event.Message.ChatID, chatType, actorUID)
	if err != nil {
		return err
	}
	if binding == nil {
		if groupCoordinatorMode {
			return h.replyToFeishu(ctx, replyIDType, replyID, "这个群聊还没有选择群聊调度员。\n请先发送「员工列表」查看可用虚拟员工，再发送「切换到 员工名」选择一个虚拟员工作为群聊调度员。")
		}
		return h.replyToFeishu(ctx, replyIDType, replyID, "请先选择一个虚拟员工。\n"+h.formatFeishuRosterReply(appID))
	}
	if msg, ok, err := h.feishuBindingDeliverableMessage(binding); err != nil {
		return err
	} else if !ok {
		return h.replyToFeishu(ctx, replyIDType, replyID, msg)
	}
	metadata := h.feishuInboundMetadata(appID, channelUserID, &event, binding, chatType, messageType)
	if groupCoordinatorMode {
		delivered, err := h.deliverFeishuGroupAgentTurns(appID, channelUserID, actorUID, &event, binding, text, messageType)
		if err != nil {
			return err
		}
		if delivered > 0 {
			return nil
		}
		agent, _ := h.db.GetUser(binding.AgentUID)
		metadata["channel_group_coordinator_mode"] = "team"
		metadata["channel_group_original_text"] = text
		text = buildFeishuGroupCoordinatorText(text, binding, agent)
	}
	if media != nil {
		metadata["channel_media_key"] = media.ResourceKey
		download, err := h.api.DownloadMessageResource(ctx, event.Message.MessageID, media.ResourceKey, media.ResourceType)
		if err != nil {
			log.Printf("download feishu media failed: %v", err)
			return h.replyToFeishu(ctx, replyIDType, replyID, "读取飞书图片或文件失败，请稍后重试。")
		}
		if download.FileName == "" {
			download.FileName = media.FileName
		}
		if download.ContentType == "" {
			download.ContentType = media.ContentType
		}
		file, err := saveChannelMediaUpload(media.UploadType, download)
		if err != nil {
			log.Printf("save feishu media failed: %v", err)
			return h.replyToFeishu(ctx, replyIDType, replyID, "保存飞书图片或文件失败，请稍后重试。")
		}
		return h.deliverInboundMessageToAgent(actorUID, binding.AgentUID, text, []uploadPayload{file}, "feishu:"+event.Message.MessageID, metadata)
	}
	return h.deliverInboundTextToAgent(actorUID, binding.AgentUID, text, "feishu:"+event.Message.MessageID, metadata)
}

func (h *FeishuChannelHandler) feishuInboundMetadata(appID, channelUserID string, event *feishuMessageEvent, binding *types.ChannelAgentBinding, chatType string, messageType string) map[string]interface{} {
	return map[string]interface{}{
		"source_channel":                 "feishu",
		"channel_app_id":                 appID,
		"channel_user_id":                channelUserID,
		"channel_conversation_id":        event.Message.ChatID,
		"channel_conversation_type":      chatType,
		"channel_message_id":             event.Message.MessageID,
		"channel_message_type":           messageType,
		"channel_identity_source":        "feishu.event",
		"channel_identity_trust":         "feishu_event_callback",
		"channel_gateway_mode":           "feishu_agent_gateway",
		"channel_selected_agent_id":      binding.AgentUID,
		"channel_agent_binding_entry_id": binding.EntryID,
		"channel_actor_uid":              binding.ActorUID,
		"channel_canonical_uid":          binding.CanonicalUID,
		"channel_agent_binding_id":       binding.ID,
		"channel_device_access_enabled":  binding.DeviceAccessEnabled,
	}
}

func (h *FeishuChannelHandler) deliverInboundTextToAgent(actorUID, agentUID int64, text, clientMsgID string, metadata map[string]interface{}) error {
	return deliverInboundChannelTextToAgent(h.db, h.hub, actorUID, agentUID, text, clientMsgID, "feishu", metadata)
}

func (h *FeishuChannelHandler) deliverInboundMessageToAgent(actorUID, agentUID int64, text string, files []uploadPayload, clientMsgID string, metadata map[string]interface{}) error {
	return deliverInboundChannelMessageToAgent(h.db, h.hub, actorUID, agentUID, text, files, clientMsgID, "feishu", metadata)
}

func (h *FeishuChannelHandler) deliverFeishuGroupAgentTurns(appID, channelUserID string, actorUID int64, event *feishuMessageEvent, coordinatorBinding *types.ChannelAgentBinding, text, messageType string) (int, error) {
	if h == nil || event == nil || coordinatorBinding == nil || normalizeFeishuChatType(event.Message.ChatType) != "group" {
		return 0, nil
	}
	speakers := feishuGroupAgentSpeakersForChat(event.Message.ChatID)
	if len(speakers) == 0 {
		return 0, nil
	}
	roster, err := h.listFeishuRoster(appID)
	if err != nil {
		return 0, err
	}
	entriesByAgent := map[int64]*feishuRosterItem{}
	for i := range roster {
		item := roster[i]
		if item.Entry != nil && item.Entry.AgentUID > 0 {
			entriesByAgent[item.Entry.AgentUID] = &item
		}
	}
	participants := make([]*types.User, 0, len(speakers))
	for _, speaker := range speakers {
		if item := entriesByAgent[speaker.AgentUID]; item != nil {
			participants = append(participants, item.Agent)
		}
	}
	if len(participants) == 0 {
		return 0, nil
	}
	coordinator, _ := h.db.GetUser(coordinatorBinding.AgentUID)
	delivered := 0
	for _, speaker := range speakers {
		item := entriesByAgent[speaker.AgentUID]
		if item == nil || item.Entry == nil {
			continue
		}
		binding := coordinatorBinding
		if speaker.AgentUID != coordinatorBinding.AgentUID {
			nextBinding, _, err := h.bindOrRequestFeishuIdentityWithCanonical(item.Entry, actorUID, channelUserID, event.Message.ChatID, "group", coordinatorBinding.CanonicalUID)
			if err != nil {
				return delivered, err
			}
			binding = nextBinding
		}
		if msg, ok, err := h.feishuBindingDeliverableMessage(binding); err != nil {
			return delivered, err
		} else if !ok {
			log.Printf("skip feishu group agent speaker agent=%d chat=%s: %s", speaker.AgentUID, event.Message.ChatID, strings.ReplaceAll(msg, "\n", " "))
			continue
		}
		metadata := h.feishuInboundMetadata(appID, channelUserID, event, binding, "group", messageType)
		metadata["channel_group_coordinator_mode"] = "multi_agent"
		metadata["channel_group_original_text"] = text
		metadata["channel_group_speaker_agent_uid"] = speaker.AgentUID
		metadata["channel_group_speaker_count"] = len(participants)
		turnText := buildFeishuGroupAgentTurnText(text, item.Agent, coordinator, participants, delivered+1, len(participants))
		clientMsgID := fmt.Sprintf("feishu-group-agent:%s:%d", strings.TrimSpace(event.Message.MessageID), speaker.AgentUID)
		if err := h.deliverInboundTextToAgent(actorUID, speaker.AgentUID, turnText, clientMsgID, metadata); err != nil {
			return delivered, err
		}
		delivered++
		if delivered >= 4 {
			break
		}
	}
	return delivered, nil
}

type feishuGatewayCommand struct {
	Kind    string
	Target  string
	Trigger bool
}

type feishuRosterItem struct {
	Entry *types.ChannelAgentEntry
	Agent *types.User
	Name  string
}

func parseFeishuGatewayCommand(text string) feishuGatewayCommand {
	text = strings.TrimSpace(stripFeishuLeadingMentions(text))
	lower := strings.ToLower(text)
	switch lower {
	case "帮助", "help", "/help", "菜单", "menu", "/menu":
		return feishuGatewayCommand{Kind: "help", Trigger: true}
	case "员工列表", "虚拟员工", "列表", "list", "/list":
		return feishuGatewayCommand{Kind: "list", Trigger: true}
	case "当前员工", "当前虚拟员工", "current", "/current":
		return feishuGatewayCommand{Kind: "current", Trigger: true}
	case "群id", "群 id", "群聊id", "群聊 id", "chat_id", "/chat_id":
		return feishuGatewayCommand{Kind: "chat_id", Trigger: true}
	case "绑定账号", "绑定catsco", "绑定 catsco", "/bind":
		return feishuGatewayCommand{Kind: "bind", Trigger: true}
	case "设备授权", "绑定设备", "/device":
		return feishuGatewayCommand{Kind: "device", Trigger: true}
	}
	for _, prefix := range []string{"切换到", "切换 ", "选择 ", "/use ", "use "} {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return feishuGatewayCommand{Kind: "select", Target: strings.TrimSpace(text[len(prefix):]), Trigger: true}
		}
	}
	return feishuGatewayCommand{}
}

func feishuGatewayHelpText() string {
	return "我是 CatsCo 飞书虚拟员工入口。\n\n常用命令：\n- 员工列表：查看可用虚拟员工\n- 切换到 员工名：选择当前员工\n- 当前员工：查看当前会话服务者\n- 群ID：查看当前飞书群 chat_id\n- 绑定账号：绑定 CatsCo 账号\n- 设备授权：授权我使用你自己的设备\n\n未选择员工、未绑定账号、申请未通过或设备未授权时，我不会把消息交给模型或操作设备。"
}

func (h *FeishuChannelHandler) formatFeishuChatIDReply(chatID, chatType string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "当前会话没有可用的飞书 chat_id。"
	}
	if normalizeFeishuChatType(chatType) == "group" {
		return "当前飞书群 chat_id：\n" + chatID
	}
	return "当前飞书会话 chat_id：\n" + chatID
}

func feishuReplyTarget(channelUserID, chatID, chatType string) (string, string) {
	if normalizeFeishuChatType(chatType) == "group" && strings.TrimSpace(chatID) != "" {
		return "chat_id", strings.TrimSpace(chatID)
	}
	return "open_id", strings.TrimSpace(channelUserID)
}

func (h *FeishuChannelHandler) replyToFeishuSafely(ctx context.Context, channelUserID, chatID, chatType, text string) error {
	if normalizeFeishuChatType(chatType) != "group" {
		idType, id := feishuReplyTarget(channelUserID, chatID, chatType)
		return h.replyToFeishu(ctx, idType, id, text)
	}
	if err := h.replyToFeishu(ctx, "open_id", strings.TrimSpace(channelUserID), text); err != nil {
		return err
	}
	if strings.TrimSpace(chatID) == "" {
		return nil
	}
	return h.replyToFeishu(ctx, "chat_id", strings.TrimSpace(chatID), "我已把详细信息私信给你。")
}

func feishuEventMentionsBot(event *feishuMessageEvent, text string) bool {
	text = strings.TrimSpace(text)
	lowerText := strings.ToLower(text)
	for _, alias := range feishuBotMentionAliases() {
		if alias == "" {
			continue
		}
		if strings.Contains(lowerText, strings.ToLower("@"+alias)) ||
			strings.Contains(lowerText, strings.ToLower("＠"+alias)) {
			return true
		}
	}
	if event != nil {
		for _, mention := range event.Message.Mentions {
			if feishuMentionMatchesAlias(mention.Key) || feishuMentionMatchesAlias(mention.Name) || feishuMentionMatchesConfiguredBotID(mention.ID.OpenID, mention.ID.UserID, mention.ID.UnionID) {
				return true
			}
		}
	}
	return false
}

func feishuMentionMatchesAlias(value string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	value = strings.TrimPrefix(value, "＠")
	if value == "" {
		return false
	}
	for _, alias := range feishuBotMentionAliases() {
		if strings.EqualFold(value, alias) {
			return true
		}
	}
	return false
}

func feishuMentionMatchesConfiguredBotID(openID, userID, unionID string) bool {
	if stringInList(strings.TrimSpace(openID), feishuBotMentionOpenIDs()) {
		return true
	}
	if stringInList(strings.TrimSpace(userID), feishuBotMentionUserIDs()) {
		return true
	}
	return stringInList(strings.TrimSpace(unionID), feishuBotMentionUnionIDs())
}

func stringInList(value string, values []string) bool {
	if value == "" {
		return false
	}
	for _, candidate := range values {
		if strings.EqualFold(value, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func stripFeishuLeadingMentions(text string) string {
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, "@") {
		fields := strings.Fields(text)
		if len(fields) <= 1 {
			return ""
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	}
	return text
}

func (h *FeishuChannelHandler) listFeishuRoster(appID string) ([]feishuRosterItem, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	entries, err := bindings.ListChannelAgentEntriesByChannelApp("feishu", appID)
	if err != nil {
		return nil, err
	}
	items := make([]feishuRosterItem, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Status != "active" || entry.AgentUID <= 0 {
			continue
		}
		agent, err := h.db.GetUser(entry.AgentUID)
		if err != nil || agent == nil || agent.AccountType != types.AccountBot {
			continue
		}
		name := displayNameOrUsername(agent.DisplayName, agent.Username)
		items = append(items, feishuRosterItem{Entry: entry, Agent: agent, Name: name})
	}
	return items, nil
}

func (h *FeishuChannelHandler) formatFeishuRosterReply(appID string) string {
	items, err := h.listFeishuRoster(appID)
	if err != nil {
		log.Printf("list feishu roster failed: %v", err)
		return "暂时无法读取虚拟员工列表，请稍后重试。"
	}
	if len(items) == 0 {
		return "当前飞书应用还没有可用的虚拟员工入口。请先在 CatsCo 中为虚拟员工生成飞书入口码。"
	}
	var b strings.Builder
	b.WriteString("可用虚拟员工：")
	for i, item := range items {
		fmt.Fprintf(&b, "\n%d. %s", i+1, item.Name)
	}
	b.WriteString("\n\n发送「切换到 员工名」选择当前员工。")
	return b.String()
}

func (h *FeishuChannelHandler) findFeishuRosterEntry(appID, target string) (*feishuRosterItem, string, error) {
	items, err := h.listFeishuRoster(appID)
	if err != nil {
		return nil, "", err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, "请告诉我要切换到哪个虚拟员工。\n" + h.formatFeishuRosterReply(appID), nil
	}
	if n, err := strconv.Atoi(target); err == nil && n >= 1 && n <= len(items) {
		return &items[n-1], "", nil
	}
	var matches []feishuRosterItem
	lowerTarget := strings.ToLower(target)
	for _, item := range items {
		name := strings.ToLower(item.Name)
		username := ""
		if item.Agent != nil {
			username = strings.ToLower(item.Agent.Username)
		}
		if name == lowerTarget || username == lowerTarget {
			return &item, "", nil
		}
		if strings.Contains(name, lowerTarget) || strings.Contains(username, lowerTarget) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return &matches[0], "", nil
	}
	if len(matches) > 1 {
		var b strings.Builder
		b.WriteString("找到多个相似的虚拟员工，请说完整名称或编号：")
		for i, item := range matches {
			fmt.Fprintf(&b, "\n%d. %s", i+1, item.Name)
		}
		return nil, b.String(), nil
	}
	return nil, "没有找到这个虚拟员工。\n" + h.formatFeishuRosterReply(appID), nil
}

func (h *FeishuChannelHandler) selectFeishuAgent(appID, channelUserID, conversationID, conversationType string, actorUID int64, target string) (string, error) {
	item, message, err := h.findFeishuRosterEntry(appID, target)
	if err != nil || item == nil {
		return message, err
	}
	binding, _, err := h.bindOrRequestFeishuIdentity(item.Entry, actorUID, channelUserID, conversationID, conversationType)
	if err != nil {
		return "", err
	}
	if binding != nil {
		if _, err := h.upsertFeishuRoute(appID, channelUserID, conversationID, conversationType, actorUID, binding.AgentUID, "manual"); err != nil {
			return "", err
		}
	}
	if binding == nil {
		return fmt.Sprintf("已向「%s」发送好友申请。管理员通过后，你就可以在这里提问。", item.Name), nil
	}
	if msg, ok, err := h.feishuBindingDeliverableMessage(binding); err != nil {
		return "", err
	} else if !ok {
		return fmt.Sprintf("已选择「%s」。\n%s", item.Name, msg), nil
	}
	return fmt.Sprintf("已切换到「%s」。现在可以直接提问；如需使用你的电脑文件，请发送「设备授权」。", item.Name), nil
}

func (h *FeishuChannelHandler) upsertFeishuRoute(appID, channelUserID, conversationID, conversationType string, actorUID, agentUID int64, source string) (*types.ChannelAgentRoute, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	return bindings.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "feishu",
		ChannelAppID:            appID,
		ChannelUserID:           channelUserID,
		ChannelConversationID:   strings.TrimSpace(conversationID),
		ChannelConversationType: normalizeFeishuChatType(conversationType),
		ActorUID:                actorUID,
		AgentUID:                agentUID,
		Source:                  source,
	})
}

func (h *FeishuChannelHandler) upsertFeishuGroupBinding(link *types.ChannelGroupMobileLink, actorUID int64, channelUserID, conversationID, conversationType string) (*types.ChannelGroupBinding, error) {
	if link == nil {
		return nil, errors.New("missing group mobile link")
	}
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	return bindings.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
		Channel:                 "feishu",
		ChannelAppID:            h.effectiveAppID(""),
		ChannelUserID:           strings.TrimSpace(channelUserID),
		ChannelConversationID:   strings.TrimSpace(conversationID),
		ChannelConversationType: normalizeFeishuChatType(conversationType),
		ActorUID:                actorUID,
		CanonicalUID:            link.CanonicalUID,
		GroupID:                 link.GroupID,
		TopicID:                 link.TopicID,
		Status:                  types.ChannelAgentBindingActive,
	})
}

func (h *FeishuChannelHandler) resolveFeishuGroupBinding(appID, channelUserID, conversationID, conversationType string, actorUID int64) (*types.ChannelGroupBinding, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	conversationType = normalizeFeishuChatType(conversationType)
	query := types.ChannelGroupBindingQuery{
		Channel:                 "feishu",
		ChannelAppID:            strings.TrimSpace(appID),
		ChannelUserID:           strings.TrimSpace(channelUserID),
		ChannelConversationID:   strings.TrimSpace(conversationID),
		ChannelConversationType: conversationType,
		ActorUID:                actorUID,
	}
	binding, err := bindings.ResolveChannelGroupBinding(query)
	if err != nil || binding != nil || conversationType != "p2p" || strings.TrimSpace(conversationID) == "" {
		return binding, err
	}
	query.ChannelConversationID = ""
	return bindings.ResolveChannelGroupBinding(query)
}

func (h *FeishuChannelHandler) resolveCurrentFeishuBinding(appID, channelUserID, conversationID, conversationType string, actorUID int64) (*types.ChannelAgentBinding, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	conversationType = normalizeFeishuChatType(conversationType)
	conversationID = strings.TrimSpace(conversationID)
	route, err := bindings.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "feishu",
		ChannelAppID:            appID,
		ChannelUserID:           channelUserID,
		ChannelConversationID:   conversationID,
		ChannelConversationType: conversationType,
		ActorUID:                actorUID,
	})
	if err != nil {
		return nil, err
	}
	if conversationType == "p2p" && conversationID != "" {
		baseRoute, err := bindings.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
			Channel:                 "feishu",
			ChannelAppID:            appID,
			ChannelUserID:           channelUserID,
			ChannelConversationType: "p2p",
			ActorUID:                actorUID,
		})
		if err != nil {
			return nil, err
		}
		if feishuBaseRouteShouldReplaceConversationRoute(route, baseRoute) {
			source := strings.TrimSpace(baseRoute.Source)
			if source == "" {
				source = "oauth"
			}
			route, err = h.upsertFeishuRoute(appID, channelUserID, conversationID, "p2p", actorUID, baseRoute.AgentUID, source)
			if err != nil {
				return nil, err
			}
		}
	}
	if route == nil {
		return nil, nil
	}
	query := types.ChannelAgentBindingQuery{
		Channel:                 "feishu",
		ChannelAppID:            appID,
		ChannelUserID:           channelUserID,
		ChannelConversationID:   conversationID,
		ChannelConversationType: conversationType,
		AgentUID:                route.AgentUID,
		ActorUID:                actorUID,
	}
	binding, err := bindings.ResolveChannelAgentBinding(query)
	if err != nil {
		return binding, err
	}
	var baseBinding *types.ChannelAgentBinding
	if conversationType == "p2p" && conversationID != "" {
		baseBinding, err = bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
			Channel:                 "feishu",
			ChannelAppID:            appID,
			ChannelUserID:           channelUserID,
			ChannelConversationType: "p2p",
			AgentUID:                route.AgentUID,
			ActorUID:                actorUID,
		})
		if err != nil {
			return nil, err
		}
	}
	if binding != nil {
		if baseBinding != nil && conversationType == "p2p" && conversationID != "" && feishuBindingShouldInheritBase(binding, baseBinding) {
			return bindings.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
				Channel:                 binding.Channel,
				ChannelAppID:            binding.ChannelAppID,
				ChannelUserID:           binding.ChannelUserID,
				ChannelConversationID:   binding.ChannelConversationID,
				ChannelConversationType: binding.ChannelConversationType,
				ActorUID:                baseBinding.ActorUID,
				CanonicalUID:            baseBinding.CanonicalUID,
				OwnerUID:                baseBinding.OwnerUID,
				AgentUID:                baseBinding.AgentUID,
				EntryID:                 baseBinding.EntryID,
				Status:                  baseBinding.Status,
				DeviceAccessEnabled:     baseBinding.DeviceAccessEnabled,
			})
		}
		return binding, nil
	}
	if conversationType != "p2p" || conversationID == "" || baseBinding == nil {
		return baseBinding, nil
	}
	return bindings.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 baseBinding.Channel,
		ChannelAppID:            baseBinding.ChannelAppID,
		ChannelUserID:           baseBinding.ChannelUserID,
		ChannelConversationID:   conversationID,
		ChannelConversationType: "p2p",
		ActorUID:                baseBinding.ActorUID,
		CanonicalUID:            baseBinding.CanonicalUID,
		OwnerUID:                baseBinding.OwnerUID,
		AgentUID:                baseBinding.AgentUID,
		EntryID:                 baseBinding.EntryID,
		Status:                  baseBinding.Status,
		DeviceAccessEnabled:     baseBinding.DeviceAccessEnabled,
	})
}

func feishuBaseRouteShouldReplaceConversationRoute(route, baseRoute *types.ChannelAgentRoute) bool {
	if baseRoute == nil || baseRoute.AgentUID <= 0 {
		return false
	}
	if route == nil {
		return true
	}
	if route.AgentUID == baseRoute.AgentUID {
		return false
	}
	if baseRoute.SelectedAt.IsZero() {
		return false
	}
	if route.SelectedAt.IsZero() {
		return true
	}
	return baseRoute.SelectedAt.After(route.SelectedAt)
}

func feishuBindingShouldInheritBase(binding, baseBinding *types.ChannelAgentBinding) bool {
	if binding == nil || baseBinding == nil {
		return false
	}
	if binding.AgentUID != baseBinding.AgentUID || binding.ActorUID != baseBinding.ActorUID {
		return false
	}
	if baseBinding.CanonicalUID > 0 && binding.CanonicalUID <= 0 {
		return true
	}
	if baseBinding.Status == types.ChannelAgentBindingActive && binding.Status != types.ChannelAgentBindingActive {
		return true
	}
	if baseBinding.Status == types.ChannelAgentBindingPendingApproval && binding.Status == types.ChannelAgentBindingPendingLogin {
		return true
	}
	if baseBinding.DeviceAccessEnabled && !binding.DeviceAccessEnabled {
		return true
	}
	return false
}

func (h *FeishuChannelHandler) formatFeishuCurrentReply(appID, channelUserID, conversationID, conversationType string, actorUID int64) string {
	binding, err := h.resolveCurrentFeishuBinding(appID, channelUserID, conversationID, conversationType, actorUID)
	if err != nil {
		log.Printf("resolve feishu current failed: %v", err)
		return "暂时无法读取当前虚拟员工，请稍后重试。"
	}
	if binding == nil {
		return "当前还没有选择虚拟员工。\n" + h.formatFeishuRosterReply(appID)
	}
	agent, _ := h.db.GetUser(binding.AgentUID)
	name := "当前虚拟员工"
	if agent != nil {
		name = displayNameOrUsername(agent.DisplayName, agent.Username)
	}
	if msg, ok, err := h.feishuBindingDeliverableMessage(binding); err != nil {
		log.Printf("check feishu current failed: %v", err)
		return "当前虚拟员工状态检查失败，请稍后重试。"
	} else if !ok {
		return fmt.Sprintf("当前虚拟员工是「%s」，但暂时还不能对话：\n%s", name, msg)
	}
	return fmt.Sprintf("当前虚拟员工是「%s」。你可以直接提问，或发送「切换到 员工名」更换。", name)
}

func (h *FeishuChannelHandler) formatFeishuAccountBindingReply(appID, channelUserID, conversationID, conversationType string, actorUID int64) string {
	binding, err := h.resolveCurrentFeishuBinding(appID, channelUserID, conversationID, conversationType, actorUID)
	if err != nil || binding == nil {
		return "请先选择一个虚拟员工，再绑定 CatsCo 账号。\n" + h.formatFeishuRosterReply(appID)
	}
	if channelBindingNeedsCatsCoLogin(binding) {
		return channelBindingDeviceLinkGuidance(h.db, nil, binding)
	}
	return "你已经完成 CatsCo 账号绑定。如需使用自己的电脑文件，请发送「设备授权」。"
}

func (h *FeishuChannelHandler) formatFeishuDeviceBindingReply(appID, channelUserID, conversationID, conversationType string, actorUID int64) string {
	binding, err := h.resolveCurrentFeishuBinding(appID, channelUserID, conversationID, conversationType, actorUID)
	if err != nil || binding == nil {
		return "请先选择一个虚拟员工，再进行设备授权。\n" + h.formatFeishuRosterReply(appID)
	}
	if channelBindingNeedsCatsCoLogin(binding) {
		return channelBindingDeviceLinkGuidance(h.db, nil, binding)
	}
	if link := channelBindingDeviceLinkURL(nil, binding); link != "" {
		return "设备授权只会绑定你自己的 CatsCo 账号和设备，不会授权虚拟员工 owner 的电脑。\n请打开链接完成授权：" + link
	}
	return "暂时无法生成设备授权链接，请稍后重试。"
}

func (h *FeishuChannelHandler) feishuBindingDeliverableMessage(binding *types.ChannelAgentBinding) (string, bool, error) {
	if binding == nil {
		return "请先选择一个虚拟员工。", false, nil
	}
	if channelBindingNeedsCatsCoLogin(binding) {
		return channelBindingDeviceLinkGuidance(h.db, nil, binding), false, nil
	}
	if channelBindingRejected(binding) {
		return "你添加该虚拟员工的申请未通过，暂时不能对话。", false, nil
	}
	if pending, err := channelBindingPendingFriendApproval(h.db, binding); err != nil {
		return "", false, err
	} else if pending {
		return "你的好友申请正在等待管理员通过。通过后，我会在这里继续为你服务。", false, nil
	}
	return "", true, nil
}

func (h *FeishuChannelHandler) resolveFeishuBinding(appID, channelUserID, conversationID, conversationType string) (*types.ChannelAgentBinding, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	query := types.ChannelAgentBindingQuery{
		Channel:                 "feishu",
		ChannelAppID:            appID,
		ChannelUserID:           channelUserID,
		ChannelConversationID:   conversationID,
		ChannelConversationType: conversationType,
	}
	binding, err := bindings.ResolveChannelAgentBinding(query)
	if err != nil || binding != nil || conversationType != "p2p" || strings.TrimSpace(conversationID) == "" {
		return binding, err
	}
	baseBinding, err := bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 "feishu",
		ChannelAppID:            appID,
		ChannelUserID:           channelUserID,
		ChannelConversationType: "p2p",
	})
	if err != nil || baseBinding == nil {
		return baseBinding, err
	}
	return bindings.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 baseBinding.Channel,
		ChannelAppID:            baseBinding.ChannelAppID,
		ChannelUserID:           baseBinding.ChannelUserID,
		ChannelConversationID:   conversationID,
		ChannelConversationType: "p2p",
		ActorUID:                baseBinding.ActorUID,
		CanonicalUID:            baseBinding.CanonicalUID,
		OwnerUID:                baseBinding.OwnerUID,
		AgentUID:                baseBinding.AgentUID,
		EntryID:                 baseBinding.EntryID,
		Status:                  baseBinding.Status,
		DeviceAccessEnabled:     baseBinding.DeviceAccessEnabled,
	})
}

func (h *FeishuChannelHandler) resolveFeishuAccessRequest(appID, channelUserID, conversationID, conversationType string) (*types.ChannelAgentAccessRequest, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	return bindings.ResolveChannelAgentAccessRequest(types.ChannelAgentBindingQuery{
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

func (h *FeishuChannelHandler) resolveFeishuEntryScene(sceneKey string, consumeMobileLink bool) (*types.ChannelAgentEntry, int64, error) {
	entry, err := h.activeFeishuEntry(sceneKey)
	if err != nil {
		return nil, 0, err
	}
	if entry != nil {
		return entry, 0, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(sceneKey), "m.") {
		return nil, 0, nil
	}
	return resolveChannelIdentityMobileLink(h.db, sceneKey, "feishu", h.effectiveAppID(""), consumeMobileLink)
}

func (h *FeishuChannelHandler) bindFeishuIdentity(entry *types.ChannelAgentEntry, actorUID int64, channelUserID, conversationID, conversationType string) (*types.ChannelAgentBinding, error) {
	binding, _, err := h.bindOrRequestFeishuIdentity(entry, actorUID, channelUserID, conversationID, conversationType)
	return binding, err
}

func (h *FeishuChannelHandler) bindOrRequestFeishuIdentity(entry *types.ChannelAgentEntry, actorUID int64, channelUserID, conversationID, conversationType string) (*types.ChannelAgentBinding, *types.ChannelAgentAccessRequest, error) {
	return h.bindOrRequestFeishuIdentityWithCanonical(entry, actorUID, channelUserID, conversationID, conversationType, 0)
}

func (h *FeishuChannelHandler) bindOrRequestFeishuIdentityWithCanonical(entry *types.ChannelAgentEntry, actorUID int64, channelUserID, conversationID, conversationType string, canonicalUIDHint int64) (*types.ChannelAgentBinding, *types.ChannelAgentAccessRequest, error) {
	if entry == nil {
		return nil, nil, errors.New("missing channel entry")
	}
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, nil, errors.New("channel binding store not configured")
	}
	if conversationType == "" {
		conversationType = "p2p"
	}
	return bindOrRequestChannelAgentAccessWithCanonical(h.db, bindings, entry, actorUID, "feishu", h.effectiveAppID(""), channelUserID, strings.TrimSpace(conversationID), conversationType, canonicalUIDHint)
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

func feishuOAuthStartURL(r *http.Request, sceneKey string) string {
	return publicBaseURL(r) + "/api/channel-agent-bindings/oauth/feishu/start?scene_key=" + url.QueryEscape(sceneKey)
}

func feishuOAuthShortURL(r *http.Request, sceneKey string) string {
	return publicBaseURL(r) + "/api/f/" + url.PathEscape(sceneKey)
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
		Mentions    []struct {
			Key string `json:"key"`
			ID  struct {
				OpenID  string `json:"open_id"`
				UserID  string `json:"user_id"`
				UnionID string `json:"union_id"`
			} `json:"id"`
			Name      string `json:"name"`
			TenantKey string `json:"tenant_key"`
		} `json:"mentions"`
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

type feishuInboundMedia struct {
	ResourceKey  string
	ResourceType string
	UploadType   string
	FileName     string
	ContentType  string
	Text         string
}

func extractFeishuInboundMedia(messageType, content string) (*feishuInboundMedia, error) {
	var parsed struct {
		ImageKey    string `json:"image_key"`
		FileKey     string `json:"file_key"`
		FileName    string `json:"file_name"`
		Name        string `json:"name"`
		Text        string `json:"text"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, err
	}
	messageType = strings.ToLower(strings.TrimSpace(messageType))
	switch messageType {
	case "image":
		key := strings.TrimSpace(parsed.ImageKey)
		if key == "" {
			return nil, errors.New("missing feishu image key")
		}
		return &feishuInboundMedia{
			ResourceKey:  key,
			ResourceType: "image",
			UploadType:   "image",
			FileName:     "feishu-image-" + key + ".jpg",
			ContentType:  firstNonEmpty(parsed.ContentType, "image/jpeg"),
			Text:         strings.TrimSpace(parsed.Text),
		}, nil
	case "file":
		key := strings.TrimSpace(parsed.FileKey)
		if key == "" {
			return nil, errors.New("missing feishu file key")
		}
		fileName := firstNonEmpty(strings.TrimSpace(parsed.FileName), strings.TrimSpace(parsed.Name), "feishu-file-"+key)
		return &feishuInboundMedia{
			ResourceKey:  key,
			ResourceType: "file",
			UploadType:   "file",
			FileName:     fileName,
			ContentType:  strings.TrimSpace(parsed.ContentType),
			Text:         strings.TrimSpace(parsed.Text),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported feishu media type %q", messageType)
	}
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

func (c *feishuAPIClient) DownloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (*channelMediaDownload, error) {
	if c == nil || strings.TrimSpace(c.config.AppID) == "" || strings.TrimSpace(c.config.AppSecret) == "" {
		return nil, errors.New("feishu app is not configured")
	}
	messageID = strings.TrimSpace(messageID)
	fileKey = strings.TrimSpace(fileKey)
	resourceType = strings.TrimSpace(resourceType)
	if messageID == "" || fileKey == "" || resourceType == "" {
		return nil, errors.New("missing feishu message resource key")
	}
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultFeishuAPIBase), "/") +
		"/open-apis/im/v1/messages/" + url.PathEscape(messageID) +
		"/resources/" + url.PathEscape(fileKey) +
		"?type=" + url.QueryEscape(resourceType)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		return nil, fmt.Errorf("feishu media http %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	contentType := res.Header.Get("Content-Type")
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType == "application/json" {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		var payload struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(body, &payload); err == nil && payload.Code != 0 {
			return nil, fmt.Errorf("feishu media error %d: %s", payload.Code, payload.Msg)
		}
		return nil, fmt.Errorf("feishu media response is not a file: %s", strings.TrimSpace(string(body)))
	}
	fileName := channelMediaFileNameFromDisposition(res.Header.Get("Content-Disposition"))
	if fileName == "" {
		fileName = "feishu-" + resourceType + "-" + fileKey
	}
	return &channelMediaDownload{
		Body:        res.Body,
		FileName:    fileName,
		ContentType: contentType,
	}, nil
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
	mu          sync.Mutex
	replyRoutes map[string]channelOutboundReplyRoute
}

type channelOutboundReplyRoute struct {
	Query     types.ChannelAgentBindingQuery
	ExpiresAt time.Time
}

func NewChannelOutboundDispatcher(db store.Store, feishu feishuAPI, appID string) *ChannelOutboundDispatcher {
	return (&ChannelOutboundDispatcher{
		db:          db,
		replyRoutes: map[string]channelOutboundReplyRoute{},
	}).WithFeishu(feishu, appID)
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

func (h *Hub) recordChannelInboundReplyRoute(topicID string, canonicalUID int64, binding *types.ChannelAgentBinding) {
	if h == nil || binding == nil || canonicalUID <= 0 {
		return
	}
	h.mu.RLock()
	dispatcher := h.channelOut
	h.mu.RUnlock()
	if dispatcher == nil {
		return
	}
	dispatcher.RecordInboundReplyRoute(topicID, canonicalUID, binding)
}

func (h *Hub) clearChannelInboundReplyRoute(topicID string, canonicalUID int64, agentUID int64) {
	if h == nil || canonicalUID <= 0 || agentUID <= 0 {
		return
	}
	h.mu.RLock()
	dispatcher := h.channelOut
	h.mu.RUnlock()
	if dispatcher == nil {
		return
	}
	dispatcher.ClearInboundReplyRoute(topicID, canonicalUID, agentUID)
}

func channelOutboundReplyRouteKey(topicID string, canonicalUID int64, agentUID int64) string {
	return fmt.Sprintf("%s:%d:%d", strings.TrimSpace(topicID), canonicalUID, agentUID)
}

func (d *ChannelOutboundDispatcher) RecordInboundReplyRoute(topicID string, canonicalUID int64, binding *types.ChannelAgentBinding) {
	if d == nil || binding == nil || canonicalUID <= 0 || binding.AgentUID <= 0 || binding.ActorUID <= 0 {
		return
	}
	key := channelOutboundReplyRouteKey(topicID, canonicalUID, binding.AgentUID)
	if key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replyRoutes == nil {
		d.replyRoutes = map[string]channelOutboundReplyRoute{}
	}
	d.replyRoutes[key] = channelOutboundReplyRoute{
		Query: types.ChannelAgentBindingQuery{
			Channel:                 binding.Channel,
			ChannelAppID:            binding.ChannelAppID,
			ChannelUserID:           binding.ChannelUserID,
			ChannelConversationID:   binding.ChannelConversationID,
			ChannelConversationType: binding.ChannelConversationType,
			AgentUID:                binding.AgentUID,
			ActorUID:                binding.ActorUID,
		},
		ExpiresAt: time.Now().Add(2 * time.Hour),
	}
}

func (d *ChannelOutboundDispatcher) ClearInboundReplyRoute(topicID string, canonicalUID int64, agentUID int64) {
	if d == nil || canonicalUID <= 0 || agentUID <= 0 {
		return
	}
	key := channelOutboundReplyRouteKey(topicID, canonicalUID, agentUID)
	d.mu.Lock()
	delete(d.replyRoutes, key)
	d.mu.Unlock()
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

func (h *Hub) forwardChannelGroupBotReply(senderUID int64, topicID string, payload *normalizedMessagePayload, msgID int64) {
	if h == nil || payload == nil || msgID <= 0 || senderUID <= 0 || strings.TrimSpace(topicID) == "" {
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
	go dispatcher.ForwardGroupBotReply(context.Background(), senderUID, topicID, text)
}

func (d *ChannelOutboundDispatcher) ForwardBotReply(ctx context.Context, actorUID int64, agentUID int64, topicID string, text string) error {
	if d == nil || d.db == nil {
		return nil
	}
	bindings, ok := d.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil
	}
	if binding, ok, err := d.lookupRecordedReplyBinding(bindings, topicID, actorUID, agentUID); err != nil {
		return err
	} else if ok {
		sent, err := d.sendChannelBindingReply(ctx, binding, topicID, actorUID, agentUID, text)
		if sent || err != nil {
			return err
		}
	}
	if d.feishu != nil {
		binding, err := bindings.ResolveChannelAgentBindingForActor("feishu", d.feishuAppID, actorUID, agentUID)
		if err != nil {
			return err
		}
		if binding != nil {
			if sent, err := d.sendChannelBindingReply(ctx, binding, topicID, actorUID, agentUID, text); sent || err != nil {
				return err
			}
		}
	}
	if d.weixin != nil {
		binding, err := bindings.ResolveChannelAgentBindingForActor("weixin", d.weixinAppID, actorUID, agentUID)
		if err != nil {
			return err
		}
		if binding != nil {
			_, err := d.sendChannelBindingReply(ctx, binding, topicID, actorUID, agentUID, text)
			return err
		}
	}
	return nil
}

func (d *ChannelOutboundDispatcher) ForwardGroupBotReply(ctx context.Context, senderUID int64, topicID string, text string) error {
	if d == nil || d.db == nil || strings.TrimSpace(topicID) == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	bindingsStore, ok := d.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil
	}
	bindings, err := bindingsStore.ListChannelGroupBindingsForTopic(topicID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding == nil || binding.Status != types.ChannelAgentBindingActive {
			continue
		}
		if _, err := validateDeliverableChannelGroupBinding(d.db, binding); err != nil {
			continue
		}
		switch normalizeChannel(binding.Channel) {
		case "feishu":
			if d.feishu == nil || strings.TrimSpace(binding.ChannelAppID) != strings.TrimSpace(d.feishuAppID) {
				continue
			}
			receiveIDType := "open_id"
			receiveID := binding.ChannelUserID
			if binding.ChannelConversationType == "group" && strings.TrimSpace(binding.ChannelConversationID) != "" {
				receiveIDType = "chat_id"
				receiveID = binding.ChannelConversationID
			}
			if strings.TrimSpace(receiveID) == "" {
				continue
			}
			if err := d.feishu.SendTextMessage(ctx, receiveIDType, receiveID, text); err != nil {
				log.Printf("feishu group outbound reply failed topic=%s sender=%d binding=%d: %v", topicID, senderUID, binding.ID, err)
			}
		case "weixin":
			if d.weixin == nil || strings.TrimSpace(binding.ChannelAppID) != strings.TrimSpace(d.weixinAppID) || strings.TrimSpace(binding.ChannelUserID) == "" {
				continue
			}
			if err := d.weixin.SendTextMessage(ctx, binding.ChannelUserID, text); err != nil {
				log.Printf("weixin group outbound reply failed topic=%s sender=%d binding=%d: %v", topicID, senderUID, binding.ID, err)
			}
		}
	}
	return nil
}

func (d *ChannelOutboundDispatcher) lookupRecordedReplyBinding(bindings store.ChannelAgentBindingStore, topicID string, canonicalUID int64, agentUID int64) (*types.ChannelAgentBinding, bool, error) {
	if d == nil || bindings == nil || canonicalUID <= 0 || agentUID <= 0 {
		return nil, false, nil
	}
	key := channelOutboundReplyRouteKey(topicID, canonicalUID, agentUID)
	now := time.Now()
	d.mu.Lock()
	route, ok := d.replyRoutes[key]
	if ok && !route.ExpiresAt.IsZero() && now.After(route.ExpiresAt) {
		delete(d.replyRoutes, key)
		ok = false
	}
	d.mu.Unlock()
	if !ok {
		return nil, false, nil
	}
	binding, err := bindings.ResolveChannelAgentBinding(route.Query)
	if err != nil || binding == nil {
		return binding, false, err
	}
	return binding, true, nil
}

func (d *ChannelOutboundDispatcher) sendChannelBindingReply(ctx context.Context, binding *types.ChannelAgentBinding, topicID string, actorUID int64, agentUID int64, text string) (bool, error) {
	if d == nil || binding == nil {
		return false, nil
	}
	if err := validateDeliverableChannelBinding(d.db, binding); err != nil {
		return false, nil
	}
	switch normalizeChannel(binding.Channel) {
	case "feishu":
		if binding.ChannelConversationType == "group" && strings.TrimSpace(binding.ChannelConversationID) != "" {
			if speaker, ok := feishuGroupAgentSpeakerFor(binding.ChannelConversationID, binding.AgentUID); ok {
				if err := sendFeishuCustomBotText(ctx, speaker, text); err != nil {
					log.Printf("feishu group custom bot outbound failed topic=%s actor=%d agent=%d chat=%s: %v", topicID, actorUID, agentUID, binding.ChannelConversationID, err)
					return true, err
				}
				return true, nil
			}
		}
		if d.feishu == nil {
			return false, nil
		}
		receiveIDType := "open_id"
		receiveID := binding.ChannelUserID
		if binding.ChannelConversationType == "group" && binding.ChannelConversationID != "" {
			receiveIDType = "chat_id"
			receiveID = binding.ChannelConversationID
		}
		if receiveID == "" {
			return false, nil
		}
		if err := d.feishu.SendTextMessage(ctx, receiveIDType, receiveID, text); err != nil {
			log.Printf("feishu outbound reply failed topic=%s actor=%d agent=%d: %v", topicID, actorUID, agentUID, err)
			return true, err
		}
		return true, nil
	case "weixin":
		if d.weixin == nil || binding.ChannelUserID == "" {
			return false, nil
		}
		if err := d.weixin.SendTextMessage(ctx, binding.ChannelUserID, text); err != nil {
			log.Printf("weixin outbound reply failed topic=%s actor=%d agent=%d: %v", topicID, actorUID, agentUID, err)
			return true, err
		}
		return true, nil
	default:
		return false, nil
	}
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

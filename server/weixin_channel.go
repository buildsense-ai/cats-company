package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	defaultWeixinAPIBase    = "https://api.weixin.qq.com"
	defaultWeixinQRShowBase = "https://mp.weixin.qq.com/cgi-bin/showqrcode"
)

// WeixinChannelConfig contains the official-account settings for the cloud
// Weixin channel.
type WeixinChannelConfig struct {
	AppID         string
	AppSecret     string
	EventToken    string
	APIBaseURL    string
	QRShowBaseURL string
}

// WeixinQRCode describes a Weixin parameter QR code.
type WeixinQRCode struct {
	Ticket   string
	ImageURL string
	URL      string
}

type weixinAPI interface {
	AppID() string
	CreatePermanentQRCode(ctx context.Context, sceneKey string) (*WeixinQRCode, error)
	SendTextMessage(ctx context.Context, openID string, text string) error
}

// WeixinChannelHandler owns Weixin official-account QR binding and callbacks.
type WeixinChannelHandler struct {
	db     store.Store
	hub    *Hub
	config WeixinChannelConfig
	api    weixinAPI
}

func NewWeixinChannelHandlerFromEnv(db store.Store, hub *Hub) *WeixinChannelHandler {
	cfg := weixinConfigFromEnv()
	return NewWeixinChannelHandler(db, hub, cfg, newWeixinAPIClient(cfg))
}

func NewWeixinChannelHandler(db store.Store, hub *Hub, cfg WeixinChannelConfig, api weixinAPI) *WeixinChannelHandler {
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = defaultWeixinAPIBase
	}
	if strings.TrimSpace(cfg.QRShowBaseURL) == "" {
		cfg.QRShowBaseURL = defaultWeixinQRShowBase
	}
	if api == nil {
		api = newWeixinAPIClient(cfg)
	}
	return &WeixinChannelHandler{db: db, hub: hub, config: cfg, api: api}
}

func (h *WeixinChannelHandler) InstallOutboundDispatcher() {
	if h == nil || h.hub == nil {
		return
	}
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	if h.hub.channelOut == nil {
		h.hub.channelOut = NewChannelOutboundDispatcher(h.db, nil, "").WithWeixin(h.api, h.effectiveAppID(""))
		return
	}
	h.hub.channelOut.WithWeixin(h.api, h.effectiveAppID(""))
}

func weixinConfigFromEnv() WeixinChannelConfig {
	return WeixinChannelConfig{
		AppID:         firstEnv("CATSCO_WEIXIN_APP_ID", "CATSCO_WECHAT_APP_ID", "WEIXIN_APP_ID", "WECHAT_APP_ID"),
		AppSecret:     firstEnv("CATSCO_WEIXIN_APP_SECRET", "CATSCO_WECHAT_APP_SECRET", "WEIXIN_APP_SECRET", "WECHAT_APP_SECRET"),
		EventToken:    firstEnv("CATSCO_WEIXIN_EVENT_TOKEN", "CATSCO_WECHAT_EVENT_TOKEN", "WEIXIN_EVENT_TOKEN", "WECHAT_EVENT_TOKEN"),
		APIBaseURL:    firstEnv("CATSCO_WEIXIN_API_BASE_URL", "CATSCO_WECHAT_API_BASE_URL", "WEIXIN_API_BASE_URL", "WECHAT_API_BASE_URL"),
		QRShowBaseURL: firstEnv("CATSCO_WEIXIN_QR_SHOW_BASE_URL", "CATSCO_WECHAT_QR_SHOW_BASE_URL"),
	}
}

func weixinQRCodeConfiguredFromEnv() bool {
	cfg := weixinConfigFromEnv()
	return strings.TrimSpace(cfg.AppID) != "" && strings.TrimSpace(cfg.AppSecret) != ""
}

func weixinQRCodePath(sceneKey string) string {
	return "/api/channel-agent-entry/weixin-qrcode?scene_key=" + url.QueryEscape(sceneKey)
}

// HandleQRCode redirects a public entry scene key to its official Weixin
// parameter QR image.
func (h *WeixinChannelHandler) HandleQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sceneKey := strings.TrimSpace(r.URL.Query().Get("scene_key"))
	if sceneKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing scene_key"})
		return
	}
	appID := h.effectiveAppID("")
	entry, err := h.activeWeixinEntry(sceneKey, appID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load entry"})
		return
	}
	if entry == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "entry not found or expired"})
		return
	}
	if h.api == nil || strings.TrimSpace(h.api.AppID()) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "weixin official account is not configured"})
		return
	}
	qr, err := h.api.CreatePermanentQRCode(r.Context(), entry.SceneKey)
	if err != nil {
		log.Printf("weixin qr create failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create weixin qr code"})
		return
	}
	if qr == nil || strings.TrimSpace(qr.ImageURL) == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "weixin qr code missing image url"})
		return
	}
	http.Redirect(w, r, qr.ImageURL, http.StatusFound)
}

// HandleEvents receives Weixin official-account URL verification, scan events,
// and private text messages.
func (h *WeixinChannelHandler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleURLVerification(w, r)
	case http.MethodPost:
		h.handleEventPost(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *WeixinChannelHandler) handleURLVerification(w http.ResponseWriter, r *http.Request) {
	if !h.verifySignature(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(r.URL.Query().Get("echostr")))
}

func (h *WeixinChannelHandler) handleEventPost(w http.ResponseWriter, r *http.Request) {
	if !h.verifySignature(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if encryptType := strings.TrimSpace(r.URL.Query().Get("encrypt_type")); encryptType != "" && encryptType != "raw" {
		log.Printf("encrypted weixin events are not enabled: encrypt_type=%s", encryptType)
		writeWeixinSuccess(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var msg weixinEventMessage
	if err := xml.NewDecoder(r.Body).Decode(&msg); err != nil {
		log.Printf("decode weixin event failed: %v", err)
		writeWeixinSuccess(w)
		return
	}
	switch strings.ToLower(strings.TrimSpace(msg.MsgType)) {
	case "event":
		h.handleScanEvent(w, r.Context(), &msg)
	case "text":
		h.handleTextMessage(w, r.Context(), &msg)
	default:
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "当前微信入口先支持文本消息，图片和文件能力会在后续版本接入。")
	}
}

func (h *WeixinChannelHandler) handleScanEvent(w http.ResponseWriter, ctx context.Context, msg *weixinEventMessage) {
	if msg == nil {
		writeWeixinSuccess(w)
		return
	}
	event := strings.ToLower(strings.TrimSpace(msg.Event))
	if event != "subscribe" && event != "scan" {
		writeWeixinSuccess(w)
		return
	}
	openID := strings.TrimSpace(msg.FromUserName)
	sceneKey := normalizeWeixinSceneKey(msg.EventKey)
	if openID == "" {
		writeWeixinSuccess(w)
		return
	}
	if sceneKey == "" {
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "欢迎关注 CatsCo。请扫描虚拟员工入口二维码开始对话。")
		return
	}
	appID := h.effectiveAppID(msg.ToUserName)
	entry, err := h.activeWeixinEntry(sceneKey, appID)
	if err != nil {
		log.Printf("weixin scan entry lookup failed: %v", err)
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "读取虚拟员工入口失败，请稍后重试。")
		return
	}
	if entry == nil {
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "这个虚拟员工入口不存在或已失效，请联系管理员重新生成入口码。")
		return
	}
	actorUID, err := h.ensureWeixinActor(appID, openID)
	if err != nil {
		log.Printf("ensure weixin actor failed: %v", err)
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "创建微信用户身份失败，请稍后重试。")
		return
	}
	binding, err := h.bindWeixinIdentity(entry, actorUID, appID, openID, "", "p2p")
	if err != nil {
		log.Printf("bind weixin identity failed: %v", err)
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "保存虚拟员工绑定失败，请稍后重试。")
		return
	}
	if err := h.db.CreateTopic(p2pTopicID(actorUID, entry.AgentUID), "p2p", actorUID); err != nil {
		log.Printf("create weixin agent topic failed: %v", err)
	}
	agent, _ := h.db.GetUser(entry.AgentUID)
	name := "该虚拟员工"
	if agent != nil {
		name = displayNameOrUsername(agent.DisplayName, agent.Username)
	}
	reply := fmt.Sprintf("已绑定「%s」。你现在可以直接在公众号聊天框里提问。", name)
	if link := channelBindingDeviceLinkURL(nil, binding); link != "" {
		reply += "\n\n如需让我使用你的电脑文件，请登录 CatsCo 完成设备授权：\n" + link
	}
	writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, reply)
}

func (h *WeixinChannelHandler) handleTextMessage(w http.ResponseWriter, ctx context.Context, msg *weixinEventMessage) {
	if msg == nil {
		writeWeixinSuccess(w)
		return
	}
	openID := strings.TrimSpace(msg.FromUserName)
	text := strings.TrimSpace(msg.Content)
	if openID == "" || text == "" {
		writeWeixinSuccess(w)
		return
	}
	appID := h.effectiveAppID(msg.ToUserName)
	binding, err := h.resolveWeixinBinding(appID, openID, "", "p2p")
	if err != nil {
		log.Printf("resolve weixin binding failed: %v", err)
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "读取虚拟员工绑定失败，请稍后重试。")
		return
	}
	if binding == nil {
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "请先扫描虚拟员工入口二维码完成绑定，然后再回到公众号聊天框提问。")
		return
	}
	actorUID := binding.ActorUID
	if actorUID <= 0 {
		actorUID, err = h.ensureWeixinActor(appID, openID)
		if err != nil {
			log.Printf("ensure legacy weixin actor failed: %v", err)
			writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "创建微信用户身份失败，请稍后重试。")
			return
		}
		if _, err = h.bindWeixinIdentity(bindingAsEntry(binding), actorUID, appID, openID, binding.ChannelConversationID, binding.ChannelConversationType); err != nil {
			log.Printf("upgrade legacy weixin binding failed: %v", err)
			writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "更新微信用户身份失败，请稍后重试。")
			return
		}
	}
	clientMsgID := weixinClientMsgID(msg)
	if err := deliverInboundChannelTextToAgent(h.db, h.hub, actorUID, binding.AgentUID, text, clientMsgID, "weixin", map[string]interface{}{
		"source_channel":                 "weixin",
		"channel_app_id":                 appID,
		"channel_user_id":                openID,
		"channel_conversation_type":      "p2p",
		"channel_message_id":             strings.TrimSpace(msg.MsgID),
		"channel_identity_source":        "weixin.event",
		"channel_identity_trust":         "weixin_official_account_callback",
		"channel_agent_binding_entry_id": binding.EntryID,
	}); err != nil {
		log.Printf("deliver weixin message failed: %v", err)
		writeWeixinTextReply(w, msg.FromUserName, msg.ToUserName, "虚拟员工暂时不可用，请稍后再试。")
		return
	}
	writeWeixinSuccess(w)
}

func (h *WeixinChannelHandler) resolveWeixinBinding(appID, channelUserID, conversationID, conversationType string) (*types.ChannelAgentBinding, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	return bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 "weixin",
		ChannelAppID:            appID,
		ChannelUserID:           channelUserID,
		ChannelConversationID:   conversationID,
		ChannelConversationType: normalizeConversationType(conversationType),
	})
}

func (h *WeixinChannelHandler) activeWeixinEntry(sceneKey, appID string) (*types.ChannelAgentEntry, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	entry, err := bindings.GetChannelAgentEntryBySceneKey(sceneKey)
	if err != nil || entry == nil {
		return entry, err
	}
	if entry.Status != "active" || entry.Channel != "weixin" {
		return nil, nil
	}
	entryAppID := strings.TrimSpace(entry.ChannelAppID)
	appID = strings.TrimSpace(appID)
	if appID != "" && entryAppID != appID {
		return nil, nil
	}
	if appID == "" && entryAppID != "" {
		return nil, nil
	}
	if !h.entryAgentAvailable(entry) {
		return nil, nil
	}
	return entry, nil
}

func (h *WeixinChannelHandler) bindWeixinIdentity(entry *types.ChannelAgentEntry, actorUID int64, appID, channelUserID, conversationID, conversationType string) (*types.ChannelAgentBinding, error) {
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
	if strings.TrimSpace(appID) == "" {
		appID = h.effectiveAppID(entry.ChannelAppID)
	}
	if strings.TrimSpace(appID) == "" && isProductionLikeEnv() {
		return nil, errors.New("weixin app id is required")
	}
	return bindings.UpsertChannelAgentBinding(&types.ChannelAgentBinding{
		Channel:                 "weixin",
		ChannelAppID:            strings.TrimSpace(appID),
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

func (h *WeixinChannelHandler) entryAgentAvailable(entry *types.ChannelAgentEntry) bool {
	if entry == nil || h == nil || h.db == nil {
		return false
	}
	agent, err := h.db.GetUser(entry.AgentUID)
	if err != nil || agent == nil || agent.AccountType != types.AccountBot || agent.State != 0 {
		return false
	}
	ownerUID, err := h.db.GetBotOwner(entry.AgentUID)
	if err != nil || ownerUID != entry.OwnerUID {
		return false
	}
	return true
}

func (h *WeixinChannelHandler) ensureWeixinActor(appID, openID string) (int64, error) {
	username := channelActorUsername("weixin", appID, openID)
	if user, err := h.db.GetUserByUsername(username); err == nil && user != nil {
		return user.ID, nil
	} else if err != nil {
		return 0, err
	}
	uid, err := h.db.CreateUser(&types.User{
		Username:    username,
		DisplayName: "Weixin User",
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

func (h *WeixinChannelHandler) effectiveAppID(value string) string {
	if appID := strings.TrimSpace(h.config.AppID); appID != "" {
		return appID
	}
	if h.api != nil {
		if appID := strings.TrimSpace(h.api.AppID()); appID != "" {
			return appID
		}
	}
	return strings.TrimSpace(value)
}

func (h *WeixinChannelHandler) verifySignature(r *http.Request) bool {
	required := strings.TrimSpace(h.config.EventToken)
	if required == "" {
		return !isProductionLikeEnv()
	}
	signature := strings.TrimSpace(r.URL.Query().Get("signature"))
	timestamp := strings.TrimSpace(r.URL.Query().Get("timestamp"))
	nonce := strings.TrimSpace(r.URL.Query().Get("nonce"))
	if signature == "" || timestamp == "" || nonce == "" {
		return false
	}
	parts := []string{required, timestamp, nonce}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	expected := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

type weixinEventMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Event        string   `xml:"Event"`
	EventKey     string   `xml:"EventKey"`
	Ticket       string   `xml:"Ticket"`
	Content      string   `xml:"Content"`
	MsgID        string   `xml:"MsgId"`
}

func normalizeWeixinSceneKey(eventKey string) string {
	value := strings.TrimSpace(eventKey)
	value = strings.TrimPrefix(value, "qrscene_")
	return strings.TrimSpace(value)
}

func weixinClientMsgID(msg *weixinEventMessage) string {
	if msg == nil {
		return "weixin:unknown"
	}
	if id := strings.TrimSpace(msg.MsgID); id != "" {
		return "weixin:" + id
	}
	sum := sha1.Sum([]byte(strings.Join([]string{
		msg.FromUserName,
		msg.ToUserName,
		strings.TrimSpace(msg.Content),
		fmt.Sprintf("%d", msg.CreateTime),
	}, "\x00")))
	return "weixin:" + hex.EncodeToString(sum[:])[:24]
}

type weixinTextReply struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
}

func writeWeixinTextReply(w http.ResponseWriter, toUser, fromUser, content string) {
	resp := weixinTextReply{
		ToUserName:   toUser,
		FromUserName: fromUser,
		CreateTime:   time.Now().Unix(),
		MsgType:      "text",
		Content:      content,
	}
	body, err := xml.Marshal(resp)
	if err != nil {
		writeWeixinSuccess(w)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeWeixinSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
}

type weixinAPIClient struct {
	config  WeixinChannelConfig
	http    *http.Client
	mu      sync.Mutex
	token   string
	expiry  time.Time
	qrCache map[string]*WeixinQRCode
}

func newWeixinAPIClient(cfg WeixinChannelConfig) *weixinAPIClient {
	return &weixinAPIClient{
		config:  cfg,
		http:    &http.Client{Timeout: 10 * time.Second},
		qrCache: map[string]*WeixinQRCode{},
	}
}

func (c *weixinAPIClient) AppID() string {
	return strings.TrimSpace(c.config.AppID)
}

func (c *weixinAPIClient) CreatePermanentQRCode(ctx context.Context, sceneKey string) (*WeixinQRCode, error) {
	sceneKey = strings.TrimSpace(sceneKey)
	if sceneKey == "" {
		return nil, errors.New("missing weixin qr scene key")
	}
	if c == nil || strings.TrimSpace(c.config.AppID) == "" || strings.TrimSpace(c.config.AppSecret) == "" {
		return nil, errors.New("weixin official account is not configured")
	}
	c.mu.Lock()
	if cached := c.qrCache[sceneKey]; cached != nil && cached.ImageURL != "" {
		next := *cached
		c.mu.Unlock()
		return &next, nil
	}
	c.mu.Unlock()

	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	createURL := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultWeixinAPIBase), "/") + "/cgi-bin/qrcode/create?access_token=" + url.QueryEscape(token)
	payload := map[string]interface{}{
		"action_name": "QR_LIMIT_STR_SCENE",
		"action_info": map[string]interface{}{
			"scene": map[string]string{"scene_str": sceneKey},
		},
	}
	var resp struct {
		Ticket        string `json:"ticket"`
		ExpireSeconds int64  `json:"expire_seconds"`
		URL           string `json:"url"`
		ErrCode       int    `json:"errcode"`
		ErrMsg        string `json:"errmsg"`
	}
	if err := c.postJSON(ctx, createURL, payload, &resp); err != nil {
		return nil, err
	}
	if resp.ErrCode != 0 {
		return nil, fmt.Errorf("weixin qr create error %d: %s", resp.ErrCode, resp.ErrMsg)
	}
	if strings.TrimSpace(resp.Ticket) == "" {
		return nil, errors.New("weixin qr response missing ticket")
	}
	qr := &WeixinQRCode{
		Ticket:   resp.Ticket,
		ImageURL: strings.TrimRight(firstNonEmpty(c.config.QRShowBaseURL, defaultWeixinQRShowBase), "?") + "?ticket=" + url.QueryEscape(resp.Ticket),
		URL:      resp.URL,
	}
	c.mu.Lock()
	c.qrCache[sceneKey] = qr
	c.mu.Unlock()
	next := *qr
	return &next, nil
}

func (c *weixinAPIClient) SendTextMessage(ctx context.Context, openID string, text string) error {
	if c == nil || strings.TrimSpace(c.config.AppID) == "" || strings.TrimSpace(c.config.AppSecret) == "" {
		return errors.New("weixin official account is not configured")
	}
	openID = strings.TrimSpace(openID)
	if openID == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	sendURL := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultWeixinAPIBase), "/") + "/cgi-bin/message/custom/send?access_token=" + url.QueryEscape(token)
	payload := map[string]interface{}{
		"touser":  openID,
		"msgtype": "text",
		"text": map[string]string{
			"content": text,
		},
	}
	var resp struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := c.postJSON(ctx, sendURL, payload, &resp); err != nil {
		return err
	}
	if resp.ErrCode != 0 {
		return fmt.Errorf("weixin send message error %d: %s", resp.ErrCode, resp.ErrMsg)
	}
	return nil
}

func (c *weixinAPIClient) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.expiry.Add(-time.Minute)) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	tokenURL := strings.TrimRight(firstNonEmpty(c.config.APIBaseURL, defaultWeixinAPIBase), "/") +
		"/cgi-bin/token?grant_type=client_credential&appid=" + url.QueryEscape(c.config.AppID) +
		"&secret=" + url.QueryEscape(c.config.AppSecret)
	var resp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := c.getJSON(ctx, tokenURL, &resp); err != nil {
		return "", err
	}
	if resp.ErrCode != 0 {
		return "", fmt.Errorf("weixin access token error %d: %s", resp.ErrCode, resp.ErrMsg)
	}
	if strings.TrimSpace(resp.AccessToken) == "" {
		return "", errors.New("weixin access token response missing token")
	}
	expires := resp.ExpiresIn
	if expires <= 0 {
		expires = 7200
	}
	c.mu.Lock()
	c.token = resp.AccessToken
	c.expiry = time.Now().Add(time.Duration(expires) * time.Second)
	c.mu.Unlock()
	return resp.AccessToken, nil
}

func (c *weixinAPIClient) postJSON(ctx context.Context, endpoint string, payload interface{}, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req, out)
}

func (c *weixinAPIClient) getJSON(ctx context.Context, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, out)
}

func (c *weixinAPIClient) doJSON(req *http.Request, out interface{}) error {
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
		return fmt.Errorf("weixin http %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode weixin response: %w", err)
	}
	return nil
}

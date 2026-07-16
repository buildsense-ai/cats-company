package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	urlpath "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	weixinClawBotChannelVersion      = "catsco-weixin-clawbot/1.0"
	defaultWeixinClawBotCDNBaseURL   = "https://novac2c.cdn.weixin.qq.com/c2c"
	weixinClawBotMaxOutboundMessages = 8
)

type WeixinClawBotConfig struct {
	ILinkBaseURL             string
	CDNBaseURL               string
	MediaHostAllowlist       []string
	MediaDownloadURLTemplate string
	WorkerEnabled            bool
	RefreshInterval          time.Duration
	LongPollTimeout          time.Duration
}

type WeixinClawBotHandler struct {
	db     store.Store
	hub    *Hub
	config WeixinClawBotConfig
	api    weixinClawBotAPI

	mu      sync.Mutex
	cancel  context.CancelFunc
	running map[int64]context.CancelFunc
}

type weixinClawBotAPI interface {
	GetQRCodeStatus(ctx context.Context, qrcode string) (*weixinClawBotQRCodeStatus, error)
	GetUpdates(ctx context.Context, botToken string, getUpdatesBuf string) (*weixinClawBotUpdates, error)
	DownloadMedia(ctx context.Context, botToken string, ref weixinClawBotMediaRef) (*channelMediaDownload, error)
	SendTextMessage(ctx context.Context, botToken string, toUserID string, text string, contextToken string, fromUserID string) error
	SendMediaMessage(ctx context.Context, botToken string, toUserID string, media weixinClawBotOutboundMedia, contextToken string, fromUserID string) error
}

type weixinClawBotQRCodeStatus struct {
	Ret         int    `json:"ret,omitempty"`
	ErrCode     int    `json:"errcode,omitempty"`
	ErrMsg      string `json:"errmsg,omitempty"`
	Status      string `json:"status,omitempty"`
	BotToken    string `json:"bot_token,omitempty"`
	ILinkBotID  string `json:"ilink_bot_id,omitempty"`
	ILinkUserID string `json:"ilink_user_id,omitempty"`
	BaseURL     string `json:"baseurl,omitempty"`
}

type weixinClawBotUpdates struct {
	Ret           int                    `json:"ret,omitempty"`
	ErrCode       int                    `json:"errcode,omitempty"`
	ErrMsg        string                 `json:"errmsg,omitempty"`
	Messages      []weixinClawBotMessage `json:"msgs,omitempty"`
	GetUpdatesBuf string                 `json:"get_updates_buf,omitempty"`
}

type weixinClawBotMessage struct {
	MessageID    json.RawMessage            `json:"message_id,omitempty"`
	MessageType  int                        `json:"message_type,omitempty"`
	FromUserID   string                     `json:"from_user_id,omitempty"`
	ToUserID     string                     `json:"to_user_id,omitempty"`
	ContextToken string                     `json:"context_token,omitempty"`
	ItemList     []weixinClawBotMessageItem `json:"item_list,omitempty"`
}

type weixinClawBotMessageItem struct {
	Type           int                    `json:"type,omitempty"`
	TextItem       weixinClawBotTextItem  `json:"text_item,omitempty"`
	ImageItem      map[string]interface{} `json:"image_item,omitempty"`
	FileItem       map[string]interface{} `json:"file_item,omitempty"`
	MediaItem      map[string]interface{} `json:"media_item,omitempty"`
	AttachmentItem map[string]interface{} `json:"attachment_item,omitempty"`
	DocItem        map[string]interface{} `json:"doc_item,omitempty"`
	Raw            json.RawMessage        `json:"-"`
	Fields         map[string]interface{} `json:"-"`
}

type weixinClawBotTextItem struct {
	Text string `json:"text,omitempty"`
}

func (i *weixinClawBotMessageItem) UnmarshalJSON(data []byte) error {
	type itemAlias weixinClawBotMessageItem
	var decoded itemAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(data, &fields); err == nil {
		decoded.Fields = fields
	}
	decoded.Raw = append(decoded.Raw[:0], data...)
	*i = weixinClawBotMessageItem(decoded)
	return nil
}

type weixinClawBotMediaRef struct {
	Kind        string
	Name        string
	URL         string
	AESKey      string
	MediaID     string
	ContentType string
	Size        int64
	ItemType    int
	Source      string
	Raw         json.RawMessage
}

type weixinClawBotUnsupportedItem struct {
	Type   int
	Reason string
	Name   string
	Raw    json.RawMessage
}

type weixinClawBotOutboundMedia struct {
	Type        string
	Name        string
	Path        string
	Size        int64
	ContentType string
}

type weixinClawBotAPIError struct {
	Operation string
	Status    int
	Ret       int
	ErrCode   int
	ErrMsg    string
}

func (e *weixinClawBotAPIError) Error() string {
	if e == nil {
		return ""
	}
	if e.ErrMsg != "" {
		return fmt.Sprintf("weixin clawbot %s failed: %s", e.Operation, e.ErrMsg)
	}
	if e.Status > 0 {
		return fmt.Sprintf("weixin clawbot %s failed: http status %d", e.Operation, e.Status)
	}
	return fmt.Sprintf("weixin clawbot %s failed: ret=%d errcode=%d", e.Operation, e.Ret, e.ErrCode)
}

func NewWeixinClawBotHandlerFromEnv(db store.Store, hub *Hub) *WeixinClawBotHandler {
	cfg := weixinClawBotConfigFromEnv()
	return NewWeixinClawBotHandler(db, hub, cfg, nil)
}

func NewWeixinClawBotHandler(db store.Store, hub *Hub, cfg WeixinClawBotConfig, api weixinClawBotAPI) *WeixinClawBotHandler {
	if strings.TrimSpace(cfg.ILinkBaseURL) == "" {
		cfg.ILinkBaseURL = configuredWeixinClawBotILinkBaseURL()
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 10 * time.Second
	}
	if cfg.LongPollTimeout <= 0 {
		cfg.LongPollTimeout = 30 * time.Second
	}
	if api == nil {
		api = newHTTPWeixinClawBotAPI(cfg)
	}
	return &WeixinClawBotHandler{
		db:      db,
		hub:     hub,
		config:  cfg,
		api:     api,
		running: map[int64]context.CancelFunc{},
	}
}

func weixinClawBotConfigFromEnv() WeixinClawBotConfig {
	return WeixinClawBotConfig{
		ILinkBaseURL:             configuredWeixinClawBotILinkBaseURL(),
		CDNBaseURL:               firstEnv("CATSCO_WEIXIN_CLAWBOT_CDN_BASE_URL", "WEIXIN_CLAWBOT_CDN_BASE_URL", "WEIXIN_CDN_BASE_URL"),
		MediaHostAllowlist:       commaListEnv("CATSCO_WEIXIN_CLAWBOT_MEDIA_HOST_ALLOWLIST", "WEIXIN_CLAWBOT_MEDIA_HOST_ALLOWLIST"),
		MediaDownloadURLTemplate: firstEnv("CATSCO_WEIXIN_CLAWBOT_MEDIA_DOWNLOAD_URL_TEMPLATE", "WEIXIN_CLAWBOT_MEDIA_DOWNLOAD_URL_TEMPLATE"),
		WorkerEnabled:            !falseEnv(firstEnv("CATSCO_WEIXIN_CLAWBOT_WORKER_ENABLED", "WEIXIN_CLAWBOT_WORKER_ENABLED")),
		RefreshInterval:          secondsEnvDuration("CATSCO_WEIXIN_CLAWBOT_WORKER_REFRESH_SECONDS", 10, 2, 120),
		LongPollTimeout:          secondsEnvDuration("CATSCO_WEIXIN_CLAWBOT_LONGPOLL_SECONDS", 30, 5, 120),
	}
}

func (h *WeixinClawBotHandler) InstallOutboundDispatcher() {
	if h == nil || h.hub == nil {
		return
	}
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	if h.hub.channelOut == nil {
		h.hub.channelOut = NewChannelOutboundDispatcher(h.db, nil, "").WithWeixinClawBot(h)
		return
	}
	h.hub.channelOut.WithWeixinClawBot(h)
}

func (h *WeixinClawBotHandler) Start() {
	if h == nil || !h.config.WorkerEnabled {
		return
	}
	h.mu.Lock()
	if h.cancel != nil {
		h.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.mu.Unlock()
	go h.manageTokenWorkers(ctx)
}

func (h *WeixinClawBotHandler) Stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	cancel := h.cancel
	h.cancel = nil
	for id, stop := range h.running {
		stop()
		delete(h.running, id)
	}
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (h *WeixinClawBotHandler) HandleQRCodeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	qrcode := strings.TrimSpace(r.URL.Query().Get("qrcode"))
	sceneKey := strings.TrimSpace(r.URL.Query().Get("scene_key"))
	if qrcode == "" || sceneKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing qrcode or scene_key"})
		return
	}
	status, err := h.api.GetQRCodeStatus(r.Context(), qrcode)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]interface{}{
		"ret":            status.Ret,
		"errcode":        status.ErrCode,
		"errmsg":         status.ErrMsg,
		"status":         status.Status,
		"token_received": strings.TrimSpace(status.BotToken) != "",
	}
	if strings.EqualFold(strings.TrimSpace(status.Status), "confirmed") && strings.TrimSpace(status.BotToken) != "" {
		token, target, saveErr := h.saveAuthorizedTokenForScene(r, sceneKey, status)
		if saveErr != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": saveErr.Error()})
			return
		}
		resp["token_saved"] = true
		resp["target"] = target
		resp["token"] = token
		if h.config.WorkerEnabled {
			h.syncTokenWorkers(context.Background())
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *WeixinClawBotHandler) saveAuthorizedTokenForScene(r *http.Request, sceneKey string, status *weixinClawBotQRCodeStatus) (*types.WeixinClawBotToken, string, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, "", errors.New("channel binding store not configured")
	}
	canonicalUID := UIDFromContext(r.Context())
	if canonicalUID <= 0 {
		return nil, "", errors.New("login required")
	}
	if status == nil || strings.TrimSpace(status.BotToken) == "" {
		return nil, "", errors.New("ClawBot authorization did not return bot_token")
	}
	channelUserID := strings.TrimSpace(status.ILinkUserID)
	if channelUserID == "" {
		return nil, "", errors.New("ClawBot authorization did not return ilink_user_id")
	}
	botToken := strings.TrimSpace(status.BotToken)
	tokenHash := hashWeixinClawBotToken(botToken)
	token := &types.WeixinClawBotToken{
		TokenHash:      tokenHash,
		BotToken:       botToken,
		TokenLast4:     last4(botToken),
		Status:         types.WeixinClawBotTokenActive,
		ILinkBotID:     strings.TrimSpace(status.ILinkBotID),
		ILinkUserID:    channelUserID,
		BaseURL:        strings.TrimSpace(status.BaseURL),
		SourceSceneKey: sceneKey,
		ContextTokens:  map[string]types.WeixinClawBotContext{},
	}
	if strings.HasPrefix(sceneKey, "m.") {
		link, err := bindings.GetChannelIdentityMobileLink(sceneKey)
		if err != nil || link == nil {
			return nil, "", errors.New("ClawBot mobile link not found or expired")
		}
		if link.CanonicalUID != canonicalUID {
			return nil, "", errors.New("ClawBot mobile link belongs to another CatsCo user")
		}
		entry, resolvedCanonicalUID, err := resolveChannelIdentityMobileLink(h.db, sceneKey, "weixin_clawbot", "", false)
		if err != nil {
			return nil, "", err
		}
		if entry == nil || resolvedCanonicalUID != canonicalUID {
			return nil, "", errors.New("ClawBot mobile link is no longer valid")
		}
		token.OwnerUID = entry.OwnerUID
		saved, err := bindings.UpsertWeixinClawBotToken(token)
		if err != nil {
			return nil, "", err
		}
		actorUID, err := ensureChannelActor(h.db, "weixin_clawbot", tokenHash, channelUserID)
		if err != nil {
			return nil, "", err
		}
		binding, _, err := bindOrRequestChannelAgentAccessWithCanonical(
			h.db,
			bindings,
			entry,
			actorUID,
			"weixin_clawbot",
			tokenHash,
			channelUserID,
			"",
			"p2p",
			canonicalUID,
		)
		if err != nil {
			return nil, "", err
		}
		if binding == nil {
			return nil, "", errors.New("ClawBot binding was not created")
		}
		if _, err := bindings.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
			Channel:                 "weixin_clawbot",
			ChannelAppID:            tokenHash,
			ChannelUserID:           channelUserID,
			ChannelConversationType: "p2p",
			ActorUID:                actorUID,
			AgentUID:                entry.AgentUID,
			Source:                  "weixin_clawbot_mobile_link",
		}); err != nil {
			return nil, "", err
		}
		_, _, _ = resolveChannelIdentityMobileLink(h.db, sceneKey, "weixin_clawbot", "", true)
		return saved, "agent", nil
	}
	if strings.HasPrefix(sceneKey, "g.") {
		link, group, err := resolveChannelGroupMobileLink(h.db, sceneKey, "weixin_clawbot", "", false)
		if err != nil {
			return nil, "", err
		}
		if link == nil || link.CanonicalUID != canonicalUID {
			return nil, "", errors.New("ClawBot group mobile link not found or expired")
		}
		ownerUID := link.CanonicalUID
		if group != nil && group.OwnerID > 0 {
			ownerUID = group.OwnerID
		}
		token.OwnerUID = ownerUID
		saved, err := bindings.UpsertWeixinClawBotToken(token)
		if err != nil {
			return nil, "", err
		}
		actorUID, err := ensureChannelActor(h.db, "weixin_clawbot", tokenHash, channelUserID)
		if err != nil {
			return nil, "", err
		}
		if _, err := bindings.UpsertChannelGroupBinding(&types.ChannelGroupBinding{
			Channel:                 "weixin_clawbot",
			ChannelAppID:            tokenHash,
			ChannelUserID:           channelUserID,
			ChannelConversationType: "p2p",
			ActorUID:                actorUID,
			CanonicalUID:            canonicalUID,
			GroupID:                 link.GroupID,
			TopicID:                 link.TopicID,
			Status:                  types.ChannelAgentBindingActive,
		}); err != nil {
			return nil, "", err
		}
		_, _, _ = resolveChannelGroupMobileLink(h.db, sceneKey, "weixin_clawbot", "", true)
		return saved, "group", nil
	}
	return nil, "", errors.New("unsupported ClawBot mobile link")
}

func (h *WeixinClawBotHandler) SendTextMessage(ctx context.Context, binding *types.ChannelAgentBinding, text string) error {
	return h.SendOutboundMessage(ctx, binding, channelOutboundTextMessage(text))
}

func (h *WeixinClawBotHandler) SendOutboundMessage(ctx context.Context, binding *types.ChannelAgentBinding, message channelOutboundMessage) error {
	if h == nil || h.db == nil || binding == nil {
		return nil
	}
	tokenHash := strings.TrimSpace(binding.ChannelAppID)
	channelUserID := strings.TrimSpace(binding.ChannelUserID)
	if tokenHash == "" || channelUserID == "" || !message.HasContent() {
		return nil
	}
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil
	}
	token, err := bindings.GetWeixinClawBotTokenByHash(tokenHash)
	if err != nil || token == nil || token.Status != types.WeixinClawBotTokenActive {
		return err
	}
	contextValue := token.ContextTokens[channelUserID]
	if strings.TrimSpace(contextValue.ContextToken) == "" {
		return fmt.Errorf("missing weixin clawbot context token for channel user")
	}
	if text := strings.TrimSpace(message.Text); text != "" {
		if err := h.api.SendTextMessage(ctx, token.BotToken, channelUserID, text, contextValue.ContextToken, contextValue.BotUserID); err != nil {
			return err
		}
	}
	var fallbackAttachments []channelOutboundAttachment
	for index, attachment := range message.Attachments {
		if index >= weixinClawBotMaxOutboundMessages {
			fallbackAttachments = append(fallbackAttachments, message.Attachments[index:]...)
			break
		}
		media, err := weixinClawBotOutboundMediaFromAttachment(attachment)
		if err != nil {
			log.Printf("weixin clawbot outbound attachment fallback name=%s url=%s: %v", attachment.Name, attachment.URL, err)
			fallbackAttachments = append(fallbackAttachments, attachment)
			continue
		}
		if err := h.api.SendMediaMessage(ctx, token.BotToken, channelUserID, media, contextValue.ContextToken, contextValue.BotUserID); err != nil {
			log.Printf("weixin clawbot outbound media send failed name=%s path=%s: %v", media.Name, media.Path, err)
			fallbackAttachments = append(fallbackAttachments, attachment)
			continue
		}
	}
	if len(fallbackAttachments) > 0 {
		fallbackText := channelOutboundMessage{Attachments: fallbackAttachments}.TextWithAttachmentLinks()
		if strings.TrimSpace(fallbackText) != "" {
			return h.api.SendTextMessage(ctx, token.BotToken, channelUserID, fallbackText, contextValue.ContextToken, contextValue.BotUserID)
		}
	}
	return nil
}

func weixinClawBotOutboundMediaFromAttachment(attachment channelOutboundAttachment) (weixinClawBotOutboundMedia, error) {
	kind := strings.ToLower(strings.TrimSpace(attachment.Type))
	if kind != "image" {
		kind = "file"
	}
	rawURL := strings.TrimSpace(attachment.URL)
	if rawURL == "" && strings.TrimSpace(attachment.FileKey) != "" {
		rawURL = channelOutboundUploadURL(kind, attachment.FileKey)
	}
	localPath, err := weixinClawBotLocalUploadPath(rawURL, kind)
	if err != nil {
		return weixinClawBotOutboundMedia{}, err
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return weixinClawBotOutboundMedia{}, err
	}
	if !info.Mode().IsRegular() {
		return weixinClawBotOutboundMedia{}, errors.New("outbound attachment is not a regular file")
	}
	maxSize := int64(maxFileSize)
	if kind == "image" {
		maxSize = int64(maxImageSize)
	}
	if info.Size() > maxSize {
		return weixinClawBotOutboundMedia{}, fmt.Errorf("file too large; maximum supported size is %dMB", maxUploadSizeMB)
	}
	name := sanitizeChannelMediaFileName(firstNonEmpty(attachment.Name, channelOutboundFileNameFromURL(rawURL), filepath.Base(localPath)))
	contentType := strings.TrimSpace(attachment.MimeType)
	if contentType == "" {
		contentType = normalizedUploadMimeType(filepath.Ext(name), "")
	}
	return weixinClawBotOutboundMedia{
		Type:        kind,
		Name:        name,
		Path:        localPath,
		Size:        info.Size(),
		ContentType: contentType,
	}, nil
}

func weixinClawBotLocalUploadPath(raw string, kind string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("missing outbound attachment url")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "" || parsed.Host != "") && !weixinClawBotURLMatchesPublicBase(parsed) {
		return "", errors.New("outbound attachment is not a CatsCo upload URL")
	}
	cleanPath := urlpath.Clean("/" + strings.TrimPrefix(parsed.Path, "/"))
	rel := strings.TrimPrefix(cleanPath, "/uploads/")
	if rel == cleanPath || rel == "" {
		return "", errors.New("outbound attachment is not under uploads")
	}
	parts := strings.Split(rel, "/")
	if len(parts) != 2 {
		return "", errors.New("invalid outbound attachment upload path")
	}
	subDir, fileName := parts[0], parts[1]
	if subDir != "files" && subDir != "images" {
		return "", errors.New("unsupported outbound attachment upload directory")
	}
	if kind == "image" && subDir != "images" {
		return "", errors.New("image attachment is not stored under uploads/images")
	}
	if kind != "image" && subDir != "files" {
		return "", errors.New("file attachment is not stored under uploads/files")
	}
	if !uploadFileNamePattern.MatchString(fileName) {
		return "", errors.New("outbound attachment filename is not a generated upload key")
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	if subDir == "images" && !allowedImageExts[ext] {
		return "", errors.New("invalid outbound image type")
	}
	if subDir == "files" && !allowedFileExts[ext] {
		return "", errors.New("outbound file type not allowed")
	}
	baseDir, err := filepath.Abs(filepath.Join(uploadDir, subDir))
	if err != nil {
		return "", err
	}
	fullPath, err := filepath.Abs(filepath.Join(baseDir, fileName))
	if err != nil {
		return "", err
	}
	if fullPath != baseDir && !strings.HasPrefix(fullPath, baseDir+string(os.PathSeparator)) {
		return "", errors.New("invalid outbound attachment path")
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("outbound attachment must not be a symbolic link")
	}
	resolvedBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return "", err
	}
	if resolvedPath != resolvedBase && !strings.HasPrefix(resolvedPath, resolvedBase+string(os.PathSeparator)) {
		return "", errors.New("outbound attachment resolves outside uploads")
	}
	return resolvedPath, nil
}

func weixinClawBotURLMatchesPublicBase(parsed *url.URL) bool {
	if parsed == nil || parsed.Host == "" {
		return true
	}
	base, err := url.Parse(publicBaseURL(nil))
	if err != nil || base.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, base.Scheme) && strings.EqualFold(parsed.Host, base.Host)
}

func (h *WeixinClawBotHandler) manageTokenWorkers(ctx context.Context) {
	h.syncTokenWorkers(ctx)
	ticker := time.NewTicker(h.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.Stop()
			return
		case <-ticker.C:
			h.syncTokenWorkers(ctx)
		}
	}
}

func (h *WeixinClawBotHandler) syncTokenWorkers(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return
	}
	tokens, err := bindings.ListActiveWeixinClawBotTokens()
	if err != nil {
		log.Printf("list weixin clawbot tokens failed: %v", err)
		return
	}
	active := map[int64]*types.WeixinClawBotToken{}
	for _, token := range tokens {
		if token != nil && token.ID > 0 {
			active[token.ID] = token
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, cancel := range h.running {
		if _, ok := active[id]; !ok {
			cancel()
			delete(h.running, id)
		}
	}
	for id := range active {
		if _, ok := h.running[id]; ok {
			continue
		}
		tokenCtx, cancel := context.WithCancel(ctx)
		h.running[id] = cancel
		go h.pollTokenLoop(tokenCtx, id)
	}
}

func (h *WeixinClawBotHandler) pollTokenLoop(ctx context.Context, tokenID int64) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		bindings, ok := h.db.(store.ChannelAgentBindingStore)
		if !ok {
			return
		}
		token, err := bindings.GetWeixinClawBotTokenByID(tokenID)
		if err != nil {
			log.Printf("load weixin clawbot token failed id=%d: %v", tokenID, err)
			sleepWithContext(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		if token == nil || token.Status != types.WeixinClawBotTokenActive {
			return
		}
		if err := h.pollTokenOnce(ctx, token); err != nil {
			var apiErr *weixinClawBotAPIError
			if errors.As(err, &apiErr) && apiErr.ErrCode == -14 {
				_ = bindings.MarkWeixinClawBotTokenError(token.ID, types.WeixinClawBotTokenExpired, err.Error())
				return
			}
			_ = bindings.MarkWeixinClawBotTokenError(token.ID, "", err.Error())
			log.Printf("poll weixin clawbot token failed id=%d hash=%s: %v", token.ID, token.TokenHash, err)
			sleepWithContext(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
	}
}

func (h *WeixinClawBotHandler) pollTokenOnce(ctx context.Context, token *types.WeixinClawBotToken) error {
	if token == nil || token.ID <= 0 || strings.TrimSpace(token.BotToken) == "" {
		return nil
	}
	updates, err := h.api.GetUpdates(ctx, token.BotToken, token.GetUpdatesBuf)
	if err != nil {
		return err
	}
	if updates.GetUpdatesBuf != "" {
		token.GetUpdatesBuf = updates.GetUpdatesBuf
	}
	contexts := copyWeixinClawBotContexts(token.ContextTokens)
	for _, msg := range updates.Messages {
		fromUserID := strings.TrimSpace(msg.FromUserID)
		if fromUserID != "" && strings.TrimSpace(msg.ContextToken) != "" {
			contexts[fromUserID] = types.WeixinClawBotContext{
				ContextToken: strings.TrimSpace(msg.ContextToken),
				BotUserID:    strings.TrimSpace(msg.ToUserID),
				UpdatedAt:    time.Now(),
			}
		}
	}
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil
	}
	if err := bindings.UpdateWeixinClawBotTokenPollState(token.ID, token.GetUpdatesBuf, contexts); err != nil {
		return err
	}
	for _, msg := range updates.Messages {
		if err := h.handleUpdateMessage(ctx, token, msg); err != nil {
			log.Printf("handle weixin clawbot message failed token=%d message=%s: %v", token.ID, clawBotMessageID(msg), err)
		}
	}
	return nil
}

func (h *WeixinClawBotHandler) handleUpdateMessage(ctx context.Context, token *types.WeixinClawBotToken, msg weixinClawBotMessage) error {
	if msg.MessageType == 2 {
		return nil
	}
	if msg.MessageType != 0 && msg.MessageType != 1 {
		return nil
	}
	fromUserID := strings.TrimSpace(msg.FromUserID)
	if fromUserID == "" {
		return nil
	}
	text := strings.TrimSpace(weixinClawBotMessageText(msg))
	refs, unsupported := weixinClawBotMessageMediaRefs(msg)
	rawItems := weixinClawBotNonTextRawItemRecords(msg)
	if len(rawItems) > 0 {
		if raw, err := json.Marshal(rawItems); err == nil {
			log.Printf("weixin clawbot non-text items token=%d message=%s items=%s", token.ID, clawBotMessageID(msg), raw)
		}
	}
	files, downloadFailures := h.downloadClawBotMedia(ctx, token, msg, refs)
	unsupported = append(unsupported, downloadFailures...)
	if text == "" && len(files) == 0 && len(unsupported) > 0 {
		text = weixinClawBotUnsupportedAttachmentText(unsupported)
	}
	if text == "" && len(files) == 0 {
		return nil
	}
	target, err := h.resolveDeliveryTarget(token, msg)
	if err != nil {
		return err
	}
	if target.groupBinding != nil {
		return h.deliverGroupMessage(ctx, token, msg, target.groupBinding, text, files, rawItems, unsupported)
	}
	return h.deliverAgentMessage(ctx, token, msg, target.agentUID, text, files, rawItems, unsupported)
}

type weixinClawBotDeliveryTarget struct {
	agentUID     int64
	groupBinding *types.ChannelGroupBinding
}

func (h *WeixinClawBotHandler) resolveDeliveryTarget(token *types.WeixinClawBotToken, msg weixinClawBotMessage) (*weixinClawBotDeliveryTarget, error) {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return nil, errors.New("channel binding store not configured")
	}
	channelUserID := strings.TrimSpace(msg.FromUserID)
	groupBinding, err := bindings.ResolveChannelGroupBinding(types.ChannelGroupBindingQuery{
		Channel:                 "weixin_clawbot",
		ChannelAppID:            token.TokenHash,
		ChannelUserID:           channelUserID,
		ChannelConversationType: "p2p",
	})
	if err != nil {
		return nil, err
	}
	route, err := bindings.ResolveChannelAgentRoute(types.ChannelAgentRouteQuery{
		Channel:                 "weixin_clawbot",
		ChannelAppID:            token.TokenHash,
		ChannelUserID:           channelUserID,
		ChannelConversationType: "p2p",
	})
	if err != nil {
		return nil, err
	}
	if groupBinding != nil && groupBinding.Status == types.ChannelAgentBindingActive {
		if route == nil || groupBinding.SelectedAt.After(route.SelectedAt) {
			return &weixinClawBotDeliveryTarget{groupBinding: groupBinding}, nil
		}
	}
	agentUID := int64(0)
	if route != nil {
		agentUID = route.AgentUID
	}
	return &weixinClawBotDeliveryTarget{agentUID: agentUID}, nil
}

func (h *WeixinClawBotHandler) deliverAgentMessage(ctx context.Context, token *types.WeixinClawBotToken, msg weixinClawBotMessage, agentUID int64, text string, files []uploadPayload, rawItems []map[string]interface{}, unsupported []weixinClawBotUnsupportedItem) error {
	bindings, ok := h.db.(store.ChannelAgentBindingStore)
	if !ok {
		return errors.New("channel binding store not configured")
	}
	binding, err := bindings.ResolveChannelAgentBinding(types.ChannelAgentBindingQuery{
		Channel:                 "weixin_clawbot",
		ChannelAppID:            token.TokenHash,
		ChannelUserID:           strings.TrimSpace(msg.FromUserID),
		ChannelConversationType: "p2p",
		AgentUID:                agentUID,
	})
	if err != nil {
		return err
	}
	if binding == nil {
		return errors.New("weixin clawbot message user is not bound to an agent")
	}
	if _, err := bindings.UpsertChannelAgentRoute(&types.ChannelAgentRoute{
		Channel:                 "weixin_clawbot",
		ChannelAppID:            token.TokenHash,
		ChannelUserID:           strings.TrimSpace(msg.FromUserID),
		ChannelConversationType: "p2p",
		ActorUID:                binding.ActorUID,
		AgentUID:                binding.AgentUID,
		Source:                  "weixin_clawbot_getupdates",
	}); err != nil {
		return err
	}
	metadata := h.inboundMetadata(token, binding, msg)
	h.addClawBotMediaMetadata(metadata, files, rawItems, unsupported)
	return deliverInboundChannelMessageToAgent(
		h.db,
		h.hub,
		binding.ActorUID,
		binding.AgentUID,
		text,
		files,
		weixinClawBotClientMsgID(token, msg),
		"weixin_clawbot",
		metadata,
	)
}

func (h *WeixinClawBotHandler) deliverGroupMessage(ctx context.Context, token *types.WeixinClawBotToken, msg weixinClawBotMessage, groupBinding *types.ChannelGroupBinding, text string, files []uploadPayload, rawItems []map[string]interface{}, unsupported []weixinClawBotUnsupportedItem) error {
	if groupBinding == nil || groupBinding.Status != types.ChannelAgentBindingActive {
		return errors.New("weixin clawbot message user is not bound to a group")
	}
	metadata := h.groupInboundMetadata(token, groupBinding, msg)
	h.addClawBotMediaMetadata(metadata, files, rawItems, unsupported)
	if err := deliverInboundChannelMessageToGroup(
		h.db,
		h.hub,
		groupBinding.CanonicalUID,
		groupBinding,
		text,
		files,
		weixinClawBotClientMsgID(token, msg),
		"weixin_clawbot",
		metadata,
	); err != nil {
		return err
	}
	return nil
}

func (h *WeixinClawBotHandler) inboundMetadata(token *types.WeixinClawBotToken, binding *types.ChannelAgentBinding, msg weixinClawBotMessage) map[string]interface{} {
	return map[string]interface{}{
		"source_channel":                 "weixin_clawbot",
		"channel_app_id":                 token.TokenHash,
		"channel_user_id":                strings.TrimSpace(msg.FromUserID),
		"channel_conversation_type":      "p2p",
		"channel_message_id":             clawBotMessageID(msg),
		"channel_message_type":           msg.MessageType,
		"channel_identity_source":        "weixin.ilink",
		"channel_identity_trust":         "weixin_clawbot_bot_token",
		"channel_agent_binding_entry_id": binding.EntryID,
		"channel_actor_uid":              binding.ActorUID,
		"channel_canonical_uid":          binding.CanonicalUID,
		"channel_agent_binding_id":       binding.ID,
		"channel_device_access_enabled":  binding.CanonicalUID > 0 && binding.Status == types.ChannelAgentBindingActive,
		"weixin_clawbot_token_id":        token.ID,
		"weixin_clawbot_bot_user_id":     strings.TrimSpace(msg.ToUserID),
	}
}

func (h *WeixinClawBotHandler) groupInboundMetadata(token *types.WeixinClawBotToken, binding *types.ChannelGroupBinding, msg weixinClawBotMessage) map[string]interface{} {
	return map[string]interface{}{
		"source_channel":                    "weixin_clawbot",
		"channel_app_id":                    token.TokenHash,
		"channel_user_id":                   strings.TrimSpace(msg.FromUserID),
		"channel_conversation_type":         "p2p",
		"channel_message_id":                clawBotMessageID(msg),
		"channel_message_type":              msg.MessageType,
		"channel_identity_source":           "weixin.ilink",
		"channel_identity_trust":            "weixin_clawbot_bot_token",
		"channel_group_binding_id":          binding.ID,
		"channel_group_mobile_topic_id":     binding.TopicID,
		"channel_group_mobile_binding_user": binding.CanonicalUID,
		"weixin_clawbot_token_id":           token.ID,
		"weixin_clawbot_bot_user_id":        strings.TrimSpace(msg.ToUserID),
	}
}

func (h *WeixinClawBotHandler) addClawBotMediaMetadata(metadata map[string]interface{}, files []uploadPayload, rawItems []map[string]interface{}, unsupported []weixinClawBotUnsupportedItem) {
	if metadata == nil {
		return
	}
	if len(files) > 0 {
		metadata["channel_attachment_count"] = len(files)
	}
	if len(rawItems) > 0 {
		metadata["weixin_clawbot_raw_non_text_items"] = rawItems
	}
	if len(unsupported) > 0 {
		records := make([]map[string]interface{}, 0, len(unsupported))
		for _, item := range unsupported {
			record := map[string]interface{}{
				"type":   item.Type,
				"reason": item.Reason,
			}
			if strings.TrimSpace(item.Name) != "" {
				record["name"] = strings.TrimSpace(item.Name)
			}
			if len(item.Raw) > 0 {
				record["raw"] = truncateWeixinClawBotRawJSON(item.Raw)
			}
			records = append(records, record)
		}
		metadata["weixin_clawbot_unsupported_items"] = records
	}
}

func (h *WeixinClawBotHandler) downloadClawBotMedia(ctx context.Context, token *types.WeixinClawBotToken, msg weixinClawBotMessage, refs []weixinClawBotMediaRef) ([]uploadPayload, []weixinClawBotUnsupportedItem) {
	if h == nil || h.api == nil || token == nil || len(refs) == 0 {
		return nil, nil
	}
	files := make([]uploadPayload, 0, len(refs))
	failures := make([]weixinClawBotUnsupportedItem, 0)
	for _, ref := range refs {
		if strings.TrimSpace(ref.URL) == "" && strings.TrimSpace(ref.MediaID) == "" {
			failures = append(failures, weixinClawBotUnsupportedItem{Type: ref.ItemType, Reason: "missing_media_resource", Name: ref.Name, Raw: ref.Raw})
			continue
		}
		media, err := h.api.DownloadMedia(ctx, token.BotToken, ref)
		if err != nil {
			log.Printf("download weixin clawbot media failed token=%d message=%s name=%q url=%q: %v", token.ID, clawBotMessageID(msg), ref.Name, ref.URL, err)
			failures = append(failures, weixinClawBotUnsupportedItem{Type: ref.ItemType, Reason: "download_failed", Name: ref.Name, Raw: ref.Raw})
			continue
		}
		if strings.TrimSpace(media.FileName) == "" {
			media.FileName = ref.Name
		}
		if strings.TrimSpace(media.ContentType) == "" {
			media.ContentType = ref.ContentType
		}
		file, err := saveChannelMediaUpload(ref.Kind, media)
		if err != nil {
			log.Printf("save weixin clawbot media failed token=%d message=%s name=%q: %v", token.ID, clawBotMessageID(msg), ref.Name, err)
			failures = append(failures, weixinClawBotUnsupportedItem{Type: ref.ItemType, Reason: "save_failed", Name: ref.Name, Raw: ref.Raw})
			continue
		}
		files = append(files, file)
	}
	return files, failures
}

func weixinClawBotMessageMediaRefs(msg weixinClawBotMessage) ([]weixinClawBotMediaRef, []weixinClawBotUnsupportedItem) {
	var refs []weixinClawBotMediaRef
	var unsupported []weixinClawBotUnsupportedItem
	for _, item := range msg.ItemList {
		if !weixinClawBotHasNonTextPayload(item) {
			continue
		}
		itemRefs := weixinClawBotItemMediaRefs(item)
		if len(itemRefs) == 0 {
			unsupported = append(unsupported, weixinClawBotUnsupportedItem{Type: item.Type, Reason: "unrecognized_item", Raw: weixinClawBotItemRaw(item)})
			continue
		}
		refs = append(refs, itemRefs...)
	}
	return refs, unsupported
}

func weixinClawBotItemMediaRefs(item weixinClawBotMessageItem) []weixinClawBotMediaRef {
	containers := weixinClawBotItemMediaContainers(item)
	seen := map[string]bool{}
	refs := make([]weixinClawBotMediaRef, 0, len(containers))
	for _, container := range containers {
		ref, ok := weixinClawBotMediaRefFromMap(item, container.name, container.payload)
		if !ok {
			continue
		}
		key := strings.Join([]string{ref.Kind, ref.URL, ref.MediaID, ref.Name}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, ref)
	}
	return refs
}

type weixinClawBotMediaContainer struct {
	name    string
	payload map[string]interface{}
}

func weixinClawBotItemMediaContainers(item weixinClawBotMessageItem) []weixinClawBotMediaContainer {
	var containers []weixinClawBotMediaContainer
	add := func(name string, payload map[string]interface{}) {
		if len(payload) == 0 {
			return
		}
		containers = append(containers, weixinClawBotMediaContainer{name: name, payload: payload})
	}
	add("image_item", item.ImageItem)
	add("file_item", item.FileItem)
	add("media_item", item.MediaItem)
	add("attachment_item", item.AttachmentItem)
	add("doc_item", item.DocItem)
	for key, value := range item.Fields {
		normalized := weixinClawBotNormalizedKey(key)
		if normalized == "textitem" || normalized == "type" {
			continue
		}
		switch typed := value.(type) {
		case map[string]interface{}:
			if strings.Contains(normalized, "image") || strings.Contains(normalized, "file") || strings.Contains(normalized, "media") || strings.Contains(normalized, "attach") || strings.Contains(normalized, "doc") {
				add(key, typed)
			}
		case []interface{}:
			if !strings.Contains(normalized, "image") && !strings.Contains(normalized, "file") && !strings.Contains(normalized, "media") && !strings.Contains(normalized, "attach") && !strings.Contains(normalized, "doc") {
				continue
			}
			for _, entry := range typed {
				if payload, ok := entry.(map[string]interface{}); ok {
					add(key, payload)
				}
			}
		}
	}
	if len(item.Fields) > 0 {
		add("item", item.Fields)
	}
	return containers
}

func weixinClawBotMediaRefFromMap(item weixinClawBotMessageItem, source string, payload map[string]interface{}) (weixinClawBotMediaRef, bool) {
	if payload == nil {
		return weixinClawBotMediaRef{}, false
	}
	ref := weixinClawBotMediaRef{
		Kind:        weixinClawBotMediaKind(item.Type, source, weixinClawBotMapStringDeep(payload, 3, "mime_type", "mimeType", "content_type", "contentType", "file_type", "fileType"), weixinClawBotMapStringDeep(payload, 3, "file_name", "fileName", "filename", "name", "title", "display_name", "displayName")),
		Name:        weixinClawBotMapStringDeep(payload, 3, "file_name", "fileName", "filename", "name", "title", "display_name", "displayName"),
		URL:         weixinClawBotMapStringDeep(payload, 3, "download_url", "downloadUrl", "full_url", "fullUrl", "file_url", "fileUrl", "image_url", "imageUrl", "media_url", "mediaUrl", "resource_url", "resourceUrl", "url"),
		AESKey:      weixinClawBotMapStringDeep(payload, 3, "aes_key", "aesKey"),
		MediaID:     weixinClawBotMapStringDeep(payload, 3, "media_id", "mediaId", "file_id", "fileId", "resource_id", "resourceId", "resource_key", "resourceKey"),
		ContentType: weixinClawBotMapStringDeep(payload, 3, "mime_type", "mimeType", "content_type", "contentType", "file_type", "fileType"),
		Size:        weixinClawBotMapInt64Deep(payload, 3, "size", "len", "length", "file_size", "fileSize", "content_length", "contentLength"),
		ItemType:    item.Type,
		Source:      source,
		Raw:         weixinClawBotItemRaw(item),
	}
	if ref.Name == "" {
		ref.Name = channelOutboundFileNameFromURL(ref.URL)
	}
	if ref.URL == "" && ref.MediaID == "" && ref.Name == "" && ref.ContentType == "" {
		return weixinClawBotMediaRef{}, false
	}
	return ref, true
}

func weixinClawBotMediaKind(itemType int, source, contentType, name string) string {
	source = strings.ToLower(source)
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(source, "image") || strings.HasPrefix(contentType, "image/") || strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".gif") || strings.HasSuffix(name, ".webp") {
		return "image"
	}
	return "file"
}

func weixinClawBotHasNonTextPayload(item weixinClawBotMessageItem) bool {
	if len(item.ImageItem) > 0 || len(item.FileItem) > 0 || len(item.MediaItem) > 0 || len(item.AttachmentItem) > 0 || len(item.DocItem) > 0 {
		return true
	}
	for key, value := range item.Fields {
		normalized := weixinClawBotNormalizedKey(key)
		if normalized == "type" || normalized == "textitem" {
			continue
		}
		if !weixinClawBotEmptyValue(value) {
			return true
		}
	}
	return item.Type != 0 && item.Type != 1
}

func weixinClawBotNonTextRawItemRecords(msg weixinClawBotMessage) []map[string]interface{} {
	var records []map[string]interface{}
	for _, item := range msg.ItemList {
		if !weixinClawBotHasNonTextPayload(item) {
			continue
		}
		record := map[string]interface{}{
			"type": item.Type,
			"raw":  truncateWeixinClawBotRawJSON(weixinClawBotItemRaw(item)),
		}
		records = append(records, record)
	}
	return records
}

func weixinClawBotUnsupportedAttachmentText(items []weixinClawBotUnsupportedItem) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		name := strings.TrimSpace(items[0].Name)
		if name == "" {
			name = "附件"
		}
		return "收到微信 ClawBot 附件：" + name + "。当前无法下载这个附件，已记录原始消息字段用于适配。"
	}
	return fmt.Sprintf("收到 %d 个微信 ClawBot 附件。当前无法下载这些附件，已记录原始消息字段用于适配。", len(items))
}

func weixinClawBotItemRaw(item weixinClawBotMessageItem) json.RawMessage {
	if len(item.Raw) > 0 {
		return item.Raw
	}
	raw, _ := json.Marshal(struct {
		Type           int                    `json:"type,omitempty"`
		TextItem       weixinClawBotTextItem  `json:"text_item,omitempty"`
		ImageItem      map[string]interface{} `json:"image_item,omitempty"`
		FileItem       map[string]interface{} `json:"file_item,omitempty"`
		MediaItem      map[string]interface{} `json:"media_item,omitempty"`
		AttachmentItem map[string]interface{} `json:"attachment_item,omitempty"`
		DocItem        map[string]interface{} `json:"doc_item,omitempty"`
	}{
		Type:           item.Type,
		TextItem:       item.TextItem,
		ImageItem:      item.ImageItem,
		FileItem:       item.FileItem,
		MediaItem:      item.MediaItem,
		AttachmentItem: item.AttachmentItem,
		DocItem:        item.DocItem,
	})
	return raw
}

func truncateWeixinClawBotRawJSON(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err == nil {
		if redacted, err := json.Marshal(redactWeixinClawBotRawValue(decoded)); err == nil {
			value = string(redacted)
		}
	}
	const maxRawItemLogBytes = 4096
	if len(value) > maxRawItemLogBytes {
		return value[:maxRawItemLogBytes] + "...(truncated)"
	}
	return value
}

func redactWeixinClawBotRawValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(typed))
		for key, entry := range typed {
			if shouldRedactWeixinClawBotRawField(key) {
				redacted[key] = "[redacted]"
				continue
			}
			redacted[key] = redactWeixinClawBotRawValue(entry)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, 0, len(typed))
		for _, entry := range typed {
			redacted = append(redacted, redactWeixinClawBotRawValue(entry))
		}
		return redacted
	default:
		return value
	}
}

func shouldRedactWeixinClawBotRawField(key string) bool {
	switch weixinClawBotNormalizedKey(key) {
	case "aeskey", "fullurl", "downloadurl", "fileurl", "imageurl", "mediaurl", "resourceurl", "url", "encryptqueryparam", "encryptedqueryparam":
		return true
	default:
		return false
	}
}

func weixinClawBotMapStringDeep(payload map[string]interface{}, depth int, keys ...string) string {
	value, ok := weixinClawBotMapLookupDeep(payload, depth, keys...)
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func weixinClawBotMapInt64Deep(payload map[string]interface{}, depth int, keys ...string) int64 {
	value, ok := weixinClawBotMapLookupDeep(payload, depth, keys...)
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func weixinClawBotMapLookupDeep(payload map[string]interface{}, depth int, keys ...string) (interface{}, bool) {
	if payload == nil || depth < 0 {
		return nil, false
	}
	for _, key := range keys {
		normalized := weixinClawBotNormalizedKey(key)
		for payloadKey, value := range payload {
			if weixinClawBotNormalizedKey(payloadKey) == normalized {
				return value, true
			}
		}
	}
	if depth == 0 {
		return nil, false
	}
	for _, key := range keys {
		for _, value := range payload {
			switch typed := value.(type) {
			case map[string]interface{}:
				if found, ok := weixinClawBotMapLookupDeep(typed, depth-1, key); ok {
					return found, true
				}
			case []interface{}:
				for _, entry := range typed {
					if nested, ok := entry.(map[string]interface{}); ok {
						if found, ok := weixinClawBotMapLookupDeep(nested, depth-1, key); ok {
							return found, true
						}
					}
				}
			}
		}
	}
	return nil, false
}

func weixinClawBotNormalizedKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	return value
}

func weixinClawBotEmptyValue(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case map[string]interface{}:
		return len(typed) == 0
	case []interface{}:
		return len(typed) == 0
	default:
		return false
	}
}

func weixinClawBotAllowedMediaHosts(baseURL string, extra []string) map[string]bool {
	hosts := map[string]bool{}
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && parsed.Host != "" {
		hosts[weixinClawBotNormalizeMediaHost(parsed.Host)] = true
	}
	for _, value := range extra {
		if host := weixinClawBotNormalizeMediaHost(value); host != "" {
			hosts[host] = true
		}
	}
	return hosts
}

func weixinClawBotNormalizeMediaHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func weixinClawBotPrivateHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return true
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

type httpWeixinClawBotAPI struct {
	baseURL                  string
	cdnBaseURL               string
	allowedMediaHosts        map[string]bool
	mediaDownloadURLTemplate string
	httpClient               *http.Client
	longPollTimeout          time.Duration
}

func newHTTPWeixinClawBotAPI(cfg WeixinClawBotConfig) *httpWeixinClawBotAPI {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.ILinkBaseURL), "/")
	if baseURL == "" {
		baseURL = configuredWeixinClawBotILinkBaseURL()
	}
	cdnBaseURL := strings.TrimRight(strings.TrimSpace(cfg.CDNBaseURL), "/")
	if cdnBaseURL == "" {
		cdnBaseURL = defaultWeixinClawBotCDNBaseURL
	}
	allowedMediaHosts := weixinClawBotAllowedMediaHosts(baseURL, cfg.MediaHostAllowlist)
	longPoll := cfg.LongPollTimeout
	if longPoll <= 0 {
		longPoll = 30 * time.Second
	}
	return &httpWeixinClawBotAPI{
		baseURL:                  baseURL,
		cdnBaseURL:               cdnBaseURL,
		allowedMediaHosts:        allowedMediaHosts,
		mediaDownloadURLTemplate: strings.TrimSpace(cfg.MediaDownloadURLTemplate),
		httpClient:               &http.Client{Timeout: longPoll + 5*time.Second},
		longPollTimeout:          longPoll,
	}
}

func (c *httpWeixinClawBotAPI) GetQRCodeStatus(ctx context.Context, qrcode string) (*weixinClawBotQRCodeStatus, error) {
	endpoint := c.baseURL + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data weixinClawBotQRCodeStatus
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || data.Ret != 0 || data.ErrCode != 0 {
		return nil, &weixinClawBotAPIError{Operation: "qrcode-status", Status: resp.StatusCode, Ret: data.Ret, ErrCode: data.ErrCode, ErrMsg: data.ErrMsg}
	}
	return &data, nil
}

func (c *httpWeixinClawBotAPI) GetUpdates(ctx context.Context, botToken string, getUpdatesBuf string) (*weixinClawBotUpdates, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"get_updates_buf": strings.TrimSpace(getUpdatesBuf),
		"base_info": map[string]string{
			"channel_version": weixinClawBotChannelVersion,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/ilink/bot/getupdates", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setWeixinClawBotAuthHeaders(req, botToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data weixinClawBotUpdates
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || data.Ret != 0 || data.ErrCode != 0 {
		return nil, &weixinClawBotAPIError{Operation: "getupdates", Status: resp.StatusCode, Ret: data.Ret, ErrCode: data.ErrCode, ErrMsg: data.ErrMsg}
	}
	return &data, nil
}

func (c *httpWeixinClawBotAPI) DownloadMedia(ctx context.Context, botToken string, ref weixinClawBotMediaRef) (*channelMediaDownload, error) {
	downloadURL := strings.TrimSpace(ref.URL)
	if downloadURL == "" {
		downloadURL = c.mediaDownloadURLFromRef(ref)
	}
	if downloadURL == "" {
		return nil, errors.New("missing weixin clawbot media download url")
	}
	endpoint, authenticated, err := c.resolveMediaDownloadURL(downloadURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if authenticated {
		setWeixinClawBotAuthOnlyHeaders(req, botToken)
	}
	client := *c.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		_, redirectedAuthenticated, err := c.resolveMediaDownloadURL(req.URL.String())
		if err != nil {
			return err
		}
		req.Header.Del("Authorization")
		req.Header.Del("AuthorizationType")
		if redirectedAuthenticated {
			setWeixinClawBotAuthOnlyHeaders(req, botToken)
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, fmt.Errorf("weixin clawbot media http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	contentType := firstNonEmpty(resp.Header.Get("Content-Type"), ref.ContentType)
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType == "application/json" {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var payload struct {
			Ret     int    `json:"ret,omitempty"`
			ErrCode int    `json:"errcode,omitempty"`
			ErrMsg  string `json:"errmsg,omitempty"`
		}
		if err := json.Unmarshal(body, &payload); err == nil && (payload.Ret != 0 || payload.ErrCode != 0) {
			return nil, &weixinClawBotAPIError{Operation: "download-media", Status: resp.StatusCode, Ret: payload.Ret, ErrCode: payload.ErrCode, ErrMsg: payload.ErrMsg}
		}
		return nil, fmt.Errorf("weixin clawbot media response is not a file: %s", strings.TrimSpace(string(body)))
	}
	fileName := channelMediaFileNameFromDisposition(resp.Header.Get("Content-Disposition"))
	if fileName == "" {
		fileName = ref.Name
	}
	if fileName == "" {
		fileName = channelOutboundFileNameFromURL(endpoint)
	}
	if fileName == "" {
		fileName = "weixin-clawbot-" + ref.Kind + "-" + randomClawBotHex(4)
	}
	body := resp.Body
	if strings.TrimSpace(ref.AESKey) != "" {
		encrypted, err := readWeixinClawBotEncryptedMediaBody(resp.Body, ref.Kind)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		decrypted, err := decryptWeixinClawBotMediaBody(encrypted, ref.AESKey)
		if err != nil {
			return nil, err
		}
		body = io.NopCloser(bytes.NewReader(decrypted))
	}
	return &channelMediaDownload{
		Body:        body,
		FileName:    fileName,
		ContentType: contentType,
	}, nil
}

func readWeixinClawBotEncryptedMediaBody(src io.Reader, kind string) ([]byte, error) {
	return readWeixinClawBotEncryptedMediaBodyWithLimit(src, maxWeixinClawBotPlainMediaSize(kind))
}

func readWeixinClawBotEncryptedMediaBodyWithLimit(src io.Reader, maxPlainSize int64) ([]byte, error) {
	if src == nil {
		return nil, errors.New("missing weixin clawbot encrypted media body")
	}
	if maxPlainSize <= 0 {
		maxPlainSize = int64(maxFileSize)
	}
	maxEncryptedSize := maxPlainSize + aes.BlockSize
	limited := &io.LimitedReader{R: src, N: maxEncryptedSize + 1}
	encrypted, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(encrypted)) > maxEncryptedSize {
		return nil, fmt.Errorf("file too large; maximum supported size is %dMB", maxUploadSizeMB)
	}
	return encrypted, nil
}

func maxWeixinClawBotPlainMediaSize(kind string) int64 {
	if strings.EqualFold(strings.TrimSpace(kind), "image") {
		return int64(maxImageSize)
	}
	return int64(maxFileSize)
}

func decryptWeixinClawBotMediaBody(encrypted []byte, rawAESKey string) ([]byte, error) {
	key, err := decodeWeixinClawBotMediaAESKey(rawAESKey)
	if err != nil {
		return nil, err
	}
	if len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid weixin clawbot encrypted media size: %d", len(encrypted))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	decrypted := make([]byte, len(encrypted))
	for offset := 0; offset < len(encrypted); offset += aes.BlockSize {
		block.Decrypt(decrypted[offset:offset+aes.BlockSize], encrypted[offset:offset+aes.BlockSize])
	}
	return unpadWeixinClawBotPKCS7(decrypted)
}

func decodeWeixinClawBotMediaAESKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("missing weixin clawbot media aes key")
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode weixin clawbot media aes key: %w", err)
	}
	keyHex := strings.TrimSpace(string(decoded))
	key, err := hex.DecodeString(keyHex)
	if err == nil && len(key) == aes.BlockSize {
		return key, nil
	}
	if len(decoded) == aes.BlockSize {
		return decoded, nil
	}
	return nil, fmt.Errorf("invalid weixin clawbot media aes key length: decoded=%d hex=%d", len(decoded), len(key))
}

func unpadWeixinClawBotPKCS7(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid weixin clawbot padded media size: %d", len(data))
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(data) {
		return nil, fmt.Errorf("invalid weixin clawbot media padding: %d", pad)
	}
	for i := len(data) - pad; i < len(data); i++ {
		if int(data[i]) != pad {
			return nil, errors.New("invalid weixin clawbot media padding bytes")
		}
	}
	return data[:len(data)-pad], nil
}

func (c *httpWeixinClawBotAPI) mediaDownloadURLFromRef(ref weixinClawBotMediaRef) string {
	template := strings.TrimSpace(c.mediaDownloadURLTemplate)
	mediaID := strings.TrimSpace(ref.MediaID)
	if template == "" || mediaID == "" {
		return ""
	}
	escaped := url.QueryEscape(mediaID)
	replacer := strings.NewReplacer(
		"{media_id}", escaped,
		"{mediaId}", escaped,
		"{resource_id}", escaped,
		"{resourceId}", escaped,
		"{resource_key}", escaped,
		"{resourceKey}", escaped,
		"{file_id}", escaped,
		"{fileId}", escaped,
	)
	return replacer.Replace(template)
}

func (c *httpWeixinClawBotAPI) resolveMediaDownloadURL(raw string) (string, bool, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", false, errors.New("invalid weixin clawbot base url")
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false, err
	}
	if parsed.Scheme == "" && parsed.Host == "" {
		return base.ResolveReference(parsed).String(), true, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false, fmt.Errorf("unsupported weixin clawbot media url scheme: %s", parsed.Scheme)
	}
	sameOrigin := strings.EqualFold(parsed.Scheme, base.Scheme) && strings.EqualFold(parsed.Host, base.Host)
	if parsed.Scheme == "http" && !sameOrigin {
		return "", false, errors.New("weixin clawbot media url must use https")
	}
	if !sameOrigin && weixinClawBotPrivateHost(parsed.Hostname()) {
		return "", false, errors.New("weixin clawbot media url host is not allowed")
	}
	if !sameOrigin && !c.allowedMediaHosts[weixinClawBotNormalizeMediaHost(parsed.Host)] {
		return "", false, fmt.Errorf("weixin clawbot media url host is not allowed: %s", parsed.Host)
	}
	return parsed.String(), sameOrigin, nil
}

func (c *httpWeixinClawBotAPI) SendTextMessage(ctx context.Context, botToken string, toUserID string, text string, contextToken string, fromUserID string) error {
	if strings.TrimSpace(contextToken) == "" {
		return errors.New("context_token is required for Weixin ClawBot sendmessage")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return c.sendMessageItems(ctx, botToken, toUserID, contextToken, fromUserID, []map[string]interface{}{
		{"type": 1, "text_item": map[string]string{"text": text}},
	}, "sendmessage:text")
}

func (c *httpWeixinClawBotAPI) SendMediaMessage(ctx context.Context, botToken string, toUserID string, media weixinClawBotOutboundMedia, contextToken string, fromUserID string) error {
	if strings.TrimSpace(contextToken) == "" {
		return errors.New("context_token is required for Weixin ClawBot sendmessage")
	}
	media.Type = strings.ToLower(strings.TrimSpace(media.Type))
	if media.Type != "image" {
		media.Type = "file"
	}
	media.Name = sanitizeChannelMediaFileName(media.Name)
	if strings.TrimSpace(media.Path) == "" {
		return errors.New("missing outbound media path")
	}
	if strings.TrimSpace(media.Name) == "" {
		media.Name = filepath.Base(media.Path)
	}
	info, err := os.Stat(media.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("outbound media path is not a regular file")
	}
	media.Size = info.Size()
	maxSize := int64(maxFileSize)
	if media.Type == "image" {
		maxSize = int64(maxImageSize)
	}
	if media.Size > maxSize {
		return fmt.Errorf("file too large; maximum supported size is %dMB", maxUploadSizeMB)
	}
	rawMD5, err := weixinClawBotFileMD5(media.Path, maxSize)
	if err != nil {
		return err
	}
	aesKey := make([]byte, aes.BlockSize)
	if _, err := rand.Read(aesKey); err != nil {
		return err
	}
	fileKey := randomClawBotHex(16)
	paddedSize := weixinClawBotAESPaddedSize(media.Size)
	uploadParam, err := c.requestMediaUploadURL(ctx, botToken, weixinClawBotUploadMediaType(media.Type), toUserID, fileKey, media.Size, rawMD5, paddedSize, aesKey)
	if err != nil {
		return err
	}
	downloadParam, err := c.uploadMediaToCDN(ctx, uploadParam, fileKey, media.Path, media.Size, aesKey)
	if err != nil {
		return err
	}
	item := weixinClawBotOutboundMediaItem(media, downloadParam, aesKey, paddedSize)
	return c.sendMessageItems(ctx, botToken, toUserID, contextToken, fromUserID, []map[string]interface{}{item}, "sendmessage:"+media.Type)
}

func (c *httpWeixinClawBotAPI) sendMessageItems(ctx context.Context, botToken string, toUserID string, contextToken string, fromUserID string, items []map[string]interface{}, operation string) error {
	if strings.TrimSpace(contextToken) == "" {
		return errors.New("context_token is required for Weixin ClawBot sendmessage")
	}
	if strings.TrimSpace(toUserID) == "" || len(items) == 0 {
		return nil
	}
	clientID := "catsco-" + randomClawBotHex(6)
	body, _ := json.Marshal(map[string]interface{}{
		"msg": map[string]interface{}{
			"from_user_id":  strings.TrimSpace(fromUserID),
			"to_user_id":    strings.TrimSpace(toUserID),
			"client_id":     clientID,
			"message_type":  2,
			"message_state": 2,
			"item_list":     items,
			"context_token": strings.TrimSpace(contextToken),
		},
		"base_info": map[string]string{
			"channel_version": weixinClawBotChannelVersion,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/ilink/bot/sendmessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	setWeixinClawBotAuthHeaders(req, botToken)
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var data struct {
		Ret     int    `json:"ret,omitempty"`
		ErrCode int    `json:"errcode,omitempty"`
		ErrMsg  string `json:"errmsg,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || data.Ret != 0 || data.ErrCode != 0 {
		return &weixinClawBotAPIError{Operation: operation, Status: resp.StatusCode, Ret: data.Ret, ErrCode: data.ErrCode, ErrMsg: data.ErrMsg}
	}
	return nil
}

func (c *httpWeixinClawBotAPI) requestMediaUploadURL(ctx context.Context, botToken string, mediaType int, toUserID string, fileKey string, rawSize int64, rawMD5 string, encryptedSize int64, aesKey []byte) (string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"filekey":       fileKey,
		"media_type":    mediaType,
		"to_user_id":    strings.TrimSpace(toUserID),
		"rawsize":       rawSize,
		"rawfilemd5":    rawMD5,
		"filesize":      encryptedSize,
		"no_need_thumb": true,
		"aeskey":        hex.EncodeToString(aesKey),
		"base_info": map[string]string{
			"channel_version": weixinClawBotChannelVersion,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/ilink/bot/getuploadurl", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	setWeixinClawBotAuthHeaders(req, botToken)
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var data struct {
		Ret           int    `json:"ret,omitempty"`
		ErrCode       int    `json:"errcode,omitempty"`
		ErrMsg        string `json:"errmsg,omitempty"`
		UploadParam   string `json:"upload_param,omitempty"`
		UploadFullURL string `json:"upload_full_url,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || data.Ret != 0 || data.ErrCode != 0 {
		return "", &weixinClawBotAPIError{Operation: "getuploadurl", Status: resp.StatusCode, Ret: data.Ret, ErrCode: data.ErrCode, ErrMsg: data.ErrMsg}
	}
	uploadParam := strings.TrimSpace(data.UploadParam)
	if uploadParam == "" {
		uploadParam = weixinClawBotUploadParamFromFullURL(data.UploadFullURL)
	}
	if uploadParam == "" {
		return "", errors.New("weixin clawbot getuploadurl response missing upload_param")
	}
	return uploadParam, nil
}

func (c *httpWeixinClawBotAPI) uploadMediaToCDN(ctx context.Context, uploadParam string, fileKey string, filePath string, rawSize int64, aesKey []byte) (string, error) {
	if strings.TrimSpace(c.cdnBaseURL) == "" {
		return "", errors.New("weixin clawbot cdn base url is not configured")
	}
	endpoint := strings.TrimRight(c.cdnBaseURL, "/") + "/upload?encrypted_query_param=" + url.QueryEscape(uploadParam) + "&filekey=" + url.QueryEscape(fileKey)
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := newWeixinClawBotAESECBEncryptReader(file, aesKey)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", err
	}
	req.ContentLength = weixinClawBotAESPaddedSize(rawSize)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errText, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("weixin clawbot cdn upload http %d: %s", resp.StatusCode, strings.TrimSpace(string(errText)))
	}
	downloadParam := strings.TrimSpace(resp.Header.Get("x-encrypted-param"))
	if downloadParam == "" {
		return "", errors.New("weixin clawbot cdn upload response missing x-encrypted-param")
	}
	return downloadParam, nil
}

func weixinClawBotOutboundMediaItem(media weixinClawBotOutboundMedia, downloadParam string, aesKey []byte, encryptedSize int64) map[string]interface{} {
	mediaPayload := map[string]interface{}{
		"encrypt_query_param": strings.TrimSpace(downloadParam),
		"aes_key":             base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(aesKey))),
		"encrypt_type":        1,
	}
	if media.Type == "image" {
		return map[string]interface{}{
			"type": 2,
			"image_item": map[string]interface{}{
				"media":    mediaPayload,
				"mid_size": encryptedSize,
			},
		}
	}
	return map[string]interface{}{
		"type": 4,
		"file_item": map[string]interface{}{
			"media":     mediaPayload,
			"file_name": media.Name,
			"len":       strconv.FormatInt(media.Size, 10),
		},
	}
}

func weixinClawBotUploadMediaType(kind string) int {
	if strings.EqualFold(strings.TrimSpace(kind), "image") {
		return 1
	}
	return 3
}

func weixinClawBotUploadParamFromFullURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	query := parsed.Query()
	if value := strings.TrimSpace(query.Get("encrypted_query_param")); value != "" {
		return value
	}
	return strings.TrimSpace(query.Get("encrypt_query_param"))
}

func weixinClawBotFileMD5(filePath string, maxSize int64) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := md5.New()
	limited := &io.LimitedReader{R: file, N: maxSize + 1}
	written, err := io.Copy(hash, limited)
	if err != nil {
		return "", err
	}
	if written > maxSize {
		return "", fmt.Errorf("file too large; maximum supported size is %dMB", maxUploadSizeMB)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func weixinClawBotAESPaddedSize(rawSize int64) int64 {
	if rawSize < 0 {
		rawSize = 0
	}
	return (rawSize/int64(aes.BlockSize) + 1) * int64(aes.BlockSize)
}

type weixinClawBotAESECBEncryptReader struct {
	src      io.Reader
	block    interface{ Encrypt(dst, src []byte) }
	pending  []byte
	out      []byte
	finished bool
}

func newWeixinClawBotAESECBEncryptReader(src io.Reader, key []byte) (io.Reader, error) {
	if src == nil {
		return nil, errors.New("missing plaintext media body")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &weixinClawBotAESECBEncryptReader{src: src, block: block}, nil
}

func (r *weixinClawBotAESECBEncryptReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 && !r.finished {
		if err := r.fill(); err != nil {
			return 0, err
		}
	}
	if len(r.out) > 0 {
		n := copy(p, r.out)
		r.out = r.out[n:]
		return n, nil
	}
	return 0, io.EOF
}

func (r *weixinClawBotAESECBEncryptReader) fill() error {
	buf := make([]byte, 32*1024)
	n, err := r.src.Read(buf)
	data := append(r.pending, buf[:n]...)
	if err == io.EOF {
		padding := aes.BlockSize - len(data)%aes.BlockSize
		if padding == 0 {
			padding = aes.BlockSize
		}
		data = append(data, bytes.Repeat([]byte{byte(padding)}, padding)...)
		r.out = encryptWeixinClawBotAESECBBlocks(r.block, data)
		r.pending = nil
		r.finished = true
		return nil
	}
	if err != nil {
		return err
	}
	encryptLen := len(data) - len(data)%aes.BlockSize
	if encryptLen > 0 {
		r.out = encryptWeixinClawBotAESECBBlocks(r.block, data[:encryptLen])
	}
	r.pending = append(r.pending[:0], data[encryptLen:]...)
	return nil
}

func encryptWeixinClawBotAESECBBlocks(block interface{ Encrypt(dst, src []byte) }, data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	encrypted := make([]byte, len(data))
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		block.Encrypt(encrypted[offset:offset+aes.BlockSize], data[offset:offset+aes.BlockSize])
	}
	return encrypted
}

func setWeixinClawBotAuthHeaders(req *http.Request, botToken string) {
	setWeixinClawBotAuthOnlyHeaders(req, botToken)
	req.Header.Set("Content-Type", "application/json")
}

func setWeixinClawBotAuthOnlyHeaders(req *http.Request, botToken string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(botToken))
	req.Header.Set("AuthorizationType", "ilink_bot_token")
}

func weixinClawBotMessageText(msg weixinClawBotMessage) string {
	var parts []string
	for _, item := range msg.ItemList {
		if strings.TrimSpace(item.TextItem.Text) != "" {
			parts = append(parts, item.TextItem.Text)
		}
	}
	return strings.Join(parts, "")
}

func clawBotMessageID(msg weixinClawBotMessage) string {
	raw := strings.TrimSpace(string(msg.MessageID))
	raw = strings.Trim(raw, `"`)
	return raw
}

func weixinClawBotClientMsgID(token *types.WeixinClawBotToken, msg weixinClawBotMessage) string {
	messageID := clawBotMessageID(msg)
	if messageID == "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s", token.ID, msg.FromUserID, msg.ToUserID, weixinClawBotMessageFingerprint(msg))))
		messageID = hex.EncodeToString(sum[:16])
	}
	return "weixin_clawbot:" + token.TokenHash + ":" + messageID
}

func weixinClawBotMessageFingerprint(msg weixinClawBotMessage) string {
	parts := make([]string, 0, len(msg.ItemList)+1)
	if text := strings.TrimSpace(weixinClawBotMessageText(msg)); text != "" {
		parts = append(parts, text)
	}
	for _, item := range msg.ItemList {
		raw := weixinClawBotItemRaw(item)
		if len(raw) > 0 {
			parts = append(parts, string(raw))
		}
	}
	return strings.Join(parts, "\x00")
}

func hashWeixinClawBotToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func last4(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return value
	}
	return value[len(value)-4:]
}

func copyWeixinClawBotContexts(contexts map[string]types.WeixinClawBotContext) map[string]types.WeixinClawBotContext {
	next := make(map[string]types.WeixinClawBotContext, len(contexts))
	for key, value := range contexts {
		next[key] = value
	}
	return next
}

func randomClawBotHex(n int) string {
	if n <= 0 {
		n = 6
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func randomWechatUIN() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	n := binary.BigEndian.Uint32(buf)
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", n)))
}

func sleepWithContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return time.Second
	}
	next := current * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	return next
}

func commaListEnv(names ...string) []string {
	raw := strings.TrimSpace(firstEnv(names...))
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
	})
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(field); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func secondsEnvDuration(name string, defaultSeconds int, minSeconds int, maxSeconds int) time.Duration {
	value := strings.TrimSpace(firstEnv(name))
	seconds := defaultSeconds
	if value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			seconds = parsed
		}
	}
	if seconds < minSeconds {
		seconds = minSeconds
	}
	if maxSeconds > 0 && seconds > maxSeconds {
		seconds = maxSeconds
	}
	return time.Duration(seconds) * time.Second
}

func falseEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off", "disabled":
		return true
	default:
		return false
	}
}

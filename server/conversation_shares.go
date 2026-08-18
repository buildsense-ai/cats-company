package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	conversationShareDefaultTTL = 7 * 24 * time.Hour
	conversationShareMinTTL     = time.Hour
	conversationShareMaxTTL     = 30 * 24 * time.Hour
	conversationShareMaxItems   = 100
	conversationShareMaxAsset   = 64 << 20
	conversationShareMaxAssets  = 128 << 20
)

var conversationShareUploadKeyPattern = regexp.MustCompile(`^\d{8}_[a-f0-9]{32}\.[a-z0-9]+$`)

// ConversationShareHandler creates immutable, deliberately limited excerpts
// from authenticated conversations and serves them to holders of a capability
// URL. It never delegates guest traffic to the regular message history APIs.
type ConversationShareHandler struct {
	db         store.Store
	hub        *Hub
	uploadRoot string
	assetRoot  string
	now        func() time.Time
}

func NewConversationShareHandler(db store.Store, hub *Hub, uploadRoot, assetRoot string) *ConversationShareHandler {
	return &ConversationShareHandler{
		db:         db,
		hub:        hub,
		uploadRoot: strings.TrimSpace(uploadRoot),
		assetRoot:  strings.TrimSpace(assetRoot),
		now:        func() time.Time { return time.Now().UTC() },
	}
}

type createConversationShareRequest struct {
	TopicID    string  `json:"topic_id"`
	MessageIDs []int64 `json:"message_ids"`
	Title      string  `json:"title"`
	ExpiresIn  int64   `json:"expires_in"`
}

type conversationShareSnapshot struct {
	ID            string               `json:"id"`
	Speaker       string               `json:"speaker"`
	CreatedAt     *time.Time           `json:"created_at,omitempty"`
	Content       string               `json:"content,omitempty"`
	ContentBlocks []types.ContentBlock `json:"content_blocks,omitempty"`
}

// HandleAuthenticated owns the creation and revocation side of a share. It is
// mounted behind JWT auth; the handler still verifies the context uid so tests
// and future routes fail closed.
func (h *ConversationShareHandler) HandleAuthenticated(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversation sharing is unavailable"})
		return
	}
	if r.URL.Path == "/api/conversation-shares" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handleCreate(w, r)
		return
	}

	shareID := strings.TrimPrefix(r.URL.Path, "/api/conversation-shares/")
	if shareID == "" || strings.Contains(shareID, "/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	h.handleRevoke(w, r, shareID)
}

func (h *ConversationShareHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}

	var req createConversationShareRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid share request"})
		return
	}
	req.TopicID = strings.TrimSpace(req.TopicID)
	req.Title = strings.TrimSpace(req.Title)
	if req.TopicID == "" || len(req.TopicID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid topic_id required"})
		return
	}
	if len(req.MessageIDs) == 0 || len(req.MessageIDs) > conversationShareMaxItems {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "select between 1 and 100 messages"})
		return
	}
	if req.Title == "" {
		req.Title = "会话片段"
	}
	if utf8.RuneCountInString(req.Title) > 80 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title must be 80 characters or fewer"})
		return
	}

	selected := make(map[int64]struct{}, len(req.MessageIDs))
	for _, id := range req.MessageIDs {
		if id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message_ids must be positive"})
			return
		}
		if _, exists := selected[id]; exists {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message_ids must be unique"})
			return
		}
		selected[id] = struct{}{}
	}

	if code, text := h.validateSourceAccess(uid, req.TopicID); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return
	}

	messageStore, ok := h.db.(store.MessagesByIDStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "conversation sharing is unavailable"})
		return
	}
	shareStore, ok := h.db.(store.ConversationShareStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "conversation sharing is unavailable"})
		return
	}
	messages, err := messageStore.GetMessagesByIDs(req.TopicID, req.MessageIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load selected messages"})
		return
	}
	if len(messages) != len(selected) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one or more selected messages are unavailable"})
		return
	}
	found := make(map[int64]struct{}, len(messages))
	for _, message := range messages {
		if message == nil || message.TopicID != req.TopicID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one or more selected messages are unavailable"})
			return
		}
		if _, requested := selected[message.ID]; !requested {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one or more selected messages are unavailable"})
			return
		}
		if _, duplicate := found[message.ID]; duplicate {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one or more selected messages are unavailable"})
			return
		}
		found[message.ID] = struct{}{}
	}
	if len(found) != len(selected) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one or more selected messages are unavailable"})
		return
	}
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].CreatedAt.Equal(messages[j].CreatedAt) {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].CreatedAt.Before(messages[j].CreatedAt)
	})

	ttl := conversationShareDefaultTTL
	if req.ExpiresIn != 0 {
		ttl = time.Duration(req.ExpiresIn) * time.Second
	}
	if ttl < conversationShareMinTTL || ttl > conversationShareMaxTTL {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_in must be between 1 hour and 30 days"})
		return
	}
	shareID, err := newConversationShareID()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create share"})
		return
	}
	token, err := newConversationShareToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create share"})
		return
	}
	now := h.clockNow()
	expiresAt := now.Add(ttl)
	share := &store.ConversationShare{
		ID:        shareID,
		OwnerUID:  uid,
		TopicID:   req.TopicID,
		TokenHash: conversationShareTokenHash(token),
		Title:     req.Title,
		State:     store.ConversationShareStateActive,
		ExpiresAt: &expiresAt,
		CreatedAt: now,
	}

	items := make([]*store.ConversationShareItem, 0, len(messages))
	assets := make([]*store.ConversationShareAsset, 0)
	createdAssetPaths := make([]string, 0)
	for position, message := range messages {
		itemID, itemIDErr := newConversationShareID()
		if itemIDErr != nil {
			h.removeCreatedAssets(createdAssetPaths)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create share"})
			return
		}
		snapshot, itemAssets, assetPaths, snapshotErr := h.makeSnapshot(uid, shareID, itemID, message, conversationShareMaxAssets-assetsSize(assets))
		if snapshotErr != nil {
			h.removeCreatedAssets(append(createdAssetPaths, assetPaths...))
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": snapshotErr.Error()})
			return
		}
		createdAssetPaths = append(createdAssetPaths, assetPaths...)
		serialized, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			h.removeCreatedAssets(createdAssetPaths)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create share"})
			return
		}
		items = append(items, &store.ConversationShareItem{
			ID:              itemID,
			ShareID:         shareID,
			Position:        position + 1,
			SourceMessageID: message.ID,
			Speaker:         snapshot.Speaker,
			Snapshot:        string(serialized),
		})
		assets = append(assets, itemAssets...)
	}
	if err := shareStore.CreateConversationShare(share, items, assets); err != nil {
		h.removeCreatedAssets(createdAssetPaths)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save share"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":            share.ID,
		"title":         share.Title,
		"url":           conversationShareURL(r, token),
		"expires_at":    share.ExpiresAt,
		"message_count": len(items),
	})
}

func (h *ConversationShareHandler) handleRevoke(w http.ResponseWriter, r *http.Request, shareID string) {
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	shareStore, ok := h.db.(store.ConversationShareStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "conversation sharing is unavailable"})
		return
	}
	revoked, err := shareStore.RevokeConversationShare(uid, shareID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke share"})
		return
	}
	if !revoked {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "share not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// HandlePublic only accepts the capability token route. It intentionally has
// no route into the authenticated history or WebSocket subsystems.
func (h *ConversationShareHandler) HandlePublic(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.db == nil || !strings.HasPrefix(r.URL.Path, "/api/shared-conversations/") {
		h.writeUnavailableShare(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/shared-conversations/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		h.writeUnavailableShare(w)
		return
	}
	token := parts[0]
	share, shareStore, ok := h.loadPublicShare(token)
	if !ok {
		h.writeUnavailableShare(w)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			h.writePublicJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handlePublicSnapshot(w, token, share, shareStore)
		return
	}
	if len(parts) == 3 && parts[1] == "assets" && parts[2] != "" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			h.writeUnavailableShare(w)
			return
		}
		h.handlePublicAsset(w, r, share, shareStore, parts[2])
		return
	}
	h.writeUnavailableShare(w)
}

func (h *ConversationShareHandler) handlePublicSnapshot(w http.ResponseWriter, token string, share *store.ConversationShare, shareStore store.ConversationShareStore) {
	items, err := shareStore.GetConversationShareItems(share.ID)
	if err != nil {
		h.writeUnavailableShare(w)
		return
	}
	publicItems := make([]conversationShareSnapshot, 0, len(items))
	for _, item := range items {
		if item == nil || item.ShareID != share.ID {
			continue
		}
		var snapshot conversationShareSnapshot
		if err := json.Unmarshal([]byte(item.Snapshot), &snapshot); err != nil {
			h.writeUnavailableShare(w)
			return
		}
		snapshot.ID = item.ID
		snapshot.Speaker = normalizeConversationShareSpeaker(item.Speaker)
		for blockIndex := range snapshot.ContentBlocks {
			payload := snapshot.ContentBlocks[blockIndex].Payload
			if payload == nil {
				continue
			}
			assetID, _ := payload["asset_id"].(string)
			if assetID == "" {
				continue
			}
			payload["url"] = conversationShareAssetURL(token, assetID)
			delete(payload, "asset_id")
		}
		publicItems = append(publicItems, snapshot)
	}
	h.writePublicJSON(w, http.StatusOK, map[string]interface{}{
		"title":      share.Title,
		"expires_at": share.ExpiresAt,
		"items":      publicItems,
	})
}

func (h *ConversationShareHandler) handlePublicAsset(w http.ResponseWriter, r *http.Request, share *store.ConversationShare, shareStore store.ConversationShareStore, assetID string) {
	asset, err := shareStore.GetConversationShareAsset(share.ID, assetID)
	if err != nil || asset == nil || asset.ShareID != share.ID {
		h.writeUnavailableShare(w)
		return
	}
	fullPath, ok := h.assetPath(asset.StorageKey)
	if !ok {
		h.writeUnavailableShare(w)
		return
	}
	if _, err := os.Stat(fullPath); err != nil {
		h.writeUnavailableShare(w)
		return
	}
	h.writePublicHeaders(w)
	if mediaType, _, err := mime.ParseMediaType(asset.MimeType); err == nil && mediaType != "" {
		w.Header().Set("Content-Type", mediaType)
		if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
			// A shared attachment is served from the application origin. Keep an
			// HTML report in an opaque sandbox so opening it directly cannot read
			// an owner's browser session or application storage. The permissions
			// mirror regular uploaded HTML and retain interactive report previews
			// without restoring its same-origin privileges.
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals")
		} else if mediaType == "image/svg+xml" {
			// SVG can contain executable content when opened as a document. Shared
			// copies never need scripting, so keep them opaque and scriptless.
			w.Header().Set("Content-Security-Policy", "sandbox")
		}
	}
	if asset.Name != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", safeConversationShareFileName(asset.Name)))
	}
	http.ServeFile(w, r, fullPath)
}

func (h *ConversationShareHandler) validateSourceAccess(uid int64, topicID string) (int, string) {
	if h.hub != nil {
		return h.hub.validateTopicReadAccess(uid, types.AccountHuman, topicID)
	}
	if !p2pTopicIncludesUID(topicID, uid) {
		return http.StatusForbidden, "conversation is not accessible"
	}
	return 0, ""
}

func (h *ConversationShareHandler) loadPublicShare(token string) (*store.ConversationShare, store.ConversationShareStore, bool) {
	shareStore, ok := h.db.(store.ConversationShareStore)
	if !ok || strings.TrimSpace(token) == "" {
		return nil, nil, false
	}
	share, err := shareStore.GetConversationShareByTokenHash(conversationShareTokenHash(token))
	if err != nil || !conversationShareIsActive(share, h.clockNow()) {
		return nil, nil, false
	}
	return share, shareStore, true
}

func conversationShareIsActive(share *store.ConversationShare, now time.Time) bool {
	if share == nil || share.State != store.ConversationShareStateActive {
		return false
	}
	return share.ExpiresAt == nil || share.ExpiresAt.After(now)
}

func (h *ConversationShareHandler) makeSnapshot(ownerUID int64, shareID, itemID string, message *types.Message, remainingAssets int64) (conversationShareSnapshot, []*store.ConversationShareAsset, []string, error) {
	if message == nil {
		return conversationShareSnapshot{}, nil, nil, fmt.Errorf("selected message is unavailable")
	}
	displayType := inferDisplayTypeFromStoredMessage(message.MsgType, message.Content, message.ContentBlocks)
	if !isUserVisibleMessageType(displayType) || isInternalAgentWorkingMessage(displayType, decodeStoredContent(message.Content), message.ContentBlocks) {
		return conversationShareSnapshot{}, nil, nil, fmt.Errorf("a selected message cannot be shared")
	}
	snapshot := conversationShareSnapshot{
		ID:      itemID,
		Speaker: h.snapshotSpeaker(ownerUID, message.FromUID),
	}
	if !message.CreatedAt.IsZero() {
		createdAt := message.CreatedAt.UTC()
		snapshot.CreatedAt = &createdAt
	}
	blocks := append([]types.ContentBlock(nil), message.ContentBlocks...)
	if len(blocks) == 0 {
		if displayType == "text" {
			if text := conversationSharePlainText(decodeStoredContent(message.Content)); text != "" {
				snapshot.Content = text
			}
		} else if block, ok := conversationShareRichBlock(message); ok {
			blocks = append(blocks, block)
		}
	}

	assets := make([]*store.ConversationShareAsset, 0)
	paths := make([]string, 0)
	for _, block := range blocks {
		sanitized, asset, assetPath, err := h.sanitizeSnapshotBlock(shareID, itemID, block, remainingAssets-assetsSize(assets))
		if err != nil {
			return conversationShareSnapshot{}, nil, paths, err
		}
		if sanitized == nil {
			continue
		}
		snapshot.ContentBlocks = append(snapshot.ContentBlocks, *sanitized)
		if asset != nil {
			assets = append(assets, asset)
			paths = append(paths, assetPath)
		}
	}
	if snapshot.Content == "" && len(snapshot.ContentBlocks) == 0 {
		return conversationShareSnapshot{}, nil, paths, fmt.Errorf("a selected message has no shareable content")
	}
	return snapshot, assets, paths, nil
}

func conversationSharePlainText(content interface{}) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]interface{}:
		for _, key := range []string{"text", "content"} {
			if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
		if payload, ok := value["payload"].(map[string]interface{}); ok {
			for _, key := range []string{"text", "content"} {
				if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func (h *ConversationShareHandler) snapshotSpeaker(ownerUID, fromUID int64) string {
	if fromUID == ownerUID {
		return "self"
	}
	if user, err := h.db.GetUser(fromUID); err == nil && user != nil && user.AccountType == types.AccountBot {
		return "assistant"
	}
	return "participant"
}

func normalizeConversationShareSpeaker(value string) string {
	switch value {
	case "self", "assistant", "participant":
		return value
	default:
		return "participant"
	}
}

func conversationShareRichBlock(message *types.Message) (types.ContentBlock, bool) {
	if message == nil || strings.TrimSpace(message.Content) == "" {
		return types.ContentBlock{}, false
	}
	var raw struct {
		Type    string                 `json:"type"`
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(message.Content), &raw); err != nil || raw.Type == "" {
		return types.ContentBlock{}, false
	}
	return types.ContentBlock{Type: raw.Type, Payload: raw.Payload}, true
}

func (h *ConversationShareHandler) sanitizeSnapshotBlock(shareID, itemID string, block types.ContentBlock, remainingAssets int64) (*types.ContentBlock, *store.ConversationShareAsset, string, error) {
	switch strings.ToLower(strings.TrimSpace(block.Type)) {
	case "text", "assistant_text":
		text := strings.TrimSpace(block.Text)
		if text == "" {
			text = strings.TrimSpace(block.Content)
		}
		if text == "" {
			return nil, nil, "", nil
		}
		return &types.ContentBlock{Type: "text", Text: text}, nil, "", nil
	case "image", "file", "audio", "voice", "video":
		asset, assetPath, err := h.copySnapshotAsset(shareID, itemID, strings.ToLower(strings.TrimSpace(block.Type)), block.Payload, remainingAssets)
		if err != nil {
			return nil, nil, "", err
		}
		payload := map[string]interface{}{
			"asset_id":  asset.ID,
			"name":      asset.Name,
			"mime_type": asset.MimeType,
			"size":      asset.Size,
		}
		return &types.ContentBlock{Type: strings.ToLower(strings.TrimSpace(block.Type)), Payload: payload}, asset, assetPath, nil
	default:
		// Runtime plans, tool details, debugging, link previews, and unknown
		// extension blocks are never part of a public transcript.
		return nil, nil, "", nil
	}
}

func (h *ConversationShareHandler) copySnapshotAsset(shareID, itemID, kind string, payload map[string]interface{}, remainingAssets int64) (*store.ConversationShareAsset, string, error) {
	if h.uploadRoot == "" || h.assetRoot == "" || remainingAssets <= 0 {
		return nil, "", fmt.Errorf("selected attachment cannot be shared")
	}
	sourceURL := conversationSharePayloadString(payload, "url")
	if sourceURL == "" {
		fileKey := strings.TrimPrefix(conversationSharePayloadString(payload, "file_key"), "/")
		if fileKey != "" {
			sourceURL = "/uploads/" + fileKey
		}
	}
	subDir, fileName, ok := conversationShareSourcePath(sourceURL)
	if !ok {
		return nil, "", fmt.Errorf("selected attachment cannot be shared")
	}
	sourcePath, ok := safeConversationSharePath(h.uploadRoot, filepath.Join(subDir, fileName))
	if !ok {
		return nil, "", fmt.Errorf("selected attachment cannot be shared")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, "", fmt.Errorf("selected attachment is unavailable")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > conversationShareMaxAsset || info.Size() > remainingAssets {
		return nil, "", fmt.Errorf("selected attachment is too large to share")
	}
	assetID, err := newConversationShareID()
	if err != nil {
		return nil, "", fmt.Errorf("failed to prepare attachment")
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	storageKey := filepath.ToSlash(filepath.Join(shareID, assetID+ext))
	destinationPath, ok := h.assetPath(storageKey)
	if !ok {
		return nil, "", fmt.Errorf("failed to prepare attachment")
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
		return nil, "", fmt.Errorf("failed to prepare attachment")
	}
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("failed to prepare attachment")
	}
	written, copyErr := io.Copy(destination, io.LimitReader(source, conversationShareMaxAsset+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written != info.Size() {
		_ = os.Remove(destinationPath)
		return nil, "", fmt.Errorf("failed to copy selected attachment")
	}

	name := safeConversationShareFileName(conversationSharePayloadFirstString(payload, "name", "file_name"))
	if name == "" {
		name = fileName
	}
	mimeType := safeConversationShareMimeType(conversationSharePayloadFirstString(payload, "mime_type", "content_type"), ext)
	return &store.ConversationShareAsset{
		ID:         assetID,
		ShareID:    shareID,
		ItemID:     itemID,
		StorageKey: storageKey,
		Name:       name,
		MimeType:   mimeType,
		Size:       written,
		Kind:       kind,
	}, destinationPath, nil
}

func conversationShareSourcePath(raw string) (string, string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = parsed.Path
	}
	segments := strings.Split(strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "/"), "/")
	if len(segments) != 3 || segments[0] != "uploads" {
		return "", "", false
	}
	if (segments[1] != "images" && segments[1] != "files") || !conversationShareUploadKeyPattern.MatchString(segments[2]) {
		return "", "", false
	}
	return segments[1], segments[2], true
}

func safeConversationSharePath(root, relative string) (string, bool) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(relative) == "" {
		return "", false
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	full, err := filepath.Abs(filepath.Join(base, relative))
	if err != nil || (full != base && !strings.HasPrefix(full, base+string(os.PathSeparator))) {
		return "", false
	}
	return full, true
}

func (h *ConversationShareHandler) assetPath(storageKey string) (string, bool) {
	return safeConversationSharePath(h.assetRoot, filepath.FromSlash(storageKey))
}

func (h *ConversationShareHandler) removeCreatedAssets(paths []string) {
	root, err := filepath.Abs(h.assetRoot)
	if err != nil {
		return
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		fullPath, fullErr := filepath.Abs(path)
		if fullErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(root, fullPath)
		if relErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			continue
		}
		_ = os.Remove(fullPath)
	}
}

func conversationSharePayloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, exists := payload[key]
	if !exists || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func conversationSharePayloadFirstString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := conversationSharePayloadString(payload, key); value != "" {
			return value
		}
	}
	return ""
}

func safeConversationShareFileName(value string) string {
	name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	if name == "." || name == "/" || len(name) > 240 {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, name)
}

func safeConversationShareMimeType(value, ext string) string {
	if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value)); err == nil && mediaType != "" {
		return mediaType
	}
	if guessed := mime.TypeByExtension(ext); guessed != "" {
		if mediaType, _, err := mime.ParseMediaType(guessed); err == nil && mediaType != "" {
			return mediaType
		}
	}
	return "application/octet-stream"
}

func assetsSize(assets []*store.ConversationShareAsset) int64 {
	var total int64
	for _, asset := range assets {
		if asset != nil && asset.Size > 0 {
			total += asset.Size
		}
	}
	return total
}

func (h *ConversationShareHandler) writeUnavailableShare(w http.ResponseWriter) {
	h.writePublicJSON(w, http.StatusNotFound, map[string]string{"error": "share unavailable"})
}

func (h *ConversationShareHandler) writePublicHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (h *ConversationShareHandler) writePublicJSON(w http.ResponseWriter, status int, payload interface{}) {
	h.writePublicHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *ConversationShareHandler) clockNow() time.Time {
	if h != nil && h.now != nil {
		return h.now().UTC()
	}
	return time.Now().UTC()
}

func newConversationShareID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func newConversationShareToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func conversationShareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func conversationShareURL(r *http.Request, token string) string {
	if r == nil {
		return "/share/" + url.PathEscape(token)
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "https" || forwarded == "http" {
		scheme = forwarded
	}
	// The edge proxy sets Host to the public application hostname. Do not trust
	// X-Forwarded-Host here: a caller-controlled value could turn the returned
	// capability URL into a link that discloses its token to another origin.
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return "/share/" + url.PathEscape(token)
	}
	return scheme + "://" + host + "/share/" + url.PathEscape(token)
}

func conversationShareAssetURL(token, assetID string) string {
	return "/api/shared-conversations/" + url.PathEscape(token) + "/assets/" + url.PathEscape(assetID)
}

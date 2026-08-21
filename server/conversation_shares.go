package server

import (
	"context"
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
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	// A share is a snapshot of already accepted uploads, so it must not impose
	// a smaller limit than the normal upload path.
	conversationShareMaxAssetBytes      = maxFileSize
	conversationShareMaxTotalAssetBytes = maxFileSize
	// Keep the public asset fan-out bounded independently from the byte budget.
	// This also gives the public asset rate-limit bucket a deterministic ceiling.
	conversationShareMaxAssetCount = 256
	// Snapshot JSON is loaded into memory both while a share is created and
	// while it is rendered publicly. Keep unusually large messages from turning
	// a capability link into an unbounded database response or heap allocation.
	conversationShareMaxSnapshotBytes      = 1 << 20
	conversationShareMaxTotalSnapshotBytes = 8 << 20
	conversationShareCleanupEvery          = time.Hour
)

var conversationShareIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Text messages may carry old attachment and capability links. Capture URL-like
// spans and classify them after URL decoding and path normalization instead of
// assuming their current route shape or generated filename. CJK prose is a
// delimiter so a link immediately followed by a Chinese sentence stays intact.
var conversationShareURLCandidatePattern = regexp.MustCompile(`(?i)(?:https?://[^\s<>"'(),，。！？；：\p{Han}]+|(?:/|%2f)[^\s<>"'(),，。！？；：\p{Han}]+)`)

// ConversationShareHandler creates immutable, deliberately limited excerpts
// from authenticated conversations and serves them to holders of a capability
// URL. It never delegates guest traffic to the regular message history APIs.
type ConversationShareHandler struct {
	db         store.Store
	hub        *Hub
	uploadRoot string
	assetRoot  string
	now        func() time.Time
	assetMu    sync.Mutex
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

// conversationShareOwnerSummary deliberately omits the capability token and
// source topic metadata. It is only used to let an authenticated owner revoke
// a previously-created link from the originating conversation.
type conversationShareOwnerSummary struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
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
		switch r.Method {
		case http.MethodGet:
			h.handleList(w, r)
		case http.MethodPost:
			h.handleCreate(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
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
	if req.TopicID == "" || len(req.TopicID) > 64 {
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

	ttl, validTTL := conversationShareTTL(req.ExpiresIn)
	if !validTTL {
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

	// Keep a freshly-created directory out of a concurrent cleanup pass until
	// its matching database record has been committed.
	h.assetMu.Lock()
	defer h.assetMu.Unlock()

	items := make([]*store.ConversationShareItem, 0, len(messages))
	assets := make([]*store.ConversationShareAsset, 0)
	createdAssetPaths := make([]string, 0)
	var snapshotBytes int
	for position, message := range messages {
		itemID, itemIDErr := newConversationShareID()
		if itemIDErr != nil {
			h.removeCreatedAssets(createdAssetPaths)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create share"})
			return
		}
		snapshot, itemAssets, assetPaths, snapshotErr := h.makeSnapshot(
			uid,
			shareID,
			itemID,
			message,
			conversationShareMaxTotalAssetBytes-assetsSize(assets),
			conversationShareMaxAssetCount-len(assets),
		)
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
		serializedBytes := len(serialized)
		if serializedBytes > conversationShareMaxSnapshotBytes {
			h.removeCreatedAssets(createdAssetPaths)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a selected message is too large to share"})
			return
		}
		if snapshotBytes > conversationShareMaxTotalSnapshotBytes-serializedBytes {
			h.removeCreatedAssets(createdAssetPaths)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "selected messages are too large to share"})
			return
		}
		snapshotBytes += serializedBytes
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

func (h *ConversationShareHandler) handleList(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	topicID := strings.TrimSpace(r.URL.Query().Get("topic_id"))
	if topicID == "" || len(topicID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid topic_id required"})
		return
	}
	shareStore, ok := h.db.(store.ConversationShareStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "conversation sharing is unavailable"})
		return
	}
	shares, err := shareStore.ListConversationShares(uid, topicID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load shares"})
		return
	}
	now := h.clockNow()
	response := make([]conversationShareOwnerSummary, 0, len(shares))
	for _, share := range shares {
		if share == nil {
			continue
		}
		state := string(share.State)
		if share.State == store.ConversationShareStateActive && !conversationShareIsActive(share, now) {
			state = "expired"
		}
		response = append(response, conversationShareOwnerSummary{
			ID:        share.ID,
			Title:     share.Title,
			State:     state,
			ExpiresAt: share.ExpiresAt,
			CreatedAt: share.CreatedAt,
			RevokedAt: share.RevokedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"shares": response})
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
	h.assetMu.Lock()
	defer h.assetMu.Unlock()
	h.removeShareAssetDirectory(shareID)
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// StartAssetCleanup removes copied files that can no longer be reached through
// a share: explicit revocations, expiry, and DB-level cascade deletion all
// converge on the same safe directory sweep. A database error retains files
// for the next pass rather than deleting a possibly active share.
func (h *ConversationShareHandler) StartAssetCleanup(ctx context.Context) {
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.cleanupInactiveAssetDirectories()
	go func() {
		ticker := time.NewTicker(conversationShareCleanupEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.cleanupInactiveAssetDirectories()
			}
		}
	}()
}

func (h *ConversationShareHandler) cleanupInactiveAssetDirectories() {
	if h == nil || strings.TrimSpace(h.assetRoot) == "" {
		return
	}
	shareStore, ok := h.db.(store.ConversationShareStore)
	if !ok {
		return
	}
	h.assetMu.Lock()
	defer h.assetMu.Unlock()
	entries, err := os.ReadDir(h.assetRoot)
	if err != nil {
		return
	}
	now := h.clockNow()
	for _, entry := range entries {
		if !entry.IsDir() || !conversationShareIDPattern.MatchString(entry.Name()) {
			continue
		}
		share, err := shareStore.GetConversationShareByID(entry.Name())
		if err != nil {
			continue
		}
		if !conversationShareIsActive(share, now) {
			h.removeShareAssetDirectory(entry.Name())
		}
	}
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
	if len(items) > conversationShareMaxItems {
		h.writeUnavailableShare(w)
		return
	}
	publicItems := make([]conversationShareSnapshot, 0, len(items))
	var snapshotBytes int
	for _, item := range items {
		if item == nil || item.ShareID != share.ID {
			continue
		}
		serializedBytes := len(item.Snapshot)
		if serializedBytes > conversationShareMaxSnapshotBytes || snapshotBytes > conversationShareMaxTotalSnapshotBytes-serializedBytes {
			h.writeUnavailableShare(w)
			return
		}
		snapshotBytes += serializedBytes
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
	contentType, disposition, contentSecurityPolicy := conversationShareAssetResponseMetadata(asset)
	w.Header().Set("Content-Type", contentType)
	if contentSecurityPolicy != "" {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	}
	fileName := safeConversationShareFileName(asset.Name)
	if fileName == "" {
		fileName = safeConversationShareFileName(filepath.Base(asset.StorageKey))
	}
	if fileName == "" {
		fileName = "shared-asset"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, fileName))
	http.ServeFile(w, r, fullPath)
}

const conversationShareHTMLContentSecurityPolicy = "sandbox allow-scripts allow-forms allow-popups allow-modals"

// conversationShareAssetResponseMetadata treats the validated storage
// extension as authoritative. The MIME value came from an earlier message
// payload and must not be allowed to turn a passive upload such as .txt or
// .js into an executable same-origin response.
func conversationShareAssetResponseMetadata(asset *store.ConversationShareAsset) (contentType, disposition, contentSecurityPolicy string) {
	contentType = "application/octet-stream"
	disposition = "attachment"
	if asset == nil {
		return contentType, disposition, ""
	}

	ext := strings.ToLower(filepath.Ext(asset.StorageKey))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(asset.Name))
	}
	contentType = conversationShareCanonicalMimeType(ext, asset.Kind)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".pdf":
		disposition = "inline"
	case ".mp3", ".ogg", ".wav":
		// Ogg audio is safe to play inline; unsupported audio extensions stay
		// downloads even when an old payload reports an audio MIME type.
		disposition = "inline"
	case ".mp4", ".m4v", ".webm", ".ogv", ".mov":
		disposition = "inline"
	case ".html", ".htm":
		disposition = "inline"
		contentSecurityPolicy = conversationShareHTMLContentSecurityPolicy
	case ".svg":
		disposition = "inline"
		contentSecurityPolicy = "sandbox"
	}
	if conversationShareActiveAssetExtension(ext) {
		// Script-like and XML-like uploads are never opened as documents from a
		// capability URL, even when an old record reports an active MIME type.
		contentType = "application/octet-stream"
		disposition = "attachment"
		contentSecurityPolicy = ""
	}
	return contentType, disposition, contentSecurityPolicy
}

func conversationShareCanonicalMimeType(ext, kind string) string {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg":
		if strings.EqualFold(kind, "video") {
			return "video/ogg"
		}
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".mov":
		return "video/quicktime"
	case ".html", ".htm":
		return "text/html"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	default:
		if guessed := mime.TypeByExtension(strings.ToLower(ext)); guessed != "" {
			if mediaType, _, err := mime.ParseMediaType(guessed); err == nil && mediaType != "" {
				return mediaType
			}
		}
		return "application/octet-stream"
	}
}

func conversationShareActiveAssetExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".py", ".go", ".css", ".xml", ".xhtml":
		return true
	default:
		return false
	}
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

func (h *ConversationShareHandler) makeSnapshot(ownerUID int64, shareID, itemID string, message *types.Message, remainingAssetBytes int64, remainingAssetCount int) (conversationShareSnapshot, []*store.ConversationShareAsset, []string, error) {
	if message == nil {
		return conversationShareSnapshot{}, nil, nil, fmt.Errorf("selected message is unavailable")
	}
	displayType := inferDisplayTypeFromStoredMessage(message.MsgType, message.Content, message.ContentBlocks)
	if !isConversationShareableMessageType(displayType) || conversationShareIsInternalAgentWorkingMessage(displayType, decodeStoredContent(message.Content), message.ContentBlocks) {
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
	plainText := conversationSharePlainText(decodeStoredContent(message.Content))
	blocks := append([]types.ContentBlock(nil), message.ContentBlocks...)
	if len(blocks) == 0 {
		if displayType != "text" {
			if block, ok := conversationShareRichBlock(message); ok {
				blocks = append(blocks, block)
			}
		} else if plainText == "" {
			if block, ok := conversationShareRichBlock(message); ok {
				blocks = append(blocks, block)
			}
		}
	}

	assets := make([]*store.ConversationShareAsset, 0)
	paths := make([]string, 0)
	hasTextBlock := false
	hasOmittedBlock := false
	for _, block := range blocks {
		sanitized, asset, assetPath, err := h.sanitizeSnapshotBlock(
			shareID,
			itemID,
			block,
			remainingAssetBytes-assetsSize(assets),
			remainingAssetCount-len(assets),
		)
		if err != nil {
			return conversationShareSnapshot{}, nil, paths, err
		}
		if sanitized == nil {
			hasOmittedBlock = true
			continue
		}
		if sanitized.Type == "text" {
			hasTextBlock = true
		}
		snapshot.ContentBlocks = append(snapshot.ContentBlocks, *sanitized)
		if asset != nil {
			assets = append(assets, asset)
			paths = append(paths, assetPath)
		}
	}
	// Legacy text may accompany an otherwise structured attachment message.
	// Do not use the raw fallback if any block was omitted: the source content
	// can contain details the snapshot's whitelist deliberately excluded.
	if !hasTextBlock && !hasOmittedBlock {
		snapshot.Content = plainText
	}
	if snapshot.Content == "" && len(snapshot.ContentBlocks) == 0 {
		return conversationShareSnapshot{}, nil, paths, fmt.Errorf("a selected message has no shareable content")
	}
	return snapshot, assets, paths, nil
}

func isConversationShareableMessageType(displayType string) bool {
	return isUserVisibleMessageType(displayType) || strings.EqualFold(strings.TrimSpace(displayType), "audio")
}

// Conversation sharing may preserve an audio block without changing the
// visibility rules used by ordinary message delivery and durable agent context.
func conversationShareIsInternalAgentWorkingMessage(displayType string, content interface{}, blocks []types.ContentBlock) bool {
	switch strings.ToLower(strings.TrimSpace(displayType)) {
	case "runtime_plan", "thinking", "tool_use", "tool_result", "debug",
		"stream_delta", "stream_cancel", taskStatusType:
		return true
	}

	text := strings.TrimSpace(normalizeContentText(content))
	if strings.HasPrefix(text, "AI文本:") || strings.HasPrefix(text, "AI文本：") {
		return true
	}

	hasInternalBlock := false
	hasShareableBlock := false
	for _, block := range blocks {
		if isConversationShareInternalBlock(block) {
			hasInternalBlock = true
			continue
		}
		if isConversationShareableContentBlock(block.Type) {
			hasShareableBlock = true
		}
	}
	return hasInternalBlock && !hasShareableBlock
}

func isConversationShareInternalBlock(block types.ContentBlock) bool {
	return isInternalAgentContentBlock(block.Type) || strings.EqualFold(strings.TrimSpace(block.PresentationRole), "process")
}

func isConversationShareableContentBlock(blockType string) bool {
	switch strings.ToLower(strings.TrimSpace(blockType)) {
	case "text", "assistant_text", "image", "voice", "audio", "file", "video":
		return true
	default:
		return false
	}
}

func conversationSharePlainText(content interface{}) string {
	var text string
	switch value := content.(type) {
	case string:
		text = value
	case map[string]interface{}:
		for _, key := range []string{"text", "content"} {
			if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
				if sanitized := sanitizeConversationShareText(text); sanitized != "" {
					return sanitized
				}
			}
		}
		if payload, ok := value["payload"].(map[string]interface{}); ok {
			for _, key := range []string{"text", "content"} {
				if text, ok := payload[key].(string); ok && strings.TrimSpace(text) != "" {
					if sanitized := sanitizeConversationShareText(text); sanitized != "" {
						return sanitized
					}
				}
			}
		}
	}
	return sanitizeConversationShareText(text)
}

// sanitizeConversationShareText removes capability-bearing or owner-upload
// URLs that may survive in legacy plain-text messages. It normalizes encoded
// separators and dot segments before classifying a path, so a link cannot keep
// a private route hidden behind a redirect parameter or path traversal. Structured
// attachment blocks get a fresh share-scoped URL separately; ordinary external
// links stay intact.
func sanitizeConversationShareText(value string) string {
	return strings.TrimSpace(conversationShareURLCandidatePattern.ReplaceAllStringFunc(value, func(candidate string) string {
		if conversationSharePrivateURLCandidate(candidate, 0) {
			return ""
		}
		return candidate
	}))
}

func conversationSharePrivateURLCandidate(value string, depth int) bool {
	const maxConversationShareURLDecodeDepth = 2
	if depth > maxConversationShareURLDecodeDepth || strings.TrimSpace(value) == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		decoded, decodeErr := url.PathUnescape(value)
		if decodeErr != nil || decoded == value {
			return false
		}
		return conversationSharePrivateURLCandidate(decoded, depth+1)
	}
	if conversationSharePrivateURLPath(parsed.EscapedPath()) || conversationSharePrivateURLPath(parsed.Path) {
		return true
	}
	for _, values := range parsed.Query() {
		for _, nested := range values {
			if conversationSharePrivateURLCandidate(nested, depth+1) {
				return true
			}
		}
	}
	return conversationSharePrivateURLCandidate(parsed.Fragment, depth+1)
}

func conversationSharePrivateURLPath(value string) bool {
	decoded := value
	for attempt := 0; attempt < 2; attempt++ {
		unescaped, err := url.PathUnescape(decoded)
		if err != nil || unescaped == decoded {
			break
		}
		decoded = unescaped
	}
	decoded = strings.ReplaceAll(decoded, "\\", "/")
	normalized := path.Clean("/" + strings.TrimPrefix(decoded, "/"))
	return strings.HasPrefix(normalized, "/uploads/files/") ||
		strings.HasPrefix(normalized, "/uploads/images/") ||
		strings.HasPrefix(normalized, "/uploads/feedback/") ||
		strings.HasPrefix(normalized, "/api/shared-conversations/") ||
		strings.HasPrefix(normalized, "/share/")
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

func (h *ConversationShareHandler) sanitizeSnapshotBlock(shareID, itemID string, block types.ContentBlock, remainingAssetBytes int64, remainingAssetCount int) (*types.ContentBlock, *store.ConversationShareAsset, string, error) {
	if isConversationShareInternalBlock(block) {
		return nil, nil, "", nil
	}
	switch strings.ToLower(strings.TrimSpace(block.Type)) {
	case "text", "assistant_text":
		text := sanitizeConversationShareText(block.Text)
		if text == "" {
			text = sanitizeConversationShareText(block.Content)
		}
		if text == "" {
			return nil, nil, "", nil
		}
		return &types.ContentBlock{Type: "text", Text: text}, nil, "", nil
	case "image", "file", "audio", "voice", "video":
		asset, assetPath, err := h.copySnapshotAsset(
			shareID,
			itemID,
			strings.ToLower(strings.TrimSpace(block.Type)),
			block.Payload,
			remainingAssetBytes,
			remainingAssetCount,
		)
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

func (h *ConversationShareHandler) copySnapshotAsset(shareID, itemID, kind string, payload map[string]interface{}, remainingAssetBytes int64, remainingAssetCount int) (*store.ConversationShareAsset, string, error) {
	if remainingAssetCount <= 0 {
		return nil, "", fmt.Errorf("share contains too many attachments")
	}
	if h.uploadRoot == "" || h.assetRoot == "" || remainingAssetBytes <= 0 {
		return nil, "", fmt.Errorf("selected attachment cannot be shared")
	}
	sourceURL := conversationShareSourceURL(payload, kind)
	subDir, fileName, ok := conversationShareSourcePath(sourceURL)
	if !ok {
		return nil, "", fmt.Errorf("selected attachment cannot be shared")
	}
	sourcePath, ok := safeConversationShareSourcePath(h.uploadRoot, filepath.Join(subDir, fileName))
	if !ok {
		return nil, "", fmt.Errorf("selected attachment cannot be shared")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, "", fmt.Errorf("selected attachment is unavailable")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > conversationShareMaxAssetBytes || info.Size() > remainingAssetBytes {
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
	written, copyErr := io.Copy(destination, io.LimitReader(source, conversationShareMaxAssetBytes+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written != info.Size() {
		_ = os.Remove(destinationPath)
		return nil, "", fmt.Errorf("failed to copy selected attachment")
	}

	name := safeConversationShareFileName(conversationSharePayloadFirstString(payload, "name", "file_name"))
	if name == "" {
		name = fileName
	}
	// The source payload MIME is advisory. Derive the stored metadata from the
	// extension that passed the upload whitelist so the visitor renderer cannot
	// be tricked into treating a passive file as HTML or another active type.
	mimeType := conversationShareCanonicalMimeType(ext, kind)
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

func conversationShareSourceURL(payload map[string]interface{}, kind string) string {
	sourceURL := conversationSharePayloadString(payload, "url")
	if sourceURL != "" {
		return sourceURL
	}
	fileKey := strings.TrimPrefix(conversationSharePayloadString(payload, "file_key"), "/")
	if fileKey == "" {
		return ""
	}
	if strings.HasPrefix(fileKey, "uploads/") {
		return "/" + fileKey
	}
	if strings.Contains(fileKey, "/") {
		return "/uploads/" + fileKey
	}
	subDir := "files"
	if strings.EqualFold(strings.TrimSpace(kind), "image") {
		subDir = "images"
	}
	return "/uploads/" + subDir + "/" + fileKey
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
	if (segments[1] != "images" && segments[1] != "files") || !uploadFileNamePattern.MatchString(segments[2]) {
		return "", "", false
	}
	ext := strings.ToLower(filepath.Ext(segments[2]))
	if (segments[1] == "images" && !allowedImageExts[ext]) || (segments[1] == "files" && !allowedFileExts[ext]) {
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

// safeConversationShareSourcePath resolves the source through the filesystem
// before copying it. A lexical path check alone would follow an uploads-root
// symlink and could copy a file outside the upload storage into a public share.
func safeConversationShareSourcePath(root, relative string) (string, bool) {
	lexical, ok := safeConversationSharePath(root, relative)
	if !ok {
		return "", false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	realPath, err := filepath.EvalSymlinks(lexical)
	if err != nil {
		return "", false
	}
	relativePath, err := filepath.Rel(realRoot, realPath)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(os.PathSeparator)) {
		return "", false
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return realPath, true
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

func (h *ConversationShareHandler) removeShareAssetDirectory(shareID string) {
	if h == nil || !conversationShareIDPattern.MatchString(shareID) {
		return
	}
	path, ok := h.assetPath(shareID)
	if !ok {
		return
	}
	_ = os.RemoveAll(path)
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
	if base := conversationShareConfiguredPublicBaseURL(); base != "" {
		return base + "/share/" + url.PathEscape(token)
	}
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

func conversationShareConfiguredPublicBaseURL() string {
	raw := strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL"))
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return ""
	}
	return strings.TrimRight(parsed.String(), "/")
}

func conversationShareTTL(expiresIn int64) (time.Duration, bool) {
	if expiresIn == 0 {
		return conversationShareDefaultTTL, true
	}
	minSeconds := int64(conversationShareMinTTL / time.Second)
	maxSeconds := int64(conversationShareMaxTTL / time.Second)
	if expiresIn < minSeconds || expiresIn > maxSeconds {
		return 0, false
	}
	return time.Duration(expiresIn) * time.Second, true
}

func conversationShareAssetURL(token, assetID string) string {
	return "/api/shared-conversations/" + url.PathEscape(token) + "/assets/" + url.PathEscape(assetID)
}

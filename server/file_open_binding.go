package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	// fileOpenBindingTTL is the parent-held file annotation capability TTL.
	// It never slides and is capped by the issuing auth session expiry.
	fileOpenBindingTTL = 30 * time.Minute
	fileOpenBindingMax = 4096
	// File open bindings live in the same process-local fail-closed store as
	// artifact bindings: restart or another replica cannot resolve them, and
	// callers must reopen. Unlike artifact bindings there is no gateway
	// registry or unique-bot requirement.
)

// fileOpenBindingRecord is the server-held descriptor of one file annotation
// capability. The version digest covers the canonical descriptor JSON so a
// filename or URL change is detected without ambiguity from delimiters.
type fileOpenBindingRecord struct {
	ContractVersion string               `json:"contract_version"`
	OpenRef         string               `json:"open_ref"`
	TopicID         string               `json:"topic_id"`
	ExpiresAt       time.Time            `json:"expires_at"`
	Source          fileAnnotationSource `json:"source"`
	ActorUID        int64                `json:"actor_uid"`
	AuthFingerprint [32]byte             `json:"auth_session_fingerprint"`
	Revoked         bool                 `json:"revoked"`
}

// FileOpenBindingStore is the process-local bounded TTL capability store.
// It never guesses: any unresolved reference fails closed.
type FileOpenBindingStore struct {
	mu      sync.Mutex
	records map[string]fileOpenBindingRecord
}

func newFileOpenBindingStore() *FileOpenBindingStore {
	return &FileOpenBindingStore{records: make(map[string]fileOpenBindingRecord)}
}

func (s *FileOpenBindingStore) issue(record fileOpenBindingRecord, now time.Time) (fileOpenBindingRecord, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return record, err
	}
	record.OpenRef = "fob_" + base64.RawURLEncoding.EncodeToString(random[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for ref, existing := range s.records {
		if !now.Before(existing.ExpiresAt) {
			delete(s.records, ref)
		}
	}
	if len(s.records) >= fileOpenBindingMax {
		return record, errors.New("file open binding store full")
	}
	if _, exists := s.records[record.OpenRef]; exists {
		return record, errors.New("file open binding reference collision")
	}
	s.records[record.OpenRef] = record
	return record, nil
}

// persistenceGate linearizes revoke against save with the same semantics as
// the artifact store: revoke before the gate prevents save; a save already
// inside the gate completes first. TTL is re-checked with a fresh timestamp
// after acquisition. No lookups or external calls may happen under this lock.
func (s *FileOpenBindingStore) persistenceGate(ref string, uid int64, fingerprint [32]byte, now func() time.Time) (func(), bool) {
	s.mu.Lock()
	record, ok := s.records[ref]
	if !ok || record.ActorUID != uid || record.AuthFingerprint != fingerprint || record.Revoked || !now().Before(record.ExpiresAt) {
		s.mu.Unlock()
		return nil, false
	}
	return s.mu.Unlock, true
}

func (s *FileOpenBindingStore) lookup(ref string, uid int64, fingerprint [32]byte, now time.Time, revoke bool) (fileOpenBindingRecord, bool) {
	if s == nil {
		return fileOpenBindingRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[ref]
	if !ok || record.ActorUID != uid || record.AuthFingerprint != fingerprint {
		return fileOpenBindingRecord{}, false
	}
	if !now.Before(record.ExpiresAt) {
		delete(s.records, ref)
		return fileOpenBindingRecord{}, false
	}
	if revoke {
		record.Revoked = true
		s.records[ref] = record
		return record, true
	}
	return record, !record.Revoked
}

// fileAnnotationDescriptor is the hashed canonical descriptor. Fields follow
// the frozen wire contract: name/url/file_key/mime/type/size plus identity.
// Width/height are NOT part of the matching descriptor: the server derives
// them from the real attachment and clients must not be required to echo
// undeclared optionals back.
type fileAnnotationDescriptor struct {
	TopicID         string `json:"topic_id"`
	MessageID       int64  `json:"message_id"`
	AttachmentIndex int64  `json:"attachment_index"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	FileKey         string `json:"file_key,omitempty"`
	MimeType        string `json:"mime_type,omitempty"`
	Type            string `json:"type"`
	Size            int64  `json:"size"`
}

// fileAnnotationDescriptorDigest hashes the canonical JSON of the descriptor,
// never a delimiter-joined string: names and URLs may contain '|'.
func fileAnnotationDescriptorDigest(source fileAnnotationDescriptor) string {
	raw, err := compactGatewayAnnotationsJSON(source)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// canonicalFileAnnotationSource derives the authoritative descriptor from an
// actually stored message attachment, using the exact filtered attachment list
// the agent file panel exposes (content_blocks file|image, else legacy rich
// file|image payload). Index is 0-based over that filtered list.
func canonicalFileAnnotationSource(message *types.Message, attachmentIndex int) (fileAnnotationSource, bool) {
	if message == nil || attachmentIndex < 0 {
		return fileAnnotationSource{}, false
	}
	attachments := fileAttachmentsFromMessage(message)
	if attachmentIndex >= len(attachments) {
		return fileAnnotationSource{}, false
	}
	attachment := attachments[attachmentIndex]
	resource, err := url.Parse(attachment.URL)
	if err != nil || resource.User != nil || attachment.URL == "" {
		return fileAnnotationSource{}, false
	}
	key := attachment.FileKey
	if strings.HasPrefix(resource.Path, "/uploads/files/") || strings.HasPrefix(resource.Path, "/uploads/images/") {
		pathKey := filepath.Base(resource.Path)
		if key != "" && key != pathKey {
			return fileAnnotationSource{}, false
		}
		if key == "" {
			key = pathKey
		}
	}
	source := fileAnnotationSource{
		TopicID:         message.TopicID,
		MessageID:       message.ID,
		AttachmentIndex: int64(attachmentIndex),
		Name:            firstNonEmpty(attachment.Name, channelOutboundFileNameFromURL(attachment.URL), attachment.FileKey, "文件"),
		URL:             attachment.URL,
		FileKey:         key,
		MimeType:        attachment.MimeType,
		Type:            attachment.Type,
		Size:            attachment.Size,
		Width:           attachment.Width,
		Height:          attachment.Height,
	}
	source.Version = fileAnnotationDescriptorDigest(fileAnnotationDescriptor{
		TopicID:         source.TopicID,
		MessageID:       source.MessageID,
		AttachmentIndex: source.AttachmentIndex,
		Name:            source.Name,
		URL:             source.URL,
		FileKey:         source.FileKey,
		MimeType:        source.MimeType,
		Type:            source.Type,
		Size:            source.Size,
	})
	return source, true
}

// fileOpenBindingSubmit is the dedicated handler request.
type fileOpenBindingSubmit struct {
	OpenRef         string               `json:"open_ref"`
	ClientMsgID     string               `json:"client_msg_id"`
	FileAnnotations json.RawMessage      `json:"file_annotations"`
	ContentBlocks   []types.ContentBlock `json:"content_blocks,omitempty"`
}

// FileOpenBindingHandler owns file annotation capabilities and their submit.
type FileOpenBindingHandler struct {
	hub      *Hub
	messages *MessageHandler
	uploads  *UploadHandler
	store    *FileOpenBindingStore
	now      func() time.Time
}

func NewFileOpenBindingHandler(hub *Hub, messages *MessageHandler, uploads *UploadHandler) *FileOpenBindingHandler {
	return &FileOpenBindingHandler{hub: hub, messages: messages, uploads: uploads, store: newFileOpenBindingStore(), now: time.Now}
}

// SetStoreForTest replaces the process-local store; tests only.
func (h *FileOpenBindingHandler) SetStoreForTest(store *FileOpenBindingStore) { h.store = store }

func (h *FileOpenBindingHandler) session(r *http.Request) (*JWTClaims, [32]byte, int, string) {
	var empty [32]byte
	if h == nil || h.hub == nil || h.hub.db == nil {
		return nil, empty, http.StatusServiceUnavailable, "file_open_binding_unavailable"
	}
	claims, status, msg := ownerClaimsFromRequest(r, h.hub.db.GetUser)
	if status != 0 {
		return nil, empty, status, msg
	}
	if claims.UID <= 0 || claims.UID != UIDFromContext(r.Context()) {
		return nil, empty, http.StatusUnauthorized, "unauthorized"
	}
	return claims, sha256.Sum256([]byte(extractToken(r))), 0, ""
}

// HandleOpenBinding mints a parent-only file annotation capability for one real
// persisted message attachment. No gateway registry, no unique-bot check: any
// accessible conversation (p2p or multi-bot group) may be annotated.
func (h *FileOpenBindingHandler) HandleOpenBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	claims, fingerprint, status, msg := h.session(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_request_invalid"})
		return
	}
	if len(body) > 16<<10 {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file_request_too_large"})
		return
	}
	var request struct {
		TopicID         string `json:"topic_id"`
		MessageID       int64  `json:"message_id"`
		AttachmentIndex int64  `json:"attachment_index"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_request_invalid"})
		return
	}
	topicID := strings.TrimSpace(request.TopicID)
	if request.MessageID <= 0 || request.AttachmentIndex < 0 || !validCanonicalTopicID(topicID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_request_invalid"})
		return
	}
	// Publish permission (membership, mute, channel-managed group isolation,
	// p2p friend/owner access) without applying the rate limit.
	if code, text := h.hub.validateMessagePublish(claims.UID, types.AccountHuman, topicID, false); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return
	}
	if code, text := h.hub.validateTopicReadAccess(claims.UID, types.AccountHuman, topicID); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return
	}
	if h.hub.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file_open_binding_unavailable"})
		return
	}
	// Re-resolve the real persisted message with the existing bounded store
	// query (no new SQL adapter surface, no unchecked type assertion).
	messages, err := h.hub.db.GetMessagesSince(topicID, request.MessageID-1, 1)
	if err != nil || len(messages) != 1 || messages[0] == nil || messages[0].ID != request.MessageID || messages[0].TopicID != topicID {
		// Indistinguishable from no attachment: do not leak message existence.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file_source_not_found"})
		return
	}
	source, ok := canonicalFileAnnotationSource(messages[0], int(request.AttachmentIndex))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file_source_not_found"})
		return
	}
	expires := h.now().UTC().Add(fileOpenBindingTTL)
	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(expires) {
		expires = claims.ExpiresAt.Time.UTC()
	}
	record, err := h.store.issue(fileOpenBindingRecord{
		ContractVersion: FileOpenBindingContractV1,
		TopicID:         topicID,
		ExpiresAt:       expires,
		Source:          source,
		ActorUID:        claims.UID,
		AuthFingerprint: fingerprint,
	}, h.now())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file_open_binding_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"contract_version": record.ContractVersion,
		"open_ref":         record.OpenRef,
		"topic_id":         record.TopicID,
		"expires_at":       record.ExpiresAt.Format(time.RFC3339),
		"source":           record.Source,
	})
}

// HandleAnnotations accepts a file annotation submit bound to a real attachment.
func (h *FileOpenBindingHandler) HandleAnnotations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	claims, fingerprint, status, msg := h.session(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_request_invalid"})
		return
	}
	if len(body) > 64<<10 {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file_request_too_large"})
		return
	}
	var request fileOpenBindingSubmit
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_request_invalid"})
		return
	}
	record, ok := h.store.lookup(request.OpenRef, claims.UID, fingerprint, h.now(), false)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_open_binding_invalid", "code": "file_open_binding_invalid"})
		return
	}
	if strings.TrimSpace(request.ClientMsgID) == "" || len(request.ClientMsgID) > 128 || containsControlCharacter(request.ClientMsgID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_request_invalid"})
		return
	}
	// Re-check current permissions against the bound original topic (never the
	// caller's current UI topic), then resolve the actual stored message and
	// require the descriptor to still match byte-for-byte on declared fields.
	if code, text := h.hub.validateMessagePublish(claims.UID, types.AccountHuman, record.TopicID, false); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return
	}
	// Re-resolve the real persisted message with the existing bounded store
	// query (no new SQL adapter surface, no unchecked type assertion).
	messages, err := h.hub.db.GetMessagesSince(record.TopicID, record.Source.MessageID-1, 1)
	if err != nil || len(messages) != 1 || messages[0] == nil || messages[0].ID != record.Source.MessageID || messages[0].TopicID != record.TopicID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_open_binding_invalid", "code": "file_open_binding_invalid"})
		return
	}
	current, hasCurrent := canonicalFileAnnotationSource(messages[0], int(record.Source.AttachmentIndex))
	if !hasCurrent || current.Version != record.Source.Version {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_source_changed", "code": "file_source_changed"})
		return
	}
	// Declared-field equality is enforced server-side below: the client's
	// width/height (undeclared on the wire) never gate acceptance.
	if !fileAnnotationSourceDeclaredMatch(record.Source, current) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_source_changed", "code": "file_source_changed"})
		return
	}
	document, err := normalizeFileAnnotations(request.FileAnnotations)
	if err != nil || document == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_annotations_invalid"})
		return
	}
	if !fileAnnotationSourceDeclaredMatch(document.Source, record.Source) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_source_mismatch", "code": "file_source_mismatch"})
		return
	}
	if !isLowerHex64(document.Source.Version) || document.Source.Version != record.Source.Version {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_source_mismatch", "code": "file_source_mismatch"})
		return
	}
	for index := range document.Annotations {
		if (document.Annotations[index].Kind == "text" || document.Annotations[index].Kind == "code") && h.uploads != nil {
			if err := h.uploads.verifyFileTextQuote(document.Source, document.Annotations[index].Target); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_annotations_quote_invalid"})
				return
			}
		}
	}
	validator := &ArtifactOpenBindingHandler{uploads: h.uploads}
	blocks, err := validator.validatedScreenshotBlocks(claims.UID, request.ContentBlocks)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_screenshots_invalid"})
		return
	}
	_, metadataStore := h.hub.db.(store.MessageMetadataStore)
	if h.messages == nil || h.messages.hub != h.hub || h.messages.db == nil || !metadataStore {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file_open_binding_unavailable"})
		return
	}
	if _, ok := h.store.lookup(request.OpenRef, claims.UID, fingerprint, h.now(), false); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_open_binding_invalid", "code": "file_open_binding_invalid"})
		return
	}
	document.Source = current
	if document.CreatedAt.IsZero() {
		document.CreatedAt = h.now().UTC()
	}
	value, err := document.restamped()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_annotations_invalid"})
		return
	}
	content := fileAnnotationsHumanContent(document)
	// Derive every text block on the server; client blocks contribute only
	// validated owned images, never independent text or structured mentions.
	images := make([]types.ContentBlock, 0, 2)
	for _, block := range blocks {
		if block.Type == "image" {
			block.Payload["name"] = "file-" + block.Payload["screenshot_role"].(string) + ".jpg"
			images = append(images, block)
		}
	}
	blocks = nil
	if len(images) != 0 {
		blocks = append([]types.ContentBlock{{Type: "text", Text: content}}, images...)
	}
	ordinary := SendMessageRequest{
		TopicID:                record.TopicID,
		ClientMsgID:            request.ClientMsgID,
		Content:                mustJSONString(content),
		ContentBlocks:          blocks,
		Type:                   "text",
		MsgType:                "text",
		Metadata:               map[string]interface{}{fileAnnotationsMetadataKey: value},
		fileAnnotationsTrusted: true,
	}
	// Ordinary message delivery for multi-bot groups targets every member;
	// annotation deliberately adds no mention, exactly like a normal file
	// comment. The agent-readable summary is resolved per recipient below.
	h.messages.sendMessage(w, r, ordinary, func() (func(), bool) {
		currentClaims, currentFingerprint, status, _ := h.session(r)
		if status != 0 || r.Context().Err() != nil {
			return nil, false
		}
		if code, _ := h.hub.validateMessagePublish(currentClaims.UID, types.AccountHuman, record.TopicID, false); code != 0 {
			return nil, false
		}
		latest, err := h.hub.db.GetMessagesSince(record.TopicID, record.Source.MessageID-1, 1)
		if err != nil || len(latest) != 1 || latest[0].ID != record.Source.MessageID || latest[0].TopicID != record.TopicID {
			return nil, false
		}
		actual, exists := canonicalFileAnnotationSource(latest[0], int(record.Source.AttachmentIndex))
		if !exists || actual.Version != record.Source.Version {
			return nil, false
		}
		return h.store.persistenceGate(request.OpenRef, currentClaims.UID, currentFingerprint, func() time.Time {
			now := h.now()
			if currentClaims.ExpiresAt != nil && !time.Now().Before(currentClaims.ExpiresAt.Time) {
				return record.ExpiresAt
			}
			return now
		})
	})
}

// HandleRevoke revokes the caller's own file annotation capability.
func (h *FileOpenBindingHandler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	claims, fingerprint, status, msg := h.session(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, "/api/files/open-bindings/")
	if _, ok := h.store.lookup(ref, claims.UID, fingerprint, h.now(), true); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "file_open_binding_invalid", "code": "file_open_binding_invalid"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fileAnnotationSourceDeclaredMatch compares only the frozen wire fields.
// Width/height are server-derived from the real attachment and are never
// required of the client, so they do not participate in equality.
func fileAnnotationSourceDeclaredMatch(declared, current fileAnnotationSource) bool {
	return declared.TopicID == current.TopicID &&
		declared.MessageID == current.MessageID &&
		declared.AttachmentIndex == current.AttachmentIndex &&
		declared.Name == current.Name &&
		declared.URL == current.URL &&
		declared.FileKey == current.FileKey &&
		declared.MimeType == current.MimeType &&
		declared.Type == current.Type &&
		declared.Size == current.Size
}

func validCanonicalTopicID(topic string) bool {
	if isGroupTopic(topic) {
		groupID := extractGroupID(topic)
		return groupID > 0 && topic == "grp_"+strconv.FormatInt(groupID, 10)
	}
	low, high, ok := canonicalP2PTopic(topic)
	return ok && low > 0 && high > 0 && topic == p2pTopicID(low, high)
}

func canonicalP2PTopic(topic string) (int64, int64, bool) {
	rest, found := strings.CutPrefix(topic, "p2p_")
	if !found {
		return 0, 0, false
	}
	left, right, found := strings.Cut(rest, "_")
	if !found {
		return 0, 0, false
	}
	low, errLow := strconv.ParseInt(left, 10, 64)
	high, errHigh := strconv.ParseInt(right, 10, 64)
	if errLow != nil || errHigh != nil {
		return 0, 0, false
	}
	return low, high, true
}

func mustJSONString(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(encoded)
}

// fileMessageLookup is intentionally unused: production resolution uses the
// existing bounded GetMessagesSince query so no SQL adapter surface is added.

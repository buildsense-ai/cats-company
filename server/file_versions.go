package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	fileVersionBindingBodyLimit = 32 << 10
	fileVersionKeyLimit         = 255
	fileVersionURLLimit         = 1024
)

type fileVersionBindingRequest struct {
	TopicID               string `json:"topic_id"`
	SourceMessageID       int64  `json:"source_message_id"`
	SourceAttachmentIndex int64  `json:"source_attachment_index"`
	SourceFileKey         string `json:"source_file_key"`
	LatestFileKey         string `json:"latest_file_key"`
	LatestURL             string `json:"latest_url"`
	LatestName            string `json:"latest_name"`
	LatestSize            int64  `json:"latest_size"`
	LatestMimeType        string `json:"latest_mime_type"`
}

// FileVersionBindingHandler persists and resolves the durable source-attachment
// to latest-Office-file pointer. Message attachments remain immutable: this
// table is a separate index, never a rewrite of messages or content_blocks.
type FileVersionBindingHandler struct {
	hub *Hub
}

func NewFileVersionBindingHandler(hub *Hub) *FileVersionBindingHandler {
	return &FileVersionBindingHandler{hub: hub}
}

// Handle owns both routes so method validation and cache headers stay identical.
func (h *FileVersionBindingHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodPost:
		h.handleUpsert(w, r)
	case http.MethodGet:
		h.handleGet(w, r)
	default:
		w.Header().Set("Allow", http.MethodPost+", "+http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (h *FileVersionBindingHandler) session(r *http.Request) (*JWTClaims, int, string) {
	if h == nil || h.hub == nil || h.hub.db == nil {
		return nil, http.StatusServiceUnavailable, "file_version_binding_unavailable"
	}
	claims, status, msg := ownerClaimsFromRequest(r, h.hub.db.GetUser)
	if status != 0 {
		return nil, status, msg
	}
	if claims.UID <= 0 || claims.UID != UIDFromContext(r.Context()) {
		return nil, http.StatusUnauthorized, "unauthorized"
	}
	return claims, 0, ""
}

func (h *FileVersionBindingHandler) bindingStore() (store.FileVersionBindingStore, bool) {
	if h == nil || h.hub == nil || h.hub.db == nil {
		return nil, false
	}
	persistence, ok := h.hub.db.(store.FileVersionBindingStore)
	return persistence, ok
}

func (h *FileVersionBindingHandler) handleUpsert(w http.ResponseWriter, r *http.Request) {
	claims, status, msg := h.session(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, fileVersionBindingBodyLimit+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_version_request_invalid"})
		return
	}
	if len(body) > fileVersionBindingBodyLimit {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file_version_request_too_large"})
		return
	}
	var request fileVersionBindingRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_version_request_invalid"})
		return
	}
	normalizeFileVersionRequest(&request)
	if !validFileVersionRequest(request) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_version_fields_invalid"})
		return
	}
	if code, text := h.hub.validateMessagePublish(claims.UID, types.AccountHuman, request.TopicID, false); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return
	}
	persistence, ok := h.bindingStore()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file_version_binding_unavailable"})
		return
	}
	source, status, msg := h.resolveSource(request.TopicID, request.SourceMessageID, request.SourceAttachmentIndex, request.SourceFileKey)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if !sameFileVersionSource(source, request.SourceFileKey) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "file_source_key_mismatch"})
		return
	}
	binding, err := persistence.UpsertFileVersionBinding(r.Context(), &store.FileVersionBinding{
		TopicID:               request.TopicID,
		SourceMessageID:       request.SourceMessageID,
		SourceAttachmentIndex: request.SourceAttachmentIndex,
		SourceFileKey:         request.SourceFileKey,
		LatestFileKey:         request.LatestFileKey,
		LatestURL:             request.LatestURL,
		LatestName:            request.LatestName,
		LatestSize:            request.LatestSize,
		LatestMimeType:        request.LatestMimeType,
		UpdatedByUID:          claims.UID,
	})
	if err != nil || binding == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file_version_binding_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, binding)
}

func (h *FileVersionBindingHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	claims, status, msg := h.session(r)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	topicID := strings.TrimSpace(r.URL.Query().Get("topic_id"))
	sourceMessageID, errMessage := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("source_message_id")), 10, 64)
	attachmentIndex, errAttachment := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("source_attachment_index")), 10, 64)
	if topicID == "" || errMessage != nil || sourceMessageID <= 0 || errAttachment != nil || attachmentIndex < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_version_query_invalid"})
		return
	}
	if code, text := h.hub.validateTopicReadAccess(claims.UID, types.AccountHuman, topicID); code != 0 {
		writeJSON(w, code, map[string]string{"error": text})
		return
	}
	persistence, ok := h.bindingStore()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file_version_binding_unavailable"})
		return
	}
	if _, status, msg := h.resolveSource(topicID, sourceMessageID, attachmentIndex, ""); status != 0 {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	binding, err := persistence.GetFileVersionBinding(r.Context(), topicID, sourceMessageID, attachmentIndex)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "file_version_binding_unavailable"})
		return
	}
	if binding == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file_version_binding_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, binding)
}

func normalizeFileVersionRequest(request *fileVersionBindingRequest) {
	request.TopicID = strings.TrimSpace(request.TopicID)
	request.SourceFileKey = strings.TrimSpace(request.SourceFileKey)
	request.LatestFileKey = strings.TrimSpace(request.LatestFileKey)
	request.LatestURL = strings.TrimSpace(request.LatestURL)
	request.LatestName = strings.TrimSpace(request.LatestName)
	request.LatestMimeType = strings.TrimSpace(request.LatestMimeType)
}

func validFileVersionRequest(request fileVersionBindingRequest) bool {
	if request.TopicID == "" || request.SourceMessageID <= 0 || request.SourceAttachmentIndex < 0 {
		return false
	}
	if request.SourceFileKey == "" || request.LatestFileKey == "" || request.LatestURL == "" ||
		request.LatestName == "" || request.LatestMimeType == "" || request.LatestSize < 0 {
		return false
	}
	if len(request.SourceFileKey) > fileVersionKeyLimit || len(request.LatestFileKey) > fileVersionKeyLimit ||
		len(request.LatestName) > fileVersionKeyLimit || len(request.LatestMimeType) > fileVersionKeyLimit ||
		len(request.LatestURL) > fileVersionURLLimit {
		return false
	}
	if containsControlCharacter(request.SourceFileKey) || containsControlCharacter(request.LatestFileKey) ||
		containsControlCharacter(request.LatestURL) || containsControlCharacter(request.LatestName) ||
		containsControlCharacter(request.LatestMimeType) {
		return false
	}
	return validOfficeUploadFile(request.LatestFileKey, request.LatestURL)
}

func (h *FileVersionBindingHandler) resolveSource(topicID string, sourceMessageID, attachmentIndex int64, expectedFileKey string) (fileAnnotationSource, int, string) {
	if h == nil || h.hub == nil || h.hub.db == nil {
		return fileAnnotationSource{}, http.StatusServiceUnavailable, "file_version_binding_unavailable"
	}
	messages, err := h.hub.db.GetMessagesSince(topicID, sourceMessageID-1, 1)
	if err != nil {
		return fileAnnotationSource{}, http.StatusServiceUnavailable, "file_version_binding_unavailable"
	}
	if len(messages) != 1 || messages[0] == nil || messages[0].ID != sourceMessageID || messages[0].TopicID != topicID {
		return fileAnnotationSource{}, http.StatusNotFound, "file_source_not_found"
	}
	source, ok := canonicalFileAnnotationSource(messages[0], int(attachmentIndex))
	if !ok {
		return fileAnnotationSource{}, http.StatusNotFound, "file_source_not_found"
	}
	if !validOfficeUploadFile(source.FileKey, source.URL) {
		return fileAnnotationSource{}, http.StatusBadRequest, "file_source_not_office"
	}
	if expectedFileKey != "" && source.FileKey != expectedFileKey {
		return fileAnnotationSource{}, http.StatusConflict, "file_source_key_mismatch"
	}
	return source, 0, ""
}

func sameFileVersionSource(source fileAnnotationSource, fileKey string) bool {
	return source.FileKey != "" && source.FileKey == fileKey
}

func validOfficeUploadFile(fileKey, rawURL string) bool {
	if fileKey == "" || strings.ContainsAny(fileKey, `/\`) {
		return false
	}
	resource, err := url.Parse(rawURL)
	if err != nil || resource.IsAbs() || resource.Host != "" || resource.User != nil ||
		resource.RawQuery != "" || resource.Fragment != "" {
		return false
	}
	if path.Clean(resource.Path) != resource.Path || !strings.HasPrefix(resource.Path, "/uploads/files/") {
		return false
	}
	if path.Base(resource.Path) != fileKey {
		return false
	}
	extension := strings.ToLower(path.Ext(fileKey))
	return extension == ".docx" || extension == ".pptx"
}

// Package server implements Cats Company file upload service.
package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	urlpath "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxUploadSizeMB           = 300
	maxImageSize              = maxUploadSizeMB << 20
	maxFileSize               = maxUploadSizeMB << 20
	uploadDir                 = "uploads"
	rawUploadQueryParam       = "raw"
	rawUploadQueryValue       = "1"
	rawUploadFileNameHeader   = "X-CatsCo-File-Name"
	rawUploadFileSizeHeader   = "X-CatsCo-File-Size"
	uploadIncompleteCode      = "upload_incomplete"
	uploadMetadataInvalidCode = "upload_metadata_invalid"
	uploadTooLargeCode        = "upload_too_large"
)

var allowedImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

var allowedUploadDirs = map[string]bool{
	"images":   true,
	"files":    true,
	"feedback": true,
	"tutorial": true,
}

var uploadFileNamePattern = regexp.MustCompile(`^\d{8}_[a-f0-9]{32}\.[a-z0-9]+$`)

// Allowed image MIME types
var allowedImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

func isAllowedImageContentType(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return allowedImageTypes[strings.ToLower(mediaType)]
}

// Allowed file extensions (whitelist)
var allowedFileExts = map[string]bool{
	".txt": true, ".pdf": true, ".doc": true, ".docx": true,
	".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".zip": true, ".rar": true, ".7z": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".mp3": true, ".mp4": true, ".webm": true, ".ogg": true, ".ogv": true,
	".m4v": true, ".mov": true, ".wav": true,
	".csv": true, ".json": true, ".xml": true,
	".html": true, ".htm": true,
	".md": true, ".go": true, ".py": true, ".js": true,
}

// UploadHandler handles file upload requests.
type UploadHandler struct {
	baseDir        string
	baseURL        string
	mobileSessions map[string]*mobileUploadSession
	mobileMu       sync.Mutex
}

type mobileUploadSession struct {
	ID        string          `json:"session_id"`
	Topic     string          `json:"topic"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	Files     []uploadPayload `json:"files"`
}

type uploadPayload struct {
	FileKey  string `json:"file_key"`
	URL      string `json:"url"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
	MimeType string `json:"mime_type"`
}

// NewUploadHandler creates a new UploadHandler.
func NewUploadHandler(baseDir, baseURL string) *UploadHandler {
	os.MkdirAll(filepath.Join(baseDir, "images"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "files"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "feedback"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "tutorial"), 0755)
	return &UploadHandler{
		baseDir:        baseDir,
		baseURL:        baseURL,
		mobileSessions: make(map[string]*mobileUploadSession),
	}
}

// HandleUpload handles POST /api/upload
func (h *UploadHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeUploadJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// Parse multipart form
	uploadType := r.URL.Query().Get("type") // "image" or "file"
	maxSize := maxFileSize
	isImageUpload := uploadType == "image" || uploadType == "feedback"
	if isImageUpload {
		maxSize = maxImageSize
	}
	if r.URL.Query().Get(rawUploadQueryParam) == rawUploadQueryValue {
		if payload, ok := h.receiveRawUpload(w, r, uploadType, maxSize, isImageUpload); ok {
			writeUploadJSON(w, http.StatusOK, payload)
		}
		return
	}

	if !parseUploadMultipart(w, r, maxSize) {
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "no file provided"})
		return
	}
	defer file.Close()

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if isImageUpload && !allowedImageExts[ext] {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image type"})
		return
	}
	if !isImageUpload && !allowedFileExts[ext] {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "file type not allowed"})
		return
	}

	// For images, also validate MIME type
	if isImageUpload {
		contentType := header.Header.Get("Content-Type")
		if !isAllowedImageContentType(contentType) {
			writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image type"})
			return
		}
	}

	// Preserve the audio/video distinction for Ogg containers in the stored key.
	storedExt, mimeType := normalizedUploadMetadata(ext, header.Header.Get("Content-Type"), file)
	fileKey := generateFileKey(storedExt)
	subDir := "files"
	if uploadType == "image" {
		subDir = "images"
	} else if uploadType == "feedback" {
		subDir = "feedback"
	}

	destPath := filepath.Join(h.baseDir, subDir, fileKey)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}

	dest, err := os.Create(destPath)
	if err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}
	defer dest.Close()

	written, err := io.Copy(dest, file)
	if err != nil {
		os.Remove(destPath)
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}

	url := fmt.Sprintf("%s/%s/%s", h.baseURL, subDir, fileKey)

	writeUploadJSON(w, http.StatusOK, uploadPayload{
		FileKey:  fileKey,
		URL:      url,
		Name:     header.Filename,
		Size:     written,
		Type:     uploadType,
		MimeType: mimeType,
	})
}

// HandleMobileUploadSession handles short-lived QR upload sessions.
func (h *UploadHandler) HandleMobileUploadSession(w http.ResponseWriter, r *http.Request) {
	basePath := "/api/mobile-upload/sessions"
	if r.URL.Path == basePath {
		if r.Method != http.MethodPost {
			writeUploadJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handleCreateMobileUploadSession(w, r)
		return
	}

	if !strings.HasPrefix(r.URL.Path, basePath+"/") {
		writeUploadJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, basePath+"/")
	sessionID := rest
	isFileUpload := false
	if strings.HasSuffix(rest, "/files") {
		sessionID = strings.TrimSuffix(rest, "/files")
		isFileUpload = true
	}
	if sessionID == "" {
		writeUploadJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	if isFileUpload {
		if r.Method != http.MethodPost {
			writeUploadJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handleMobileUploadFile(w, r, sessionID)
		return
	}

	if r.Method != http.MethodGet {
		writeUploadJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	h.handleGetMobileUploadSession(w, r, sessionID)
}

func (h *UploadHandler) handleCreateMobileUploadSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic string `json:"topic"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sessionID := generateSessionID()
	now := time.Now().UTC()
	session := &mobileUploadSession{
		ID:        sessionID,
		Topic:     strings.TrimSpace(req.Topic),
		CreatedAt: now,
		ExpiresAt: now.Add(30 * time.Minute),
		Files:     []uploadPayload{},
	}

	h.mobileMu.Lock()
	h.mobileSessions[sessionID] = session
	h.mobileMu.Unlock()

	uploadPath := "/mobile-upload/" + sessionID
	apiUploadPath := "/api/mobile-upload/sessions/" + sessionID + "/files"
	uploadURL := uploadPath
	if baseURL := mobileUploadBaseURL(r); baseURL != "" {
		uploadURL = strings.TrimRight(baseURL, "/") + uploadPath
	}

	writeUploadJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":              sessionID,
		"topic":                   session.Topic,
		"upload_url":              uploadURL,
		"relative_upload_url":     uploadPath,
		"api_upload_url":          apiUploadPath,
		"relative_api_upload_url": apiUploadPath,
		"expires_at":              session.ExpiresAt,
	})
}

func mobileUploadBaseURL(r *http.Request) string {
	if configured := strings.TrimSpace(os.Getenv("CATSCO_MOBILE_UPLOAD_BASE_URL")); configured != "" {
		return strings.TrimRight(configured, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		scheme = forwardedProto
	}
	host := strings.TrimSpace(r.Host)
	if forwardedHost := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func firstForwardedValue(value string) string {
	if value == "" {
		return ""
	}
	parts := strings.Split(value, ",")
	return strings.TrimSpace(parts[0])
}

func (h *UploadHandler) handleGetMobileUploadSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	session := h.getMobileSession(sessionID)
	if session == nil {
		writeUploadJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	writeUploadJSON(w, http.StatusOK, session)
}

func (h *UploadHandler) handleMobileUploadFile(w http.ResponseWriter, r *http.Request, sessionID string) {
	session := h.getMobileSession(sessionID)
	if session == nil {
		writeUploadJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	uploadType := r.URL.Query().Get("type")
	if uploadType == "" {
		uploadType = "file"
	}
	maxSize := maxFileSize
	isImageUpload := uploadType == "image"
	if isImageUpload {
		maxSize = maxImageSize
	}
	if r.URL.Query().Get(rawUploadQueryParam) == rawUploadQueryValue {
		payload, ok := h.receiveRawUpload(w, r, uploadType, maxSize, isImageUpload)
		if !ok {
			return
		}
		h.mobileMu.Lock()
		if current := h.mobileSessions[sessionID]; current != nil {
			current.Files = append(current.Files, payload)
		}
		h.mobileMu.Unlock()
		writeUploadJSON(w, http.StatusOK, payload)
		return
	}

	if !parseUploadMultipart(w, r, maxSize) {
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "no file provided"})
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if isImageUpload && !allowedImageExts[ext] {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image type"})
		return
	}
	if !isImageUpload && !allowedFileExts[ext] {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "file type not allowed"})
		return
	}
	if isImageUpload {
		contentType := header.Header.Get("Content-Type")
		if !isAllowedImageContentType(contentType) {
			writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image type"})
			return
		}
	}

	storedExt, mimeType := normalizedUploadMetadata(ext, header.Header.Get("Content-Type"), file)
	fileKey := generateFileKey(storedExt)
	subDir := "files"
	if uploadType == "image" {
		subDir = "images"
	}
	destPath := filepath.Join(h.baseDir, subDir, fileKey)
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}
	dest, err := os.Create(destPath)
	if err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}
	defer dest.Close()
	written, err := io.Copy(dest, file)
	if err != nil {
		os.Remove(destPath)
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}

	payload := uploadPayload{
		FileKey:  fileKey,
		URL:      fmt.Sprintf("%s/%s/%s", h.baseURL, subDir, fileKey),
		Name:     header.Filename,
		Size:     written,
		Type:     uploadType,
		MimeType: mimeType,
	}

	h.mobileMu.Lock()
	if current := h.mobileSessions[sessionID]; current != nil {
		current.Files = append(current.Files, payload)
	}
	h.mobileMu.Unlock()

	writeUploadJSON(w, http.StatusOK, payload)
}

func (h *UploadHandler) getMobileSession(sessionID string) *mobileUploadSession {
	h.mobileMu.Lock()
	defer h.mobileMu.Unlock()
	session := h.mobileSessions[sessionID]
	if session == nil {
		return nil
	}
	if time.Now().UTC().After(session.ExpiresAt) {
		delete(h.mobileSessions, sessionID)
		return nil
	}
	copySession := *session
	copySession.Files = append([]uploadPayload(nil), session.Files...)
	return &copySession
}

// HandleServeFile handles GET /uploads/* - serves uploaded files.
func (h *UploadHandler) HandleServeFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	relPath := strings.TrimPrefix(r.URL.Path, "/uploads/")
	cleanPath := urlpath.Clean("/" + relPath)
	parts := strings.Split(strings.TrimPrefix(cleanPath, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}

	subDir, fileName := parts[0], parts[1]
	if !allowedUploadDirs[subDir] || !uploadFileNamePattern.MatchString(fileName) {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	if (subDir == "images" || subDir == "feedback") && !allowedImageExts[ext] {
		http.NotFound(w, r)
		return
	}
	if subDir == "files" && !allowedFileExts[ext] {
		http.NotFound(w, r)
		return
	}

	baseDir, err := filepath.Abs(filepath.Join(h.baseDir, subDir))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fullPath, err := filepath.Abs(filepath.Join(baseDir, fileName))
	if err != nil || !strings.HasPrefix(fullPath, baseDir+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if subDir == "files" {
		forceDownload := r.URL.Query().Get("download") == "1"
		w.Header().Set("Content-Disposition", contentDispositionForUploadFile(fileName, ext, forceDownload))
		if videoMime, ok := inlineVideoMimeType(ext); ok {
			w.Header().Set("Content-Type", videoMime)
		}
		if isHTMLUploadExtension(ext) && !forceDownload {
			// Uploaded HTML may contain active content. Let browsers render it for
			// navigation/preview, but keep it in an opaque sandboxed origin.
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals")
		}
	}
	http.ServeFile(w, r, fullPath)
}

func contentDispositionForUploadFile(fileName, ext string, forceDownload bool) string {
	if forceDownload {
		return "attachment"
	}
	disposition := "attachment"
	if strings.EqualFold(ext, ".pdf") || isHTMLUploadExtension(ext) || isInlineVideoExt(ext) {
		disposition = "inline"
	}
	return fmt.Sprintf("%s; filename=%q", disposition, fileName)
}

func isInlineVideoExt(ext string) bool {
	_, ok := inlineVideoMimeType(ext)
	return ok
}

func inlineVideoMimeType(ext string) (string, bool) {
	switch strings.ToLower(ext) {
	case ".mp4", ".m4v":
		return "video/mp4", true
	case ".webm":
		return "video/webm", true
	case ".ogv":
		return "video/ogg", true
	case ".mov":
		return "video/quicktime", true
	default:
		return "", false
	}
}

func isHTMLUploadExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".html", ".htm":
		return true
	default:
		return false
	}
}

func normalizedUploadMimeType(ext, headerType string) string {
	if videoMime, ok := inlineVideoMimeType(ext); ok {
		return videoMime
	}
	if strings.EqualFold(ext, ".ogg") {
		return "audio/ogg"
	}

	switch strings.ToLower(ext) {
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	}

	if extType := mime.TypeByExtension(strings.ToLower(ext)); extType != "" {
		if mediaType, _, err := mime.ParseMediaType(extType); err == nil && mediaType != "" {
			return mediaType
		}
	}

	if mediaType, _, err := mime.ParseMediaType(headerType); err == nil && mediaType != "" {
		return mediaType
	}

	return "application/octet-stream"
}

func normalizedUploadMetadata(ext, headerType string, file io.ReaderAt) (string, string) {
	storedExt := normalizedUploadExtension(ext, headerType, file)
	return storedExt, normalizedUploadMimeType(storedExt, headerType)
}

func normalizedUploadExtension(ext, headerType string, file io.ReaderAt) string {
	if !strings.EqualFold(ext, ".ogg") {
		return ext
	}
	mediaType, _, err := mime.ParseMediaType(headerType)
	if (err == nil && strings.EqualFold(mediaType, "video/ogg")) || containsTheoraIdentificationHeader(file) {
		return ".ogv"
	}
	return ext
}

func containsTheoraIdentificationHeader(file io.ReaderAt) bool {
	if file == nil {
		return false
	}
	probe := make([]byte, 64<<10)
	n, _ := file.ReadAt(probe, 0)
	return bytes.Contains(probe[:n], []byte("\x80theora"))
}

func (h *UploadHandler) receiveRawUpload(
	w http.ResponseWriter,
	r *http.Request,
	uploadType string,
	maxSize int,
	isImageUpload bool,
) (uploadPayload, bool) {
	encodedName := strings.TrimSpace(r.Header.Get(rawUploadFileNameHeader))
	expectedSize, sizeErr := strconv.ParseInt(strings.TrimSpace(r.Header.Get(rawUploadFileSizeHeader)), 10, 64)
	decodedName, nameErr := url.PathUnescape(encodedName)
	fileName := filepath.Base(strings.ReplaceAll(decodedName, "\\", "/"))
	if encodedName == "" || nameErr != nil || sizeErr != nil || expectedSize < 0 || fileName == "" || fileName == "." {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{
			"code":  uploadMetadataInvalidCode,
			"error": "upload metadata is invalid",
		})
		return uploadPayload{}, false
	}
	if expectedSize > int64(maxSize) || r.ContentLength > int64(maxSize) {
		writeUploadTooLarge(w)
		return uploadPayload{}, false
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	if isImageUpload && !allowedImageExts[ext] {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image type"})
		return uploadPayload{}, false
	}
	if !isImageUpload && !allowedFileExts[ext] {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "file type not allowed"})
		return uploadPayload{}, false
	}
	contentType := r.Header.Get("Content-Type")
	if isImageUpload && !isAllowedImageContentType(contentType) {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image type"})
		return uploadPayload{}, false
	}

	subDir := "files"
	if uploadType == "image" {
		subDir = "images"
	} else if uploadType == "feedback" {
		subDir = "feedback"
	}
	destinationDir := filepath.Join(h.baseDir, subDir)
	if err := os.MkdirAll(destinationDir, 0755); err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return uploadPayload{}, false
	}

	temp, err := os.CreateTemp(destinationDir, ".upload-*")
	if err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return uploadPayload{}, false
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()

	r.Body = http.MaxBytesReader(w, r.Body, int64(maxSize))
	written, copyErr := io.Copy(temp, r.Body)
	if copyErr != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(copyErr, &maxBytesError) {
			writeUploadTooLarge(w)
			return uploadPayload{}, false
		}
		log.Printf("[upload] interrupted raw body path=%q expected_size=%d written=%d user_agent=%q err=%v",
			r.URL.Path, expectedSize, written, r.UserAgent(), copyErr)
		writeUploadIncomplete(w)
		return uploadPayload{}, false
	}
	if written != expectedSize {
		log.Printf("[upload] incomplete raw body path=%q expected_size=%d written=%d user_agent=%q",
			r.URL.Path, expectedSize, written, r.UserAgent())
		writeUploadIncomplete(w)
		return uploadPayload{}, false
	}

	storedExt, mimeType := normalizedUploadMetadata(ext, contentType, temp)
	fileKey := generateFileKey(storedExt)
	destPath := filepath.Join(destinationDir, fileKey)
	if err := temp.Chmod(0644); err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return uploadPayload{}, false
	}
	if err := temp.Close(); err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return uploadPayload{}, false
	}
	if err := os.Rename(tempPath, destPath); err != nil {
		writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return uploadPayload{}, false
	}
	tempPath = ""

	return uploadPayload{
		FileKey:  fileKey,
		URL:      fmt.Sprintf("%s/%s/%s", h.baseURL, subDir, fileKey),
		Name:     fileName,
		Size:     written,
		Type:     uploadType,
		MimeType: mimeType,
	}, true
}

func parseUploadMultipart(w http.ResponseWriter, r *http.Request, maxSize int) bool {
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxSize))
	if err := r.ParseMultipartForm(int64(maxSize)); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeUploadTooLarge(w)
			return false
		}

		log.Printf("[upload] incomplete multipart path=%q content_length=%d user_agent=%q err=%v",
			r.URL.Path, r.ContentLength, r.UserAgent(), err)
		writeUploadIncomplete(w)
		return false
	}
	return true
}

func writeUploadTooLarge(w http.ResponseWriter) {
	writeUploadJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
		"code":        uploadTooLargeCode,
		"error":       fmt.Sprintf("file too large; maximum supported size is %dMB", maxUploadSizeMB),
		"max_size_mb": maxUploadSizeMB,
	})
}

func writeUploadIncomplete(w http.ResponseWriter) {
	writeUploadJSON(w, http.StatusBadRequest, map[string]interface{}{
		"code":      uploadIncompleteCode,
		"error":     "upload request is incomplete; please retry",
		"retryable": true,
	})
}

func generateFileKey(ext string) string {
	b := make([]byte, 16)
	rand.Read(b)
	ts := time.Now().Format("20060102")
	return fmt.Sprintf("%s_%s%s", ts, hex.EncodeToString(b), ext)
}

func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// writeUploadJSON writes a JSON response (local to upload to avoid conflict with friends.go writeJSON).
func writeUploadJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

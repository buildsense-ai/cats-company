// Package server implements Cats Company file upload service.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
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
	uploadPreviewQueryParam   = "preview"
	uploadIncompleteCode      = "upload_incomplete"
	uploadInvalidRequestCode  = "upload_invalid_request"
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
	".m4v": true, ".mov": true, ".wav": true, ".amr": true, ".opus": true,
	".aac": true, ".m4a": true, ".silk": true,
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

// ValidateArtifactSourcePath confirms that path names an existing regular file
// in this instance's uploaded-file storage.
func (h *UploadHandler) ValidateArtifactSourcePath(value string) error {
	const prefix = "/uploads/files/"
	if h == nil || !strings.HasPrefix(value, prefix) {
		return errors.New("artifact source is not an uploaded file")
	}
	fileName := strings.TrimPrefix(value, prefix)
	if !uploadFileNamePattern.MatchString(fileName) || !allowedFileExts[strings.ToLower(filepath.Ext(fileName))] {
		return errors.New("artifact source file key is invalid")
	}

	baseDir, err := filepath.Abs(filepath.Join(h.baseDir, "files"))
	if err != nil {
		return errors.New("artifact upload storage is unavailable")
	}
	fullPath, err := filepath.Abs(filepath.Join(baseDir, fileName))
	if err != nil || !strings.HasPrefix(fullPath, baseDir+string(os.PathSeparator)) {
		return errors.New("artifact source file path is invalid")
	}
	info, err := os.Lstat(fullPath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("artifact source file does not exist")
	}
	realBaseDir, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return errors.New("artifact upload storage is unavailable")
	}
	realPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return errors.New("artifact source file does not exist")
	}
	relative, err := filepath.Rel(realBaseDir, realPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.New("artifact source file escapes upload storage")
	}
	return nil
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

type countingReadCloser struct {
	io.ReadCloser
	bytesRead int64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += int64(n)
	return n, err
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
	return requestOriginFromRequest(r)
}

func requestOriginFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := strings.ToLower(firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))); forwardedProto == "http" || forwardedProto == "https" {
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
	forceDownload := r.URL.Query().Get("download") == "1"
	if !forceDownload && r.URL.Query().Get(uploadPreviewQueryParam) == "1" {
		if subDir != "files" || !isUploadPreviewableExtension(ext) {
			http.NotFound(w, r)
			return
		}
		fileInfo, statErr := os.Stat(fullPath)
		if statErr != nil || !fileInfo.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		serveUploadPreviewPage(w, r, fileName, ext)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	if forceDownload {
		w.Header().Set("Content-Disposition", "attachment")
	}
	if subDir == "files" {
		if !forceDownload {
			w.Header().Set("Content-Disposition", contentDispositionForUploadFile(fileName, ext, false))
		}
		if videoMime, ok := inlineVideoMimeType(ext); ok {
			w.Header().Set("Content-Type", videoMime)
		} else if audioMime, ok := inlineAudioMimeType(ext); ok {
			w.Header().Set("Content-Type", audioMime)
		}
		if isHTMLUploadExtension(ext) && !forceDownload {
			// Uploaded HTML may contain active content. Let browsers render it for
			// navigation/preview, but keep it in an opaque sandboxed origin.
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals")
		}
	}
	http.ServeFile(w, r, fullPath)
}

func serveUploadPreviewPage(w http.ResponseWriter, r *http.Request, fileName, ext string) {
	publicOrigin, err := configuredPublicBaseURL()
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "preview unavailable", http.StatusServiceUnavailable)
		return
	}

	name := sanitizeUploadPreviewName(r.URL.Query().Get("name"))
	if name == "" {
		name = fileName
	}
	kind := "PDF"
	if isHTMLUploadExtension(ext) {
		kind = "HTML"
	}
	isVideo := isInlineVideoExt(ext)
	if isVideo {
		kind = "VIDEO"
	}

	resourceURL := r.URL.Path
	downloadURL := resourceURL + "?download=1"
	pageURL := r.URL.RequestURI()
	ogImageURL := publicOrigin + "/pwa-512x512.png"
	ogVideoURL := publicOrigin + resourceURL
	canonicalURL := publicOrigin + pageURL
	escapedName := html.EscapeString(name)
	escapedKind := html.EscapeString(kind)
	escapedResourceURL := html.EscapeString(resourceURL)
	escapedDownloadURL := html.EscapeString(downloadURL)
	escapedPageURL := html.EscapeString(canonicalURL)
	escapedOGImageURL := html.EscapeString(ogImageURL)
	escapedOGVideoURL := html.EscapeString(ogVideoURL)
	escapedOGType := "website"
	mediaMetadata := ""
	previewElement := fmt.Sprintf(`<iframe src="%s" title="%s"%s></iframe>`,
		escapedResourceURL,
		escapedName,
		htmlAttributeForUploadPreview(ext),
	)
	if isVideo {
		escapedOGType = "video.other"
		videoMime, _ := inlineVideoMimeType(ext)
		mediaMetadata = fmt.Sprintf("  <meta property=\"og:video\" content=\"%s\">\n  <meta property=\"og:video:secure_url\" content=\"%s\">\n  <meta property=\"og:video:type\" content=\"%s\">\n",
			escapedOGVideoURL,
			escapedOGVideoURL,
			html.EscapeString(videoMime),
		)
		previewElement = fmt.Sprintf(`<video controls playsinline preload="metadata" src="%s" aria-label="%s">您的浏览器暂不支持视频播放。</video>`,
			escapedResourceURL,
			escapedName,
		)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-src 'self'; media-src 'self'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <link rel="canonical" href="%s">
  <meta name="description" content="在 CatsCo 中预览 %s 文件。">
  <meta property="og:type" content="%s">
  <meta property="og:site_name" content="CatsCo">
%s  <meta property="og:title" content="%s">
  <meta property="og:description" content="在 CatsCo 中预览 %s 文件。">
  <meta property="og:url" content="%s">
  <meta property="og:image" content="%s">
  <meta property="og:image:type" content="image/png">
  <meta property="og:image:width" content="512">
  <meta property="og:image:height" content="512">
  <meta property="og:image:alt" content="CatsCo 文件预览">
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:title" content="%s">
  <meta name="twitter:description" content="在 CatsCo 中预览 %s 文件。">
  <meta name="twitter:image" content="%s">
  <style>
    :root { color-scheme: light; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; background: #f3f7f5; color: #243a33; }
    main { display: grid; gap: 16px; min-height: 100vh; box-sizing: border-box; padding: 24px; }
    header { display: grid; gap: 4px; }
    h1 { margin: 0; font-size: 20px; line-height: 1.35; overflow-wrap: anywhere; }
    p { margin: 0; color: #60716b; font-size: 14px; }
    iframe, video { display: block; width: 100%%; min-height: min(72vh, 900px); border: 0; border-radius: 12px; background: #fdfefd; }
    video { max-height: min(72vh, 900px); object-fit: contain; }
    nav { display: flex; flex-wrap: wrap; gap: 12px; }
    a { display: inline-flex; align-items: center; min-height: 40px; box-sizing: border-box; padding: 0 16px; border-radius: 10px; background: #fdfefd; color: #176b57; font-weight: 600; text-decoration: none; }
    a[download] { background: #176b57; color: #fdfefd; }
  </style>
</head>
<body>
  <main>
    <header>
      <h1>%s</h1>
      <p>%s 文件预览</p>
    </header>
    %s
    <nav aria-label="文件操作">
      <a href="%s" target="_blank" rel="noopener noreferrer">打开原文件</a>
      <a href="%s" download>下载文件</a>
    </nav>
  </main>
</body>
</html>
`, escapedName, escapedPageURL, escapedKind, escapedOGType, mediaMetadata, escapedName, escapedKind, escapedPageURL, escapedOGImageURL,
		escapedName, escapedKind, escapedOGImageURL, escapedName, escapedKind, previewElement,
		escapedResourceURL, escapedDownloadURL)
}

func htmlAttributeForUploadPreview(ext string) string {
	if isHTMLUploadExtension(ext) {
		return ` sandbox="allow-scripts allow-forms allow-popups allow-modals"`
	}
	return ""
}

func sanitizeUploadPreviewName(value string) string {
	value = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 240 {
		value = string(runes[:240])
	}
	return value
}

func requestAbsoluteURL(path string) (string, error) {
	origin, err := configuredPublicBaseURL()
	if err != nil {
		return "", err
	}
	return origin + path, nil
}

func contentDispositionForUploadFile(fileName, ext string, forceDownload bool) string {
	if forceDownload {
		return "attachment"
	}
	disposition := "attachment"
	if strings.EqualFold(ext, ".pdf") || isHTMLUploadExtension(ext) || isInlineVideoExt(ext) || isInlineAudioExt(ext) {
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

func isInlineAudioExt(ext string) bool {
	_, ok := inlineAudioMimeType(ext)
	return ok
}

func inlineAudioMimeType(ext string) (string, bool) {
	switch strings.ToLower(ext) {
	case ".mp3":
		return "audio/mpeg", true
	case ".ogg":
		return "audio/ogg", true
	case ".wav":
		return "audio/wav", true
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

func isUploadPreviewableExtension(ext string) bool {
	return strings.EqualFold(ext, ".pdf") || isHTMLUploadExtension(ext) || isInlineVideoExt(ext)
}

func normalizedUploadMimeType(ext, headerType string) string {
	if videoMime, ok := inlineVideoMimeType(ext); ok {
		return videoMime
	}
	if audioMime, ok := inlineAudioMimeType(ext); ok {
		return audioMime
	}
	// Opus is intentionally download-only in the web client. Do not let the
	// host MIME table or an upstream audio/ogg response erase its extension
	// distinction, otherwise the client could mistake it for a supported Ogg
	// attachment and render a broken inline player.
	if strings.EqualFold(ext, ".opus") {
		return "audio/opus"
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

	// Preserve channel-provided audio types before consulting the host MIME
	// database. Legacy formats can otherwise be mislabeled by that database.
	if mediaType, _, err := mime.ParseMediaType(headerType); err == nil && strings.HasPrefix(strings.ToLower(mediaType), "audio/") {
		return strings.ToLower(mediaType)
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
	mediaType, _, _ := mime.ParseMediaType(headerType)
	if strings.EqualFold(mediaType, "audio/opus") {
		// The web client intentionally keeps Opus download-only. A channel may
		// label such media as an Ogg file, but retaining .ogg would make the
		// stored URL look previewable and serve it inline.
		return ".opus"
	}
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
		writeUploadMetadataInvalid(w)
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

	declaredContentLength := r.ContentLength
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxSize))
	written, copyErr := io.Copy(temp, r.Body)
	if copyErr != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(copyErr, &maxBytesError) {
			writeUploadTooLarge(w)
			return uploadPayload{}, false
		}
		var pathError *os.PathError
		if errors.As(copyErr, &pathError) {
			log.Printf("[upload] raw storage failure path=%q user_agent=%q err=%v",
				r.URL.Path, r.UserAgent(), copyErr)
			writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
			return uploadPayload{}, false
		}
		log.Printf("[upload] interrupted raw body path=%q expected_size=%d written=%d user_agent=%q err=%v",
			r.URL.Path, expectedSize, written, r.UserAgent(), copyErr)
		writeUploadIncomplete(w)
		return uploadPayload{}, false
	}
	if declaredContentLength >= 0 && written != declaredContentLength {
		if written > declaredContentLength {
			writeUploadMetadataInvalid(w)
			return uploadPayload{}, false
		}
		log.Printf("[upload] incomplete raw content length path=%q content_length=%d written=%d user_agent=%q",
			r.URL.Path, declaredContentLength, written, r.UserAgent())
		writeUploadIncomplete(w)
		return uploadPayload{}, false
	}
	if written != expectedSize {
		if written > expectedSize {
			writeUploadMetadataInvalid(w)
			return uploadPayload{}, false
		}
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
	body := &countingReadCloser{ReadCloser: r.Body}
	r.Body = http.MaxBytesReader(w, body, int64(maxSize))
	if err := r.ParseMultipartForm(int64(maxSize)); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeUploadTooLarge(w)
			return false
		}

		var pathError *os.PathError
		if errors.As(err, &pathError) {
			log.Printf("[upload] multipart storage failure path=%q user_agent=%q err=%v",
				r.URL.Path, r.UserAgent(), err)
			writeUploadJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
			return false
		}

		if isUploadBodyInterrupted(err, r.ContentLength, body.bytesRead) {
			log.Printf("[upload] incomplete multipart path=%q content_length=%d user_agent=%q err=%v",
				r.URL.Path, r.ContentLength, r.UserAgent(), err)
			writeUploadIncomplete(w)
			return false
		}

		log.Printf("[upload] invalid multipart path=%q content_length=%d user_agent=%q err=%v",
			r.URL.Path, r.ContentLength, r.UserAgent(), err)
		writeUploadInvalidRequest(w)
		return false
	}
	return true
}

func isUploadBodyInterrupted(err error, contentLength, bytesRead int64) bool {
	interrupted := errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
	var networkError net.Error
	interrupted = interrupted || errors.As(err, &networkError)
	if !interrupted {
		return false
	}
	if contentLength >= 0 {
		return bytesRead < contentLength
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

func writeUploadMetadataInvalid(w http.ResponseWriter) {
	writeUploadJSON(w, http.StatusBadRequest, map[string]interface{}{
		"code":      uploadMetadataInvalidCode,
		"error":     "upload metadata is invalid",
		"retryable": false,
	})
}

func writeUploadInvalidRequest(w http.ResponseWriter) {
	writeUploadJSON(w, http.StatusBadRequest, map[string]interface{}{
		"code":      uploadInvalidRequestCode,
		"error":     "upload request is invalid",
		"retryable": false,
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

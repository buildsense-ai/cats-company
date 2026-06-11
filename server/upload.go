// Package server implements Cats Company file upload service.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	urlpath "path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxUploadSizeMB = 300
	maxImageSize    = maxUploadSizeMB << 20
	maxFileSize     = maxUploadSizeMB << 20
	uploadDir       = "uploads"
)

var allowedImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

var allowedUploadDirs = map[string]bool{
	"images":   true,
	"files":    true,
	"feedback": true,
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
	".mp3": true, ".mp4": true, ".wav": true,
	".csv": true, ".json": true, ".xml": true,
	".html": true, ".htm": true,
	".md": true, ".go": true, ".py": true, ".js": true,
}

// UploadHandler handles file upload requests.
type UploadHandler struct {
	baseDir string
	baseURL string
}

// NewUploadHandler creates a new UploadHandler.
func NewUploadHandler(baseDir, baseURL string) *UploadHandler {
	os.MkdirAll(filepath.Join(baseDir, "images"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "files"), 0755)
	os.MkdirAll(filepath.Join(baseDir, "feedback"), 0755)
	return &UploadHandler{baseDir: baseDir, baseURL: baseURL}
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

	r.Body = http.MaxBytesReader(w, r.Body, int64(maxSize))
	if err := r.ParseMultipartForm(int64(maxSize)); err != nil {
		writeUploadJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("file too large; maximum supported size is %dMB", maxUploadSizeMB)})
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

	// Generate unique file key
	fileKey := generateFileKey(ext)
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

	writeUploadJSON(w, http.StatusOK, map[string]interface{}{
		"file_key":  fileKey,
		"url":       url,
		"name":      header.Filename,
		"size":      written,
		"type":      uploadType,
		"mime_type": normalizedUploadMimeType(ext, header.Header.Get("Content-Type")),
	})
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
	w.Header().Set("Cache-Control", cacheControlForUpload(subDir))
	if subDir == "files" {
		w.Header().Set("Content-Disposition", contentDispositionForUploadFile(fileName, ext))
	}
	http.ServeFile(w, r, fullPath)
}

func cacheControlForUpload(subDir string) string {
	if subDir == "images" || subDir == "feedback" {
		return "public, max-age=31536000, immutable"
	}
	return "private, max-age=86400"
}

func contentDispositionForUploadFile(fileName, ext string) string {
	disposition := "attachment"
	if strings.EqualFold(ext, ".pdf") {
		disposition = "inline"
	}
	return fmt.Sprintf("%s; filename=%q", disposition, fileName)
}

func normalizedUploadMimeType(ext, headerType string) string {
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

func generateFileKey(ext string) string {
	b := make([]byte, 16)
	rand.Read(b)
	ts := time.Now().Format("20060102")
	return fmt.Sprintf("%s_%s%s", ts, hex.EncodeToString(b), ext)
}

// writeUploadJSON writes a JSON response (local to upload to avoid conflict with friends.go writeJSON).
func writeUploadJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

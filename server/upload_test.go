package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleUploadAllowsHTMLAsFileAttachment(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := buildUploadRequest(t, "/api/upload?type=file", "page.html", []byte("<!doctype html><script>alert(1)</script>"))
	rec := httptest.NewRecorder()

	handler.HandleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		FileKey  string `json:"file_key"`
		URL      string `json:"url"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		MimeType string `json:"mime_type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Name != "page.html" {
		t.Fatalf("name = %q, want page.html", body.Name)
	}
	if body.Type != "file" {
		t.Fatalf("type = %q, want file", body.Type)
	}
	if !strings.HasSuffix(body.FileKey, ".html") {
		t.Fatalf("file_key = %q, want .html suffix", body.FileKey)
	}
	if !strings.HasPrefix(body.URL, "/uploads/files/") {
		t.Fatalf("url = %q, want /uploads/files prefix", body.URL)
	}
	if body.MimeType != "text/html" {
		t.Fatalf("mime_type = %q, want text/html", body.MimeType)
	}
}

func TestHandleUploadAllowsWebMVideoAttachment(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := buildUploadRequest(t, "/api/upload?type=file", "demo.webm", []byte("webm video bytes"))
	rec := httptest.NewRecorder()

	handler.HandleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		FileKey  string `json:"file_key"`
		MimeType string `json:"mime_type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasSuffix(body.FileKey, ".webm") {
		t.Fatalf("file_key = %q, want .webm suffix", body.FileKey)
	}
	if body.MimeType != "video/webm" {
		t.Fatalf("mime_type = %q, want video/webm", body.MimeType)
	}
}

func TestHandleUploadKeepsOggAudioAsAttachment(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := buildUploadRequestWithPartContentType(t, "/api/upload?type=file", "demo.ogg", "audio/ogg", []byte("ogg audio bytes"))
	rec := httptest.NewRecorder()

	handler.HandleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		FileKey  string `json:"file_key"`
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if !strings.HasSuffix(body.FileKey, ".ogg") {
		t.Fatalf("file_key = %q, want .ogg suffix", body.FileKey)
	}
	if body.MimeType != "audio/ogg" {
		t.Fatalf("mime_type = %q, want audio/ogg", body.MimeType)
	}

	serveRec := httptest.NewRecorder()
	handler.HandleServeFile(serveRec, httptest.NewRequest(http.MethodGet, body.URL, nil))
	if serveRec.Code != http.StatusOK {
		t.Fatalf("serve status = %d, want %d", serveRec.Code, http.StatusOK)
	}
	if got := serveRec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("content-disposition = %q, want attachment", got)
	}
}

func TestHandleUploadNormalizesOggVideoToOGV(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := buildUploadRequestWithPartContentType(t, "/api/upload?type=file", "demo.ogg", "video/ogg", []byte("ogg video bytes"))
	rec := httptest.NewRecorder()

	handler.HandleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		FileKey  string `json:"file_key"`
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if !strings.HasSuffix(body.FileKey, ".ogv") {
		t.Fatalf("file_key = %q, want .ogv suffix", body.FileKey)
	}
	if !strings.HasSuffix(body.URL, ".ogv") {
		t.Fatalf("url = %q, want .ogv suffix", body.URL)
	}
	if body.MimeType != "video/ogg" {
		t.Fatalf("mime_type = %q, want video/ogg", body.MimeType)
	}

	serveRec := httptest.NewRecorder()
	handler.HandleServeFile(serveRec, httptest.NewRequest(http.MethodGet, body.URL, nil))
	if serveRec.Code != http.StatusOK {
		t.Fatalf("serve status = %d, want %d", serveRec.Code, http.StatusOK)
	}
	if got := serveRec.Header().Get("Content-Type"); got != "video/ogg" {
		t.Fatalf("content-type = %q, want video/ogg", got)
	}
	if got := serveRec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Fatalf("content-disposition = %q, want inline", got)
	}
}

func TestHandleUploadDetectsTheoraVideoWithAmbiguousOggMime(t *testing.T) {
	for _, contentType := range []string{"", "application/ogg"} {
		t.Run(contentType, func(t *testing.T) {
			handler := NewUploadHandler(t.TempDir(), "/uploads")
			req := buildUploadRequestWithPartContentType(
				t,
				"/api/upload?type=file",
				"demo.ogg",
				contentType,
				[]byte("OggS\x00\x02theora stream \x80theora"),
			)
			rec := httptest.NewRecorder()

			handler.HandleUpload(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("upload status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			var body struct {
				FileKey  string `json:"file_key"`
				MimeType string `json:"mime_type"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode upload response: %v", err)
			}
			if !strings.HasSuffix(body.FileKey, ".ogv") {
				t.Fatalf("file_key = %q, want .ogv suffix", body.FileKey)
			}
			if body.MimeType != "video/ogg" {
				t.Fatalf("mime_type = %q, want video/ogg", body.MimeType)
			}
		})
	}
}

func TestHandleUploadKeepsAmbiguousVorbisOggAsAudio(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := buildUploadRequestWithPartContentType(
		t,
		"/api/upload?type=file",
		"recording.ogg",
		"application/ogg",
		[]byte("OggS\x00\x02audio stream \x01vorbis"),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		FileKey  string `json:"file_key"`
		MimeType string `json:"mime_type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if !strings.HasSuffix(body.FileKey, ".ogg") {
		t.Fatalf("file_key = %q, want .ogg suffix", body.FileKey)
	}
	if body.MimeType != "audio/ogg" {
		t.Fatalf("mime_type = %q, want audio/ogg", body.MimeType)
	}
}

func TestHandleServeFileAllowsGeneratedFeedbackImage(t *testing.T) {
	dir := t.TempDir()
	fileName := "20260428_0123456789abcdef0123456789abcdef.png"
	fullPath := filepath.Join(dir, "feedback", fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("fake image"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewUploadHandler(dir, "/uploads")
	req := httptest.NewRequest(http.MethodGet, "/uploads/feedback/"+fileName, nil)
	rec := httptest.NewRecorder()

	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestHandleServeFileDisablesHTTPCaching(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		subDir   string
		fileName string
	}{
		{name: "image", subDir: "images", fileName: "20260428_0123456789abcdef0123456789abcdef.png"},
		{name: "feedback", subDir: "feedback", fileName: "20260428_0123456789abcdef0123456789abcdef.png"},
		{name: "file", subDir: "files", fileName: "20260428_0123456789abcdef0123456789abcdef.pdf"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			fullPath := filepath.Join(dir, testCase.subDir, testCase.fileName)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fullPath, []byte("private upload"), 0644); err != nil {
				t.Fatal(err)
			}

			handler := NewUploadHandler(dir, "/uploads")
			recorder := httptest.NewRecorder()
			handler.HandleServeFile(
				recorder,
				httptest.NewRequest(http.MethodGet, "/uploads/"+testCase.subDir+"/"+testCase.fileName, nil),
			)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestHandleServeFileServesHTMLFilesInlineWithSandbox(t *testing.T) {
	for _, ext := range []string{".html", ".htm"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			fileName := "20260428_0123456789abcdef0123456789abcdef" + ext
			fullPath := filepath.Join(dir, "files", fileName)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fullPath, []byte("<!doctype html><script>alert(1)</script>"), 0644); err != nil {
				t.Fatal(err)
			}

			handler := NewUploadHandler(dir, "/uploads")
			req := httptest.NewRequest(http.MethodGet, "/uploads/files/"+fileName, nil)
			rec := httptest.NewRecorder()

			handler.HandleServeFile(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
				t.Fatalf("Content-Disposition = %q, want inline", got)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts allow-forms allow-popups allow-modals" {
				t.Fatalf("Content-Security-Policy = %q, want sandboxed HTML", got)
			}
		})
	}
}

func TestHandleServeFileForcesHTMLDownloadWithoutSandbox(t *testing.T) {
	dir := t.TempDir()
	fileName := "20260428_0123456789abcdef0123456789abcdef.html"
	fullPath := filepath.Join(dir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("<!doctype html><h1>Report</h1>"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewUploadHandler(dir, "/uploads")
	req := httptest.NewRequest(http.MethodGet, "/uploads/files/"+fileName+"?download=1", nil)
	rec := httptest.NewRecorder()

	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment" {
		t.Fatalf("Content-Disposition = %q, want attachment", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("Content-Security-Policy = %q, want empty for downloads", got)
	}
}

func TestHandleServeFileForcesDownloadWithoutFilenameInURL(t *testing.T) {
	dir := t.TempDir()
	fileName := "20260715_f547bf132d510e621877d89214098db5.pdf"
	fullPath := filepath.Join(dir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("%PDF-1.7\n"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewUploadHandler(dir, "/uploads")
	req := httptest.NewRequest(http.MethodGet, "/uploads/files/"+fileName+"?download=1", nil)
	rec := httptest.NewRecorder()
	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment" {
		t.Fatalf("Content-Disposition = %q, want attachment without a server-side filename", got)
	}
}

func TestHandleServeFileServesPDFFilesInline(t *testing.T) {
	dir := t.TempDir()
	fileName := "20260428_0123456789abcdef0123456789abcdef.pdf"
	fullPath := filepath.Join(dir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("%PDF-1.7\n"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewUploadHandler(dir, "/uploads")
	req := httptest.NewRequest(http.MethodGet, "/uploads/files/"+fileName, nil)
	rec := httptest.NewRecorder()

	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
}

func TestHandleServeFileServesMP4FilesInlineWithRangeSupport(t *testing.T) {
	dir := t.TempDir()
	fileName := "20260727_0123456789abcdef0123456789abcdef.mp4"
	fullPath := filepath.Join(dir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	videoBytes := []byte("0123456789abcdef")
	if err := os.WriteFile(fullPath, videoBytes, 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewUploadHandler(dir, "/uploads")
	url := "/uploads/files/" + fileName

	rec := httptest.NewRecorder()
	handler.HandleServeFile(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), videoBytes) {
		t.Fatalf("body = %q, want %q", rec.Body.Bytes(), videoBytes)
	}

	rangeReq := httptest.NewRequest(http.MethodGet, url, nil)
	rangeReq.Header.Set("Range", "bytes=0-3")
	rangeRec := httptest.NewRecorder()
	handler.HandleServeFile(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want %d", rangeRec.Code, http.StatusPartialContent)
	}
	if got := rangeRec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if got := rangeRec.Header().Get("Content-Range"); got != "bytes 0-3/16" {
		t.Fatalf("Content-Range = %q, want bytes 0-3/16", got)
	}
	if got := rangeRec.Body.String(); got != "0123" {
		t.Fatalf("range body = %q, want 0123", got)
	}

	downloadRec := httptest.NewRecorder()
	handler.HandleServeFile(downloadRec, httptest.NewRequest(http.MethodGet, url+"?download=1", nil))
	if got := downloadRec.Header().Get("Content-Disposition"); got != "attachment" {
		t.Fatalf("download Content-Disposition = %q, want attachment", got)
	}
}

func TestHandleServeFileServesBrowserVideoFormatsInline(t *testing.T) {
	testCases := []struct {
		ext      string
		mimeType string
	}{
		{ext: ".webm", mimeType: "video/webm"},
		{ext: ".ogv", mimeType: "video/ogg"},
		{ext: ".m4v", mimeType: "video/mp4"},
		{ext: ".mov", mimeType: "video/quicktime"},
	}

	for _, tc := range testCases {
		t.Run(tc.ext, func(t *testing.T) {
			dir := t.TempDir()
			fileName := "20260727_0123456789abcdef0123456789abcdef" + tc.ext
			fullPath := filepath.Join(dir, "files", fileName)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fullPath, []byte("video bytes"), 0644); err != nil {
				t.Fatal(err)
			}

			handler := NewUploadHandler(dir, "/uploads")
			rec := httptest.NewRecorder()
			handler.HandleServeFile(rec, httptest.NewRequest(http.MethodGet, "/uploads/files/"+fileName, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.mimeType {
				t.Fatalf("Content-Type = %q, want %q", got, tc.mimeType)
			}
			if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "inline") {
				t.Fatalf("Content-Disposition = %q, want inline", got)
			}
		})
	}
}

func TestHandleServeFileServesOggFilesAsAttachments(t *testing.T) {
	dir := t.TempDir()
	fileName := "20260727_0123456789abcdef0123456789abcdef.ogg"
	fullPath := filepath.Join(dir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("ogg bytes"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewUploadHandler(dir, "/uploads")
	rec := httptest.NewRecorder()
	handler.HandleServeFile(rec, httptest.NewRequest(http.MethodGet, "/uploads/files/"+fileName, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/ogg" {
		t.Fatalf("Content-Type = %q, want audio/ogg", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("Content-Disposition = %q, want attachment", got)
	}
}

func TestHandleServeFileServesDOCXFilesAsAttachments(t *testing.T) {
	dir := t.TempDir()
	fileName := "20260428_0123456789abcdef0123456789abcdef.docx"
	fullPath := filepath.Join(dir, "files", fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("fake docx bytes"), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewUploadHandler(dir, "/uploads")
	req := httptest.NewRequest(http.MethodGet, "/uploads/files/"+fileName, nil)
	rec := httptest.NewRecorder()

	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("Content-Disposition = %q, want attachment", got)
	}
}

func TestHandleServeFileRejectsUnexpectedDirectory(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := httptest.NewRequest(http.MethodGet, "/uploads/secrets/20260428_0123456789abcdef0123456789abcdef.png", nil)
	rec := httptest.NewRecorder()

	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleServeFileRejectsNonGeneratedName(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := httptest.NewRequest(http.MethodGet, "/uploads/feedback/manual.png", nil)
	rec := httptest.NewRecorder()

	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleServeFileRejectsMutationMethods(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := httptest.NewRequest(http.MethodPost, "/uploads/feedback/20260428_0123456789abcdef0123456789abcdef.png", nil)
	rec := httptest.NewRecorder()

	handler.HandleServeFile(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleUploadAllowsImageContentTypeWithParameters(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := buildUploadRequestWithPartContentType(t, "/api/upload?type=image", "photo.jpg", "image/jpeg; charset=utf-8", []byte("fake image bytes"))
	rec := httptest.NewRecorder()

	handler.HandleUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandleUploadRejectsUnsupportedImageMimeType(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")
	req := buildUploadRequestWithPartContentType(t, "/api/upload?type=image", "photo.jpg", "image/svg+xml", []byte("fake image bytes"))
	rec := httptest.NewRecorder()

	handler.HandleUpload(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid image type") {
		t.Fatalf("body = %q, want invalid image type", rec.Body.String())
	}
}

func TestMobileUploadSessionAcceptsPhoneUploadsAndListsFiles(t *testing.T) {
	handler := NewUploadHandler(t.TempDir(), "/uploads")

	createReq := httptest.NewRequest(http.MethodPost, "/api/mobile-upload/sessions", strings.NewReader(`{"topic":"p2p_1_2"}`))
	createRec := httptest.NewRecorder()
	handler.HandleMobileUploadSession(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", createRec.Code, createRec.Body.String())
	}

	var created struct {
		SessionID    string `json:"session_id"`
		UploadURL    string `json:"upload_url"`
		APIUploadURL string `json:"api_upload_url"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.SessionID == "" || !strings.Contains(created.UploadURL, created.SessionID) || !strings.Contains(created.APIUploadURL, created.SessionID) {
		t.Fatalf("unexpected create response: %+v", created)
	}
	if len(created.SessionID) < 32 {
		t.Fatalf("session id length = %d, want at least 32 hex chars", len(created.SessionID))
	}

	uploadReq := buildUploadRequestWithPartContentType(t, "/api/mobile-upload/sessions/"+created.SessionID+"/files?type=image", "paper.jpg", "image/jpeg", []byte("fake paper image"))
	uploadRec := httptest.NewRecorder()
	handler.HandleMobileUploadSession(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body=%s", uploadRec.Code, uploadRec.Body.String())
	}

	videoUploadReq := buildUploadRequestWithPartContentType(
		t,
		"/api/mobile-upload/sessions/"+created.SessionID+"/files?type=file",
		"demo.ogg",
		"application/ogg",
		[]byte("OggS\x00\x02theora stream \x80theora"),
	)
	videoUploadRec := httptest.NewRecorder()
	handler.HandleMobileUploadSession(videoUploadRec, videoUploadReq)
	if videoUploadRec.Code != http.StatusOK {
		t.Fatalf("video upload status = %d, body=%s", videoUploadRec.Code, videoUploadRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/mobile-upload/sessions/"+created.SessionID, nil)
	listRec := httptest.NewRecorder()
	handler.HandleMobileUploadSession(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var listed struct {
		SessionID string `json:"session_id"`
		Topic     string `json:"topic"`
		Files     []struct {
			FileKey  string `json:"file_key"`
			URL      string `json:"url"`
			Name     string `json:"name"`
			Type     string `json:"type"`
			MimeType string `json:"mime_type"`
		} `json:"files"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listed.Topic != "p2p_1_2" {
		t.Fatalf("topic = %q, want p2p_1_2", listed.Topic)
	}
	if len(listed.Files) != 2 {
		t.Fatalf("files = %+v, want two files", listed.Files)
	}
	if listed.Files[0].Name != "paper.jpg" || listed.Files[0].Type != "image" || !strings.HasPrefix(listed.Files[0].URL, "/uploads/images/") {
		t.Fatalf("unexpected file result: %+v", listed.Files[0])
	}
	if listed.Files[1].Name != "demo.ogg" || listed.Files[1].MimeType != "video/ogg" || !strings.HasSuffix(listed.Files[1].FileKey, ".ogv") {
		t.Fatalf("unexpected video result: %+v", listed.Files[1])
	}
}

func buildUploadRequest(t *testing.T, target, fileName string, content []byte) *http.Request {
	t.Helper()
	return buildUploadRequestWithPartContentType(t, target, fileName, "application/octet-stream", content)
}

func buildUploadRequestWithPartContentType(t *testing.T, target, fileName, partContentType string, content []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q; filename=%q", "file", fileName))
	if partContentType != "" {
		headers.Set("Content-Type", partContentType)
	}
	part, err := writer.CreatePart(headers)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

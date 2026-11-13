package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This deliberately locks in the approved security downgrade: legacy URLs are
// readable without persisted-message authorization. Use synthetic data only.
func TestAttachmentPrivacyRollbackLegacyAnd630Reads(t *testing.T) {
	const fixture = "20261112_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.pdf"
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://app.example")
	base := t.TempDir()
	dir := filepath.Join(base, "files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fixture), []byte("synthetic-rollback"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := NewUploadHandler(base, "/uploads")
	for _, tc := range []struct {
		name, method, query, authorization, byteRange string
		status                                        int
		body                                          string
	}{
		{"anonymous", "GET", "", "", "", 200, "synthetic-rollback"},
		{"630-api-key", "GET", "?message_id=42", "ApiKey synthetic", "", 200, "synthetic-rollback"},
		{"630-connector", "GET", "?message_id=42", "DeviceConnector synthetic", "", 200, "synthetic-rollback"},
		{"head", "HEAD", "?message_id=42", "ApiKey synthetic", "", 200, ""},
		{"range", "GET", "?message_id=42", "ApiKey synthetic", "bytes=0-8", 206, "synthetic"},
		{"download", "GET", "?download=1&message_id=42", "ApiKey synthetic", "", 200, "synthetic-rollback"},
		{"preview", "GET", "?preview=1", "", "", 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/uploads/files/"+fixture+tc.query, nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			if tc.byteRange != "" {
				req.Header.Set("Range", tc.byteRange)
			}
			rec := httptest.NewRecorder()
			handler.HandleServeFile(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d", rec.Code, tc.status)
			}
			if tc.name == "preview" {
				if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
					t.Fatal("preview must remain HTML")
				}
			} else if rec.Body.String() != tc.body {
				t.Fatalf("unexpected synthetic body %q", rec.Body.String())
			}
		})
	}
	missing := httptest.NewRecorder()
	handler.HandleServeFile(missing, httptest.NewRequest(http.MethodGet, "/uploads/files/20261112_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.md?message_id=42", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d", missing.Code)
	}
}

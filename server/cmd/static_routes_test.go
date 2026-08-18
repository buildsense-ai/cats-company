package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticRoutesServeAuthenticationPathsAsSPA(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("CatsCo application shell"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	mux := http.NewServeMux()
	registerStaticRoutes(mux, staticDir)

	for _, path := range []string{
		"/e/invite-1",
		"/share",
		"/share/visitor-capability",
		"/mobile-upload/session-1",
		"/login?next=%2Fe%2Finvite-1",
		"/login/",
		"/register",
		"/register/",
		"/reset-password",
		"/reset-password/",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			if recorder.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", path, recorder.Code, http.StatusOK)
			}
			if body := recorder.Body.String(); body != "CatsCo application shell" {
				t.Fatalf("GET %s body = %q, want application shell", path, body)
			}
			if path == "/share" || strings.HasPrefix(path, "/share/") {
				if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
					t.Fatalf("GET %s Cache-Control = %q, want no-store", path, got)
				}
				if got := recorder.Header().Get("Referrer-Policy"); got != "no-referrer" {
					t.Fatalf("GET %s Referrer-Policy = %q, want no-referrer", path, got)
				}
			}
		})
	}
}

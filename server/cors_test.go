package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddlewareAllowsRawUploadHeaders(t *testing.T) {
	nextCalled := false
	handler := CORSMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/upload?type=image&raw=1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if nextCalled {
		t.Fatal("preflight request reached the wrapped handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != allowedCORSHeaders {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, allowedCORSHeaders)
	}
}

func TestCORSMiddlewareAllowsCnMigrationOrigin(t *testing.T) {
	handler := CORSMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Origin", "https://app.catsco.cn")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.catsco.cn" {
		t.Fatalf("allow-origin = %q, want cn origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("vary = %q, want Origin", got)
	}
}

func TestCORSMiddlewareDoesNotReflectUnknownOrigin(t *testing.T) {
	handler := CORSMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.catsco.cc" {
		t.Fatalf("allow-origin = %q, want canonical origin for unknown origin", got)
	}
}

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

func TestCORSMiddlewareAllowsBothPublicOrigins(t *testing.T) {
	for _, origin := range []string{"https://app.catsco.cc", "https://app.catsco.cn"} {
		t.Run(origin, func(t *testing.T) {
			handler := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, origin)
			}
			if got := rec.Header().Get("Vary"); got != "Origin" {
				t.Fatalf("Vary = %q, want Origin", got)
			}
		})
	}
}

func TestCORSMiddlewareRejectsUntrustedOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://evil.example",
		"https://app.catsco.cc.evil.example",
		"http://app.catsco.cc",
		"https://app.catsco.cc:8443",
	} {
		t.Run(origin, func(t *testing.T) {
			handler := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodOptions, "/api/me", nil)
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
			}
		})
	}
}

func TestCORSMiddlewareDoesNotRequireOriginForSameOriginRequests(t *testing.T) {
	nextCalled := false
	handler := CORSMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatal("same-origin request did not reach the wrapped handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

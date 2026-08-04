package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelayAdminConfigFromEnv(t *testing.T) {
	t.Setenv("CATSCO_ADMIN_UID_WHITELIST", "38, 2")
	t.Setenv("CATSCO_RELAY_ADMIN_URL", "http://127.0.0.1:18090")
	cfg := relayAdminConfigFromEnv()
	if !cfg.allows(38) || !cfg.allows(2) || cfg.allows(7) {
		t.Fatalf("whitelist mismatch: %+v", cfg)
	}
	if cfg.relayURL != "http://127.0.0.1:18090" {
		t.Fatalf("relay url: %s", cfg.relayURL)
	}
	t.Setenv("CATSCO_ADMIN_UID_WHITELIST", "")
	if relayAdminConfigFromEnv().allows(38) {
		t.Fatal("empty whitelist must disable access")
	}
	if relayAdminConfigFromEnv().relayURL != "http://127.0.0.1:18090" {
		t.Fatal("default relay url expected")
	}
}

func TestRelayAdminPathWhitelist(t *testing.T) {
	ok := []string{
		"/local/usage-admin",
		"/local/usage-summary?window=24h",
		"/local/usage-users?limit=10",
		"/local/pricing-analytics",
		"/local/pricing-analytics/data?window=24h",
		"/local/pricing-analytics/user?uid=2",
		"/local/pricing-rules",
		"/local/users/38/key/state",
		"/local/users/38/key/limits",
		"/local/users/38/key/usage-reset",
	}
	for _, p := range ok {
		if !relayAdminPathAllowed(p) {
			t.Fatalf("expected allowed: %s", p)
		}
	}
	bad := []string{
		"/internal/usage/users", "/internal/keys/38",
		"/public/me", "/public/session",
		"/health", "/api/foo", "/local/other",
	}
	for _, p := range bad {
		if relayAdminPathAllowed(p) {
			t.Fatalf("expected denied: %s", p)
		}
	}
}

func TestRelayAdminAccessAndProxy(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local/usage-admin":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><a href="/local/usage-summary">s</a><script>fetch('/local/usage-users')</script></body></html>`)
		case "/local/usage-summary":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"model":"gpt-5.6","quota":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer relay.Close()

	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38, 2}})
	h.setRateLimit(1000, 60)

	// access: allowed uid
	req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/access", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleAccess(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"allowed":true`) {
		t.Fatalf("access allowed: %d %s", rec.Code, rec.Body.String())
	}
	// access: denied uid
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/access", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec = httptest.NewRecorder()
	h.HandleAccess(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"allowed":false`) {
		t.Fatalf("access denied: %d %s", rec.Code, rec.Body.String())
	}
	// proxy: whitelisted path, allowed uid, response rewritten
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-admin", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec = httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/api/admin/relay/local/usage-summary") ||
		!strings.Contains(body, "/api/admin/relay/local/usage-users") ||
		strings.Contains(body, `href="/local/usage-summary"`) {
		t.Fatalf("rewrite failed: %s", body)
	}
	// proxy: non-whitelist path
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/internal/usage/users", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec = httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("internal path must be forbidden: %d", rec.Code)
	}
	// proxy: non-whitelist uid
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-admin", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec = httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-whitelist uid must be forbidden: %d", rec.Code)
	}
}

type testWriter struct{ lines *[]string }

func (w testWriter) Write(p []byte) (int, error) {
	*w.lines = append(*w.lines, string(p))
	return len(p), nil
}

func TestRelayAdminRateLimitAndAudit(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	var auditLines []string
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.auditLogger = log.New(testWriter{&auditLines}, "", 0)
	h.setRateLimit(3, 60)

	do := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		h.HandleProxy(rec, req)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		if code := do(); code != http.StatusOK {
			t.Fatalf("req %d status=%d", i, code)
		}
	}
	if code := do(); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", code)
	}
	if len(auditLines) < 4 {
		t.Fatalf("audit missing entries: %d lines %v", len(auditLines), auditLines)
	}
	joined := strings.Join(auditLines, "\n")
	if !strings.Contains(joined, "uid=38") || !strings.Contains(joined, "/local/usage-summary") {
		t.Fatalf("audit content: %s", joined)
	}
}

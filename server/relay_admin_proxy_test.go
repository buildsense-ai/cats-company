package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

type relayAdminManagedBudgetTestStore struct {
	budgets map[int64][]*types.CommercialManagedRelayBudget
	err     error
}

func (s relayAdminManagedBudgetTestStore) ListCommercialManagedRelayBudgets(uid int64) ([]*types.CommercialManagedRelayBudget, error) {
	return s.budgets[uid], s.err
}

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

func TestRelayAdminSessionCookie(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/access", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleAccess(rec, req)
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == relayAdminCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.Path != "/api/admin/relay/" {
		t.Fatalf("expected scoped HttpOnly cookie, got %+v", rec.Header())
	}

	// Proxy with the cookie, no context uid -> allowed.
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie proxy status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Tampered cookie -> rejected.
	tampered := &http.Cookie{Name: relayAdminCookieName, Value: sessionCookie.Value + "x"}
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
	req.AddCookie(tampered)
	rec = httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered cookie status=%d", rec.Code)
	}
}

func TestRelayAdminUnauthorized(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without any credential, got %d", rec.Code)
	}
}

func TestRelayAdminEncodedPathRejected(t *testing.T) {
	for _, p := range []string{
		"/local/usage-admin%2e%2e%2fetc",
		"/local/usage-admin/%2e%2e/x",
		"/local/usage-users%2f..%2f",
		"/local%5c..%5cusers",
		"/local/usage-admin%00x",
	} {
		if relayAdminPathAllowed(p) {
			t.Fatalf("encoded path must be rejected: %s", p)
		}
	}
	// Proxy-level 403 for encoded traversal.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-admin%2e%2e%2fetc", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("encoded traversal proxy status=%d", rec.Code)
	}
}

func TestRelayAdminPricingWriteMarker(t *testing.T) {
	var sawMarker bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local/pricing-rules" && r.Method == http.MethodPost {
			sawMarker = r.Header.Get("X-Cats-Relay-Local-Write") == "pricing-rules"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/relay/local/pricing-rules", strings.NewReader(`{}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pricing write status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !sawMarker {
		t.Fatal("relay did not receive the local pricing write marker")
	}
}

func TestRelayAdminLimitsWritePreservesBody(t *testing.T) {
	const payload = `{"monthly_budget":321,"provider_configs":[]}`
	var received string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/relay/local/users/7/key/limits", strings.NewReader(payload))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusOK || received != payload {
		t.Fatalf("status=%d received=%q body=%s", rec.Code, received, rec.Body.String())
	}
}

func TestRelayAdminRejectsCommercialManagedLimitsWrite(t *testing.T) {
	upstreamCalls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	store := relayAdminManagedBudgetTestStore{budgets: map[int64][]*types.CommercialManagedRelayBudget{
		38: {{UID: 38, Model: "deepseek-v4-flash"}},
	}}
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}}, store)
	h.setRateLimit(1000, 60)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/relay/local/users/38/key/limits", strings.NewReader(`{}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "商业化页面") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("commercial-managed write reached relay %d time(s)", upstreamCalls)
	}
}

func TestRelayAdminManagedBudgetCheckFailureDoesNotWrite(t *testing.T) {
	upstreamCalls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(
		relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}},
		relayAdminManagedBudgetTestStore{err: fmt.Errorf("database unavailable")},
	)
	h.setRateLimit(1000, 60)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/relay/local/users/38/key/limits", strings.NewReader(`{}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusBadGateway || upstreamCalls != 0 {
		t.Fatalf("status=%d upstreamCalls=%d body=%s", rec.Code, upstreamCalls, rec.Body.String())
	}
}

func TestRelayAdminSecurityHeaders(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("missing Cache-Control no-store: %q", cc)
	}
	if xfo := rec.Header().Get("X-Frame-Options"); xfo != "SAMEORIGIN" {
		t.Fatalf("missing X-Frame-Options SAMEORIGIN: %q", xfo)
	}
}

func TestRelayAdminAuthMiddleware(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)

	jwtCalls := 0
	fakeJWT := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			jwtCalls++
			uid := UIDFromContext(r.Context())
			if uid <= 0 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next(w, r)
		}
	}
	auth := h.AuthMiddleware(fakeJWT)

	// 1. Cookie path: obtain the scoped cookie via HandleAccess, then pass through
	// the middleware with the cookie and NO JWT context — must NOT hit the JWT layer.
	req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/access", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleAccess(rec, req)
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == relayAdminCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie issued")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	auth(h.HandleProxy)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie path status=%d body=%s", rec.Code, rec.Body.String())
	}
	if jwtCalls != 0 {
		t.Fatalf("cookie path must not hit JWT layer, jwtCalls=%d", jwtCalls)
	}

	// 2. JWT fallback: no cookie, JWT layer injects uid into context.
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec = httptest.NewRecorder()
	auth(h.HandleProxy)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("jwt path status=%d body=%s", rec.Code, rec.Body.String())
	}
	if jwtCalls != 1 {
		t.Fatalf("jwt fallback must be used once, jwtCalls=%d", jwtCalls)
	}

	// 3. JWT rejection: no cookie, no uid -> 401 from the JWT layer.
	req = httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-summary", nil)
	rec = httptest.NewRecorder()
	auth(h.HandleProxy)(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", rec.Code)
	}
	if jwtCalls != 2 {
		t.Fatalf("jwt fallback must be called for unauthenticated, jwtCalls=%d", jwtCalls)
	}
}

func TestRelayAdminQueryTokenStripped(t *testing.T) {
	var relayQuery string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/relay/local/usage-users?limit=10&token=SECRET&api_key=SECRET2", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
	rec := httptest.NewRecorder()
	h.HandleProxy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if strings.Contains(relayQuery, "token") || strings.Contains(relayQuery, "api_key") {
		t.Fatalf("credential query leaked to relay: %q", relayQuery)
	}
	if !strings.Contains(relayQuery, "limit=10") {
		t.Fatalf("legitimate query params lost: %q", relayQuery)
	}
}

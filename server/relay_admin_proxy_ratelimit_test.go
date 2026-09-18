package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The portal proxy only forwards the verbs the admin pages use: GET for every
// read path and POST for the known local write paths. Everything else must be
// rejected before it can reach the relay.
func TestRelayAdminProxyMethodAllowlist(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" marker="+r.Header.Get("X-Cats-Relay-Local-Write"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimit(1000, 60)

	call := func(method, path string) int {
		req := httptest.NewRequest(method, relayAdminRewritePrefix+path, strings.NewReader(`{}`))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
		rec := httptest.NewRecorder()
		h.HandleProxy(rec, req)
		return rec.Code
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPut, "/local/pricing-rules", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/local/users/38/key/limits", http.StatusMethodNotAllowed},
		{http.MethodPost, "/local/usage-summary", http.StatusMethodNotAllowed},
		{http.MethodPost, "/local/pricing-analytics/data", http.StatusMethodNotAllowed},
		{http.MethodPost, "/local/pricing-rules", http.StatusOK},
		{http.MethodPost, "/local/users/38/key/limits", http.StatusOK},
	} {
		if code := call(tc.method, tc.path); code != tc.status {
			t.Fatalf("%s %s status=%d want=%d", tc.method, tc.path, code, tc.status)
		}
	}
	mu.Lock()
	joined := strings.Join(seen, "\n")
	count := len(seen)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("rejected verbs must not reach the relay: %v", joined)
	}
	if strings.Count(joined, "marker=pricing-rules") != 1 {
		t.Fatalf("write marker not forwarded exactly once: %s", joined)
	}
}

// Reads and writes keep separate per-caller budgets: an exhausted read window
// (the panel polling while a snapshot refresh runs) must not block a write,
// while writes stay on their own tighter ceiling.
func TestRelayAdminReadAndWriteBudgetsStaySeparate(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer relay.Close()
	h := NewRelayAdminProxyHandler(relayAdminConfig{relayURL: relay.URL, allowedUIDs: []int64{38}})
	h.setRateLimits(2, 1, 60)

	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, relayAdminRewritePrefix+path, strings.NewReader(`{}`))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(38)))
		req.RemoteAddr = "10.0.0.7:1234"
		rec := httptest.NewRecorder()
		h.HandleProxy(rec, req)
		return rec
	}
	if code := call(http.MethodGet, "/local/usage-summary").Code; code != http.StatusOK {
		t.Fatalf("read 1 status=%d", code)
	}
	if code := call(http.MethodGet, "/local/usage-summary").Code; code != http.StatusOK {
		t.Fatalf("read 2 status=%d", code)
	}
	limited := call(http.MethodGet, "/local/usage-summary")
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("read budget must run out: %d", limited.Code)
	}
	if limited.Header().Get("Retry-After") == "" || !strings.Contains(limited.Body.String(), "retry_after_seconds") {
		t.Fatalf("429 must carry backoff hints: %v %s", limited.Header(), limited.Body.String())
	}
	// Writes keep their own budget: an exhausted read window must not block them.
	if code := call(http.MethodPost, "/local/pricing-rules").Code; code != http.StatusOK {
		t.Fatalf("write after read exhaustion: %d", code)
	}
	if code := call(http.MethodPost, "/local/pricing-rules").Code; code != http.StatusTooManyRequests {
		t.Fatalf("write budget must still apply: %d", code)
	}
}

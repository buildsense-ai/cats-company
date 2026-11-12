package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Failure modes for the owner Daily Notes proxy, written before the handler:
//
//	F1 a signed-in user reads another account's Agent (must be 404, no upstream call)
//	F2 the caller chooses the CatsLog agent_id, uid or lane (unknown fields and
//	   operations are 400, no upstream call; agent_id comes only from the path)
//	F3 the account bearer is not forwarded verbatim, or is sent when the request is refused
//	F4 a CatsLog 401/403 surfaces as 401 and logs the CatsCompany session out
//	F5 CatsLog busy (429) loses its Retry-After, so the page hammers it
//	F6 an unbounded upstream body is buffered into memory
//	F7 an unconfigured base URL makes outbound calls to an empty host
//	F8 a slow CatsLog holds the request past the proxy deadline
func TestDailyNotesProxy(t *testing.T) {
	var calls atomic.Int32
	var lastAuth, lastBody atomic.Value
	upstreamStatus := atomic.Int32{}
	upstreamStatus.Store(http.StatusOK)
	var upstreamBody atomic.Value
	upstreamBody.Store(`{"nodes":[],"edges":[],"exhausted":true,"lane":"diaries","units":0}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/catsco/user/knowledge/v1/query" || r.Method != http.MethodPost {
			http.Error(w, "wrong route", http.StatusTeapot)
			return
		}
		body, _ := io.ReadAll(r.Body)
		lastAuth.Store(r.Header.Get("Authorization"))
		lastBody.Store(string(body))
		status := int(upstreamStatus.Load())
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "2")
		}
		if status == http.StatusGatewayTimeout {
			time.Sleep(300 * time.Millisecond)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, upstreamBody.Load().(string))
	}))
	defer upstream.Close()

	db := &agentTestStore{owners: map[int64]int64{574: 275, 900: 11}}
	handler := NewDailyNotesHandler(db, upstream.URL)
	handler.timeout = 150 * time.Millisecond
	handler.maxBody = 1 << 10

	call := func(viewer int64, agent, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/agents/"+agent+"/daily-notes", strings.NewReader(body))
		req.SetPathValue("uid", agent)
		req.Header.Set("Authorization", "Bearer owner-jwt")
		req = req.WithContext(context.WithValue(req.Context(), uidKey, viewer))
		rec := httptest.NewRecorder()
		handler.HandleQuery(rec, req)
		return rec
	}

	refused := []struct {
		name, agent, body string
		viewer            int64
		status            int
	}{
		{"F1 other owner", "900", `{"operation":"graph"}`, 275, http.StatusNotFound},
		{"F1 unknown agent", "12345", `{"operation":"graph"}`, 275, http.StatusNotFound},
		{"F1 signed out", "574", `{"operation":"graph"}`, 0, http.StatusUnauthorized},
		{"F2 caller agent_id", "574", `{"operation":"graph","agent_id":"usr900"}`, 275, http.StatusBadRequest},
		{"F2 caller uid", "574", `{"operation":"graph","uid":"11"}`, 275, http.StatusBadRequest},
		{"F2 caller lane", "574", `{"operation":"graph","lane":"source"}`, 275, http.StatusBadRequest},
		{"F2 source op", "574", `{"operation":"source"}`, 275, http.StatusBadRequest},
		{"F2 read without document", "574", `{"operation":"read"}`, 275, http.StatusBadRequest},
		{"F2 malformed document", "574", `{"operation":"read","document_id":"../x"}`, 275, http.StatusBadRequest},
		{"F2 trailing value", "574", `{"operation":"graph"}{}`, 275, http.StatusBadRequest},
		{"F2 bad agent path", "usr574", `{"operation":"graph"}`, 275, http.StatusBadRequest},
	}
	for _, tc := range refused {
		before := calls.Load()
		rec := call(tc.viewer, tc.agent, tc.body)
		if rec.Code != tc.status || calls.Load() != before {
			t.Fatalf("%s: status=%d upstream calls=%d, want %d and no call", tc.name, rec.Code, calls.Load()-before, tc.status)
		}
	}

	rec := call(275, "574", `{"operation":"graph","cursor":"c1"}`)
	if rec.Code != http.StatusOK || lastAuth.Load() != "Bearer owner-jwt" {
		t.Fatalf("F3 graph: status=%d auth=%v", rec.Code, lastAuth.Load())
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(lastBody.Load().(string)), &sent)
	if sent["agent_id"] != "usr574" || sent["operation"] != "graph" || sent["uid"] != nil {
		t.Fatalf("F2 graph body = %s", lastBody.Load())
	}
	if g, _ := sent["graph"].(map[string]any); g["lane"] != "diaries" || g["cursor"] != "c1" || len(g) != 2 {
		t.Fatalf("F2 graph selector = %v", sent["graph"])
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("graph cache header = %q", rec.Header().Get("Cache-Control"))
	}

	rec = call(275, "574", `{"operation":"read","document_id":"akd-0123456789abcdef01234567","entry_id":"ake-0123456789abcdef01234567"}`)
	_ = json.Unmarshal([]byte(lastBody.Load().(string)), &sent)
	if r, _ := sent["read"].(map[string]any); rec.Code != http.StatusOK || r["document_id"] != "akd-0123456789abcdef01234567" || r["entry_id"] != "ake-0123456789abcdef01234567" {
		t.Fatalf("read: status=%d body=%s", rec.Code, lastBody.Load())
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		upstreamStatus.Store(int32(status))
		if rec = call(275, "574", `{"operation":"graph"}`); rec.Code != http.StatusBadGateway {
			t.Fatalf("F4 upstream %d mapped to %d", status, rec.Code)
		}
	}
	upstreamStatus.Store(http.StatusTooManyRequests)
	if rec = call(275, "574", `{"operation":"graph"}`); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("F5 busy: status=%d retry=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	upstreamStatus.Store(http.StatusOK)
	upstreamBody.Store(`{"nodes":"` + strings.Repeat("x", 2<<10) + `"}`)
	if rec = call(275, "574", `{"operation":"graph"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("F6 oversize body mapped to %d", rec.Code)
	}
	upstreamStatus.Store(http.StatusGatewayTimeout)
	if rec = call(275, "574", `{"operation":"graph"}`); rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("F8 slow upstream mapped to %d", rec.Code)
	}

	before := calls.Load()
	unconfigured := NewDailyNotesHandler(db, "")
	req := httptest.NewRequest(http.MethodPost, "/api/agents/574/daily-notes", strings.NewReader(`{"operation":"graph"}`))
	req.SetPathValue("uid", "574")
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(275)))
	rec = httptest.NewRecorder()
	unconfigured.HandleQuery(rec, req)
	if rec.Code != http.StatusServiceUnavailable || calls.Load() != before {
		t.Fatalf("F7 unconfigured: status=%d calls=%d", rec.Code, calls.Load()-before)
	}
}

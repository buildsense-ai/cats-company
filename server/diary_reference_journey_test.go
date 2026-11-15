package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P1 old references must keep their exact akr revision through the HTTP proxy.
// P2 graph requests and malformed revision selectors must not reach CatsLog.
// P3 adding historical reads must preserve owner-only access and no-store.
// This exercises two real HTTP servers and writes the forwarded request as proof.
func TestDiaryReferenceVersionHTTPJourney(t *testing.T) {
	const doc = "akd-0123456789abcdef01234567"
	const entry = "ake-0123456789abcdef01234567"
	rev := "akr-" + strings.Repeat("a", 64)
	var forwarded []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]any
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
		}
		forwarded = append(forwarded, q)
		if r.Header.Get("Authorization") != "Bearer owner-fixture" {
			t.Error("lost account bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"document_id": doc, "revision": q["read"].(map[string]any)["revision"], "entries": []any{}, "exhausted": true})
	}))
	defer upstream.Close()
	handler := NewDailyNotesHandler(&agentTestStore{owners: map[int64]int64{574: 275, 900: 11}}, upstream.URL)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agents/{uid}/daily-notes", func(w http.ResponseWriter, r *http.Request) {
		handler.HandleQuery(w, r.WithContext(context.WithValue(r.Context(), uidKey, int64(275))))
	})
	app := httptest.NewServer(mux)
	defer app.Close()
	post := func(agent, body string) (int, []byte, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, app.URL+"/api/agents/"+agent+"/daily-notes", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer owner-fixture")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b, res.Header
	}
	query := `{"operation":"read","document_id":"` + doc + `","entry_id":"` + entry + `","revision":"` + rev + `"}`
	status, body, headers := post("574", query)
	if status != 200 {
		t.Fatalf("P1 pinned read status=%d body=%s", status, body)
	}
	if len(forwarded) != 1 || forwarded[0]["read"].(map[string]any)["revision"] != rev || headers.Get("Cache-Control") != "private, no-store" {
		t.Fatal("P1/P3 revision or privacy lost")
	}
	before := len(forwarded)
	for _, invalid := range []string{
		`{"operation":"graph","revision":"` + rev + `"}`,
		strings.Replace(query, rev, "head", 1),
		strings.Replace(query, rev, "../revision", 1),
	} {
		if code, _, _ := post("574", invalid); code != 400 {
			t.Fatalf("P2 invalid request returned %d", code)
		}
	}
	if code, _, _ := post("900", query); code != 404 {
		t.Fatalf("P3 other owner=%d", code)
	}
	if len(forwarded) != before {
		t.Fatal("P2/P3 refused read reached upstream")
	}
	dir := os.Getenv("DIARY_ARTIFACT_DIR")
	if dir == "" {
		dir = "/tmp/catsco-diary-sources"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	artifact, _ := json.MarshalIndent(map[string]any{"forwarded": forwarded, "response": json.RawMessage(body), "refused_requests": 4}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "proxy-journey.json"), artifact, 0644); err != nil {
		t.Fatal(err)
	}
}

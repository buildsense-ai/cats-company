package server

// Cross-server failure modes, specified before implementation: exact pins
// survive forwarding; only the bearer owner's Agent can be read; no caller
// uid/scope/enumeration or unknown fields; assertion closures stay bounded;
// a public runtime snapshot survives normalization/replay without source bodies.
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMemorySourceHTTPJourney(t *testing.T) {
	ref := "ref_" + strings.Repeat("a", 64)
	var forwarded []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catsco/user/knowledge/v1/memory/read" || r.Header.Get("Authorization") != "Bearer owner-fixture" {
			t.Error("wrong owner route or bearer")
		}
		var q map[string]any
		json.NewDecoder(r.Body).Decode(&q)
		forwarded = append(forwarded, q)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ref": q["projection_ref"], "revision": strings.Repeat("b", 64), "document": map[string]any{"nodes": []any{}}})
	}))
	defer upstream.Close()
	handler := NewDailyNotesHandler(&agentTestStore{owners: map[int64]int64{574: 275, 900: 11}}, upstream.URL)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agents/{uid}/memory-sources", func(w http.ResponseWriter, r *http.Request) {
		handler.HandleMemoryRead(w, r.WithContext(context.WithValue(r.Context(), uidKey, int64(275))))
	})
	app := httptest.NewServer(mux)
	defer app.Close()
	post := func(agent, body string) int {
		req, _ := http.NewRequest("POST", app.URL+"/api/agents/"+agent+"/memory-sources", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer owner-fixture")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		if !strings.Contains(res.Header.Get("Cache-Control"), "no-store") || res.StatusCode == 200 && res.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("cached private source: status=%d headers=%v", res.StatusCode, res.Header)
		}
		return res.StatusCode
	}
	q := `{"kind":"projection","projection_ref":"` + ref + `","revision":"` + strings.Repeat("b", 64) + `"}`
	if code := post("574", q); code != 200 {
		t.Fatal(code)
	}
	if forwarded[0]["agent_id"] != "usr574" || forwarded[0]["projection_ref"] != ref || forwarded[0]["operation"] != "memory_projection" {
		t.Fatal(forwarded)
	}
	for _, body := range []string{strings.Replace(q, "projection", "graph", 1), strings.Replace(q, ref, "head", 1), strings.TrimSuffix(q, "}") + `,"uid":"11"}`, `{"kind":"source","anchor":{"kind":"knowledge_entry","id":"ake-any"}}`, `{"kind":"projection","projection_ref":"` + ref + `","session_id":"another"}`} {
		if code := post("574", body); code != 400 {
			t.Fatal("invalid selector", code, body)
		}
	}
	if code := post("900", q); code != 404 {
		t.Fatal("other owner", code)
	}
	if len(forwarded) != 1 {
		t.Fatal("rejected request reached source store")
	}
	os.MkdirAll("/tmp/catsco-branch-sources", 0755)
	data, _ := json.MarshalIndent(forwarded, "", "  ")
	if e := os.WriteFile("/tmp/catsco-branch-sources/proxy-journey.json", data, 0644); e != nil {
		t.Fatal(e)
	}
}

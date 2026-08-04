package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSkillHubProxyForwardsCatalogueQuery(t *testing.T) {
	var gotPath string
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"skills":[]}`))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills?q=code%20review&category=dev", nil)
	rec := httptest.NewRecorder()
	h.HandleSkills(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/skills" || gotQuery != "category=dev&q=code+review" {
		t.Fatalf("upstream request = %s?%s", gotPath, gotQuery)
	}
}

func TestSkillHubProxyForwardsVersionAndEscapesSkillID(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"skill":{}}`))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills/lin/review/versions/1.2.0", nil)
	rec := httptest.NewRecorder()
	h.HandleSkill(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/skills/lin/review/versions/1.2.0" {
		t.Fatalf("upstream path = %s", gotPath)
	}
}

func TestSkillHubProxyEncodesSkillPathExactlyOnce(t *testing.T) {
	var gotEscapedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscapedPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"skill":{}}`))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills/%E4%B8%AD%E6%96%87/skill%20name", nil)
	rec := httptest.NewRecorder()
	h.HandleSkill(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotEscapedPath != "/api/skills/%E4%B8%AD%E6%96%87/skill%20name" {
		t.Fatalf("upstream path = %s", gotEscapedPath)
	}
}

func TestSkillHubProxyRejectsUnsafeSkillID(t *testing.T) {
	h := NewSkillHubProxyHandler("http://127.0.0.1:1", SkillHubProxyOptions{Timeout: time.Second})
	for _, path := range []string{
		"/api/skillhub/skills/../secret",
		"/api/skillhub/skills/%2e%2e/secret",
		"/api/skillhub/skills/lin%2Freview",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.HandleSkill(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("path %s status = %d", path, rec.Code)
		}
	}
}

func TestSkillHubProxyMapsUpstreamFailuresWithoutLeakingBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"secret":"do-not-leak"}`))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills", nil)
	rec := httptest.NewRecorder()
	h.HandleSkills(rec, req)
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "do-not-leak") {
		t.Fatalf("status/body = %d/%s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload["error"] == "" {
		t.Fatalf("unexpected error payload: %s", rec.Body.String())
	}
}

func TestSkillHubProxyRejectsInvalidJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills", nil)
	rec := httptest.NewRecorder()
	h.HandleSkills(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSkillHubProxyRejectsNonGetAndOversizedResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"skills":["` + strings.Repeat("x", 64) + `"]}`))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{MaxResponseSize: 16, Timeout: time.Second})
	methodReq := httptest.NewRequest(http.MethodPost, "/api/skillhub/skills", nil)
	methodRec := httptest.NewRecorder()
	h.HandleSkills(methodRec, methodReq)
	if methodRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", methodRec.Code)
	}
	if methodRec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("allow header = %q", methodRec.Header().Get("Allow"))
	}

	largeReq := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills", nil)
	largeRec := httptest.NewRecorder()
	h.HandleSkills(largeRec, largeReq)
	if largeRec.Code != http.StatusBadGateway || !strings.Contains(largeRec.Body.String(), "too large") {
		t.Fatalf("large response = %d/%s", largeRec.Code, largeRec.Body.String())
	}
}

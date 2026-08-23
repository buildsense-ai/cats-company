package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestSkillHubPrivateMetadataUsesBotCredentialsAndReturnsOnlyRequestedNames(t *testing.T) {
	var gotAuthorization string
	var gotBotID string
	var gotReferences []privateSkillMetadataReference
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotBotID = r.Header.Get("X-CatsCo-Bot-Id")
		var body privateSkillMetadataRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotReferences = body.References
		_, _ = w.Write([]byte(`{"skills":[` +
			`{"skillId":"priv_owned","version":"v_1","displayName":"cloud-html-artifact","revisionNumber":3,"lastChangedByUserUid":7,"lastChangedAt":"2026-08-22T02:03:04Z","changeSource":"conversation_mutation","contentHash":"do-not-forward"},` +
			`{"skillId":"priv_unrequested","version":"v_2","displayName":"ignore-me"}` +
			`]}`))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	metadata, err := h.ResolvePrivateSkillMetadata(context.Background(), "43", "secret-bot-key", []types.BotSkillRef{
		{Source: "skillhub", SkillID: "priv_owned", Version: "v_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "ApiKey secret-bot-key" || gotBotID != "43" {
		t.Fatalf("credentials were not forwarded correctly: auth=%q bot=%q", gotAuthorization, gotBotID)
	}
	if len(gotReferences) != 1 || gotReferences[0].SkillID != "priv_owned" {
		t.Fatalf("references=%+v", gotReferences)
	}
	presentation := metadata[botSkillMetadataKey("priv_owned", "v_1")]
	if len(metadata) != 1 || presentation.DisplayName != "cloud-html-artifact" ||
		presentation.RevisionNumber != 3 || presentation.LastChangedByUserUID != 7 ||
		presentation.LastChangedAt != "2026-08-22T02:03:04Z" || presentation.ChangeSource != "conversation_mutation" {
		t.Fatalf("metadata=%+v", metadata)
	}
}

func TestSkillHubPrivateHistoryUsesBotCredentialsAndSanitizesVersions(t *testing.T) {
	var gotAuthorization string
	var gotBotID string
	var gotPath string
	var gotBody privateSkillHistoryRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotBotID = r.Header.Get("X-CatsCo-Bot-Id")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"skillId":"priv_owned","versions":[` +
			`{"source":"skillhub","skillId":"priv_owned","version":"v_2","displayName":"review-helper","revisionNumber":2,"lastChangedAt":"2026-08-23T02:03:04Z","changeSource":"conversation_mutation","contentHash":"do-not-forward","actorUserUid":7},` +
			`{"source":"skillhub","skillId":"priv_other","version":"v_1","displayName":"ignore","revisionNumber":1}` +
			`],"nextBeforeRevisionNumber":2}`))
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	history, err := h.ResolvePrivateSkillHistory(context.Background(), "43", "secret-bot-key", "priv_owned", 20, 9)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != skillHubPrivateHistoryPath || gotAuthorization != "ApiKey secret-bot-key" || gotBotID != "43" {
		t.Fatalf("request path/auth/bot = %q/%q/%q", gotPath, gotAuthorization, gotBotID)
	}
	if gotBody.SkillID != "priv_owned" || gotBody.Limit != 20 || gotBody.BeforeRevisionNumber != 9 {
		t.Fatalf("request body = %+v", gotBody)
	}
	if history.SkillID != "priv_owned" || history.NextBeforeRevisionNumber != 2 || len(history.Versions) != 1 {
		t.Fatalf("history = %+v", history)
	}
	version := history.Versions[0]
	if version.SkillID != "priv_owned" || version.Version != "v_2" || version.DisplayName != "review-helper" ||
		version.RevisionNumber != 2 || version.LastChangedAt != "2026-08-23T02:03:04Z" ||
		version.ChangeSource != "conversation_mutation" || version.LastChangedBy != "" || version.Current {
		t.Fatalf("version = %+v", version)
	}
}

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
	req := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills?q=code%20review&category=dev&search_mode=name&ignored=value", nil)
	rec := httptest.NewRecorder()
	h.HandleSkills(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/skills" || gotQuery != "category=dev&q=code+review&search_mode=name" {
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

func TestSkillHubProxyRejectsCrossOriginRedirects(t *testing.T) {
	redirectTargetReached := false
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetReached = true
		_, _ = w.Write([]byte(`{"skills":[{"secret":"internal"}]}`))
	}))
	defer redirectTarget.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/internal", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()

	h := NewSkillHubProxyHandler(upstream.URL, SkillHubProxyOptions{Timeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/api/skillhub/skills", nil)
	rec := httptest.NewRecorder()
	h.HandleSkills(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if redirectTargetReached {
		t.Fatal("cross-origin redirect target must not be requested")
	}
	if strings.Contains(rec.Body.String(), "internal") {
		t.Fatalf("redirect target response leaked: %s", rec.Body.String())
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

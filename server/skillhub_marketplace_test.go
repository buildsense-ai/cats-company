package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func marketplaceRequest(method, path, body string, uid int64) *http.Request {
	r := httptest.NewRequest(method, marketplacePrefix+path, strings.NewReader(body))
	r.Header.Set("Authorization", fmt.Sprintf("Bearer user-%d", uid))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(context.WithValue(r.Context(), uidKey, uid))
}

func marketplaceTestHandler(t *testing.T, upstream http.HandlerFunc) *SkillHubMarketplaceHandler {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	return NewSkillHubMarketplaceHandler(NewSkillHubProxyHandler(srv.URL, SkillHubProxyOptions{Timeout: time.Second}), SkillHubMarketplaceOptions{Enabled: true, WritesEnabled: true})
}

func exchangeForTest(w http.ResponseWriter, r *http.Request, t *testing.T) bool {
	t.Helper()
	if r.URL.Path != "/api/auth/catsco-exchange" {
		return false
	}
	var payload map[string]string
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Error(err)
	}
	if len(payload) != 1 || !strings.HasPrefix(payload["token"], "user-") {
		t.Errorf("unexpected exchange fields: %v", payload)
	}
	uid := strings.TrimPrefix(payload["token"], "user-")
	http.SetCookie(w, &http.Cookie{Name: "catsco_session", Value: "session-" + uid, HttpOnly: true})
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"catsCo":{"uid":"%s"},"user":{"email":"not-for-browser"}}`, uid)
	return true
}

func TestSkillHubMarketplacePublicRoutesStripCredentialsAndAllowlistQuery(t *testing.T) {
	for _, tc := range []struct{ path, wantPath, query string }{
		{"/catalogue/skills?q=read&limit=20&cursor=abc&search_mode=name&category=documents&token=secret&url=http://evil", "/api/catalogue/skills", "category=documents&cursor=abc&limit=20&q=read&search_mode=name"},
		{"/catalogue/categories?bot_uid=123", "/api/catalogue/categories", ""},
		{"/presentations?skillId=author%2Fread&version=1.0.0&draft=true", "/api/skill-presentations", "skillId=author%2Fread&version=1.0.0"},
	} {
		t.Run(tc.wantPath, func(t *testing.T) {
			var calls int
			h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != tc.wantPath || r.URL.RawQuery != tc.query {
					t.Errorf("path/query=%s?%s", r.URL.Path, r.URL.RawQuery)
				}
				for _, key := range []string{"Cookie", "Authorization", "Origin", "X-CatsCo-Bot-Id", "X-Real-IP"} {
					if r.Header.Get(key) != "" {
						t.Errorf("forwarded %s", key)
					}
				}
				w.Header().Set("Set-Cookie", "secret=upstream")
				w.Header().Set("Cache-Control", "public, max-age=86400")
				_, _ = w.Write([]byte(`{"presentation":null}`))
			})
			r := marketplaceRequest("GET", tc.path, "", 7)
			r.Header.Set("Cookie", "catsco_session=another-account")
			r.Header.Set("Origin", "https://attacker.invalid")
			r.Header.Set("X-CatsCo-Bot-Id", "999")
			r.Header.Set("X-Real-IP", "192.0.2.40")
			w := httptest.NewRecorder()
			h.Handle(w, r)
			if w.Code != 200 || calls != 1 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("response=%d %v calls=%d", w.Code, w.Header(), calls)
			}
		})
	}
}

func TestSkillHubMarketplaceEditorUsesOneRequestSessionAndRevokesIt(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Error("JWT leaked as upstream authorization")
		}
		if exchangeForTest(w, r, t) {
			actions = append(actions, "exchange")
			return
		}
		c, err := r.Cookie("catsco_session")
		if err != nil {
			t.Error("no temporary cookie")
			return
		}
		actions = append(actions, r.Method+":"+r.URL.Path+":"+c.Value)
		if r.URL.Path == "/api/auth/logout" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path != "/api/skill-presentations/draft" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		w.Header().Set("Set-Cookie", "do-not-leak=secret")
		_, _ = w.Write([]byte(`{"presentation":{"revision":1}}`))
	})
	for _, uid := range []int64{7, 8} {
		r := marketplaceRequest("PUT", "/presentations/draft?token=someone-else", `{"skillId":"author/read","version":"1.0.0","expectedRevision":0,"content":{}}`, uid)
		r.Header.Set("Cookie", "catsco_session=browser-admin")
		w := httptest.NewRecorder()
		h.Handle(w, r)
		if w.Code != 200 || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "not-for-browser") {
			t.Fatalf("response=%d %s", w.Code, w.Body)
		}
	}
	want := "exchange,PUT:/api/skill-presentations/draft:session-7,POST:/api/auth/logout:session-7,exchange,PUT:/api/skill-presentations/draft:session-8,POST:/api/auth/logout:session-8"
	if strings.Join(actions, ",") != want {
		t.Fatalf("actions=%v", actions)
	}
}

func TestSkillHubMarketplaceRouteMethodsAndBodyBounds(t *testing.T) {
	var calls atomic.Int32
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, tc := range []struct {
		method, path, body, contentType string
		status                          int
	}{
		{"POST", "/catalogue/skills", `{}`, "application/json", 405},
		{"GET", "/presentations/publish", "", "", 405},
		{"PUT", "/assets", `{}`, "application/json", 405},
		{"DELETE", "/assets/pa_" + strings.Repeat("a", 32) + "/preview", `{}`, "application/json", 405},
		{"GET", "/presentations/../auth/logout", "", "", 404},
		{"GET", "/%70resentations/draft", "", "", 404},
		{"GET", "/assets/%2e%2e/secret", "", "", 404},
		{"GET", "/assets/pa_invalid", "", "", 404},
		{"GET", "/catalogue/skills?limit=2&limit=3", "", "", 400},
		{"GET", "/catalogue/skills?cursor=" + strings.Repeat("x", 8193), "", "", 400},
		{"PUT", "/presentations/draft", `{}`, "text/plain", 415},
		{"PUT", "/presentations/draft", `{invalid}`, "application/json", 400},
		{"PUT", "/presentations/draft", strings.Repeat(" ", 176<<10+1), "application/json", 413},
		{"POST", "/assets", strings.Repeat(" ", 3<<20+1), "application/json", 413},
	} {
		r := marketplaceRequest(tc.method, tc.path, tc.body, 7)
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		h.Handle(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %.80s: status=%d want=%d", tc.method, tc.path, w.Code, tc.status)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests contacted upstream %d times", calls.Load())
	}
}

func TestSkillHubMarketplaceRolloutAndBearerRequirements(t *testing.T) {
	var calls int
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	h.enabled = false
	w := httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("GET", "/capabilities", "", 7))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("capabilities=%s", w.Body)
	}
	w = httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("GET", "/presentations", "", 7))
	if w.Code != 503 {
		t.Fatalf("status=%d", w.Code)
	}
	h.enabled, h.writesEnabled = true, false
	w = httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("POST", "/presentations/publish", `{}`, 7))
	if w.Code != 503 {
		t.Fatalf("status=%d", w.Code)
	}
	for _, auth := range []string{"", "ApiKey bot-secret", "Bearer "} {
		r := marketplaceRequest("GET", "/presentations?token=user-7", "", 7)
		r.Header.Set("Authorization", auth)
		w = httptest.NewRecorder()
		h.Handle(w, r)
		if w.Code != 401 {
			t.Fatalf("status=%d", w.Code)
		}
	}
	w = httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("GET", "/presentations", "", 0))
	if w.Code != 401 || calls != 0 {
		t.Fatalf("status/calls=%d/%d", w.Code, calls)
	}
	t.Setenv("CATSCO_SKILLHUB_MARKETPLACE_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_MARKETPLACE_WRITES_ENABLED", "")
	defaults := NewSkillHubMarketplaceHandlerFromEnv(h.proxy)
	if defaults.enabled || defaults.writesEnabled {
		t.Fatal("production gates enabled by default")
	}
}

func TestSkillHubMarketplaceExchangeIdentityMismatchFailsClosedAndCleansUp(t *testing.T) {
	for _, body := range []string{`{"catsCo":{"uid":"8"}}`, `{"catsCo":{"uid":null}}`, `<html>secret</html>`, strings.Repeat("x", 64<<10+1)} {
		var writes, logouts int
		h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/auth/catsco-exchange":
				http.SetCookie(w, &http.Cookie{Name: "catsco_session", Value: "wrong-user", HttpOnly: true})
				_, _ = w.Write([]byte(body))
			case "/api/auth/logout":
				logouts++
				_, _ = w.Write([]byte(`{}`))
			default:
				writes++
				_, _ = w.Write([]byte(`{}`))
			}
		})
		w := httptest.NewRecorder()
		h.Handle(w, marketplaceRequest("PUT", "/presentations/draft", `{}`, 7))
		if w.Code != 502 || writes != 0 || logouts != 1 || strings.Contains(w.Body.String(), "wrong-user") {
			t.Fatalf("status=%d writes=%d logouts=%d", w.Code, writes, logouts)
		}
	}
}

func TestSkillHubMarketplaceImagesNeverPromotePublicReadsToEditorPreviews(t *testing.T) {
	const id = "pa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, preview := range []bool{false, true} {
		var exchanges, logouts int
		webp := []byte("RIFF1234WEBPdata")
		h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
			if exchangeForTest(w, r, t) {
				exchanges++
				return
			}
			if r.URL.Path == "/api/auth/logout" {
				logouts++
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, err := r.Cookie("catsco_session")
			if preview != (err == nil) {
				t.Errorf("cookie presence=%v preview=%v", err == nil, preview)
			}
			want := "/api/skill-presentations/assets/" + id
			if preview {
				want += "/preview"
			}
			if r.URL.Path != want {
				t.Errorf("upstream path %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "image/webp")
			w.Header().Set("Cache-Control", "public,immutable")
			_, _ = w.Write(webp)
		})
		path := "/assets/" + id
		if preview {
			path += "/preview"
		}
		w := httptest.NewRecorder()
		h.Handle(w, marketplaceRequest("GET", path, "", 7))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("status/headers=%d/%v", w.Code, w.Header())
		}
		want := 0
		if preview {
			want = 1
		}
		if exchanges != want || logouts != want {
			t.Fatalf("exchange/logout=%d/%d", exchanges, logouts)
		}
	}
}

func TestSkillHubMarketplaceRejectsInvalidImageAndOversizeResponses(t *testing.T) {
	for _, tc := range []struct{ contentType, body string }{
		{"text/html", "<script>secret</script>"}, {"image/webp", "<svg>secret</svg>"}, {"image/webp", "RIFF1234WEBP" + strings.Repeat("x", 2<<20)},
	} {
		h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", tc.contentType)
			_, _ = w.Write([]byte(tc.body))
		})
		w := httptest.NewRecorder()
		h.Handle(w, marketplaceRequest("GET", "/assets/pa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", 7))
		if w.Code != 502 || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
}

func TestSkillHubMarketplacePreservesActionableErrorsWithoutLeakingUpstreamMessages(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{409, "presentation.revision_conflict"}, {409, "catalogue.cursor_stale"}, {404, "presentation.asset_not_found"}, {429, "presentation.asset_quota"}, {503, "presentation.disabled"}, {403, "auth_error"}} {
		h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Set-Cookie", "secret=not-forwarded")
			w.WriteHeader(tc.status)
			_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":"upstream private details"}}`, tc.code)
		})
		w := httptest.NewRecorder()
		h.Handle(w, marketplaceRequest("GET", "/presentations?skillId=a%%2Fb&version=1.0.0", "", 7))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "private details") || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("response=%d %s", w.Code, w.Body)
		}
	}
}

func TestSkillHubMarketplaceRejectsAllRedirectsAndDoesNotInheritCookieJar(t *testing.T) {
	var leaks atomic.Int32
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			leaks.Add(1)
		}
		if r.URL.Path == "/unexpected" {
			leaks.Add(1)
		}
		http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
	})
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(h.proxy.baseURL, []*http.Cookie{{Name: "catsco_session", Value: "cached-admin"}})
	h.proxy.client.Jar = jar
	w := httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("GET", "/presentations", "", 7))
	if w.Code != 502 || leaks.Load() != 0 {
		t.Fatalf("status=%d leaks=%d", w.Code, leaks.Load())
	}
	w = httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("PUT", "/presentations/draft", `{}`, 7))
	if w.Code != 502 || leaks.Load() != 0 {
		t.Fatalf("status=%d leaks=%d", w.Code, leaks.Load())
	}
}

func TestSkillHubMarketplaceConcurrentAccountsDoNotShareSessions(t *testing.T) {
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if exchangeForTest(w, r, t) {
			return
		}
		if r.URL.Path == "/api/auth/logout" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		cookie, _ := r.Cookie("catsco_session")
		var body struct {
			Account int64 `json:"account"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if cookie == nil || cookie.Value != fmt.Sprintf("session-%d", body.Account) {
			t.Errorf("account/cookie mismatch %d %v", body.Account, cookie)
		}
		_, _ = w.Write([]byte(`{"presentation":{}}`))
	})
	var wg sync.WaitGroup
	for uid := int64(1); uid <= 12; uid++ {
		wg.Add(1)
		go func(uid int64) {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.Handle(w, marketplaceRequest("PUT", "/presentations/draft", fmt.Sprintf(`{"account":%d}`, uid), uid))
			if w.Code != 200 {
				t.Errorf("status=%d", w.Code)
			}
		}(uid)
	}
	wg.Wait()
}

func TestSkillHubMarketplaceOwnerMiddlewareRejectsBotAndDisabledAccounts(t *testing.T) {
	db := authStateTestStore{users: map[int64]*types.User{1: {ID: 1, AccountType: types.AccountHuman, State: 0}, 2: {ID: 2, AccountType: types.AccountBot, State: 0}, 3: {ID: 3, AccountType: types.AccountHuman, State: 1}}}
	h := NewSkillHubMarketplaceHandler(nil, SkillHubMarketplaceOptions{})
	wrapped := OwnerMiddlewareWithDB(db)(h.Handle)
	for _, tc := range []struct {
		uid    int64
		status int
	}{{1, 200}, {2, 403}, {3, 403}} {
		token, err := GenerateToken(tc.uid, "test", "")
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", marketplacePrefix+"/capabilities", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		wrapped(w, r)
		if w.Code != tc.status {
			t.Errorf("uid=%d status=%d want=%d", tc.uid, w.Code, tc.status)
		}
	}
	r := httptest.NewRequest("GET", marketplacePrefix+"/capabilities", nil)
	r.Header.Set("Authorization", "ApiKey bot-secret")
	w := httptest.NewRecorder()
	wrapped(w, r)
	if w.Code != 401 {
		t.Fatalf("API key accepted: %d", w.Code)
	}
}

func TestSkillHubMarketplaceNeverRetriesMutationAfterLostResponse(t *testing.T) {
	var mutations, logouts atomic.Int32
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if exchangeForTest(w, r, t) {
			return
		}
		if r.URL.Path == "/api/auth/logout" {
			logouts.Add(1)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		mutations.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	w := httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("POST", "/presentations/publish", `{}`, 7))
	if w.Code != 502 || mutations.Load() != 1 || logouts.Load() != 1 {
		t.Fatalf("status/mutations/logouts=%d/%d/%d", w.Code, mutations.Load(), logouts.Load())
	}
}

func TestSkillHubMarketplaceConfiguredBasePathIsPreserved(t *testing.T) {
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/service/api/catalogue/skills" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"skills":[]}`))
	})
	h.proxy.baseURL, _ = url.Parse(h.proxy.baseURL.String() + "/service")
	w := httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("GET", "/catalogue/skills", "", 7))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestSkillHubMarketplaceWriteRoutesRetainExactMethodAndPayload(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{"POST", "/assets", "/api/skill-presentations/assets"},
		{"DELETE", "/assets/pa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/api/skill-presentations/assets/pa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"POST", "/presentations/publish", "/api/skill-presentations/publish"},
		{"POST", "/presentations/unpublish", "/api/skill-presentations/unpublish"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			payload := `{"skillId":"author/read","version":"1.0.0","expectedRevision":3,"reason":"review"}`
			var operations int
			h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if exchangeForTest(w, r, t) {
					return
				}
				if r.URL.Path == "/api/auth/logout" {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				operations++
				body, err := readLimited(r.Body, 3<<20)
				if err != nil || string(body) != payload || r.Method != tc.method || r.URL.Path != tc.want || r.URL.RawQuery != "" {
					t.Errorf("wrong mutation %s %s %s", r.Method, r.URL, string(body))
				}
				if _, err := r.Cookie("catsco_session"); err != nil {
					t.Error("mutation missing current session")
				}
				w.WriteHeader(201)
				_, _ = w.Write([]byte(`{"asset":{}}`))
			})
			w := httptest.NewRecorder()
			h.Handle(w, marketplaceRequest(tc.method, tc.path+"?bot_uid=999", payload, 7))
			if w.Code != 201 || operations != 1 {
				t.Fatalf("status/operations=%d/%d", w.Code, operations)
			}
		})
	}
}

func TestSkillHubMarketplaceCleanupFailureDoesNotOverwriteSuccessfulWrite(t *testing.T) {
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if exchangeForTest(w, r, t) {
			return
		}
		if r.URL.Path == "/api/auth/logout" {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`private error`))
			return
		}
		_, _ = w.Write([]byte(`{"presentation":{"revision":4}}`))
	})
	w := httptest.NewRecorder()
	h.Handle(w, marketplaceRequest("POST", "/presentations/publish", `{}`, 7))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":4`) {
		t.Fatalf("result=%d %s", w.Code, w.Body)
	}
}

func TestSkillHubMarketplaceCancellationStillRevokesSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logouts atomic.Int32
	h := marketplaceTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if exchangeForTest(w, r, t) {
			return
		}
		if r.URL.Path == "/api/auth/logout" {
			logouts.Add(1)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	})
	r := marketplaceRequest("POST", "/presentations/publish", `{}`, 7).WithContext(context.WithValue(ctx, uidKey, int64(7)))
	w := httptest.NewRecorder()
	h.Handle(w, r)
	if w.Code != 502 || logouts.Load() != 1 {
		t.Fatalf("status/logouts=%d/%d", w.Code, logouts.Load())
	}
}

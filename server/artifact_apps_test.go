package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// artifactAppsCall is one request the fake gateway saw, so a test can assert on
// what the platform actually forwarded instead of on the response alone.
type artifactAppsCall struct {
	method string
	path   string
	auth   string
	body   []byte
}

// artifactAppsGateway stands in for the real gateway: it records calls, answers
// the documented shapes by default, and lets a test override the answer to
// exercise the failure mapping.
type artifactAppsGateway struct {
	server  *httptest.Server
	mu      sync.Mutex
	calls   []artifactAppsCall
	apps    []artifactApp
	respond http.HandlerFunc
	delay   time.Duration
}

func newArtifactAppsGateway(t *testing.T) *artifactAppsGateway {
	t.Helper()
	gateway := &artifactAppsGateway{}
	gateway.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gateway.mu.Lock()
		gateway.calls = append(gateway.calls, artifactAppsCall{
			method: r.Method,
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			body:   raw,
		})
		apps := append([]artifactApp(nil), gateway.apps...)
		respond := gateway.respond
		delay := gateway.delay
		gateway.mu.Unlock()

		if delay > 0 {
			time.Sleep(delay)
		}
		if respond != nil {
			respond(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": apps})
		case http.MethodPost:
			var forwarded map[string]any
			_ = json.Unmarshal(raw, &forwarded)
			id, _ := forwarded["id"].(string)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":        "registered",
				"id":            id,
				"title":         forwarded["title"],
				"agent":         forwarded["agent"],
				"remote_port":   28193,
				"url":           "https://artifact.catsco.cc/" + id + "/",
				"urls":          []string{"https://artifact.catsco.cc/" + id + "/", "https://artifact.catsco.cn/" + id + "/"},
				"transport_url": "wss://artifact.catsco.cc/_gateway/tunnel",
				"updated_at":    "2026-09-20T00:00:00Z",
			})
		case http.MethodDelete:
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "removed", "id": strings.TrimPrefix(r.URL.Path, artifactAppsGatewayPath+"/")})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(gateway.server.Close)
	return gateway
}

func (g *artifactAppsGateway) handler() *ArtifactAppsHandler {
	return &ArtifactAppsHandler{
		gatewayURL: g.server.URL,
		token:      "test-gateway-token-0123456789abcdef",
		httpClient: g.server.Client(),
	}
}

func (g *artifactAppsGateway) setApps(apps ...artifactApp) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.apps = apps
}

func (g *artifactAppsGateway) setRespond(respond http.HandlerFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.respond = respond
}

func (g *artifactAppsGateway) setDelay(delay time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.delay = delay
}

func (g *artifactAppsGateway) recorded() []artifactAppsCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]artifactAppsCall(nil), g.calls...)
}

// artifactAppsRequest mirrors the middleware, which stores the uid in the
// request context after a valid login.
func artifactAppsRequest(uid int64, method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if uid > 0 {
		request = request.WithContext(context.WithValue(request.Context(), uidKey, uid))
	}
	return request
}

func artifactAppsForwarded(t *testing.T, call artifactAppsCall) map[string]any {
	t.Helper()
	var forwarded map[string]any
	if err := json.Unmarshal(call.body, &forwarded); err != nil {
		t.Fatalf("forwarded body is not JSON: %s", call.body)
	}
	return forwarded
}

func TestArtifactAppsUnconfigured(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URL", "")
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", "")
	handler := NewArtifactAppsHandlerFromEnv()
	if handler.Enabled() {
		t.Fatal("handler without configuration must not be enabled")
	}
	for _, path := range []string{"/api/artifacts/apps", "/api/artifacts/apps/saturday-board"} {
		recorder := httptest.NewRecorder()
		handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodGet, path, ""))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503 (body %s)", path, recorder.Code, recorder.Body.String())
		}
	}

	// A token without a URL, and a URL without a token, are both incomplete.
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", strings.Repeat("a", 32))
	if NewArtifactAppsHandlerFromEnv().Enabled() {
		t.Fatal("a token without a gateway URL must not be enabled")
	}
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URL", "https://artifact.catsco.cc")
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", "")
	if NewArtifactAppsHandlerFromEnv().Enabled() {
		t.Fatal("a gateway URL without a token must not be enabled")
	}

	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", strings.Repeat("a", 32))
	if !NewArtifactAppsHandlerFromEnv().Enabled() {
		t.Fatal("a complete configuration must be enabled")
	}
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URL", "http://artifact.example.cc")
	if NewArtifactAppsHandlerFromEnv().Enabled() {
		t.Fatal("plain http gateway must not be accepted")
	}

	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URL", "https://artifact.catsco.cc")
	configured := NewArtifactAppsHandlerFromEnv()
	if !configured.Enabled() {
		t.Fatal("a complete configuration must be enabled")
	}
	if configured.httpClient.Timeout != artifactUpstreamTimeout {
		t.Errorf("timeout = %s, want %s", configured.httpClient.Timeout, artifactUpstreamTimeout)
	}
}

// The routes are registered behind jwtAuthWithDB, so an unauthenticated caller
// must be refused by the middleware and never reach the gateway.
func TestArtifactAppsRequireLoginThroughTheMiddleware(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	handler := gateway.handler()
	mux := http.NewServeMux()
	mux.HandleFunc(artifactAppsAPIPath, JWTAuthMiddlewareWithDB(nil)(handler.HandleApps))
	mux.HandleFunc(artifactAppsAPIPath+"/", JWTAuthMiddlewareWithDB(nil)(handler.HandleApps))

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/artifacts/apps", nil),
		httptest.NewRequest(http.MethodPost, "/api/artifacts/apps", strings.NewReader(`{"id":"saturday-board","title":"t","publicKey":"ssh-ed25519 AAAA"}`)),
		httptest.NewRequest(http.MethodGet, "/api/artifacts/apps/saturday-board", nil),
		httptest.NewRequest(http.MethodDelete, "/api/artifacts/apps/saturday-board", nil),
	}
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401 (body %s)", request.Method, request.URL.Path, recorder.Code, recorder.Body.String())
		}
	}
	if calls := gateway.recorded(); len(calls) != 0 {
		t.Errorf("unauthenticated requests reached the gateway: %+v", calls)
	}
}

// The registration path reads the application list before writing, to prove the
// id is not already somebody else's, so the write is the last call rather than
// the only one.
func artifactAppsWrite(t *testing.T, gateway *artifactAppsGateway) artifactAppsCall {
	t.Helper()
	calls := gateway.recorded()
	if len(calls) == 0 {
		t.Fatal("the gateway was never called")
	}
	last := calls[len(calls)-1]
	if last.method != http.MethodPost {
		t.Fatalf("last upstream call = %s %s, want POST", last.method, last.path)
	}
	return last
}

func TestArtifactAppsRegisterForwardsTheSessionOwner(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	handler := gateway.handler()
	recorder := httptest.NewRecorder()
	handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodPost, "/api/artifacts/apps",
		`{"id":"saturday-board","title":"我的看板","publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA example","localPort":20000}`))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	calls := gateway.recorded()
	if len(calls) != 2 || calls[0].method != http.MethodGet {
		t.Fatalf("gateway calls = %+v, want an ownership lookup then the write", calls)
	}
	write := artifactAppsWrite(t, gateway)
	if write.path != artifactAppsGatewayPath {
		t.Errorf("upstream request = %s %s, want POST %s", write.method, write.path, artifactAppsGatewayPath)
	}
	if write.auth != "Bearer "+handler.token {
		t.Errorf("upstream authorization = %q", write.auth)
	}
	forwarded := artifactAppsForwarded(t, write)
	// The owner is the session, never the body.
	if forwarded["agent"] != "441" {
		t.Errorf("forwarded agent = %v, want the authenticated caller 441", forwarded["agent"])
	}
	if forwarded["id"] != "saturday-board" || forwarded["title"] != "我的看板" {
		t.Errorf("forwarded id/title = %v/%v", forwarded["id"], forwarded["title"])
	}
	if forwarded["publicKey"] != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA example" {
		t.Errorf("forwarded publicKey = %v", forwarded["publicKey"])
	}
	if forwarded["localPort"] != float64(20000) {
		t.Errorf("forwarded localPort = %v (%T), want 20000", forwarded["localPort"], forwarded["localPort"])
	}
	for _, field := range []string{`"url"`, `"urls"`, `"remote_port"`, `"transport_url"`, `"status"`} {
		if !strings.Contains(recorder.Body.String(), field) {
			t.Errorf("response is missing %s: %s", field, recorder.Body.String())
		}
	}
	// remote_port is assigned by the gateway, so it must be the gateway's value.
	if !strings.Contains(recorder.Body.String(), `"remote_port":28193`) {
		t.Errorf("response does not carry the gateway's remote_port: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"url":"https://artifact.catsco.cc/saturday-board/"`) {
		t.Errorf("response does not carry the gateway's url: %s", recorder.Body.String())
	}
}

// A body that names an owner of its own must not be able to publish under that
// account: the gateway routes the tunnel by `agent` and cannot verify it.
func TestArtifactAppsRegisterIgnoresAnAgentInTheBody(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	handler := gateway.handler()
	recorder := httptest.NewRecorder()
	handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodPost, "/api/artifacts/apps",
		`{"id":"saturday-board","title":"看板","publicKey":"ssh-ed25519 AAAA","agent":"365","localPort":20000}`))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	write := artifactAppsWrite(t, gateway)
	forwarded := artifactAppsForwarded(t, write)
	if forwarded["agent"] != "441" {
		t.Errorf("forwarded agent = %v, want the session's 441 rather than the body's 365", forwarded["agent"])
	}
	if strings.Contains(string(write.body), "365") {
		t.Errorf("the body's agent leaked into the payload: %s", write.body)
	}
}

// localPort is optional, and an absent one must stay absent: the gateway treats
// a requested port as a demand, so sending 0 would not mean "no preference".
func TestArtifactAppsRegisterOmitsAnAbsentLocalPort(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	handler := gateway.handler()
	recorder := httptest.NewRecorder()
	handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodPost, "/api/artifacts/apps",
		`{"id":"saturday-board","title":"看板","publicKey":"ssh-ed25519 AAAA"}`))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	forwarded := artifactAppsForwarded(t, artifactAppsWrite(t, gateway))
	if _, present := forwarded["localPort"]; present {
		t.Errorf("localPort = %v, want the field to be omitted", forwarded["localPort"])
	}
}

func TestArtifactAppsRejectsBadInputWithoutCallingGateway(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	handler := gateway.handler()
	valid := `"title":"看板","publicKey":"ssh-ed25519 AAAA"`

	cases := []struct {
		name   string
		method string
		path   string
		uid    int64
		body   string
		want   int
	}{
		{name: "missing session", method: http.MethodPost, path: "/api/artifacts/apps", body: `{"id":"saturday-board","title":"t","publicKey":"k"}`, want: http.StatusUnauthorized},
		{name: "missing id", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{` + valid + `}`, want: http.StatusBadRequest},
		{name: "empty body", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: "", want: http.StatusBadRequest},
		{name: "broken json", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":`, want: http.StatusBadRequest},
		{name: "id with path", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"../etc",` + valid + `}`, want: http.StatusBadRequest},
		{name: "id uppercase", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"Saturday",` + valid + `}`, want: http.StatusBadRequest},
		{name: "id too long", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"` + strings.Repeat("a", 49) + `",` + valid + `}`, want: http.StatusBadRequest},
		{name: "id starting with a digit", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"1board",` + valid + `}`, want: http.StatusBadRequest},
		{name: "missing title", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"saturday-board","publicKey":"ssh-ed25519 AAAA"}`, want: http.StatusBadRequest},
		{name: "title with a newline", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"saturday-board","title":"a\nb","publicKey":"ssh-ed25519 AAAA"}`, want: http.StatusBadRequest},
		{name: "missing publicKey", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"saturday-board","title":"看板"}`, want: http.StatusBadRequest},
		{name: "publicKey with a newline", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"saturday-board","title":"看板","publicKey":"ssh-ed25519 AAAA\nevil"}`, want: http.StatusBadRequest},
		{name: "localPort zero", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"saturday-board",` + valid + `,"localPort":0}`, want: http.StatusBadRequest},
		{name: "localPort too large", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"saturday-board",` + valid + `,"localPort":65536}`, want: http.StatusBadRequest},
		{name: "body too large", method: http.MethodPost, path: "/api/artifacts/apps", uid: 441, body: `{"id":"saturday-board",` + valid + `,"comment":"` + strings.Repeat("c", artifactAppsMaxBody) + `"}`, want: http.StatusRequestEntityTooLarge},
		{name: "item with a malformed id", method: http.MethodGet, path: "/api/artifacts/apps/Saturday", uid: 441, want: http.StatusNotFound},
		{name: "item with a nested path", method: http.MethodGet, path: "/api/artifacts/apps/%2e%2e%2fetc", uid: 441, want: http.StatusNotFound},
		{name: "item with a trailing slash", method: http.MethodGet, path: "/api/artifacts/apps/saturday-board/", uid: 441, want: http.StatusNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.HandleApps(recorder, artifactAppsRequest(testCase.uid, testCase.method, testCase.path, testCase.body))
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
	if calls := gateway.recorded(); len(calls) != 0 {
		t.Errorf("gateway was called %d times for input it could have rejected: %+v", len(calls), calls)
	}
}

func TestArtifactAppsListReturnsOnlyTheCallersApps(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(
		artifactApp{ID: "mine", Title: "我的看板", Agent: "441", RemotePort: 28193, URL: "https://artifact.catsco.cc/mine/"},
		artifactApp{ID: "theirs", Title: "别人的看板", Agent: "365", RemotePort: 28194, URL: "https://artifact.catsco.cc/theirs/"},
	)
	handler := gateway.handler()
	recorder := httptest.NewRecorder()
	handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodGet, "/api/artifacts/apps", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Apps []artifactApp `json:"apps"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %s", recorder.Body.String())
	}
	if len(payload.Apps) != 1 || payload.Apps[0].ID != "mine" {
		t.Fatalf("apps = %+v, want only the caller's own", payload.Apps)
	}
	if payload.Apps[0].RemotePort != 28193 || payload.Apps[0].URL != "https://artifact.catsco.cc/mine/" {
		t.Errorf("app = %+v, want the gateway's own port and url", payload.Apps[0])
	}
	if strings.Contains(recorder.Body.String(), "theirs") {
		t.Errorf("another account's application leaked: %s", recorder.Body.String())
	}
}

// Registration can also be an update, and the gateway replaces an entry by id
// alone — it has no account of its own to check against. That is the same reason
// DELETE proves ownership first: without it any signed-in caller could move
// another account's application, and the public key its tunnel is built from,
// onto itself.
func TestArtifactAppsRegisterRefusesAnotherAccountsID(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(
		artifactApp{ID: "taken", Agent: "365", RemotePort: 28194, URL: "https://artifact.catsco.cc/taken/"},
		artifactApp{ID: "mine", Agent: "441", RemotePort: 28193, URL: "https://artifact.catsco.cc/mine/"},
	)
	handler := gateway.handler()
	recorder := httptest.NewRecorder()
	handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodPost, "/api/artifacts/apps",
		`{"id":"taken","title":"我的看板","publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA example"}`))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	// The same answer GET and DELETE give, so this route cannot be used to tell
	// "somebody else has it" from "nothing has it".
	if !strings.Contains(recorder.Body.String(), "artifact_app_not_found") {
		t.Errorf("body = %s", recorder.Body.String())
	}
	// Proving ownership is a read: nothing may reach the gateway's write path.
	for _, call := range gateway.recorded() {
		if call.method != http.MethodGet {
			t.Errorf("forwarded %s %s, want only the ownership lookup", call.method, call.path)
		}
	}

	// The caller's own id is an update, not a conflict.
	before := len(gateway.recorded())
	own := httptest.NewRecorder()
	handler.HandleApps(own, artifactAppsRequest(441, http.MethodPost, "/api/artifacts/apps",
		`{"id":"mine","title":"我的看板","publicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA example"}`))
	if own.Code != http.StatusCreated {
		t.Fatalf("re-publishing the caller's own application: status = %d, body = %s", own.Code, own.Body.String())
	}
	if len(gateway.recorded()) != before+2 {
		t.Errorf("gateway calls = %d, want a lookup and a write", len(gateway.recorded())-before)
	}
}

func TestArtifactAppsRefusesAnotherAccountsApp(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(
		artifactApp{ID: "theirs", Agent: "365", RemotePort: 28194, URL: "https://artifact.catsco.cc/theirs/"},
		artifactApp{ID: "mine", Agent: "441", RemotePort: 28193, URL: "https://artifact.catsco.cc/mine/"},
	)
	handler := gateway.handler()

	for _, testCase := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "read someone else's app", method: http.MethodGet, path: "/api/artifacts/apps/theirs"},
		{name: "delete someone else's app", method: http.MethodDelete, path: "/api/artifacts/apps/theirs"},
		{name: "read an app that does not exist", method: http.MethodGet, path: "/api/artifacts/apps/unknown-board"},
		{name: "delete an app that does not exist", method: http.MethodDelete, path: "/api/artifacts/apps/unknown-board"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.HandleApps(recorder, artifactAppsRequest(441, testCase.method, testCase.path, ""))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", recorder.Code, recorder.Body.String())
			}
		})
	}

	// The gateway's delete is not owner-scoped, so a refused delete must never
	// have been forwarded at all.
	for _, call := range gateway.recorded() {
		if call.method == http.MethodDelete {
			t.Errorf("a delete reached the gateway: %+v", call)
		}
	}
}

func TestArtifactAppsDeleteRemovesTheCallersOwnApp(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(artifactApp{ID: "saturday-board", Agent: "441", RemotePort: 28193, URL: "https://artifact.catsco.cc/saturday-board/"})
	handler := gateway.handler()
	recorder := httptest.NewRecorder()
	handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodDelete, "/api/artifacts/apps/saturday-board", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"status":"removed"`) || !strings.Contains(recorder.Body.String(), `"id":"saturday-board"`) {
		t.Errorf("body = %s", recorder.Body.String())
	}
	calls := gateway.recorded()
	if len(calls) != 2 {
		t.Fatalf("gateway calls = %d, want a ownership lookup then a delete: %+v", len(calls), calls)
	}
	if calls[0].method != http.MethodGet {
		t.Errorf("first call = %s %s, want the ownership lookup", calls[0].method, calls[0].path)
	}
	if calls[1].method != http.MethodDelete || calls[1].path != artifactAppsGatewayPath+"/saturday-board" {
		t.Errorf("second call = %s %s, want DELETE %s/saturday-board", calls[1].method, calls[1].path, artifactAppsGatewayPath)
	}
}

func TestArtifactAppsMapsGatewayFailures(t *testing.T) {
	cases := []struct {
		name       string
		upstream   int
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name: "validation error is relayed", upstream: http.StatusBadRequest,
			body: `{"error":"localPort 20000 is already in use by another application"}`, wantStatus: http.StatusBadRequest,
			wantBody: "localPort 20000 is already in use by another application",
		},
		{
			name: "validation error without a message", upstream: http.StatusBadRequest,
			body: `{"oops":true}`, wantStatus: http.StatusBadRequest, wantBody: "artifact_request_invalid",
		},
		{name: "unknown application", upstream: http.StatusNotFound, body: `{"error":"unknown app"}`, wantStatus: http.StatusNotFound, wantBody: "artifact_app_not_found"},
		{name: "our token rejected", upstream: http.StatusUnauthorized, body: `{"error":"unauthorized"}`, wantStatus: http.StatusBadGateway, wantBody: "artifact_gateway_unauthorized"},
		{name: "our token forbidden", upstream: http.StatusForbidden, body: `{"error":"forbidden"}`, wantStatus: http.StatusBadGateway, wantBody: "artifact_gateway_unauthorized"},
		{name: "gateway failure", upstream: http.StatusInternalServerError, body: `{}`, wantStatus: http.StatusBadGateway, wantBody: "artifact_gateway_unavailable"},
		{name: "gateway overloaded", upstream: http.StatusServiceUnavailable, body: `{}`, wantStatus: http.StatusBadGateway, wantBody: "artifact_gateway_unavailable"},
		{name: "invalid success payload", upstream: http.StatusCreated, body: `{"id":`, wantStatus: http.StatusBadGateway, wantBody: "artifact_gateway_unavailable"},
		{name: "success payload without an id", upstream: http.StatusCreated, body: `{"status":"registered"}`, wantStatus: http.StatusBadGateway, wantBody: "artifact_gateway_unavailable"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := newArtifactAppsGateway(t)
			gateway.setRespond(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.upstream)
				_, _ = w.Write([]byte(testCase.body))
			})
			recorder := httptest.NewRecorder()
			gateway.handler().HandleApps(recorder, artifactAppsRequest(441, http.MethodPost, "/api/artifacts/apps",
				`{"id":"saturday-board","title":"看板","publicKey":"ssh-ed25519 AAAA"}`))
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), testCase.wantBody) {
				t.Errorf("body = %s, want %s", recorder.Body.String(), testCase.wantBody)
			}
			// Our own credential problem must never look like the caller's.
			if testCase.upstream == http.StatusUnauthorized || testCase.upstream == http.StatusForbidden {
				if strings.Contains(recorder.Body.String(), `"error":"unauthorized"`) {
					t.Errorf("the gateway's own 401 was passed on as a caller problem: %s", recorder.Body.String())
				}
			}
		})
	}
}

// A list that fails must not be answered as "you have no applications".
func TestArtifactAppsListFailureIsNotAnEmptyList(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		upstream int
		body     string
	}{
		{name: "gateway failure", upstream: http.StatusInternalServerError, body: `{"error":"boom"}`},
		{name: "unreadable list", upstream: http.StatusOK, body: `{"apps":"not-a-list"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := newArtifactAppsGateway(t)
			gateway.setRespond(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.upstream)
				_, _ = w.Write([]byte(testCase.body))
			})
			recorder := httptest.NewRecorder()
			gateway.handler().HandleApps(recorder, artifactAppsRequest(441, http.MethodGet, "/api/artifacts/apps", ""))
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body %s)", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "artifact_gateway_unavailable") {
				t.Errorf("body = %s", recorder.Body.String())
			}
		})
	}
}

func TestArtifactAppsMapsAGatewayTimeout(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setDelay(200 * time.Millisecond)
	handler := &ArtifactAppsHandler{
		gatewayURL: gateway.server.URL,
		token:      "test-gateway-token-0123456789abcdef",
		httpClient: &http.Client{Timeout: 20 * time.Millisecond},
	}
	recorder := httptest.NewRecorder()
	handler.HandleApps(recorder, artifactAppsRequest(441, http.MethodPost, "/api/artifacts/apps",
		`{"id":"saturday-board","title":"看板","publicKey":"ssh-ed25519 AAAA"}`))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "artifact_gateway_unavailable") {
		t.Errorf("body = %s", recorder.Body.String())
	}
}

func TestArtifactAppsRoutesOwnTheAppsPrefix(t *testing.T) {
	gateway := newArtifactAppsGateway(t)
	gateway.setApps(artifactApp{ID: "mine", Agent: "441", RemotePort: 28193, URL: "https://artifact.catsco.cc/mine/"})
	handler := gateway.handler()

	// The same registration server/cmd/server.go performs, next to the existing
	// artifact routes: registering these on one mux must not conflict with the
	// /api/artifacts/ subtree, and the more specific patterns must win.
	mux := http.NewServeMux()
	subtree := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusTeapot, map[string]string{"error": "artifact subtree"})
	}
	mux.HandleFunc("/api/artifacts", subtree)
	mux.HandleFunc("/api/artifacts/", subtree)
	mux.HandleFunc(artifactAppsAPIPath, handler.HandleApps)
	mux.HandleFunc(artifactAppsAPIPath+"/", handler.HandleApps)

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string
	}{
		{name: "collection reaches the apps handler", method: http.MethodGet, path: "/api/artifacts/apps", wantStatus: http.StatusOK},
		{name: "collection with a trailing slash", method: http.MethodGet, path: "/api/artifacts/apps/", wantStatus: http.StatusOK},
		{name: "item reaches the apps handler", method: http.MethodGet, path: "/api/artifacts/apps/mine", wantStatus: http.StatusOK},
		{name: "unsupported collection method", method: http.MethodPut, path: "/api/artifacts/apps", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET, POST"},
		{name: "unsupported item method", method: http.MethodPatch, path: "/api/artifacts/apps/mine", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET, DELETE"},
		{name: "the artifact subtree is untouched", method: http.MethodGet, path: "/api/artifacts/saturday-demo", wantStatus: http.StatusTeapot},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, artifactAppsRequest(441, testCase.method, testCase.path, ""))
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if testCase.wantAllow != "" && recorder.Header().Get("Allow") != testCase.wantAllow {
				t.Errorf("Allow = %q, want %q", recorder.Header().Get("Allow"), testCase.wantAllow)
			}
		})
	}
}

func TestArtifactAppsItemID(t *testing.T) {
	valid := map[string]string{
		"/api/artifacts/apps/saturday-board": "saturday-board",
		"/api/artifacts/apps/a":              "a",
	}
	for path, want := range valid {
		got, ok := artifactAppsItemID(path)
		if !ok || got != want {
			t.Errorf("artifactAppsItemID(%q) = %q/%v, want %q", path, got, ok, want)
		}
	}
	for _, path := range []string{
		"/api/artifacts/apps",
		"/api/artifacts/apps/",
		"/api/artifacts/apps/saturday-board/",
		"/api/artifacts/apps/../etc",
		"/api/artifacts/apps/Saturday",
		"/api/artifacts/apps/a/b",
	} {
		if got, ok := artifactAppsItemID(path); ok {
			t.Errorf("artifactAppsItemID(%q) = %q, want a refusal", path, got)
		}
	}
}

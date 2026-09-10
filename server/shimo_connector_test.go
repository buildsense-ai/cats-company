package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var shimoTestSecret = []byte("0123456789abcdef0123456789abcdef")

func TestShimoConnectorMockFlowAndActorIsolation(t *testing.T) {
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "https://app.catsco.test",
		Backend:     mockShimoConnectorBackend{},
	})
	actorA := mustShimoActorToken(t, "agent-42", "user-a", "task-a")
	actorB := mustShimoActorToken(t, "agent-42", "user-b", "task-b")

	statusA := callShimoJSON(t, handler.HandleConnection, http.MethodGet, "/v1/shimo/connection", actorA, "")
	assertShimoStatus(t, statusA, http.StatusOK, true)
	if got := nestedString(statusA.body, "data", "state"); got != "disconnected" {
		t.Fatalf("initial state=%q", got)
	}
	bindingA := stringValueForTest(statusA.body["connection_binding"])
	if bindingA == "" {
		t.Fatal("missing actor A connection binding")
	}

	link := callShimoJSON(t, handler.HandleConnectionLink, http.MethodPost, "/v1/shimo/connection-link", actorA, "")
	assertShimoStatus(t, link, http.StatusOK, true)
	connectionURL := nestedString(link.body, "data", "connection_url")
	parsed, err := url.Parse(connectionURL)
	if err != nil || !strings.HasPrefix(parsed.Path, "/connect/shimo/") {
		t.Fatalf("unexpected connection URL %q: %v", connectionURL, err)
	}
	rawLoginToken := strings.TrimPrefix(parsed.Path, "/connect/shimo/")
	handler.mu.Lock()
	for storedKey := range handler.attempts {
		if storedKey == rawLoginToken {
			handler.mu.Unlock()
			t.Fatal("raw one-time login token was stored")
		}
	}
	handler.mu.Unlock()

	getLogin := httptest.NewRecorder()
	handler.HandleLoginAttempt(getLogin, httptest.NewRequest(http.MethodGet, parsed.Path, nil))
	if getLogin.Code != http.StatusOK || !strings.Contains(getLogin.Body.String(), "模拟登录成功") {
		t.Fatalf("GET login status=%d body=%s", getLogin.Code, getLogin.Body.String())
	}
	completeLogin := httptest.NewRecorder()
	handler.HandleLoginAttempt(completeLogin, httptest.NewRequest(http.MethodPost, parsed.Path, nil))
	if completeLogin.Code != http.StatusOK || !strings.Contains(completeLogin.Body.String(), "连接成功") {
		t.Fatalf("POST login status=%d body=%s", completeLogin.Code, completeLogin.Body.String())
	}
	reuseLogin := httptest.NewRecorder()
	handler.HandleLoginAttempt(reuseLogin, httptest.NewRequest(http.MethodPost, parsed.Path, nil))
	if reuseLogin.Code != http.StatusGone {
		t.Fatalf("reused login status=%d, want %d", reuseLogin.Code, http.StatusGone)
	}

	statusA = callShimoJSON(t, handler.HandleConnection, http.MethodGet, "/v1/shimo/connection", actorA, "")
	if got := nestedString(statusA.body, "data", "state"); got != "connected" {
		t.Fatalf("connected state=%q", got)
	}
	if stringValueForTest(statusA.body["connection_binding"]) != bindingA {
		t.Fatal("actor A connection binding changed within the same session")
	}

	readA := callShimoJSON(t, handler.HandleReadSheet, http.MethodPost, "/v1/shimo/sheets/read", actorA,
		`{"url":"https://shimo.im/sheets/abc/?temporary=1","sheet_name":"项目表","range":"A1:C20"}`)
	assertShimoStatus(t, readA, http.StatusOK, true)
	values, ok := readA.body["data"].(map[string]any)["values"].([]any)
	if !ok || len(values) != 2 {
		t.Fatalf("unexpected mock values: %#v", readA.body["data"])
	}

	readB := callShimoJSON(t, handler.HandleReadSheet, http.MethodPost, "/v1/shimo/sheets/read", actorB,
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:C20"}`)
	assertShimoError(t, readB, http.StatusConflict, "LOGIN_REQUIRED")
	statusB := callShimoJSON(t, handler.HandleConnection, http.MethodGet, "/v1/shimo/connection", actorB, "")
	if got := nestedString(statusB.body, "data", "state"); got != "disconnected" {
		t.Fatalf("actor B state=%q", got)
	}
	if stringValueForTest(statusB.body["connection_binding"]) == bindingA {
		t.Fatal("two actors received the same connection binding")
	}

	identityInjection := callShimoJSON(t, handler.HandleReadSheet, http.MethodPost, "/v1/shimo/sheets/read", actorA,
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:C20","actor_user_id":"user-b"}`)
	assertShimoError(t, identityInjection, http.StatusBadRequest, "INVALID_ARGUMENTS")

	disconnectA := callShimoJSON(t, handler.HandleConnection, http.MethodDelete, "/v1/shimo/connection", actorA, "")
	assertShimoStatus(t, disconnectA, http.StatusOK, true)
	readAfterDisconnect := callShimoJSON(t, handler.HandleReadSheet, http.MethodPost, "/v1/shimo/sheets/read", actorA,
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:C20"}`)
	assertShimoError(t, readAfterDisconnect, http.StatusConflict, "LOGIN_REQUIRED")
}

func TestShimoConnectorRejectsMissingAndWrongAudienceTokens(t *testing.T) {
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "https://app.catsco.test",
		Backend:     mockShimoConnectorBackend{},
	})
	missing := callShimoJSON(t, handler.HandleConnection, http.MethodGet, "/v1/shimo/connection", "", "")
	assertShimoError(t, missing, http.StatusUnauthorized, "UNAUTHORIZED")

	now := time.Now().UTC()
	claims := ShimoActorClaims{
		AgentUID: "agent-42", ActorUID: "user-a", Capability: "shimo:connect shimo:read",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: shimoConnectorIssuer, Audience: jwt.ClaimStrings{"wrong-audience"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
	}
	wrongAudience, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(shimoTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	rejected := callShimoJSON(t, handler.HandleConnection, http.MethodGet, "/v1/shimo/connection", wrongAudience, "")
	assertShimoError(t, rejected, http.StatusUnauthorized, "UNAUTHORIZED")
}

func TestShimoConnectorDisabledBackendFailsClosed(t *testing.T) {
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "https://app.catsco.test",
	})
	token := mustShimoActorToken(t, "agent-42", "user-a", "task-a")
	result := callShimoJSON(t, handler.HandleConnectionLink, http.MethodPost, "/v1/shimo/connection-link", token, "")
	assertShimoError(t, result, http.StatusServiceUnavailable, "CONNECTOR_NOT_CONFIGURED")
}

func TestGenerateShimoActorTokenRequiresTaskAndShortTTL(t *testing.T) {
	if _, err := GenerateShimoActorToken(shimoTestSecret, "agent-42", "user-a", "", "catsco/shimo-reader", time.Minute); err == nil {
		t.Fatal("missing task_ref should be rejected")
	}
	if _, err := GenerateShimoActorToken(shimoTestSecret, "agent-42", "user-a", "task-a", "catsco/shimo-reader", 11*time.Minute); err == nil {
		t.Fatal("token ttl above maximum should be rejected")
	}
}

func TestExpiredAttemptCannotClearNewerAttemptState(t *testing.T) {
	now := time.Now().UTC()
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret,
		PublicURL:   "https://app.catsco.test",
		Backend:     mockShimoConnectorBackend{},
		Now:         func() time.Time { return now },
	})
	key := shimoConnectionKey{agentUID: "agent-42", actorUID: "user-a"}
	handler.attempts["old"] = &shimoLoginAttempt{id: "old-attempt", key: key, tokenHash: "old", expiresAt: now.Add(-time.Second)}
	handler.attempts["new"] = &shimoLoginAttempt{id: "new-attempt", key: key, tokenHash: "new", expiresAt: now.Add(time.Minute)}
	handler.connections[key] = shimoConnection{state: "connecting", attemptID: "new-attempt"}
	handler.pruneAttemptsLocked(now)
	if connection, ok := handler.connections[key]; !ok || connection.attemptID != "new-attempt" {
		t.Fatalf("newer attempt state was removed: %#v ok=%v", connection, ok)
	}
}

func TestShimoLoginCompleteRequiresWorkerAuthAndSurvivesTemporaryOffline(t *testing.T) {
	const workerToken = "worker-token-that-is-longer-than-thirty-two-characters"
	now := time.Now().UTC()
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret, WorkerToken: workerToken, Now: func() time.Time { return now },
	})
	rawToken := strings.Repeat("f", 64)
	hash := shimoSHA256Hex(rawToken)
	handler.resumes[hash] = &shimoLoginResume{
		key:     shimoConnectionKey{agentUID: "usr42", actorUID: "usr7"},
		taskRef: "catsco:p2p_7_42:91", topicID: "p2p_7_42", messageID: 91,
		tokenHash: hash, expiresAt: now.Add(time.Minute),
	}
	deliveries := 0
	handler.SetLoginResumePublisher(func(ShimoLoginResume) bool {
		deliveries++
		return deliveries > 1
	})

	call := func(authorization string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/internal/shimo/login-complete", strings.NewReader(`{"completion_token":"`+rawToken+`"}`))
		request.Header.Set("Authorization", authorization)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.HandleLoginComplete(response, request)
		return response
	}
	if response := call("Bearer wrong"); response.Code != http.StatusUnauthorized || deliveries != 0 {
		t.Fatalf("unauthorized callback status=%d deliveries=%d", response.Code, deliveries)
	}
	if response := call("Bearer " + workerToken); response.Code != http.StatusConflict || deliveries != 1 {
		t.Fatalf("offline callback status=%d deliveries=%d body=%s", response.Code, deliveries, response.Body.String())
	}
	if response := call("Bearer " + workerToken); response.Code != http.StatusAccepted || deliveries != 2 {
		t.Fatalf("retry callback status=%d deliveries=%d body=%s", response.Code, deliveries, response.Body.String())
	}
	if response := call("Bearer " + workerToken); response.Code != http.StatusGone || deliveries != 2 {
		t.Fatalf("reused callback status=%d deliveries=%d", response.Code, deliveries)
	}
}

type shimoHTTPResult struct {
	status int
	body   map[string]any
}

func callShimoJSON(t *testing.T, handler http.HandlerFunc, method, target, token, body string) shimoHTTPResult {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-CatsCo-Skill-ID", "catsco/shimo-reader")
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode status=%d body=%q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return shimoHTTPResult{status: recorder.Code, body: decoded}
}

func mustShimoActorToken(t *testing.T, agentUID, actorUID, taskRef string) string {
	t.Helper()
	token, err := GenerateShimoActorToken(shimoTestSecret, agentUID, actorUID, taskRef, "catsco/shimo-reader", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func assertShimoStatus(t *testing.T, result shimoHTTPResult, wantStatus int, wantOK bool) {
	t.Helper()
	if result.status != wantStatus || result.body["ok"] != wantOK {
		t.Fatalf("status=%d body=%#v", result.status, result.body)
	}
}

func assertShimoError(t *testing.T, result shimoHTTPResult, wantStatus int, wantCode string) {
	t.Helper()
	if result.status != wantStatus || result.body["ok"] != false {
		t.Fatalf("status=%d body=%#v", result.status, result.body)
	}
	if got := nestedString(result.body, "error", "code"); got != wantCode {
		t.Fatalf("error code=%q want=%q body=%#v", got, wantCode, result.body)
	}
}

func nestedString(value map[string]any, outer, inner string) string {
	nested, _ := value[outer].(map[string]any)
	return stringValueForTest(nested[inner])
}

func stringValueForTest(value any) string {
	text, _ := value.(string)
	return text
}

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestLaunchHandler(gateway *httptest.Server) *ArtifactLaunchHandler {
	return &ArtifactLaunchHandler{
		gatewayURL:    gateway.URL,
		launchOrigins: []string{gateway.URL},
		controlToken:  "test-control-token-0123456789abcdef",
		httpClient:    gateway.Client(),
	}
}

func launchRequest(uid int64, body string) *http.Request {
	return launchRequestAs(uid, "", body)
}

func launchRequestAs(uid int64, username, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/artifacts/launch", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if uid > 0 {
		request = request.WithContext(context.WithValue(request.Context(), uidKey, uid))
	}
	if username != "" {
		// Mirrors the middleware, which stores the username next to the uid.
		request = request.WithContext(context.WithValue(request.Context(), usernameKey, username))
	}
	return request
}

func TestArtifactLaunchIssuesCodeForAuthenticatedUser(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"app_id":"saturday-demo","code":"the-code","expires_at":"2026-09-17T09:00:00Z","launch_url":"http://` + r.Host + `/_launch/the-code?next=/saturday-demo/"}`))
	}))
	defer gateway.Close()

	handler := newTestLaunchHandler(gateway)
	recorder := httptest.NewRecorder()
	handler.HandleLaunch(recorder, launchRequestAs(441, "saturday", `{"app":"saturday-demo","topic_id":"topic-1"}`))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotPath != artifactLaunchPath {
		t.Errorf("upstream path = %q, want %q", gotPath, artifactLaunchPath)
	}
	if gotAuth != "Bearer "+handler.controlToken {
		t.Errorf("upstream authorization = %q", gotAuth)
	}
	// The code owner must come from the session, not from the request body.
	var forwarded map[string]any
	if err := json.Unmarshal([]byte(gotBody), &forwarded); err != nil {
		t.Fatalf("upstream body is not JSON: %s", gotBody)
	}
	if forwarded["uid"] != "441" {
		t.Errorf("forwarded uid = %v, want the authenticated 441", forwarded["uid"])
	}
	if forwarded["app"] != "saturday-demo" || forwarded["topic"] != "topic-1" {
		t.Errorf("forwarded app/topic = %v/%v", forwarded["app"], forwarded["topic"])
	}
	if forwarded["username"] != "saturday" {
		t.Errorf("forwarded username = %v, want the authenticated 441's username", forwarded["username"])
	}
	// The host is the platform origin the user came from; the gateway matches it
	// against its own allow-list, so it must be forwarded untouched.
	if forwarded["host"] != "example.com" {
		t.Errorf("forwarded host = %v, want the request host", forwarded["host"])
	}
	for _, field := range []string{"app_id", "code", "expires_at", "launch_url"} {
		if !strings.Contains(recorder.Body.String(), `"`+field+`"`) {
			t.Errorf("response is missing %s: %s", field, recorder.Body.String())
		}
	}
}

// A session without a username is still forwarded, with the field empty, so the
// gateway always sees the same payload shape.
func TestArtifactLaunchForwardsAnEmptyUsernameWithoutGuessingOne(t *testing.T) {
	var forwarded map[string]any
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &forwarded)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"app_id":"saturday-demo","code":"code","expires_at":"2026-09-17T09:00:00Z","launch_url":"http://` + r.Host + `/_launch/code"}`))
	}))
	defer gateway.Close()

	request := launchRequest(441, `{"app":"saturday-demo"}`)
	recorder := httptest.NewRecorder()
	newTestLaunchHandler(gateway).HandleLaunch(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if forwarded["username"] != "" {
		t.Errorf("forwarded username = %v, want an empty string", forwarded["username"])
	}
	if forwarded["host"] != request.Host {
		t.Errorf("forwarded host = %v, want %q", forwarded["host"], request.Host)
	}
}

func TestArtifactLaunchRejectsBadInputWithoutCallingGateway(t *testing.T) {
	calls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
	}))
	defer gateway.Close()
	handler := newTestLaunchHandler(gateway)

	cases := []struct {
		name string
		uid  int64
		body string
		want int
	}{
		{"missing session", 0, `{"app":"saturday-demo"}`, http.StatusUnauthorized},
		{"missing app", 441, `{}`, http.StatusBadRequest},
		{"app with path", 441, `{"app":"../etc"}`, http.StatusBadRequest},
		{"app uppercase", 441, `{"app":"Saturday"}`, http.StatusBadRequest},
		{"broken json", 441, `{"app":`, http.StatusBadRequest},
		{"topic too long", 441, `{"app":"saturday-demo","topic_id":"` + strings.Repeat("t", 200) + `"}`, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.HandleLaunch(recorder, launchRequest(testCase.uid, testCase.body))
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
	if calls != 0 {
		t.Errorf("gateway was called %d times for invalid input", calls)
	}
}

func TestArtifactLaunchMapsUpstreamFailures(t *testing.T) {
	cases := []struct {
		name       string
		upstream   int
		launch     func(r *http.Request) string
		wantStatus int
		wantCode   string
	}{
		{name: "unknown application", upstream: http.StatusNotFound, wantStatus: http.StatusNotFound, wantCode: "artifact_app_not_found"},
		{name: "our credential rejected", upstream: http.StatusUnauthorized, wantStatus: http.StatusBadGateway, wantCode: "artifact_gateway_unauthorized"},
		{name: "gateway failure", upstream: http.StatusInternalServerError, wantStatus: http.StatusBadGateway, wantCode: "artifact_gateway_unavailable"},
		{
			name: "launch url on another origin", upstream: http.StatusCreated, wantStatus: http.StatusBadGateway, wantCode: "artifact_gateway_unavailable",
			launch: func(*http.Request) string { return "https://evil.example/_launch/code" },
		},
		{
			// Shares the gateway's string prefix while pointing elsewhere: the
			// old HasPrefix check accepted this, so it is covered explicitly.
			name: "launch url only sharing the gateway prefix", upstream: http.StatusCreated, wantStatus: http.StatusBadGateway, wantCode: "artifact_gateway_unavailable",
			launch: func(r *http.Request) string { return "http://" + r.Host + ".evil.example/_launch/code" },
		},
		{
			name: "launch url downgrading the scheme", upstream: http.StatusCreated, wantStatus: http.StatusBadGateway, wantCode: "artifact_gateway_unavailable",
			launch: func(r *http.Request) string { return "https://" + r.Host + "/_launch/code" },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.upstream)
				if testCase.upstream == http.StatusCreated {
					launch := "http://" + r.Host + "/_launch/code"
					if testCase.launch != nil {
						launch = testCase.launch(r)
					}
					_, _ = w.Write([]byte(`{"app_id":"saturday-demo","code":"code","expires_at":"2026-09-17T09:00:00Z","launch_url":"` + launch + `"}`))
				}
			}))
			defer gateway.Close()

			recorder := httptest.NewRecorder()
			newTestLaunchHandler(gateway).HandleLaunch(recorder, launchRequest(441, `{"app":"saturday-demo"}`))
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), testCase.wantCode) {
				t.Errorf("body = %s, want code %s", recorder.Body.String(), testCase.wantCode)
			}
		})
	}
}

func TestLaunchURLBelongsToGateway(t *testing.T) {
	const gateway = "https://artifact.catsco.cc"
	origins := []string{gateway}
	allowed := []string{
		"https://artifact.catsco.cc/_launch/abc?next=/saturday-demo/",
		"https://ARTIFACT.CATSCO.CC/_launch/abc",
	}
	for _, launch := range allowed {
		if !launchURLBelongsToGateway(launch, origins) {
			t.Errorf("should be accepted: %s", launch)
		}
	}
	rejected := []string{
		"https://artifact.catsco.cc.evil.example/_launch/abc",
		"http://artifact.catsco.cc/_launch/abc",
		"https://evil.example/_launch/abc",
		"//artifact.catsco.cc/_launch/abc",
		"/_launch/abc",
		"",
		// The gateway's other domain is a foreign origin until it is listed.
		"https://artifact.catsco.cn/_launch/abc",
	}
	for _, launch := range rejected {
		if launchURLBelongsToGateway(launch, origins) {
			t.Errorf("should be rejected: %s", launch)
		}
	}

	// Once the second public domain is configured, a launch on it is legitimate —
	// that is the whole point of the list, and without it a `.cn` visitor would
	// get a 502 instead of an identity.
	both := []string{gateway, "https://artifact.catsco.cn"}
	for _, launch := range []string{"https://artifact.catsco.cc/_launch/a", "https://artifact.catsco.cn/_launch/a"} {
		if !launchURLBelongsToGateway(launch, both) {
			t.Errorf("should be accepted once listed: %s", launch)
		}
	}
	for _, launch := range []string{"https://evil.example/_launch/a", "https://artifact.catsco.com/_launch/a"} {
		if launchURLBelongsToGateway(launch, both) {
			t.Errorf("listing a second origin must not widen the rule: %s", launch)
		}
	}
}

func TestArtifactLaunchReadsEveryGatewayOrigin(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", strings.Repeat("a", 32))
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URLS", "")
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URL", "https://artifact.catsco.cc")
	handler := NewArtifactLaunchHandlerFromEnv()
	if !handler.Enabled() || len(handler.launchOrigins) != 1 {
		t.Fatalf("unset list must keep exactly the outbound origin, got %v", handler.launchOrigins)
	}

	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URLS", " https://artifact.catsco.cn , ")
	handler = NewArtifactLaunchHandlerFromEnv()
	if !handler.Enabled() {
		t.Fatal("a valid extra origin must be accepted")
	}
	if len(handler.launchOrigins) != 2 || handler.launchOrigins[1] != "https://artifact.catsco.cn" {
		t.Fatalf("origins = %v", handler.launchOrigins)
	}
	if handler.gatewayURL != "https://artifact.catsco.cc" {
		t.Fatalf("the outbound endpoint must not move: %s", handler.gatewayURL)
	}

	for _, bad := range []string{
		"http://artifact.catsco.cn",
		"https://artifact.catsco.cn/extra",
		"https://artifact.catsco.cn?x=1",
		"not-a-url",
	} {
		t.Setenv("CATSCO_ARTIFACT_GATEWAY_URLS", bad)
		if NewArtifactLaunchHandlerFromEnv().Enabled() {
			t.Fatalf("must be rejected: %s", bad)
		}
	}
}

func TestArtifactLaunchUnconfiguredAndWrongMethod(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URL", "")
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", "")
	if NewArtifactLaunchHandlerFromEnv().Enabled() {
		t.Fatal("handler without configuration must not be enabled")
	}
	recorder := httptest.NewRecorder()
	NewArtifactLaunchHandlerFromEnv().HandleLaunch(recorder, launchRequest(441, `{"app":"saturday-demo"}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status = %d", recorder.Code)
	}

	t.Setenv("CATSCO_ARTIFACT_GATEWAY_URL", "http://artifact.example.cc")
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", strings.Repeat("a", 32))
	if NewArtifactLaunchHandlerFromEnv().Enabled() {
		t.Fatal("plain http gateway must not be accepted")
	}

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer gateway.Close()
	recorder = httptest.NewRecorder()
	handler := newTestLaunchHandler(gateway)
	handler.HandleLaunch(recorder, httptest.NewRequest(http.MethodGet, "/api/artifacts/launch", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", recorder.Code)
	}
	if recorder.Header().Get("Allow") != http.MethodPost {
		t.Errorf("Allow header = %q", recorder.Header().Get("Allow"))
	}
}

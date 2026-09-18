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
		gatewayURL:   gateway.URL,
		controlToken: "test-control-token-0123456789abcdef",
		httpClient:   gateway.Client(),
	}
}

func launchRequest(uid int64, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/artifacts/launch", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if uid > 0 {
		request = request.WithContext(context.WithValue(request.Context(), uidKey, uid))
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
	handler.HandleLaunch(recorder, launchRequest(441, `{"app":"saturday-demo","topic_id":"topic-1"}`))

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
	for _, field := range []string{"app_id", "code", "expires_at", "launch_url"} {
		if !strings.Contains(recorder.Body.String(), `"`+field+`"`) {
			t.Errorf("response is missing %s: %s", field, recorder.Body.String())
		}
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
		foreignURL bool
		wantStatus int
		wantCode   string
	}{
		{"unknown application", http.StatusNotFound, false, http.StatusNotFound, "artifact_app_not_found"},
		{"our credential rejected", http.StatusUnauthorized, false, http.StatusBadGateway, "artifact_gateway_unauthorized"},
		{"gateway failure", http.StatusInternalServerError, false, http.StatusBadGateway, "artifact_gateway_unavailable"},
		{"foreign launch url", http.StatusCreated, true, http.StatusBadGateway, "artifact_gateway_unavailable"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.upstream)
				if testCase.upstream == http.StatusCreated {
					launch := "http://" + r.Host + "/_launch/code"
					if testCase.foreignURL {
						launch = "https://evil.example/_launch/code"
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

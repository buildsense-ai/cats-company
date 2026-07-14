package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImageGenerationProxyHandlerForwardsRequestAndForcesPolicy(t *testing.T) {
	var upstreamAuthorization string
	var upstreamMethod string
	var upstreamPath string
	var upstreamUserAgent string
	var upstreamPayload map[string]interface{}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuthorization = r.Header.Get("Authorization")
		upstreamMethod = r.Method
		upstreamPath = r.URL.Path
		upstreamUserAgent = r.Header.Get("User-Agent")
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("failed to decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "provider-request-1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"aW1hZ2U="}]}`))
	}))
	defer upstream.Close()

	handler := NewImageGenerationProxyHandler(
		upstream.URL+"/v1/images/generations",
		ImageGenerationProxyOptions{
			Timeout:         5 * time.Second,
			MaxRequestBytes: 1 << 20,
			Model:           "gpt-image-2",
			APIKey:          "provider-secret",
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{
		"model":"client-model",
		"prompt":"one red circle and one blue square",
		"n":4,
		"size":"1024x1024",
		"quality":"medium"
	}`))
	req.Header.Set("Authorization", "ApiKey catsco-bot-key")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	rr := httptest.NewRecorder()

	handler.HandleGenerate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != `{"data":[{"b64_json":"aW1hZ2U="}]}` {
		t.Fatalf("unexpected response body: %q", got)
	}
	if upstreamMethod != http.MethodPost || upstreamPath != "/v1/images/generations" {
		t.Fatalf("unexpected upstream target: %s %s", upstreamMethod, upstreamPath)
	}
	if upstreamAuthorization != "Bearer provider-secret" {
		t.Fatalf("client identity leaked or provider auth missing: %q", upstreamAuthorization)
	}
	if upstreamUserAgent != "cats-company-image-proxy/1.0" {
		t.Fatalf("unexpected upstream user agent: %q", upstreamUserAgent)
	}
	if upstreamPayload["model"] != "gpt-image-2" {
		t.Fatalf("expected server model policy, got %#v", upstreamPayload["model"])
	}
	if upstreamPayload["n"] != float64(1) {
		t.Fatalf("expected n=1, got %#v", upstreamPayload["n"])
	}
	if upstreamPayload["prompt"] != "one red circle and one blue square" {
		t.Fatalf("prompt was not preserved: %#v", upstreamPayload["prompt"])
	}
	if upstreamPayload["size"] != "1024x1024" || upstreamPayload["quality"] != "medium" {
		t.Fatalf("supported request fields were not preserved: %#v", upstreamPayload)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected no-store response")
	}
	if rr.Header().Get("X-Request-Id") != "provider-request-1" {
		t.Fatalf("expected upstream request id to be preserved")
	}
}

func TestImageGenerationProxyHandlerRejectsInvalidRequests(t *testing.T) {
	handler := NewImageGenerationProxyHandler(
		"http://127.0.0.1:1/v1/images/generations",
		ImageGenerationProxyOptions{APIKey: "provider-secret", MaxRequestBytes: 64},
	)

	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "wrong method", method: http.MethodGet, contentType: "application/json", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "wrong content type", method: http.MethodPost, contentType: "text/plain", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "invalid json", method: http.MethodPost, contentType: "application/json", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "missing prompt", method: http.MethodPost, contentType: "application/json", body: `{"size":"1024x1024"}`, wantStatus: http.StatusBadRequest},
		{name: "multiple objects", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"one"} {"prompt":"two"}`, wantStatus: http.StatusBadRequest},
		{name: "too large", method: http.MethodPost, contentType: "application/json", body: `{"prompt":"this request is intentionally much larger than sixty-four bytes","size":"1024x1024"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/v1/images/generations", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rr := httptest.NewRecorder()

			handler.HandleGenerate(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestImageGenerationProxyHandlerReturnsUnavailableWhenMisconfigured(t *testing.T) {
	handler := NewImageGenerationProxyHandler("", ImageGenerationProxyOptions{APIKey: "provider-secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.HandleGenerate(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "provider-secret") {
		t.Fatalf("provider secret leaked in configuration error")
	}
}

func TestImageGenerationProxyHandlerReturnsGatewayTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler := NewImageGenerationProxyHandler(
		upstream.URL,
		ImageGenerationProxyOptions{APIKey: "provider-secret", Timeout: 10 * time.Millisecond},
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.HandleGenerate(rr, req)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestImageGenerationProxyHandlerFromEnvReadsAPIKeyFile(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "image-provider-key")
	if err := os.WriteFile(secretPath, []byte("provider-secret\n"), 0o600); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}

	t.Setenv("CATSCO_IMAGE_UPSTREAM_URL", "https://images.example.com/v1/images/generations")
	t.Setenv("CATSCO_IMAGE_UPSTREAM_API_KEY", "ignored-env-secret")
	t.Setenv("CATSCO_IMAGE_UPSTREAM_API_KEY_FILE", secretPath)
	t.Setenv("CATSCO_IMAGE_MODEL", "company-image-model")
	t.Setenv("CATSCO_IMAGE_TIMEOUT_SECONDS", "30")
	t.Setenv("CATSCO_IMAGE_MAX_REQUEST_BYTES", "2048")

	handler := NewImageGenerationProxyHandlerFromEnv()
	if err := handler.ConfigError(); err != nil {
		t.Fatalf("unexpected configuration error: %v", err)
	}
	if handler.apiKey != "provider-secret" {
		t.Fatalf("expected API key file to take precedence")
	}
	if handler.model != "company-image-model" || handler.maxRequestBytes != 2048 {
		t.Fatalf("environment options were not applied: model=%q max=%d", handler.model, handler.maxRequestBytes)
	}
}

func TestParseImageGenerationUpstreamURLRequiresHTTPSOutsideLoopback(t *testing.T) {
	if _, err := parseImageGenerationUpstreamURL("http://images.example.com/v1/images/generations"); err == nil {
		t.Fatalf("expected public HTTP endpoint to be rejected")
	}
	if _, err := parseImageGenerationUpstreamURL("http://127.0.0.1:8080/v1/images/generations"); err != nil {
		t.Fatalf("expected loopback HTTP endpoint to be accepted: %v", err)
	}
	if _, err := parseImageGenerationUpstreamURL("https://images.example.com/v1/images/generations"); err != nil {
		t.Fatalf("expected HTTPS endpoint to be accepted: %v", err)
	}
}

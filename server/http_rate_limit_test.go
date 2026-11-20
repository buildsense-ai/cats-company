package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPRateLimiterUsesRemoteAddrForDirectRequests(t *testing.T) {
	limiter := NewHTTPRateLimiter()
	handler := limiter.LimitIP(HTTPRateLimitConfig{
		Name: "direct", Limit: 1, Window: time.Hour, Burst: 1,
	})(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req1 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req1.RemoteAddr = "203.0.113.10:12345"
	req1.Header.Set("X-Forwarded-For", "198.51.100.1")
	resp1 := httptest.NewRecorder()
	handler(resp1, req1)
	if resp1.Code != http.StatusNoContent {
		t.Fatalf("first direct request status = %d, want %d", resp1.Code, http.StatusNoContent)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req2.RemoteAddr = "203.0.113.10:54321"
	req2.Header.Set("X-Forwarded-For", "198.51.100.2")
	resp2 := httptest.NewRecorder()
	handler(resp2, req2)
	if resp2.Code != http.StatusTooManyRequests {
		t.Fatalf("second direct request status = %d, want %d", resp2.Code, http.StatusTooManyRequests)
	}
}

func TestHTTPRateLimiterTrustsForwardedForFromLocalProxy(t *testing.T) {
	limiter := NewHTTPRateLimiter()
	handler := limiter.LimitIP(HTTPRateLimitConfig{
		Name: "proxy", Limit: 1, Window: time.Hour, Burst: 1,
	})(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req1 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req1.RemoteAddr = "127.0.0.1:12345"
	req1.Header.Set("X-Forwarded-For", "198.51.100.1")
	resp1 := httptest.NewRecorder()
	handler(resp1, req1)
	if resp1.Code != http.StatusNoContent {
		t.Fatalf("first proxied request status = %d, want %d", resp1.Code, http.StatusNoContent)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.Header.Set("X-Forwarded-For", "198.51.100.2")
	resp2 := httptest.NewRecorder()
	handler(resp2, req2)
	if resp2.Code != http.StatusNoContent {
		t.Fatalf("different forwarded IP status = %d, want %d", resp2.Code, http.StatusNoContent)
	}

	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req3.RemoteAddr = "127.0.0.1:12345"
	req3.Header.Set("X-Forwarded-For", "198.51.100.1")
	resp3 := httptest.NewRecorder()
	handler(resp3, req3)
	if resp3.Code != http.StatusTooManyRequests {
		t.Fatalf("repeated forwarded IP status = %d, want %d", resp3.Code, http.StatusTooManyRequests)
	}
}

func TestHTTPRateLimiterJSONFieldRestoresRequestBody(t *testing.T) {
	limiter := NewHTTPRateLimiter()
	handler := limiter.LimitJSONField(HTTPRateLimitConfig{
		Name: "account", Limit: 1, Window: time.Hour, Burst: 1,
	}, "account")(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("handler could not decode restored body: %v", err)
		}
		if payload["account"] != "demo@example.com" {
			t.Fatalf("account = %q, want demo@example.com", payload["account"])
		}
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"account":"demo@example.com"}`))
	resp := httptest.NewRecorder()
	handler(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
	}
}

func TestHTTPRateLimitExceededReportsBucketRetryAfter(t *testing.T) {
	limiter := NewHTTPRateLimiter()
	handler := limiter.LimitIP(HTTPRateLimitConfig{
		Name: "auth_send_code_slow", Limit: 3, Window: 10 * time.Minute, Burst: 3,
	})(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/send-code", nil)
		req.RemoteAddr = "203.0.113.20:1234"
		resp := httptest.NewRecorder()
		handler(resp, req)
		if resp.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want %d", i+1, resp.Code, http.StatusNoContent)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/send-code", nil)
	req.RemoteAddr = "203.0.113.20:1234"
	resp := httptest.NewRecorder()
	handler(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("exceeded status = %d, want %d", resp.Code, http.StatusTooManyRequests)
	}

	var body struct {
		Error             string `json:"error"`
		RetryAfterSeconds int    `json:"retry_after_seconds"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw %q)", err, resp.Body.String())
	}
	// This bucket refills one token every 200s, so a fixed 60s hint would send
	// the client straight back into the same rejection.
	if body.RetryAfterSeconds < 190 || body.RetryAfterSeconds > 200 {
		t.Fatalf("retry_after_seconds = %d, want 190..200", body.RetryAfterSeconds)
	}
	if got := resp.Header().Get("Retry-After"); got != strconv.Itoa(body.RetryAfterSeconds) {
		t.Fatalf("Retry-After = %q, want %d", got, body.RetryAfterSeconds)
	}
	if body.Error == "" || body.Error == "rate limit exceeded" {
		t.Fatalf("error = %q, want a user-facing Chinese hint", body.Error)
	}
}

func TestHTTPRateLimiterAllowsRelaxedSendCodeBurst(t *testing.T) {
	limiter := NewHTTPRateLimiter()
	handler := limiter.LimitJSONField(HTTPRateLimitConfig{
		Name: "auth_send_code_relaxed", Limit: 6, Window: 10 * time.Minute, Burst: 4,
	}, "email")(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/send-code", strings.NewReader(`{"email":"demo@example.com"}`))
		resp := httptest.NewRecorder()
		handler(resp, req)
		return resp
	}

	// The regression this guards: a user who resends on the 60s UI countdown
	// reached the fourth attempt at ~3 minutes and the old burst of 3 refused it.
	for i := 0; i < 4; i++ {
		if resp := send(); resp.Code != http.StatusNoContent {
			t.Fatalf("send %d status = %d, want %d", i+1, resp.Code, http.StatusNoContent)
		}
	}

	resp := send()
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth send status = %d, want %d", resp.Code, http.StatusTooManyRequests)
	}
	var body struct {
		RetryAfterSeconds int `json:"retry_after_seconds"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw %q)", err, resp.Body.String())
	}
	// 6 per 10 minutes = one token every 100s.
	if body.RetryAfterSeconds < 90 || body.RetryAfterSeconds > 100 {
		t.Fatalf("retry_after_seconds = %d, want 90..100", body.RetryAfterSeconds)
	}
}

func TestHTTPRateLimitFastBucketWaitsAtLeastOneSecond(t *testing.T) {
	limiter := NewHTTPRateLimiter()
	handler := limiter.LimitIP(HTTPRateLimitConfig{
		Name: "auth_login_fast", Limit: 60, Window: time.Minute, Burst: 1,
	})(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	first := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	first.RemoteAddr = "203.0.113.30:1234"
	handler(httptest.NewRecorder(), first)

	second := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	second.RemoteAddr = "203.0.113.30:1234"
	resp := httptest.NewRecorder()
	handler(resp, second)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusTooManyRequests)
	}
	if got := resp.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server"
)

func TestSharedConversationPublicRouteRateLimit(t *testing.T) {
	limiter := server.NewHTTPRateLimiter()
	handler := sharedConversationPublicIPLimit(limiter)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for attempt := 0; attempt < sharedConversationPublicSnapshotIPRateLimit.Burst; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/shared-conversations/capability", nil)
		request.RemoteAddr = "198.51.100.42:12345"
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want %d", attempt+1, response.Code, http.StatusNoContent)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/shared-conversations/capability", nil)
	request.RemoteAddr = "198.51.100.42:12345"
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("request after burst status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
}

func TestSharedConversationPublicAssetFanoutHasIndependentBudget(t *testing.T) {
	limiter := server.NewHTTPRateLimiter()
	handler := sharedConversationPublicIPLimit(limiter)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for attempt := 0; attempt < sharedConversationPublicSnapshotIPRateLimit.Burst; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/shared-conversations/capability", nil)
		request.RemoteAddr = "198.51.100.43:12345"
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("snapshot request %d status = %d, want %d", attempt+1, response.Code, http.StatusNoContent)
		}
	}

	for attempt := 0; attempt < 200; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/shared-conversations/capability/assets/asset", nil)
		request.RemoteAddr = "198.51.100.43:12345"
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("asset request %d status = %d, want %d", attempt+1, response.Code, http.StatusNoContent)
		}
	}

	for attempt := 200; attempt < sharedConversationPublicAssetIPRateLimit.Burst; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/shared-conversations/capability/assets/asset", nil)
		request.RemoteAddr = "198.51.100.43:12345"
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("asset request %d status = %d, want %d", attempt+1, response.Code, http.StatusNoContent)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/shared-conversations/capability/assets/asset", nil)
	request.RemoteAddr = "198.51.100.43:12345"
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("asset request after burst status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
}

func TestSharedConversationCreateRateLimitOnlyCountsPosts(t *testing.T) {
	limiter := server.NewHTTPRateLimiter()
	handler := sharedConversationCreateLimit(limiter)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for attempt := 0; attempt < sharedConversationCreateIPRateLimit.Burst; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/conversation-shares", nil)
		request.RemoteAddr = "198.51.100.44:12345"
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("create request %d status = %d, want %d", attempt+1, response.Code, http.StatusNoContent)
		}
	}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		request := httptest.NewRequest(method, "/api/conversation-shares", nil)
		request.RemoteAddr = "198.51.100.44:12345"
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s after create burst status = %d, want %d", method, response.Code, http.StatusNoContent)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/conversation-shares", nil)
	request.RemoteAddr = "198.51.100.44:12345"
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("create request after burst status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
}

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudArtifactHandlerListsValidatedIndex(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"contract_version":"cloud-artifacts.index.v1",
			"updated_at":"2026-07-22T06:00:00.000Z",
			"artifacts":[{"id":"lesson-game","title":"课堂小游戏","kind":"html","url":"https://example.test/lesson-game/latest/","updated_at":"2026-07-22T06:00:00.000Z"}]
		}`))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactHandler(upstream.URL, upstream.Client())
	req := authenticatedArtifactRequest(http.MethodGet)
	rec := httptest.NewRecorder()
	handler.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"lesson-game"`) {
		t.Fatalf("response does not contain artifact: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestCloudArtifactHandlerRejectsInvalidUpstreamIndex(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contract_version":"wrong","artifacts":[]}`))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactHandler(upstream.URL, upstream.Client())
	rec := httptest.NewRecorder()
	handler.HandleList(rec, authenticatedArtifactRequest(http.MethodGet))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerRequiresAuthentication(t *testing.T) {
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	rec := httptest.NewRecorder()
	handler.HandleList(rec, httptest.NewRequest(http.MethodGet, "/api/artifacts", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerRejectsOtherMethods(t *testing.T) {
	handler := NewCloudArtifactHandler("https://example.test/artifacts-index.json", nil)
	rec := httptest.NewRecorder()
	handler.HandleList(rec, authenticatedArtifactRequest(http.MethodPost))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func authenticatedArtifactRequest(method string) *http.Request {
	req := httptest.NewRequest(method, "/api/artifacts", nil)
	return req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
}

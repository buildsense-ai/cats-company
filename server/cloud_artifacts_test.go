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

func TestCloudArtifactHandlerListsManagedMetadata(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("status"); got != "active" {
			t.Fatalf("status = %q", got)
		}
		_, _ = w.Write([]byte(`{
			"contract_version":"cloud-artifacts.management-list.v1",
			"status":"active",
			"count":1,
			"artifacts":[{
				"id":"lesson-game",
				"title":"课堂小游戏",
				"kind":"html",
				"url":"https://example.test/lesson-game/latest/",
				"status":"active",
				"created_at":"2026-07-22T05:00:00.000Z",
				"updated_at":"2026-07-22T06:00:00.000Z",
				"publish_version":2,
				"agent_name":"豆包",
				"source_title":"课堂任务",
				"deleted_at":"",
				"can_delete":true,
				"can_restore":false
			}]
		}`))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL,
		token,
		upstream.Client(),
	)
	rec := httptest.NewRecorder()
	handler.Handle(rec, authenticatedArtifactRequestPath(http.MethodGet, "/api/artifacts?status=active"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"agent_name":"豆包"`) || !strings.Contains(rec.Body.String(), `"can_delete":true`) {
		t.Fatalf("managed metadata missing: %s", rec.Body.String())
	}
}

func TestCloudArtifactHandlerDeletesExactIDWithAuthenticatedActor(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/witch-poison-game-2" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("invalid body: %v", err)
		}
		if payload["actor_uid"] != "7" {
			t.Fatalf("actor_uid = %q", payload["actor_uid"])
		}
		_, _ = w.Write([]byte(managedOperationJSON("witch-poison-game-2", "deleted")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL,
		token,
		upstream.Client(),
	)
	rec := httptest.NewRecorder()
	handler.Handle(rec, authenticatedArtifactRequestPath(http.MethodDelete, "/api/artifacts/witch-poison-game-2"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"witch-poison-game-2"`) {
		t.Fatalf("response does not contain exact artifact: %s", rec.Body.String())
	}
}

func TestCloudArtifactHandlerRestoresExactID(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/lesson-game/restore" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(managedOperationJSON("lesson-game", "active")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL,
		token,
		upstream.Client(),
	)
	rec := httptest.NewRecorder()
	handler.Handle(rec, authenticatedArtifactRequestPath(http.MethodPost, "/api/artifacts/lesson-game/restore"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerMapsManagementConflict(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"artifact_already_deleted","message":"already deleted"}}`))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL,
		token,
		upstream.Client(),
	)
	rec := httptest.NewRecorder()
	handler.Handle(rec, authenticatedArtifactRequestPath(http.MethodDelete, "/api/artifacts/lesson-game"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"artifact_already_deleted"`) {
		t.Fatalf("stable code missing: %s", rec.Body.String())
	}
}

func TestCloudArtifactHandlerMutationRequiresAuthentication(t *testing.T) {
	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		"https://example.test/internal/artifacts",
		"test-management-token-abcdefghijklmnopqrstuvwxyz",
		nil,
	)
	rec := httptest.NewRecorder()
	handler.Handle(rec, httptest.NewRequest(http.MethodDelete, "/api/artifacts/lesson-game", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func authenticatedArtifactRequest(method string) *http.Request {
	req := httptest.NewRequest(method, "/api/artifacts", nil)
	return req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
}

func authenticatedArtifactRequestPath(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
}

func managedOperationJSON(id, status string) string {
	deletedAt := ""
	canDelete := status == "active"
	canRestore := status == "deleted"
	if canRestore {
		deletedAt = "2026-07-22T07:00:00.000Z"
	}
	payload := cloudArtifactOperation{
		OK: true,
		Artifact: cloudArtifact{
			ID:         id,
			Title:      "Artifact " + id,
			Kind:       "html",
			URL:        "https://example.test/" + id + "/latest/",
			Status:     status,
			CreatedAt:  "2026-07-22T05:00:00.000Z",
			UpdatedAt:  "2026-07-22T07:00:00.000Z",
			DeletedAt:  deletedAt,
			CanDelete:  canDelete,
			CanRestore: canRestore,
		},
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

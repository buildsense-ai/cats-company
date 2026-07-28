package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
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

func TestCloudArtifactHandlerListsArtifactsForAccessibleManagedAgent(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agents/440/artifacts" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("status"); got != "active" {
			t.Fatalf("status = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(managedAgentListJSON("440", "active")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	friendStore := managedArtifactAgentStore(8, 440, true)
	friendStore.friendPairs[agentPairKey(7, 440)] = true
	handler.SetStore(friendStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts?status=active"),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"agent_uid":"440"`) {
		t.Fatalf("agent metadata missing: %s", rec.Body.String())
	}
}

func TestCloudArtifactHandlerListsArtifactsForBodyBoundHistoricalAgent(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agents/440/artifacts" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(managedAgentListJSON("440", "active")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	historicalStore := managedArtifactAgentStore(7, 440, false)
	historicalStore.botBodyIDs = map[int64]string{440: "body-historical-agent"}
	handler.SetStore(historicalStore)
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts?status=active"),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerDeletesOnlyThroughRequestedAgentNamespace(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/internal/agents/440/artifacts/shared-game" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"actor_uid":"7"`) {
			t.Fatalf("actor body = %s", body)
		}
		_, _ = w.Write([]byte(managedAgentOperationJSON("440", "shared-game", "deleted")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	handler.SetStore(managedArtifactAgentStore(7, 440, true))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodDelete,
		"/api/agents/440/artifacts/shared-game",
		strings.NewReader(`{"actor_uid":"999"}`),
	)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerRestoresThroughRequestedAgentNamespace(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/agents/440/artifacts/shared-game/restore" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"actor_uid":"7"`) {
			t.Fatalf("actor body = %s", body)
		}
		_, _ = w.Write([]byte(managedAgentOperationJSON("440", "shared-game", "active")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	handler.SetStore(managedArtifactAgentStore(7, 440, true))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(
			http.MethodPost,
			"/api/agents/440/artifacts/shared-game/restore",
		),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerRejectsInaccessibleOrUnmanagedAgent(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		_, _ = w.Write([]byte(managedAgentListJSON("440", "active")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	handler.SetStore(managedArtifactAgentStore(8, 440, true))
	forbidden := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		forbidden,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts"),
	)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden status = %d, body = %s", forbidden.Code, forbidden.Body.String())
	}

	handler.SetStore(managedArtifactAgentStore(7, 440, false))
	unmanaged := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		unmanaged,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts"),
	)
	if unmanaged.Code != http.StatusNotFound {
		t.Fatalf("unmanaged status = %d, body = %s", unmanaged.Code, unmanaged.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestCloudArtifactHandlerRejectsMismatchedAgentMetadata(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(managedAgentListJSON("512", "active")))
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	handler.SetStore(managedArtifactAgentStore(7, 440, true))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts"),
	)
	if rec.Code != http.StatusBadGateway {
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

func managedArtifactAgentStore(ownerUID, agentUID int64, managed bool) *agentTestStore {
	tenantName := ""
	if managed {
		tenantName = "tenant-managed-agent"
	}
	return &agentTestStore{
		users: map[int64]*types.User{
			agentUID: {
				ID: agentUID, Username: "managed-agent", AccountType: types.AccountBot,
			},
		},
		owners:      map[int64]int64{agentUID: ownerUID},
		friendPairs: map[string]bool{},
		tenantNames: map[int64]string{agentUID: tenantName},
	}
}

func managedAgentListJSON(agentUID, status string) string {
	artifact := cloudArtifact{
		ID:         "shared-game",
		Title:      "Shared game",
		Kind:       "html",
		URL:        "https://example.test/by-agent/" + agentUID + "/shared-game/latest/",
		Status:     status,
		CreatedAt:  "2026-07-22T05:00:00.000Z",
		UpdatedAt:  "2026-07-22T07:00:00.000Z",
		AgentUID:   agentUID,
		DeletedAt:  "",
		CanDelete:  status == "active",
		CanRestore: status == "deleted",
	}
	if status == "deleted" {
		artifact.DeletedAt = "2026-07-22T07:00:00.000Z"
	}
	payload := cloudArtifactManagementList{
		ContractVersion: artifactManagementContract,
		Status:          status,
		Count:           1,
		Artifacts:       []cloudArtifact{artifact},
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

func managedAgentOperationJSON(agentUID, id, status string) string {
	var payload cloudArtifactOperation
	_ = json.Unmarshal([]byte(managedOperationJSON(id, status)), &payload)
	payload.Artifact.AgentUID = agentUID
	body, _ := json.Marshal(payload)
	return string(body)
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

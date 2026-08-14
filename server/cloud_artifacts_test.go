package server

import (
	"context"
	"encoding/json"
	"errors"
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
				"source_topic_id":"p2p_7_440",
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
	if !strings.Contains(rec.Body.String(), `"agent_name":"豆包"`) ||
		!strings.Contains(rec.Body.String(), `"source_topic_id":"p2p_7_440"`) ||
		!strings.Contains(rec.Body.String(), `"can_delete":true`) {
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
	if !strings.Contains(rec.Body.String(), `"viewer_relation":"friend"`) ||
		!strings.Contains(rec.Body.String(), `"visibility":"agent_users"`) {
		t.Fatalf("viewer metadata missing: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"can_delete":true`) || !strings.Contains(rec.Body.String(), `"uploaded_by_me":true`) {
		t.Fatalf("friend did not receive own-artifact management action: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"uploader_name":"成员甲"`) {
		t.Fatalf("uploader display name missing: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"creator_type":"user"`) ||
		!strings.Contains(rec.Body.String(), `"creator_uid":"7"`) ||
		!strings.Contains(rec.Body.String(), `"creator_name":"成员甲"`) {
		t.Fatalf("creator identity missing: %s", rec.Body.String())
	}
}

func TestCloudArtifactHandlerDoesNotGuessCreatorForLegacyManagedArtifact(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(managedAgentListJSONWithUploader("440", "active", "")))
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
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts?status=active"),
	)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"creator_type":"unknown"`) ||
		strings.Contains(rec.Body.String(), `"creator_uid"`) ||
		strings.Contains(rec.Body.String(), `"creator_name"`) {
		t.Fatalf("legacy creator was guessed: %s", rec.Body.String())
	}
}

func TestCloudArtifactHandlerAllowsFriendToRemoveOwnArtifactOnly(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	var upstreamCalls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls = append(upstreamCalls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("status") == "active":
			_, _ = w.Write([]byte(managedAgentListJSON("440", "active")))
		case r.Method == http.MethodDelete && r.URL.Path == "/internal/agents/440/artifacts/shared-game":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"actor_uid":"7"`) {
				t.Fatalf("delete body = %s", body)
			}
			_, _ = w.Write([]byte(managedAgentOperationJSON("440", "shared-game", "deleted")))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
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

	allowed := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		allowed,
		authenticatedArtifactRequestPath(http.MethodDelete, "/api/agents/440/artifacts/shared-game"),
	)
	if allowed.Code != http.StatusOK {
		t.Fatalf("own delete status = %d, body = %s", allowed.Code, allowed.Body.String())
	}

	for _, request := range []*http.Request{
		authenticatedArtifactRequestPath(http.MethodPost, "/api/agents/440/artifacts/shared-game/restore"),
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts?status=deleted"),
	} {
		rec := httptest.NewRecorder()
		handler.HandleAgentArtifacts(rec, request)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("request %s %s status = %d, body = %s", request.Method, request.URL.Path, rec.Code, rec.Body.String())
		}
	}
	if len(upstreamCalls) != 2 {
		t.Fatalf("upstream calls = %v", upstreamCalls)
	}
}

func TestCloudArtifactHandlerRejectsFriendRemovingAnotherMembersArtifact(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	var deleteCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalls++
		}
		_, _ = w.Write([]byte(managedAgentListJSONWithUploader("440", "active", "8")))
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
		authenticatedArtifactRequestPath(http.MethodDelete, "/api/agents/440/artifacts/shared-game"),
	)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d", deleteCalls)
	}
}

func TestCloudArtifactHandlerPublishesForFriendWithoutApproval(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/agents/440/artifacts" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode publish body: %v", err)
		}
		if payload["actor_uid"] != "7" || payload["uploader_uid"] != "7" ||
			payload["creator_type"] != "user" || payload["creator_uid"] != "7" ||
			payload["actor_relation"] != "friend" || payload["publish_mode"] != "immediate" {
			t.Fatalf("publish identity = %#v", payload)
		}
		if payload["title"] != "课堂网页" || payload["source_topic_id"] != "p2p_7_440" {
			t.Fatalf("publish metadata = %#v", payload)
		}
		operation := cloudArtifactOperation{
			OK: true,
			Artifact: cloudArtifact{
				ID:           "member-result",
				Title:        "课堂网页",
				Kind:         "html",
				URL:          "https://example.test/by-agent/440/member-result/latest/",
				Status:       "active",
				CreatedAt:    "2026-08-12T07:00:00Z",
				UpdatedAt:    "2026-08-12T07:00:00Z",
				AgentUID:     "440",
				UploaderUID:  "7",
				UploaderName: "成员甲",
				CanDelete:    true,
			},
		}
		_ = json.NewEncoder(w).Encode(operation)
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
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/agents/440/artifacts",
		strings.NewReader(`{"title":"课堂网页","kind":"html","url":"https://example.com/uploads/files/result.html","source_topic_id":"p2p_7_440"}`),
	).WithContext(context.WithValue(context.Background(), uidKey, int64(7)))
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"uploaded_by_me":true`) ||
		!strings.Contains(rec.Body.String(), `"creator_type":"user"`) ||
		!strings.Contains(rec.Body.String(), `"creator_name":"成员甲"`) ||
		!strings.Contains(rec.Body.String(), `"can_delete":true`) ||
		strings.Contains(rec.Body.String(), "pending") {
		t.Fatalf("publish response = %s", rec.Body.String())
	}
}

type artifactPolicyTestStore struct {
	*agentTestStore
	enabled bool
	err     error
}

func (s *artifactPolicyTestStore) GetBotArtifactUploadPolicy(_ int64) (bool, error) {
	return s.enabled, s.err
}

func (s *artifactPolicyTestStore) UpdateBotArtifactUploadPolicy(_ int64, enabled bool) error {
	s.enabled = enabled
	return nil
}

func TestCloudArtifactHandlerRejectsFriendPublishWhenOwnerDisablesUploads(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		"test-management-token-abcdefghijklmnopqrstuvwxyz",
		upstream.Client(),
	)
	baseStore := managedArtifactAgentStore(8, 440, true)
	baseStore.friendPairs[agentPairKey(7, 440)] = true
	handler.SetStore(&artifactPolicyTestStore{agentTestStore: baseStore, enabled: false})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/agents/440/artifacts",
		strings.NewReader(`{"title":"课堂网页","kind":"html","url":"https://example.com/uploads/files/result.html"}`),
	).WithContext(context.WithValue(context.Background(), uidKey, int64(7)))
	handler.HandleAgentArtifacts(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d", upstreamCalls)
	}
}

func TestCloudArtifactHandlerRejectsFriendPublishWhenUploadPolicyReadFails(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()

	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		"test-management-token-abcdefghijklmnopqrstuvwxyz",
		upstream.Client(),
	)
	baseStore := managedArtifactAgentStore(8, 440, true)
	baseStore.friendPairs[agentPairKey(7, 440)] = true
	handler.SetStore(&artifactPolicyTestStore{
		agentTestStore: baseStore,
		enabled:        false,
		err:            errors.New("temporary policy store failure"),
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/agents/440/artifacts",
		strings.NewReader(`{"title":"课堂网页","kind":"html","url":"https://example.com/uploads/files/result.html"}`),
	).WithContext(context.WithValue(context.Background(), uidKey, int64(7)))
	handler.HandleAgentArtifacts(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d", upstreamCalls)
	}
}

func TestCloudArtifactHandlerRejectsClientSuppliedPublishIdentity(t *testing.T) {
	handler := NewCloudArtifactManagementHandler(
		"https://example.test/artifacts-index.json",
		"https://example.test/internal/artifacts",
		"test-management-token-abcdefghijklmnopqrstuvwxyz",
		nil,
	)
	friendStore := managedArtifactAgentStore(8, 440, true)
	friendStore.friendPairs[agentPairKey(7, 440)] = true
	handler.SetStore(friendStore)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/agents/440/artifacts",
		strings.NewReader(`{"title":"伪造成果","kind":"html","url":"https://example.test/result.html","uploader_uid":"8"}`),
	).WithContext(context.WithValue(context.Background(), uidKey, int64(7)))
	handler.HandleAgentArtifacts(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerListsArtifactsForAccessibleSelfHostedBot(t *testing.T) {
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
	handler.SetStore(managedArtifactAgentStore(7, 440, false))
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

func TestCloudArtifactHandlerRejectsInaccessibleAndAllowsAccessibleSelfHostedBot(t *testing.T) {
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

	selfHostedStore := managedArtifactAgentStore(7, 440, false)
	selfHostedStore.botBodyIDs = map[int64]string{440: "body-self-hosted-agent"}
	handler.SetStore(selfHostedStore)
	selfHosted := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		selfHosted,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts"),
	)
	if selfHosted.Code != http.StatusOK {
		t.Fatalf("self-hosted status = %d, body = %s", selfHosted.Code, selfHosted.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
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
			7: {
				ID: 7, Username: "member-a", DisplayName: "成员甲", AccountType: types.AccountHuman,
			},
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
	return managedAgentListJSONWithUploader(agentUID, status, "7")
}

func managedAgentListJSONWithUploader(agentUID, status, uploaderUID string) string {
	artifact := cloudArtifact{
		ID:          "shared-game",
		Title:       "Shared game",
		Kind:        "html",
		URL:         "https://example.test/by-agent/" + agentUID + "/shared-game/latest/",
		Status:      status,
		CreatedAt:   "2026-07-22T05:00:00.000Z",
		UpdatedAt:   "2026-07-22T07:00:00.000Z",
		AgentUID:    agentUID,
		UploaderUID: uploaderUID,
		DeletedAt:   "",
		CanDelete:   status == "active",
		CanRestore:  status == "deleted",
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

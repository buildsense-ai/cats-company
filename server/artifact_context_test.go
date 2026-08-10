package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type artifactContextResolverFunc func(context.Context, int64, string) (ArtifactContextRecord, error)

func (f artifactContextResolverFunc) ResolveActiveArtifact(ctx context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
	return f(ctx, agentUID, artifactID)
}

func TestParseArtifactRefCandidateEnforcesExactIDLength(t *testing.T) {
	validID := "a" + strings.Repeat("b", artifactIDMaxLength-1)
	if candidate, ok := parseArtifactRefCandidate(map[string]interface{}{
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version": artifactRefContract, "id": validID, "currently_visible": true,
		},
	}); !ok || candidate.ID != validID {
		t.Fatalf("64-character Artifact ID was rejected: %#v, %v", candidate, ok)
	}

	tooLongID := "a" + strings.Repeat("b", artifactIDMaxLength)
	if candidate, ok := parseArtifactRefCandidate(map[string]interface{}{
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version": artifactRefContract, "id": tooLongID, "currently_visible": true,
		},
	}); ok {
		t.Fatalf("65-character Artifact ID was accepted: %#v", candidate)
	}
	if candidate, ok := parseArtifactRefCandidate(map[string]interface{}{
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version": artifactRefContract, "id": " " + validID + " ", "currently_visible": true,
		},
	}); ok {
		t.Fatalf("whitespace-normalized Artifact ID was accepted: %#v", candidate)
	}
	if candidate, ok := parseArtifactRefCandidate(map[string]interface{}{
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version": " " + artifactRefContract + " ", "id": validID, "currently_visible": true,
		},
	}); ok {
		t.Fatalf("whitespace-normalized contract was accepted: %#v", candidate)
	}
}

func TestCanonicalizeArtifactMessageMetadataForP2P(t *testing.T) {
	store := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
	}}
	hub := NewHub(store, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
		if agentUID != 440 || artifactID != "lesson-game" {
			t.Fatalf("resolver arguments = agent %d artifact %q", agentUID, artifactID)
		}
		return ArtifactContextRecord{
			ID:             artifactID,
			Title:          "课堂小游戏",
			Kind:           "html",
			URL:            "https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/",
			PublishVersion: 3,
		}, nil
	}))

	metadata := map[string]interface{}{
		"client_note": "kept",
		artifactContextMetadataKey: map[string]interface{}{
			"agent_uid": "999",
			"url":       "https://attacker.invalid/",
		},
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version":  artifactRefContract,
			"id":                "lesson-game",
			"displayed_version": float64(2),
			"currently_visible": true,
		},
	}
	got := hub.canonicalizeArtifactMessageMetadata(context.Background(), 7, "p2p_7_440", metadata)
	if got["client_note"] != "kept" {
		t.Fatalf("unrelated metadata was lost: %#v", got)
	}
	contextValue, ok := got[artifactContextMetadataKey].(map[string]interface{})
	if !ok {
		t.Fatalf("artifact context = %#v, want object", got[artifactContextMetadataKey])
	}
	if contextValue["agent_uid"] != "440" || contextValue["url"] == "https://attacker.invalid/" {
		t.Fatalf("artifact context was not server-canonicalized: %#v", contextValue)
	}
	if contextValue["displayed_version"] != int64(2) || contextValue["latest_version"] != 3 {
		t.Fatalf("artifact versions = %#v", contextValue)
	}
	if _, exists := got[artifactRefMetadataKey]; exists {
		t.Fatalf("client artifact_ref leaked into canonical metadata: %#v", got)
	}
}

func TestCanonicalizeArtifactMessageMetadataFailsOpenWithoutLeakingTrustedFields(t *testing.T) {
	store := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
	}}
	hub := NewHub(store, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(context.Context, int64, string) (ArtifactContextRecord, error) {
		return ArtifactContextRecord{}, errors.New("artifact node unavailable")
	}))

	got := hub.canonicalizeArtifactMessageMetadata(context.Background(), 7, "p2p_7_440", map[string]interface{}{
		"trace": "kept",
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version":  artifactRefContract,
			"id":                "lesson-game",
			"currently_visible": true,
		},
		artifactContextMetadataKey: map[string]interface{}{"agent_uid": "999"},
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    "must not leak without a trusted Artifact",
		},
	})
	if !reflect.DeepEqual(got, map[string]interface{}{"trace": "kept"}) {
		t.Fatalf("fallback metadata = %#v", got)
	}
}

func TestCanonicalizeArtifactMessageMetadataSanitizesPageContext(t *testing.T) {
	store := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
	}}
	hub := NewHub(store, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, _ int64, artifactID string) (ArtifactContextRecord, error) {
		return ArtifactContextRecord{
			ID: artifactID, Title: "课堂小游戏", Kind: "html",
			URL: "https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/",
		}, nil
	}))

	got := hub.canonicalizeArtifactMessageMetadata(context.Background(), 7, "p2p_7_440", map[string]interface{}{
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version":  artifactRefContract,
			"id":                "lesson-game",
			"currently_visible": true,
		},
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    "  企业客户  ",
			"controls": []interface{}{
				map[string]interface{}{"type": "checkbox", "name": "feedback", "value": "f12", "checked": true},
				map[string]interface{}{"type": "password", "name": "secret", "value": "do-not-send"},
			},
			"local_storage": map[string]interface{}{"token": "forged"},
		},
	})
	contextValue := got[artifactContextMetadataKey].(map[string]interface{})
	pageContext := contextValue["page_context"].(map[string]interface{})
	if pageContext["selected_text"] != "企业客户" {
		t.Fatalf("selected text = %#v", pageContext["selected_text"])
	}
	controls := pageContext["controls"].([]interface{})
	if len(controls) != 1 || controls[0].(map[string]interface{})["type"] != "checkbox" {
		t.Fatalf("controls = %#v", controls)
	}
	if _, exists := pageContext["local_storage"]; exists {
		t.Fatalf("forged storage leaked: %#v", pageContext)
	}
	if _, exists := got[artifactPageContextMetadataKey]; exists {
		t.Fatalf("raw page context leaked beside canonical context: %#v", got)
	}
}

func TestCanonicalizeArtifactMessageMetadataDropsOversizedPageContext(t *testing.T) {
	store := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
	}}
	hub := NewHub(store, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, _ int64, artifactID string) (ArtifactContextRecord, error) {
		return ArtifactContextRecord{
			ID: artifactID, Title: "课堂小游戏", Kind: "html",
			URL: "https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/",
		}, nil
	}))

	got := hub.canonicalizeArtifactMessageMetadata(context.Background(), 7, "p2p_7_440", map[string]interface{}{
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version": artifactRefContract, "id": "lesson-game", "currently_visible": true,
		},
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    strings.Repeat("x", artifactPageContextMaxBytes),
		},
	})
	contextValue := got[artifactContextMetadataKey].(map[string]interface{})
	if _, exists := contextValue["page_context"]; exists {
		t.Fatalf("oversized page context was retained: %#v", contextValue)
	}
}

func TestCanonicalizeArtifactMessageMetadataBoundsArtifactResolution(t *testing.T) {
	store := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
	}}
	hub := NewHub(store, nil)
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(ctx context.Context, _ int64, _ string) (ArtifactContextRecord, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("artifact resolution context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > artifactContextResolutionTimeout+100*time.Millisecond {
			t.Fatalf("artifact resolution deadline remaining = %s", remaining)
		}
		return ArtifactContextRecord{}, context.DeadlineExceeded
	}))

	got := hub.canonicalizeArtifactMessageMetadata(context.Background(), 7, "p2p_7_440", map[string]interface{}{
		"trace": "kept",
		artifactRefMetadataKey: map[string]interface{}{
			"contract_version":  artifactRefContract,
			"id":                "lesson-game",
			"currently_visible": true,
		},
	})
	if !reflect.DeepEqual(got, map[string]interface{}{"trace": "kept"}) {
		t.Fatalf("fallback metadata = %#v", got)
	}
}

func TestArtifactAgentForTopicRejectsAmbiguousGroup(t *testing.T) {
	base := &identityMessageStore{
		users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			42: {ID: 42, AccountType: types.AccountBot},
			43: {ID: 43, AccountType: types.AccountBot},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: 80, UserID: 7},
			{GroupID: 80, UserID: 42, IsBot: true},
			{GroupID: 80, UserID: 43, IsBot: true},
		},
	}
	store := &agentTaskGroupRoutingStore{
		identityMessageStore: base,
		group:                &types.Group{ID: 80, Kind: types.GroupKindAgentTask, AgentIDs: []int64{42, 43}},
	}
	hub := NewHub(store, nil)
	if agentUID, ok := hub.artifactAgentForTopic(7, "grp_80"); ok || agentUID != 0 {
		t.Fatalf("ambiguous group resolved agent %d ok=%t", agentUID, ok)
	}
}

func TestArtifactMetadataOnlyReachesMatchingAgentAndHistoryReplay(t *testing.T) {
	canonical := map[string]interface{}{
		artifactContextMetadataKey: map[string]interface{}{
			"contract_version": artifactContextContract,
			"id":               "lesson-game",
			"agent_uid":        "440",
		},
		"trace": "kept",
	}
	matching := artifactMetadataForRecipient(canonical, 440)
	if _, ok := matching[artifactContextMetadataKey]; !ok {
		t.Fatalf("matching agent lost artifact context: %#v", matching)
	}
	other := artifactMetadataForRecipient(canonical, 441)
	if _, ok := other[artifactContextMetadataKey]; ok || other["trace"] != "kept" {
		t.Fatalf("non-matching recipient metadata = %#v", other)
	}

	store := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
		441: {ID: 441, AccountType: types.AccountBot},
	}}
	hub := NewHub(store, nil)
	message := &types.Message{
		ID:        12,
		TopicID:   "p2p_7_440",
		FromUID:   7,
		Content:   `"改一下右侧标题"`,
		MsgType:   "text",
		Metadata:  canonical,
		CreatedAt: time.Now(),
	}
	matchingHistory := hub.historyMessageDataForRecipient(440, message)
	if _, ok := matchingHistory.Metadata[artifactContextMetadataKey]; !ok {
		t.Fatalf("matching history lost artifact context: %#v", matchingHistory.Metadata)
	}
	otherHistory := hub.historyMessageDataForRecipient(441, message)
	if _, ok := otherHistory.Metadata[artifactContextMetadataKey]; ok {
		t.Fatalf("history leaked artifact context: %#v", otherHistory.Metadata)
	}
}

func TestCloudArtifactHandlerResolvesExactIndexedArtifact(t *testing.T) {
	version := 4
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/by-agent/440/artifacts-index.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(cloudArtifactIndex{
			ContractVersion: artifactIndexContract,
			UpdatedAt:       "2026-08-07T00:00:00Z",
			Artifacts: []cloudArtifact{
				{
					ID:             "other",
					Title:          "Other",
					Kind:           "html",
					URL:            serverURLForTest(r, "/by-agent/440/other/latest/"),
					UpdatedAt:      "2026-08-07T00:00:00Z",
					PublishVersion: &version,
				},
				{
					ID:             "lesson-game",
					Title:          "课堂小游戏",
					Kind:           "mini_app",
					URL:            serverURLForTest(r, "/by-agent/440/lesson-game/latest/"),
					UpdatedAt:      "2026-08-07T00:00:00Z",
					PublishVersion: &version,
				},
			},
		})
	}))
	defer server.Close()

	handler := &CloudArtifactHandler{
		httpClient: server.Client(),
		nodeRegistry: &artifactNodeRegistry{
			nodes:  map[string]artifactNode{"test": {id: "test", publicBaseURL: server.URL}},
			agents: map[int64]string{440: "test"},
		},
	}
	record, err := handler.ResolveActiveArtifact(context.Background(), 440, "lesson-game")
	if err != nil {
		t.Fatalf("resolve artifact: %v", err)
	}
	if record.ID != "lesson-game" || record.Kind != "mini_app" || record.PublishVersion != 4 {
		t.Fatalf("record = %#v", record)
	}
	if _, err := handler.ResolveActiveArtifact(context.Background(), 440, "missing"); err == nil {
		t.Fatal("missing exact ID should fail")
	}
}

func TestCloudArtifactHandlerCachesOnlySuccessfulArtifactResolution(t *testing.T) {
	version := 4
	requestCount := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		_ = json.NewEncoder(w).Encode(cloudArtifactIndex{
			ContractVersion: artifactIndexContract,
			UpdatedAt:       "2026-08-07T00:00:00Z",
			Artifacts: []cloudArtifact{{
				ID:             "lesson-game",
				Title:          "课堂小游戏",
				Kind:           "html",
				URL:            serverURLForTest(r, "/by-agent/440/lesson-game/latest/"),
				UpdatedAt:      "2026-08-07T00:00:00Z",
				PublishVersion: &version,
			}},
		})
	}))
	defer server.Close()

	handler := &CloudArtifactHandler{
		httpClient: server.Client(),
		nodeRegistry: &artifactNodeRegistry{
			nodes:  map[string]artifactNode{"test": {id: "test", publicBaseURL: server.URL}},
			agents: map[int64]string{440: "test"},
		},
	}
	for range 2 {
		if _, err := handler.ResolveActiveArtifact(context.Background(), 440, "lesson-game"); err != nil {
			t.Fatalf("resolve cached artifact: %v", err)
		}
	}
	if requestCount != 1 {
		t.Fatalf("successful resolution requests = %d, want 1", requestCount)
	}
	for range 2 {
		if _, err := handler.ResolveActiveArtifact(context.Background(), 440, "missing"); err == nil {
			t.Fatal("missing artifact should fail")
		}
	}
	if requestCount != 3 {
		t.Fatalf("failed resolution requests = %d, want 3 total", requestCount)
	}
}

func TestCloudArtifactHandlerMutationInvalidatesArtifactContextCache(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	active := true
	listRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/agents/440/artifacts":
			listRequests++
			artifacts := []cloudArtifact{}
			if active {
				version := 2
				artifacts = append(artifacts, cloudArtifact{
					ID:             "lesson-game",
					Title:          "课堂小游戏",
					Kind:           "html",
					URL:            "http://" + r.Host + "/by-agent/440/lesson-game/latest/",
					Status:         "active",
					CreatedAt:      "2026-08-07T00:00:00Z",
					UpdatedAt:      "2026-08-07T00:01:00Z",
					PublishVersion: &version,
					AgentUID:       "440",
					CanDelete:      true,
				})
			}
			_ = json.NewEncoder(w).Encode(cloudArtifactManagementList{
				ContractVersion: artifactManagementContract,
				Status:          "active",
				Count:           len(artifacts),
				Artifacts:       artifacts,
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/internal/agents/440/artifacts/lesson-game":
			active = false
			_ = json.NewEncoder(w).Encode(cloudArtifactOperation{
				OK: true,
				Artifact: cloudArtifact{
					ID:         "lesson-game",
					Title:      "课堂小游戏",
					Kind:       "html",
					URL:        "http://" + r.Host + "/by-agent/440/lesson-game/latest/",
					Status:     "deleted",
					CreatedAt:  "2026-08-07T00:00:00Z",
					UpdatedAt:  "2026-08-07T00:02:00Z",
					DeletedAt:  "2026-08-07T00:02:00Z",
					AgentUID:   "440",
					CanRestore: true,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := &CloudArtifactHandler{
		httpClient: server.Client(),
		nodeRegistry: &artifactNodeRegistry{
			nodes: map[string]artifactNode{"test": {
				id:              "test",
				publicBaseURL:   server.URL,
				managementURL:   server.URL + "/internal/artifacts",
				managementToken: token,
			}},
			agents: map[int64]string{440: "test"},
		},
	}
	if _, err := handler.ResolveActiveArtifact(context.Background(), 440, "lesson-game"); err != nil {
		t.Fatalf("initial resolution: %v", err)
	}
	if _, err := handler.ResolveActiveArtifact(context.Background(), 440, "lesson-game"); err != nil {
		t.Fatalf("cached resolution: %v", err)
	}
	if listRequests != 1 {
		t.Fatalf("list requests before mutation = %d, want 1", listRequests)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/agents/440/artifacts/lesson-game", nil)
	handler.handleMutation(
		recorder,
		request,
		"lesson-game",
		"",
		7,
		server.URL+"/internal/agents/440/artifacts",
		token,
		server.URL,
		440,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("mutation status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := handler.ResolveActiveArtifact(context.Background(), 440, "lesson-game"); err == nil {
		t.Fatal("deleted artifact should not resolve after cache invalidation")
	}
	if listRequests != 2 {
		t.Fatalf("list requests after mutation = %d, want 2", listRequests)
	}
}

func TestCloudArtifactHandlerMutationRejectsInflightStaleResolution(t *testing.T) {
	const token = "test-management-token-abcdefghijklmnopqrstuvwxyz"
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	active := true
	listRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/agents/440/artifacts":
			listRequests++
			if listRequests == 1 {
				close(requestStarted)
				<-releaseRequest
			}
			artifacts := []cloudArtifact{}
			if active || listRequests == 1 {
				version := 2
				artifacts = append(artifacts, cloudArtifact{
					ID: "lesson-game", Title: "课堂小游戏", Kind: "html",
					URL:    "http://" + r.Host + "/by-agent/440/lesson-game/latest/",
					Status: "active", CreatedAt: "2026-08-07T00:00:00Z", UpdatedAt: "2026-08-07T00:01:00Z",
					PublishVersion: &version, AgentUID: "440", CanDelete: true,
				})
			}
			_ = json.NewEncoder(w).Encode(cloudArtifactManagementList{
				ContractVersion: artifactManagementContract,
				Status:          "active", Count: len(artifacts), Artifacts: artifacts,
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/internal/agents/440/artifacts/lesson-game":
			active = false
			_ = json.NewEncoder(w).Encode(cloudArtifactOperation{
				OK: true,
				Artifact: cloudArtifact{
					ID: "lesson-game", Title: "课堂小游戏", Kind: "html",
					URL:    "http://" + r.Host + "/by-agent/440/lesson-game/latest/",
					Status: "deleted", CreatedAt: "2026-08-07T00:00:00Z", UpdatedAt: "2026-08-07T00:02:00Z",
					DeletedAt: "2026-08-07T00:02:00Z", AgentUID: "440", CanRestore: true,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := &CloudArtifactHandler{
		httpClient: server.Client(),
		nodeRegistry: &artifactNodeRegistry{
			nodes: map[string]artifactNode{"test": {
				id: "test", publicBaseURL: server.URL,
				managementURL: server.URL + "/internal/artifacts", managementToken: token,
			}},
			agents: map[int64]string{440: "test"},
		},
	}
	resolutionResult := make(chan error, 1)
	go func() {
		_, err := handler.ResolveActiveArtifact(context.Background(), 440, "lesson-game")
		resolutionResult <- err
	}()
	<-requestStarted

	recorder := httptest.NewRecorder()
	handler.handleMutation(
		recorder,
		httptest.NewRequest(http.MethodDelete, "/api/agents/440/artifacts/lesson-game", nil),
		"lesson-game", "", 7,
		server.URL+"/internal/agents/440/artifacts", token, server.URL, 440,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("mutation status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	close(releaseRequest)
	if err := <-resolutionResult; err == nil || !strings.Contains(err.Error(), "changed during resolution") {
		t.Fatalf("in-flight resolution error = %v", err)
	}
	if _, err := handler.ResolveActiveArtifact(context.Background(), 440, "lesson-game"); err == nil {
		t.Fatal("deleted artifact should not resolve after in-flight invalidation")
	}
	if listRequests != 2 {
		t.Fatalf("list requests = %d, want 2", listRequests)
	}
}

func TestArtifactContextCacheMutationOnlyInvalidatesTheTargetKey(t *testing.T) {
	handler := &CloudArtifactHandler{}
	lessonMutation := handler.artifactContextCacheMutationSnapshot(440, "lesson-game")
	reportMutation := handler.artifactContextCacheMutationSnapshot(440, "report-board")

	handler.invalidateArtifactContextCache(440, "lesson-game")

	if handler.storeArtifactContext(440, "lesson-game", ArtifactContextRecord{
		ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/lesson-game",
	}, lessonMutation) {
		t.Fatal("mutated Artifact accepted an in-flight stale cache write")
	}
	if !handler.storeArtifactContext(440, "report-board", ArtifactContextRecord{
		ID: "report-board", Title: "Report", Kind: "html", URL: "https://example.test/report-board",
	}, reportMutation) {
		t.Fatal("unrelated Artifact cache write was rejected")
	}
	if cached, ok := handler.cachedArtifactContext(440, "report-board"); !ok || cached.ID != "report-board" {
		t.Fatalf("unrelated Artifact cache entry = %#v, %v", cached, ok)
	}
}

func TestArtifactContextCacheWildcardMutationInvalidatesAllAgentsForTheArtifactID(t *testing.T) {
	handler := &CloudArtifactHandler{}
	agentAMutation := handler.artifactContextCacheMutationSnapshot(440, "lesson-game")
	agentBMutation := handler.artifactContextCacheMutationSnapshot(441, "lesson-game")
	unrelatedMutation := handler.artifactContextCacheMutationSnapshot(441, "report-board")

	handler.invalidateArtifactContextCache(0, "lesson-game")

	for _, test := range []struct {
		agentUID int64
		token    artifactContextCacheMutationToken
	}{
		{agentUID: 440, token: agentAMutation},
		{agentUID: 441, token: agentBMutation},
	} {
		if handler.storeArtifactContext(test.agentUID, "lesson-game", ArtifactContextRecord{
			ID: "lesson-game", Title: "Lesson", Kind: "html", URL: "https://example.test/lesson-game",
		}, test.token) {
			t.Fatalf("agent %d accepted a stale cache write after wildcard mutation", test.agentUID)
		}
	}
	if !handler.storeArtifactContext(441, "report-board", ArtifactContextRecord{
		ID: "report-board", Title: "Report", Kind: "html", URL: "https://example.test/report-board",
	}, unrelatedMutation) {
		t.Fatal("wildcard mutation rejected an unrelated Artifact cache write")
	}
}

func TestArtifactContextCacheFailedLookupsDoNotGrowMutationMaps(t *testing.T) {
	handler := &CloudArtifactHandler{}
	for index := 0; index < 1000; index++ {
		artifactID := fmt.Sprintf("missing-%d", index)
		if _, err := handler.ResolveActiveArtifact(context.Background(), 440, artifactID); err == nil {
			t.Fatalf("ResolveActiveArtifact(%q) unexpectedly succeeded", artifactID)
		}
	}
	if got := len(handler.artifactContextExactMutationGeneration); got != 0 {
		t.Fatalf("exact mutation map size = %d, want 0", got)
	}
	if got := len(handler.artifactContextIDMutationGeneration); got != 0 {
		t.Fatalf("wildcard mutation map size = %d, want 0", got)
	}
}

func serverURLForTest(r *http.Request, path string) string {
	return "https://" + r.Host + path
}

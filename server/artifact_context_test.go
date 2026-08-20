package server

import (
	"context"
	"encoding/json"
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

func TestExtractArtifactContextDeliveryForP2P(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			440: {ID: 440, AccountType: types.AccountBot},
		},
		owners: map[int64]int64{440: 7},
	}
	hub := NewHub(store, nil)
	snapshot, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7,
		TopicID:  "p2p_7_440",
		AgentUID: 440,
		Artifact: ArtifactContextRecord{
			ID: "lesson-game", Title: "课堂小游戏", Kind: "html",
			URL: "https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/",
		},
	})
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	metadata := map[string]interface{}{
		"client_note": "kept",
		artifactContextMetadataKey: map[string]interface{}{
			"agent_uid": "999",
			"url":       "https://attacker.invalid/",
		},
		artifactContextRefMetadataKey: snapshot.Ref,
		artifactRefMetadataKey:        map[string]interface{}{"id": "spoofed"},
		artifactPageContextMetadataKey: map[string]interface{}{
			"selected_text": "must not enter message metadata",
		},
	}
	got, delivery := hub.extractArtifactContextDelivery(7, "p2p_7_440", metadata)
	if got["client_note"] != "kept" {
		t.Fatalf("unrelated metadata was lost: %#v", got)
	}
	if delivery == nil || delivery.Ref != snapshot.Ref || delivery.AgentUID != 440 {
		t.Fatalf("delivery = %#v", delivery)
	}
	for _, key := range []string{artifactRefMetadataKey, artifactContextMetadataKey, artifactPageContextMetadataKey, artifactContextRefMetadataKey} {
		if _, exists := got[key]; exists {
			t.Fatalf("Artifact metadata %q leaked into persisted metadata: %#v", key, got)
		}
	}
}

func TestExtractArtifactContextDeliveryFailsOpenWithoutLeakingFields(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			440: {ID: 440, AccountType: types.AccountBot},
		},
		owners: map[int64]int64{440: 7},
	}
	hub := NewHub(store, nil)

	got, delivery := hub.extractArtifactContextDelivery(7, "p2p_7_440", map[string]interface{}{
		"trace":                       "kept",
		artifactContextRefMetadataKey: "acr_" + strings.Repeat("x", 43),
		artifactContextMetadataKey:    map[string]interface{}{"agent_uid": "999"},
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    "must not leak without a trusted Artifact",
		},
	})
	if !reflect.DeepEqual(got, map[string]interface{}{"trace": "kept"}) {
		t.Fatalf("fallback metadata = %#v", got)
	}
	if delivery != nil {
		t.Fatalf("unknown ref produced delivery: %#v", delivery)
	}
}

func TestParseArtifactPageContextCandidateSanitizesPageContext(t *testing.T) {
	pageContext, ok := parseArtifactPageContextCandidate(map[string]interface{}{
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    "  企业客户  ",
			"controls": []interface{}{
				map[string]interface{}{"type": "checkbox", "name": "feedback", "value": "f12", "checked": true},
				map[string]interface{}{"type": "password", "name": "secret", "value": "do-not-send"},
			},
			"semantic_context": map[string]interface{}{
				"view":      "customer-comparison",
				"selection": []interface{}{"c12", "c18"},
				"filters":   map[string]interface{}{"region": "east"},
				"ignored":   func() {},
				"__proto__": map[string]interface{}{"polluted": true},
			},
			"local_storage": map[string]interface{}{"token": "forged"},
		},
	})
	if !ok {
		t.Fatal("valid bounded page context was rejected")
	}
	if pageContext["selected_text"] != "企业客户" {
		t.Fatalf("selected text = %#v", pageContext["selected_text"])
	}
	controls := pageContext["controls"].([]interface{})
	if len(controls) != 1 || controls[0].(map[string]interface{})["type"] != "checkbox" {
		t.Fatalf("controls = %#v", controls)
	}
	semanticContext := pageContext["semantic_context"].(map[string]interface{})
	if semanticContext["view"] != "customer-comparison" ||
		!reflect.DeepEqual(semanticContext["selection"], []interface{}{"c12", "c18"}) {
		t.Fatalf("semantic context = %#v", semanticContext)
	}
	if _, exists := semanticContext["ignored"]; exists {
		t.Fatalf("unserializable semantic value leaked: %#v", semanticContext)
	}
	if _, exists := semanticContext["__proto__"]; exists {
		t.Fatalf("unsafe semantic key leaked: %#v", semanticContext)
	}
	if _, exists := pageContext["local_storage"]; exists {
		t.Fatalf("forged storage leaked: %#v", pageContext)
	}
}

func TestParseArtifactPageContextCandidateDropsOversizedSemanticContextOnly(t *testing.T) {
	oversized := make(map[string]interface{}, 20)
	for index := 0; index < 20; index++ {
		oversized[fmt.Sprintf("field_%02d", index)] = strings.Repeat("x", artifactSemanticContextMaxString)
	}
	pageContext, ok := parseArtifactPageContextCandidate(map[string]interface{}{
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    "keep this",
			"semantic_context": oversized,
		},
	})
	if !ok {
		t.Fatal("generic page context was lost")
	}
	if pageContext["selected_text"] != "keep this" {
		t.Fatalf("generic observation was lost: %#v", pageContext)
	}
	if _, exists := pageContext["semantic_context"]; exists {
		t.Fatalf("oversized semantic context was retained: %#v", pageContext)
	}
}

func TestArtifactPageContextSemanticBoundsNestedJSON(t *testing.T) {
	items := make([]interface{}, artifactSemanticContextMaxItems+10)
	for index := range items {
		items[index] = index
	}
	value, ok := artifactPageContextSemantic(map[string]interface{}{
		"items": items,
		"label": strings.Repeat("x", artifactSemanticContextMaxString-1) + "😀z",
		"bad":   make(chan int),
	})
	if !ok {
		t.Fatal("bounded semantic context was rejected")
	}
	record := value.(map[string]interface{})
	if len(record["items"].([]interface{})) != artifactSemanticContextMaxItems {
		t.Fatalf("semantic items = %d", len(record["items"].([]interface{})))
	}
	if len([]rune(record["label"].(string))) != artifactSemanticContextMaxString {
		t.Fatalf("semantic label length = %d", len([]rune(record["label"].(string))))
	}
	if !strings.HasSuffix(record["label"].(string), "😀") {
		t.Fatalf("semantic label split a Unicode character: %q", record["label"])
	}
	if _, exists := record["bad"]; exists {
		t.Fatalf("unsupported semantic value leaked: %#v", record)
	}
	if _, ok := artifactPageContextSemantic(make(chan int)); ok {
		t.Fatal("unsupported semantic root was accepted")
	}
}

func TestArtifactPageContextSemanticBoundsTraversalWork(t *testing.T) {
	var branching interface{} = map[string]interface{}{"leaf": true}
	for depth := 0; depth < artifactSemanticContextMaxDepth; depth++ {
		items := make([]interface{}, artifactSemanticContextMaxItems)
		for index := range items {
			items[index] = branching
		}
		branching = items
	}
	if _, ok := artifactPageContextSemantic(branching); ok {
		t.Fatal("high-branch semantic context exceeded its bounded traversal budget")
	}
}

func TestArtifactPageContextDropsSemanticWhenCombinedBudgetIsExceeded(t *testing.T) {
	controls := make([]interface{}, 20)
	for index := range controls {
		controls[index] = map[string]interface{}{
			"type":  "text",
			"name":  fmt.Sprintf("field_%d", index),
			"value": strings.Repeat("v", 512),
			"text":  strings.Repeat("t", 128),
		}
	}
	semanticContext := make(map[string]interface{}, 6)
	for index := 0; index < 6; index++ {
		semanticContext[fmt.Sprintf("section_%d", index)] = strings.Repeat("s", 1000)
	}
	pageContext, ok := parseArtifactPageContextCandidate(map[string]interface{}{
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    strings.Repeat("x", 1000),
			"controls":         controls,
			"semantic_context": semanticContext,
		},
	})
	if !ok {
		t.Fatal("generic page context was lost when only the combined budget was exceeded")
	}
	if len(pageContext["controls"].([]interface{})) != 20 {
		t.Fatalf("generic controls were lost: %#v", pageContext)
	}
	if _, exists := pageContext["semantic_context"]; exists {
		t.Fatalf("combined oversized semantic context was retained: %#v", pageContext)
	}
}

func TestParseArtifactPageContextCandidateDropsOversizedPageContext(t *testing.T) {
	pageContext, ok := parseArtifactPageContextCandidate(map[string]interface{}{
		artifactPageContextMetadataKey: map[string]interface{}{
			"contract_version": artifactPageContextContract,
			"observed_at":      "2026-08-07T12:00:00Z",
			"selected_text":    strings.Repeat("x", artifactPageContextMaxBytes),
		},
	})
	if ok || pageContext != nil {
		t.Fatalf("oversized page context was retained: %#v", pageContext)
	}
}

func TestCreateArtifactContextSnapshotBoundsArtifactResolution(t *testing.T) {
	store := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			440: {ID: 440, AccountType: types.AccountBot},
		},
		owners: map[int64]int64{440: 7},
	}
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

	handler := NewArtifactContextSnapshotHandler(hub)
	req := httptest.NewRequest(http.MethodPost, "/api/artifact-context/snapshots", strings.NewReader(`{
		"topic_id":"p2p_7_440",
		"artifact_ref":{"contract_version":"catsco.artifact-ref.v1","id":"lesson-game","currently_visible":true}
	}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	recorder := httptest.NewRecorder()
	handler.HandleUserSnapshots(recorder, req)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
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

func TestArtifactContextRefOnlyReachesMatchingAgentAndNeverHistory(t *testing.T) {
	delivery := &artifactContextDeliveryRef{
		Ref:      "acr_" + strings.Repeat("x", 43),
		AgentUID: 440,
	}
	matching := withArtifactContextDeliveryRef(map[string]interface{}{"trace": "kept"}, delivery, 440)
	if matching[artifactContextRefMetadataKey] != delivery.Ref {
		t.Fatalf("matching agent lost delivery ref: %#v", matching)
	}
	other := withArtifactContextDeliveryRef(map[string]interface{}{"trace": "kept"}, delivery, 441)
	if _, ok := other[artifactContextRefMetadataKey]; ok || other["trace"] != "kept" {
		t.Fatalf("non-matching recipient metadata = %#v", other)
	}

	store := &identityMessageStore{users: map[int64]*types.User{
		7:   {ID: 7, AccountType: types.AccountHuman},
		440: {ID: 440, AccountType: types.AccountBot},
		441: {ID: 441, AccountType: types.AccountBot},
	}}
	hub := NewHub(store, nil)
	message := &types.Message{
		ID:      12,
		TopicID: "p2p_7_440",
		FromUID: 7,
		Content: `"改一下右侧标题"`,
		MsgType: "text",
		Metadata: map[string]interface{}{
			"trace":                        "kept",
			artifactContextRefMetadataKey:  delivery.Ref,
			artifactContextMetadataKey:     map[string]interface{}{"id": "lesson-game"},
			artifactRefMetadataKey:         map[string]interface{}{"id": "lesson-game"},
			artifactPageContextMetadataKey: map[string]interface{}{"selected_text": "secret"},
		},
		CreatedAt: time.Now(),
	}
	matchingHistory := hub.historyMessageDataForRecipient(440, message)
	if matchingHistory.Metadata["trace"] != "kept" {
		t.Fatalf("unrelated history metadata was lost: %#v", matchingHistory.Metadata)
	}
	for _, key := range []string{artifactContextRefMetadataKey, artifactContextMetadataKey, artifactRefMetadataKey, artifactPageContextMetadataKey} {
		if _, ok := matchingHistory.Metadata[key]; ok {
			t.Fatalf("history leaked %q: %#v", key, matchingHistory.Metadata)
		}
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
		"owner",
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
		"lesson-game", "", 7, "owner",
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

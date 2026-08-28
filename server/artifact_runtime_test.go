package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type artifactRuntimeManifestResolverFunc func(context.Context, ArtifactContextRecord, int64) (ArtifactRuntimeManifest, error)

func (f artifactRuntimeManifestResolverFunc) ResolveArtifactRuntimeManifest(
	ctx context.Context,
	record ArtifactContextRecord,
	displayedVersion int64,
) (ArtifactRuntimeManifest, error) {
	return f(ctx, record, displayedVersion)
}

type artifactRuntimeMemoryStore struct {
	*identityMessageStore
	mu     sync.Mutex
	states map[string]*store.ArtifactRuntimeState
	events []*store.ArtifactRuntimeEvent
	nextID int64
	now    time.Time
}

func (s *artifactRuntimeMemoryStore) stateKey(agentUID int64, artifactID, namespace, key string) string {
	return strings.Join([]string{formatUID(agentUID), artifactID, namespace, key}, "|")
}

func (s *artifactRuntimeMemoryStore) GetArtifactRuntimeState(_ context.Context, agentUID int64, artifactID, namespace, key string) (*store.ArtifactRuntimeState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[s.stateKey(agentUID, artifactID, namespace, key)]
	if state == nil {
		return nil, false, nil
	}
	clone := *state
	clone.Value = append(json.RawMessage(nil), state.Value...)
	return &clone, true, nil
}

func (s *artifactRuntimeMemoryStore) ListArtifactRuntimeStates(_ context.Context, agentUID int64, artifactID string, _ int) ([]*store.ArtifactRuntimeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*store.ArtifactRuntimeState, 0)
	for _, state := range s.states {
		if state.AgentUID != agentUID || state.ArtifactID != artifactID {
			continue
		}
		clone := *state
		clone.Value = append(json.RawMessage(nil), state.Value...)
		result = append(result, &clone)
	}
	return result, nil
}

func (s *artifactRuntimeMemoryStore) PutArtifactRuntimeState(_ context.Context, candidate *store.ArtifactRuntimeState, baseRevision int64) (*store.ArtifactRuntimeState, *store.ArtifactRuntimeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.stateKey(candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key)
	current := s.states[key]
	currentRevision := int64(0)
	if current != nil {
		currentRevision = current.Revision
	}
	if currentRevision != baseRevision {
		return nil, nil, &store.ArtifactRuntimeRevisionConflict{CurrentRevision: currentRevision}
	}
	next := *candidate
	next.Value = append(json.RawMessage(nil), candidate.Value...)
	next.Revision = baseRevision + 1
	next.UpdatedAt = s.now.Add(time.Duration(next.Revision) * time.Second)
	if current == nil {
		next.CreatedAt = next.UpdatedAt
	} else {
		next.CreatedAt = current.CreatedAt
	}
	s.states[key] = &next
	s.nextID++
	event := &store.ArtifactRuntimeEvent{
		EventID: s.nextID, EventType: "state.updated", AgentUID: next.AgentUID,
		ArtifactID: next.ArtifactID, Namespace: next.Namespace, Key: next.Key,
		Revision: next.Revision, UpdatedByUID: next.UpdatedByUID, UpdatedBy: next.UpdatedBy,
		CreatedAt: next.UpdatedAt,
	}
	s.events = append(s.events, event)
	stateClone := next
	eventClone := *event
	return &stateClone, &eventClone, nil
}

func (s *artifactRuntimeMemoryStore) ListArtifactRuntimeEvents(_ context.Context, agentUID int64, artifactID string, afterEventID int64, limit int) ([]*store.ArtifactRuntimeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*store.ArtifactRuntimeEvent, 0)
	for _, event := range s.events {
		if event.AgentUID == agentUID && event.ArtifactID == artifactID && event.EventID > afterEventID {
			clone := *event
			result = append(result, &clone)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *artifactRuntimeMemoryStore) LatestArtifactRuntimeEventID(_ context.Context, agentUID int64, artifactID string) (int64, error) {
	events, _ := s.ListArtifactRuntimeEvents(context.Background(), agentUID, artifactID, 0, 1_000)
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].EventID, nil
}

func newArtifactRuntimeTestHandler(t *testing.T) (*ArtifactRuntimeHandler, string) {
	t.Helper()
	identity := &identityMessageStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman, State: 0},
			440: {ID: 440, AccountType: types.AccountBot, State: 0},
		},
		owners: map[int64]int64{440: 7},
	}
	db := &artifactRuntimeMemoryStore{
		identityMessageStore: identity,
		states:               make(map[string]*store.ArtifactRuntimeState),
		now:                  time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
	}
	hub := NewHub(db, nil)
	record := ArtifactContextRecord{
		ID: "risk-register", Title: "Risk register", Kind: "html",
		URL:            "https://agent-440.artifacts.catsco.fun:19991/artifacts/risk-register/latest/",
		PublishVersion: 4,
	}
	hub.SetArtifactContextResolver(artifactContextResolverFunc(func(_ context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
		if agentUID != 440 || artifactID != record.ID {
			t.Fatalf("unexpected runtime target agent=%d artifact=%s", agentUID, artifactID)
		}
		return record, nil
	}))
	hub.SetArtifactRuntimeManifestResolver(artifactRuntimeManifestResolverFunc(func(_ context.Context, resolved ArtifactContextRecord, version int64) (ArtifactRuntimeManifest, error) {
		if resolved.ID != record.ID || version != 4 {
			t.Fatalf("unexpected manifest target artifact=%s version=%d", resolved.ID, version)
		}
		return ArtifactRuntimeManifest{
			Version:  "0.1",
			Surfaces: []ArtifactRuntimeSurface{{ID: "risk-list"}},
			State:    []ArtifactRuntimeStateDeclaration{{Namespace: "risks", Mode: "read-write"}},
		}, nil
	}))
	snapshot, _, err := hub.artifactContextSnapshots.create(artifactContextSnapshot{
		ActorUID: 7, TopicID: "p2p_7_440", AgentUID: 440,
		Artifact: record, DisplayedVersion: 4,
		PageContext: map[string]interface{}{
			"semantic_context": map[string]interface{}{
				"runtime_view": map[string]interface{}{
					"surface": "risk-list", "selectedIds": []interface{}{"r1"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create Runtime context snapshot: %v", err)
	}
	return NewArtifactRuntimeHandler(hub, db), snapshot.Ref
}

func TestArtifactRuntimeBotObserveAndApply(t *testing.T) {
	handler, contextRef := newArtifactRuntimeTestHandler(t)

	putBody := `{
		"contract_version":"catsco.artifact-runtime-request.v1",
		"operation":"state.put",
		"context_ref":"` + contextRef + `",
		"namespace":"risks",
		"key":"main",
		"base_revision":0,
		"value":{"items":[{"id":"r1","status":"open"}]}
	}`
	putRequest := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(putBody))
	putRequest = putRequest.WithContext(context.WithValue(putRequest.Context(), uidKey, int64(440)))
	putResponse := httptest.NewRecorder()
	handler.HandleBot(putResponse, putRequest)
	if putResponse.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", putResponse.Code, putResponse.Body.String())
	}
	var put map[string]interface{}
	if err := json.Unmarshal(putResponse.Body.Bytes(), &put); err != nil {
		t.Fatalf("decode put: %v", err)
	}
	if put["applied"] != true || put["state"].(map[string]interface{})["revision"] != float64(1) {
		t.Fatalf("unexpected put response: %#v", put)
	}

	observeRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/bot/artifact-runtime?context_ref="+contextRef+"&namespace=risks&key=main",
		nil,
	)
	observeRequest = observeRequest.WithContext(context.WithValue(observeRequest.Context(), uidKey, int64(440)))
	observeResponse := httptest.NewRecorder()
	handler.HandleBot(observeResponse, observeRequest)
	if observeResponse.Code != http.StatusOK {
		t.Fatalf("observe status=%d body=%s", observeResponse.Code, observeResponse.Body.String())
	}
	var observed map[string]interface{}
	if err := json.Unmarshal(observeResponse.Body.Bytes(), &observed); err != nil {
		t.Fatalf("decode observation: %v", err)
	}
	view := observed["untrusted"].(map[string]interface{})["view"].(map[string]interface{})
	if view["surface"] != "risk-list" || observed["state"].(map[string]interface{})["revision"] != float64(1) {
		t.Fatalf("unexpected Runtime observation: %#v", observed)
	}

	patchBody := `{
		"contract_version":"catsco.artifact-runtime-request.v1",
		"operation":"state.patch",
		"context_ref":"` + contextRef + `",
		"namespace":"risks",
		"key":"main",
		"base_revision":1,
		"patch":[{"op":"replace","path":"/items/0/status","value":"closed"}]
	}`
	patchRequest := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(patchBody))
	patchRequest = patchRequest.WithContext(context.WithValue(patchRequest.Context(), uidKey, int64(440)))
	patchResponse := httptest.NewRecorder()
	handler.HandleBot(patchResponse, patchRequest)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patchResponse.Code, patchResponse.Body.String())
	}
	var patched map[string]interface{}
	if err := json.Unmarshal(patchResponse.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patched["state"].(map[string]interface{})["revision"] != float64(2) ||
		patched["event"].(map[string]interface{})["type"] != "state.updated" {
		t.Fatalf("unexpected patch response: %#v", patched)
	}
}

func TestArtifactRuntimeRevisionConflictDoesNotCreateEvent(t *testing.T) {
	handler, contextRef := newArtifactRuntimeTestHandler(t)
	first := `{"contract_version":"catsco.artifact-runtime-request.v1","operation":"state.put","context_ref":"` + contextRef + `","namespace":"risks","key":"main","base_revision":0,"value":{"items":[]}}`
	request := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(first))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
	handler.HandleBot(httptest.NewRecorder(), request)

	stale := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(first))
	stale = stale.WithContext(context.WithValue(stale.Context(), uidKey, int64(440)))
	response := httptest.NewRecorder()
	handler.HandleBot(response, stale)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"current_revision":1`) {
		t.Fatalf("conflict status=%d body=%s", response.Code, response.Body.String())
	}
	store := handler.store.(*artifactRuntimeMemoryStore)
	if len(store.events) != 1 {
		t.Fatalf("events=%d, want 1", len(store.events))
	}
}

func TestArtifactRuntimePutRejectsDuplicateStateFields(t *testing.T) {
	handler, contextRef := newArtifactRuntimeTestHandler(t)
	body := `{"contract_version":"catsco.artifact-runtime-request.v1","operation":"state.put","context_ref":"` + contextRef + `","namespace":"risks","key":"main","base_revision":0,"value":{"status":"open","status":"closed"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
	response := httptest.NewRecorder()
	handler.HandleBot(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_request") {
		t.Fatalf("duplicate State status=%d body=%s", response.Code, response.Body.String())
	}
	if len(handler.store.(*artifactRuntimeMemoryStore).events) != 0 {
		t.Fatal("invalid State must not create an Event")
	}
}

func TestArtifactRuntimePutRejectsNonCanonicalStateKey(t *testing.T) {
	handler, contextRef := newArtifactRuntimeTestHandler(t)
	body := `{"contract_version":"catsco.artifact-runtime-request.v1","operation":"state.put","context_ref":"` + contextRef + `","namespace":" risks ","key":"main","base_revision":0,"value":{"status":"open"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
	response := httptest.NewRecorder()
	handler.HandleBot(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_state_key") {
		t.Fatalf("non-canonical key status=%d body=%s", response.Code, response.Body.String())
	}
	if len(handler.store.(*artifactRuntimeMemoryStore).states) != 0 {
		t.Fatal("non-canonical State key was persisted")
	}
}

func TestArtifactRuntimePutRejectsOversizedNamespace(t *testing.T) {
	handler, contextRef := newArtifactRuntimeTestHandler(t)
	body := `{"contract_version":"catsco.artifact-runtime-request.v1","operation":"state.put","context_ref":"` + contextRef + `","namespace":"` + strings.Repeat("a", 65) + `","key":"main","base_revision":0,"value":{"status":"open"}}`
	request := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(440)))
	response := httptest.NewRecorder()
	handler.HandleBot(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_state_key") {
		t.Fatalf("oversized namespace status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestArtifactRuntimeViewerRequiresConnectedPreview(t *testing.T) {
	handler, _ := newArtifactRuntimeTestHandler(t)
	human := &Client{
		uid: 7, accountType: types.AccountHuman,
		connectionID: "runtime-viewer", send: make(chan []byte, 1),
	}
	handler.hub.ensureClientRuntimeRoute(human)
	handler.hub.addClient(human)
	previewSession, err := handler.hub.artifactPreviewSessions.issue(7, handler.hub.clientRoute(human))
	if err != nil {
		t.Fatalf("issue Runtime preview session: %v", err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"contract_version": artifactRuntimeRequestContract,
		"operation":        "connect",
		"topic_id":         "p2p_7_440",
		"artifact_ref": map[string]interface{}{
			"contract_version":  artifactRefContract,
			"id":                "risk-register",
			"displayed_version": 4,
			"currently_visible": true,
		},
		"preview_session": previewSession,
	})
	if err != nil {
		t.Fatalf("encode Runtime connect: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/artifact-runtime", strings.NewReader(string(body)))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(7)))
	response := httptest.NewRecorder()
	handler.HandleUser(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("connected viewer status=%d body=%s", response.Code, response.Body.String())
	}
	var connected map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &connected); err != nil {
		t.Fatalf("decode Runtime connect: %v", err)
	}
	artifact := connected["artifact"].(map[string]interface{})
	runtime := connected["runtime"].(map[string]interface{})
	if artifact["id"] != "risk-register" || artifact["displayed_version"] != float64(4) ||
		runtime["version"] != "0.1" {
		t.Fatalf("unexpected Runtime connect response: %#v", connected)
	}

	handler.hub.removeClient(human)
	disconnectedRequest := httptest.NewRequest(http.MethodPost, "/api/artifact-runtime", strings.NewReader(string(body)))
	disconnectedRequest = disconnectedRequest.WithContext(context.WithValue(disconnectedRequest.Context(), uidKey, int64(7)))
	disconnectedResponse := httptest.NewRecorder()
	handler.HandleUser(disconnectedResponse, disconnectedRequest)
	if disconnectedResponse.Code != http.StatusConflict ||
		!strings.Contains(disconnectedResponse.Body.String(), "preview_disconnected") {
		t.Fatalf("disconnected viewer status=%d body=%s", disconnectedResponse.Code, disconnectedResponse.Body.String())
	}
}

func TestArtifactRuntimeEventCursorAdvancesPastUndeclaredNamespace(t *testing.T) {
	handler, _ := newArtifactRuntimeTestHandler(t)
	memoryStore := handler.store.(*artifactRuntimeMemoryStore)
	memoryStore.events = append(memoryStore.events, &store.ArtifactRuntimeEvent{
		EventID: 11, EventType: "state.updated", AgentUID: 440,
		ArtifactID: "risk-register", Namespace: "legacy", Key: "main", Revision: 3,
		UpdatedByUID: 440, UpdatedBy: "agent", CreatedAt: memoryStore.now,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/artifact-runtime", nil)
	response := httptest.NewRecorder()
	handler.handleOperation(response, request, artifactRuntimeAccess{
		ActorUID: 7, AgentUID: 440, TopicID: "p2p_7_440", DisplayedVersion: 4,
		Artifact: ArtifactContextRecord{ID: "risk-register", PublishVersion: 4},
		Manifest: ArtifactRuntimeManifest{
			Version:  "0.1",
			Surfaces: []ArtifactRuntimeSurface{{ID: "risk-list"}},
			State:    []ArtifactRuntimeStateDeclaration{{Namespace: "risks", Mode: "read-write"}},
		},
	}, "events.list", "", "", nil, nil, nil, 0, 50, "viewer")
	if response.Code != http.StatusOK {
		t.Fatalf("event list status=%d body=%s", response.Code, response.Body.String())
	}
	var result map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode event list: %v", err)
	}
	if result["event_cursor"] != float64(11) || len(result["events"].([]interface{})) != 0 {
		t.Fatalf("event cursor did not pass retired namespace: %#v", result)
	}
}

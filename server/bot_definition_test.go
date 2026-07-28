package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type botDefinitionTestStore struct {
	mu      sync.Mutex
	owner   int64
	record  *types.BotDefinitionRecord
	apply   *types.BotDefinitionApplyState
	legacy  *types.BotModelConfig
	lastAck string
}

func (s *botDefinitionTestStore) GetBotOwner(int64) (int64, error) {
	return s.owner, nil
}

func (s *botDefinitionTestStore) GetBotDefinition(int64) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDefinitionRecord(s.record), cloneApplyState(s.apply), nil
}

func (s *botDefinitionTestStore) SaveBotDefinition(
	_ int64,
	expected int64,
	definition *types.BotDefinition,
) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := int64(0)
	if s.record != nil {
		current = s.record.Revision
	}
	if current != expected {
		return cloneDefinitionRecord(s.record), cloneApplyState(s.apply), store.ErrStaleBotDefinitionRevision
	}
	s.record = &types.BotDefinitionRecord{Definition: *definition, Revision: current + 1, UpdatedAt: "now"}
	if s.apply == nil {
		s.apply = &types.BotDefinitionApplyState{}
	}
	s.apply.DesiredRevision = s.record.Revision
	return cloneDefinitionRecord(s.record), cloneApplyState(s.apply), nil
}

func (s *botDefinitionTestStore) AckBotDefinition(
	_ int64,
	revision int64,
	applyError string,
) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.Revision != revision {
		return cloneDefinitionRecord(s.record), cloneApplyState(s.apply), store.ErrStaleBotDefinitionRevision
	}
	if s.apply == nil {
		s.apply = &types.BotDefinitionApplyState{}
	}
	s.apply.DesiredRevision = revision
	s.apply.LastAttemptAt = "now"
	s.apply.LastError = applyError
	s.lastAck = applyError
	if applyError == "" {
		s.apply.AppliedRevision = revision
		s.apply.AppliedAt = "now"
	}
	return cloneDefinitionRecord(s.record), cloneApplyState(s.apply), nil
}

func (s *botDefinitionTestStore) InitializeDefaultBotDefinition(botUID int64) error {
	_, _, err := s.SaveBotDefinition(botUID, 0, store.NewDefaultBotDefinition(botUID))
	return err
}

func (s *botDefinitionTestStore) GetBotModelConfig(int64) (*types.BotModelConfig, error) {
	if s.legacy == nil {
		return &types.BotModelConfig{}, nil
	}
	copy := *s.legacy
	return &copy, nil
}

func (s *botDefinitionTestStore) MarkBotModelRuntimeProtocol(int64, string) (*types.BotModelConfig, error) {
	return s.GetBotModelConfig(0)
}

func (s *botDefinitionTestStore) SaveBotDesiredModelConfig(
	int64, string, string, string, string,
) (*types.BotModelConfig, error) {
	return s.GetBotModelConfig(0)
}

func (s *botDefinitionTestStore) AckBotModelConfig(
	int64, int64, string, string, string, string,
) (*types.BotModelConfig, error) {
	return s.GetBotModelConfig(0)
}

func TestBotDefinitionOwnerPatchIsFieldLevelAndRejectsStaleRevision(t *testing.T) {
	db := &botDefinitionTestStore{
		owner: 7,
		record: &types.BotDefinitionRecord{
			Definition: *store.NewDefaultBotDefinition(43),
			Revision:   3,
		},
		apply: &types.BotDefinitionApplyState{DesiredRevision: 3},
	}
	handler := NewBotDefinitionHandler(db, db, db)
	promptBody := `{"expected_revision":3,"prompt":{"selected":"custom","customSystemPrompt":"You are careful."}}`
	resp := ownerDefinitionRequest(handler, http.MethodPatch, promptBody, 7)
	if resp.Code != http.StatusOK {
		t.Fatalf("prompt patch failed: %d %s", resp.Code, resp.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	definition := body["definition"].(map[string]interface{})
	model := definition["model"].(map[string]interface{})
	if model["modelId"] != defaultBotModelID {
		t.Fatalf("prompt patch changed model: %#v", model)
	}
	if body["revision"].(float64) != 4 {
		t.Fatalf("unexpected revision: %#v", body["revision"])
	}

	stale := ownerDefinitionRequest(handler, http.MethodPatch,
		`{"expected_revision":3,"model":{"kind":"catalog","modelId":"gpt-5.6-sol","reasoningEffort":"high"}}`, 7)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "revision_conflict") {
		t.Fatalf("stale patch was not rejected: %d %s", stale.Code, stale.Body.String())
	}
}

func TestBotDefinitionCustomSecretOwnerRedactedRuntimeDecryptedAndAckRedactsError(t *testing.T) {
	t.Setenv(botModelEncryptionKeyEnv, strings.Repeat("11", 32))
	db := &botDefinitionTestStore{
		owner: 7,
		record: &types.BotDefinitionRecord{
			Definition: *store.NewDefaultBotDefinition(43),
			Revision:   1,
		},
		apply: &types.BotDefinitionApplyState{DesiredRevision: 1},
	}
	handler := NewBotDefinitionHandler(db, db, db)
	secret := "sk-super-secret-value"
	patch := `{"expected_revision":1,"model":{"kind":"custom","protocol":"openai-chat-completions",` +
		`"apiBase":"https://example.com/v1","model":"example-model","apiKey":"` + secret + `",` +
		`"contextWindowTokens":128000}}`
	owner := ownerDefinitionRequest(handler, http.MethodPatch, patch, 7)
	if owner.Code != http.StatusOK {
		t.Fatalf("custom patch failed: %d %s", owner.Code, owner.Body.String())
	}
	if strings.Contains(owner.Body.String(), secret) || !strings.Contains(owner.Body.String(), "apiKeyConfigured") {
		t.Fatalf("owner response leaked or omitted key state: %s", owner.Body.String())
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil)
	runtimeReq = runtimeReq.WithContext(context.WithValue(runtimeReq.Context(), uidKey, int64(43)))
	runtime := httptest.NewRecorder()
	handler.HandleRuntimeDefinition(runtime, runtimeReq)
	if runtime.Code != http.StatusOK || !strings.Contains(runtime.Body.String(), secret) {
		t.Fatalf("runtime did not receive decrypted key: %d %s", runtime.Code, runtime.Body.String())
	}
	if runtime.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime secret response is cacheable")
	}

	ackReq := httptest.NewRequest(http.MethodPost, "/api/bot/definition/ack",
		bytes.NewBufferString(`{"revision":2,"error":"connector rejected `+secret+`"}`))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ack := httptest.NewRecorder()
	handler.HandleRuntimeAck(ack, ackReq)
	if ack.Code != http.StatusOK {
		t.Fatalf("ack failed: %d %s", ack.Code, ack.Body.String())
	}
	if strings.Contains(db.lastAck, secret) || !strings.Contains(db.lastAck, "[REDACTED]") {
		t.Fatalf("ack error was not redacted: %q", db.lastAck)
	}
}

func TestBotDefinitionMissingReturnsMigrationRequiredWithLegacyModel(t *testing.T) {
	db := &botDefinitionTestStore{
		owner: 7,
		legacy: &types.BotModelConfig{
			Kind: "catalog", ModelID: "minimax-m3", Revision: 4,
		},
	}
	handler := NewBotDefinitionHandler(db, db, db)
	resp := ownerDefinitionRequest(handler, http.MethodGet, "", 7)
	if resp.Code != http.StatusConflict ||
		!strings.Contains(resp.Body.String(), "migration_required") ||
		!strings.Contains(resp.Body.String(), "legacy_model") {
		t.Fatalf("unexpected migration response: %d %s", resp.Code, resp.Body.String())
	}
}

func TestCatalogDefinitionSurvivesUnavailableSavedCustomCredential(t *testing.T) {
	t.Setenv(botModelEncryptionKeyEnv, strings.Repeat("11", 32))
	definition := store.NewDefaultBotDefinition(43)
	definition.SavedCustomModel = &types.BotDefinitionCustomModel{
		Kind: "custom", APIKeyEncrypted: "not-valid-ciphertext",
	}
	db := &botDefinitionTestStore{
		owner: 7,
		record: &types.BotDefinitionRecord{
			Definition: *definition,
			Revision:   2,
		},
		apply: &types.BotDefinitionApplyState{DesiredRevision: 2},
	}
	handler := NewBotDefinitionHandler(db, db, db)

	get := ownerDefinitionRequest(handler, http.MethodGet, "", 7)
	if get.Code != http.StatusOK ||
		!strings.Contains(get.Body.String(), "savedCustomModelUnavailableReason") {
		t.Fatalf("catalog Definition was blocked by saved custom credential: %d %s", get.Code, get.Body.String())
	}
	patch := ownerDefinitionRequest(handler, http.MethodPatch,
		`{"expected_revision":2,"prompt":{"selected":"custom","customSystemPrompt":"still writable"}}`, 7)
	if patch.Code != http.StatusOK {
		t.Fatalf("prompt patch committed but response failed: %d %s", patch.Code, patch.Body.String())
	}
	if db.record == nil || db.record.Revision != 3 {
		t.Fatalf("prompt patch was not committed: %#v", db.record)
	}
}

func TestCustomDefinitionPatchCanClearMaxTokensToZero(t *testing.T) {
	t.Setenv(botModelEncryptionKeyEnv, strings.Repeat("22", 32))
	db := &botDefinitionTestStore{
		owner:  7,
		record: &types.BotDefinitionRecord{Definition: *store.NewDefaultBotDefinition(43), Revision: 1},
		apply:  &types.BotDefinitionApplyState{DesiredRevision: 1},
	}
	handler := NewBotDefinitionHandler(db, db, db)
	create := ownerDefinitionRequest(handler, http.MethodPatch,
		`{"expected_revision":1,"model":{"kind":"custom","protocol":"openai-chat-completions",`+
			`"apiBase":"https://example.test/v1","model":"m","apiKey":"sk-key",`+
			`"contextWindowTokens":128000,"maxTokens":4096,"temperature":0.7,"reasoningEffort":"high"}}`, 7)
	if create.Code != http.StatusOK {
		t.Fatalf("initial custom patch failed: %d %s", create.Code, create.Body.String())
	}
	clear := ownerDefinitionRequest(handler, http.MethodPatch,
		`{"expected_revision":2,"model":{"kind":"custom","maxTokens":0,"temperature":null,"reasoningEffort":""}}`, 7)
	if clear.Code != http.StatusOK {
		t.Fatalf("maxTokens clear failed: %d %s", clear.Code, clear.Body.String())
	}
	if db.record == nil || db.record.Definition.Model.MaxTokens != 0 ||
		db.record.Definition.Model.Temperature != nil ||
		db.record.Definition.Model.ReasoningEffort != "" {
		t.Fatalf("optional custom values were not cleared: %#v", db.record)
	}
}

func ownerDefinitionRequest(
	handler *BotDefinitionHandler,
	method string,
	body string,
	ownerUID int64,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/bots/definition?uid=43", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, ownerUID))
	resp := httptest.NewRecorder()
	handler.HandleOwnerDefinition(resp, req)
	return resp
}

func cloneDefinitionRecord(value *types.BotDefinitionRecord) *types.BotDefinitionRecord {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	var copy types.BotDefinitionRecord
	_ = json.Unmarshal(raw, &copy)
	return &copy
}

func cloneApplyState(value *types.BotDefinitionApplyState) *types.BotDefinitionApplyState {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

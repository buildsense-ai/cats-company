package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type botDefinitionTestStore struct {
	owners  map[int64]int64
	records map[int64]*types.BotDefinitionRecord
}

func (s *botDefinitionTestStore) GetBotOwner(botUID int64) (int64, error) {
	if owner, ok := s.owners[botUID]; ok {
		return owner, nil
	}
	return 0, store.ErrStaleBotModelRevision
}

func (s *botDefinitionTestStore) GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error) {
	if record := s.records[botUID]; record != nil {
		return cloneBotDefinitionRecord(record), nil
	}
	return &types.BotDefinitionRecord{}, nil
}

func (s *botDefinitionTestStore) CreateBotDefinitionIfAbsent(
	botUID int64,
	definition types.BotDefinition,
) (*types.BotDefinitionRecord, error) {
	record := s.records[botUID]
	if record == nil || !record.Exists {
		record = &types.BotDefinitionRecord{Definition: definition, Exists: true}
		if record.Definition.Prompt == nil {
			record.Definition.Prompt = &types.BotPromptDefinition{Selected: "default"}
		}
		s.records[botUID] = record
	}
	return cloneBotDefinitionRecord(record), nil
}

func (s *botDefinitionTestStore) UpdateBotDefinitionModel(
	botUID, expectedRevision int64,
	model types.BotDefinitionModel,
) (*types.BotDefinitionRecord, error) {
	record := s.ensure(botUID)
	if expectedRevision >= 0 && expectedRevision != record.Runtime.DesiredRevision {
		return nil, store.ErrStaleBotModelRevision
	}
	record.Definition.Model = model
	if record.Definition.Prompt == nil {
		record.Definition.Prompt = &types.BotPromptDefinition{Selected: "default"}
	}
	record.Runtime.DesiredRevision++
	return cloneBotDefinitionRecord(record), nil
}

func (s *botDefinitionTestStore) UpdateBotDefinitionPrompt(
	botUID, expectedRevision int64,
	prompt types.BotPromptDefinition,
) (*types.BotDefinitionRecord, error) {
	record := s.ensure(botUID)
	if expectedRevision >= 0 && expectedRevision != record.Runtime.DesiredRevision {
		return nil, store.ErrStaleBotModelRevision
	}
	record.Definition.Prompt = &prompt
	if record.Definition.Model.Kind == "" {
		record.Definition.Model = types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"}
	}
	record.Runtime.DesiredRevision++
	return cloneBotDefinitionRecord(record), nil
}

func (s *botDefinitionTestStore) AckBotDefinition(
	botUID, revision int64,
	applyError string,
) (*types.BotDefinitionRecord, error) {
	record := s.ensure(botUID)
	if revision != record.Runtime.DesiredRevision {
		return nil, store.ErrStaleBotModelRevision
	}
	record.Runtime.LastAttemptRevision = revision
	record.Runtime.LastError = applyError
	if applyError == "" {
		record.Runtime.AppliedRevision = revision
	}
	return cloneBotDefinitionRecord(record), nil
}

func (s *botDefinitionTestStore) ensure(botUID int64) *types.BotDefinitionRecord {
	record := s.records[botUID]
	if record == nil {
		record = &types.BotDefinitionRecord{
			Definition: types.BotDefinition{
				Schema: types.BotDefinitionSchema,
				BotID:  "43",
			},
			Exists: true,
		}
		s.records[botUID] = record
	}
	return record
}

func cloneBotDefinitionRecord(record *types.BotDefinitionRecord) *types.BotDefinitionRecord {
	data, _ := json.Marshal(record)
	var copy types.BotDefinitionRecord
	_ = json.Unmarshal(data, &copy)
	copy.Exists = record.Exists
	return &copy
}

func TestBotDefinitionFieldUpdatesPreserveTheOtherField(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
					Prompt: &types.BotPromptDefinition{Selected: "custom", CustomSystemPrompt: "Keep me."},
				},
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 2},
				Exists:  true,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))

	modelReq := httptest.NewRequest(http.MethodPatch, "/api/bots/definition/model?uid=43", strings.NewReader(
		`{"revision":2,"model":{"kind":"catalog","modelId":"gpt-5.6-sol","reasoningEffort":"high"}}`,
	))
	modelReq = modelReq.WithContext(context.WithValue(modelReq.Context(), uidKey, int64(7)))
	modelRec := httptest.NewRecorder()
	handler.HandleOwnerModel(modelRec, modelReq)
	if modelRec.Code != http.StatusOK {
		t.Fatalf("model status=%d body=%s", modelRec.Code, modelRec.Body.String())
	}
	if got := db.records[43]; got.Definition.Model.ModelID != "gpt-5.6-sol" ||
		got.Definition.Prompt == nil ||
		got.Definition.Prompt.CustomSystemPrompt != "Keep me." ||
		got.Runtime.DesiredRevision != 3 {
		t.Fatalf("record after model update=%+v", got)
	}

	promptReq := httptest.NewRequest(http.MethodPatch, "/api/bots/definition/prompt?uid=43", strings.NewReader(
		`{"revision":3,"prompt":{"selected":"default"}}`,
	))
	promptReq = promptReq.WithContext(context.WithValue(promptReq.Context(), uidKey, int64(7)))
	promptRec := httptest.NewRecorder()
	handler.HandleOwnerPrompt(promptRec, promptReq)
	if promptRec.Code != http.StatusOK {
		t.Fatalf("prompt status=%d body=%s", promptRec.Code, promptRec.Body.String())
	}
	if got := db.records[43]; got.Definition.Model.ModelID != "gpt-5.6-sol" ||
		got.Definition.Prompt == nil ||
		got.Definition.Prompt.Selected != "default" ||
		got.Runtime.DesiredRevision != 4 {
		t.Fatalf("record after prompt update=%+v", got)
	}
}

func TestBotDefinitionRejectsStaleRevisionAndNonOwner(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
					Prompt: &types.BotPromptDefinition{Selected: "default"},
				},
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 5},
				Exists:  true,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))

	staleReq := httptest.NewRequest(http.MethodPatch, "/api/bots/definition/prompt?uid=43", strings.NewReader(
		`{"revision":4,"prompt":{"selected":"default"}}`,
	))
	staleReq = staleReq.WithContext(context.WithValue(staleReq.Context(), uidKey, int64(7)))
	staleRec := httptest.NewRecorder()
	handler.HandleOwnerPrompt(staleRec, staleReq)
	if staleRec.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", staleRec.Code, staleRec.Body.String())
	}

	friendReq := httptest.NewRequest(http.MethodGet, "/api/bots/definition?uid=43", nil)
	friendReq = friendReq.WithContext(context.WithValue(friendReq.Context(), uidKey, int64(85)))
	friendRec := httptest.NewRecorder()
	handler.HandleOwnerDefinition(friendRec, friendReq)
	if friendRec.Code != http.StatusForbidden {
		t.Fatalf("friend status=%d body=%s", friendRec.Code, friendRec.Body.String())
	}
}

func TestRuntimeDefinitionAcknowledgementTracksApplyState(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
					Prompt: &types.BotPromptDefinition{Selected: "default"},
				},
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 6},
				Exists:  true,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))

	getReq := httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), uidKey, int64(43)))
	getRec := httptest.NewRecorder()
	handler.HandleRuntimeDefinition(getRec, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"revision":6`) {
		t.Fatalf("runtime get status=%d body=%s", getRec.Code, getRec.Body.String())
	}

	ackReq := httptest.NewRequest(http.MethodPost, "/api/bot/definition/ack", strings.NewReader(
		`{"revision":6}`,
	))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ackRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if got := db.records[43].Runtime; got.AppliedRevision != 6 || got.LastAttemptRevision != 6 || got.LastError != "" {
		t.Fatalf("runtime=%+v", got)
	}
}

func TestRuntimeDefinitionAcknowledgementRedactsCustomModelSecret(t *testing.T) {
	enableBotModelEncryption(t)
	codec, err := newBotModelSecretCodecFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := codec.encrypt(43, []byte(`{
		"protocol":"openai-responses",
		"api_base":"https://models.example.com/v1",
		"model":"private-model",
		"api_key":"sk-runtime-secret",
		"context_window_tokens":256000
	}`))
	if err != nil {
		t.Fatal(err)
	}
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Model: types.BotDefinitionModel{
						Kind:             botModelKindCustom,
						APIKeyCiphertext: ciphertext,
					},
					Prompt: &types.BotPromptDefinition{Selected: "default"},
				},
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 6},
				Exists:  true,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))

	ackReq := httptest.NewRequest(http.MethodPost, "/api/bot/definition/ack", strings.NewReader(
		`{"revision":6,"error":"request failed with sk-runtime-secret"}`,
	))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ackRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if got := db.records[43].Runtime.LastError; got != "request failed with [REDACTED]" {
		t.Fatalf("last error=%q", got)
	}
}

func TestBotDefinitionCustomSecretIsEncryptedAndOnlyReturnedToRuntime(t *testing.T) {
	enableBotModelEncryption(t)
	db := &botDefinitionTestStore{
		owners:  map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))

	patchReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/bots/definition/model?uid=43",
		strings.NewReader(`{"model":{
			"kind":"custom",
			"protocol":"openai-responses",
			"apiBase":"https://models.example.com/v1/",
			"model":"private-model",
			"apiKey":"sk-definition-secret",
			"contextWindowTokens":256000,
			"maxTokens":8192
		}}`),
	)
	patchReq = patchReq.WithContext(context.WithValue(patchReq.Context(), uidKey, int64(7)))
	patchRec := httptest.NewRecorder()
	handler.HandleOwnerModel(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	stored := db.records[43].Definition.Model
	if stored.APIKeyCiphertext == "" || strings.Contains(stored.APIKeyCiphertext, "sk-definition-secret") {
		t.Fatalf("custom key was not encrypted: %+v", stored)
	}
	if strings.Contains(patchRec.Body.String(), "sk-definition-secret") ||
		!strings.Contains(patchRec.Body.String(), `"apiKeyConfigured":true`) ||
		!strings.Contains(patchRec.Body.String(), `"apiKeyHint":"****cret"`) {
		t.Fatalf("owner response exposed or omitted key metadata: %s", patchRec.Body.String())
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil)
	runtimeReq = runtimeReq.WithContext(context.WithValue(runtimeReq.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeDefinition(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK ||
		!strings.Contains(runtimeRec.Body.String(), `"apiKey":"sk-definition-secret"`) ||
		!strings.Contains(runtimeRec.Body.String(), `"apiBase":"https://models.example.com/v1"`) {
		t.Fatalf("runtime status=%d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}
}

func TestLegacyCustomModelMigrationRestoresCompleteDefinition(t *testing.T) {
	enableBotModelEncryption(t)
	codec, err := newBotModelSecretCodecFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload := `{
		"protocol":"anthropic",
		"api_base":"https://legacy.example.com",
		"model":"legacy-model",
		"api_key":"sk-legacy-secret",
		"context_window_tokens":128000,
		"max_tokens":4096,
		"reasoning_effort":"high"
	}`
	ciphertext, err := codec.encrypt(43, []byte(legacyPayload))
	if err != nil {
		t.Fatal(err)
	}
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Model: types.BotDefinitionModel{
						Kind:             botModelKindCustom,
						Model:            "legacy-model",
						APIKeyCiphertext: ciphertext,
					},
				},
				Exists: false,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))

	req := httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(43)))
	rec := httptest.NewRecorder()
	handler.HandleRuntimeDefinition(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"protocol":"anthropic"`) ||
		!strings.Contains(rec.Body.String(), `"apiBase":"https://legacy.example.com"`) ||
		!strings.Contains(rec.Body.String(), `"contextWindowTokens":128000`) ||
		!strings.Contains(rec.Body.String(), `"apiKey":"sk-legacy-secret"`) {
		t.Fatalf("legacy custom definition was incomplete: %s", rec.Body.String())
	}
	if got := db.records[43].Definition.Model; got.Protocol != "anthropic" ||
		got.APIBase != "https://legacy.example.com" ||
		got.ContextWindowTokens != 128000 {
		t.Fatalf("migrated model=%+v", got)
	}
}

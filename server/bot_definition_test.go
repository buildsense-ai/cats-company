package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type botDefinitionTestStore struct {
	owners  map[int64]int64
	records map[int64]*types.BotDefinitionRecord
	configs map[int64]*types.BotConfig
	friends map[[2]int64]bool
}

func (s *botDefinitionTestStore) GetBotOwner(botUID int64) (int64, error) {
	if owner, ok := s.owners[botUID]; ok {
		return owner, nil
	}
	return 0, store.ErrStaleBotModelRevision
}

func (s *botDefinitionTestStore) GetBotConfig(botUID int64) (*types.BotConfig, error) {
	if config := s.configs[botUID]; config != nil {
		copy := *config
		return &copy, nil
	}
	return nil, store.ErrStaleBotModelRevision
}

func (s *botDefinitionTestStore) AreFriends(uid1, uid2 int64) (bool, error) {
	return s.friends[[2]int64{uid1, uid2}] || s.friends[[2]int64{uid2, uid1}], nil
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
		store.RememberBotDefinitionCustomModel(record, definition.Model)
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
	store.RememberBotDefinitionCustomModel(record, model)
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

func (s *botDefinitionTestStore) UpdateBotDefinitionSkills(
	botUID, expectedRevision int64,
	skills []types.BotSkillRef,
) (*types.BotDefinitionRecord, error) {
	record := s.ensure(botUID)
	if expectedRevision >= 0 && expectedRevision != record.Runtime.DesiredRevision {
		return nil, store.ErrStaleBotModelRevision
	}
	if reflect.DeepEqual(record.Definition.Skills, skills) {
		return cloneBotDefinitionRecord(record), nil
	}
	record.Definition.Skills = append([]types.BotSkillRef(nil), skills...)
	if record.Definition.Model.Kind == "" {
		record.Definition.Model = types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"}
	}
	if record.Definition.Prompt == nil {
		record.Definition.Prompt = &types.BotPromptDefinition{Selected: "default"}
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
					Skills: []types.BotSkillRef{{
						Source: "skillhub", SkillID: "lin/review", Version: "1.2.0",
						ContentHash: testSkillHash,
					}},
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
		len(got.Definition.Skills) != 1 ||
		got.Definition.Skills[0].SkillID != "lin/review" ||
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
		len(got.Definition.Skills) != 1 ||
		got.Definition.Skills[0].SkillID != "lin/review" ||
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

func TestBotDefinitionPromptRequiresRevisionAndNormalizesWhitespaceBackup(t *testing.T) {
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
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 1},
				Exists:  true,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))

	for _, body := range []string{
		`{"prompt":{"selected":"default"}}`,
		`{"revision":null,"prompt":{"selected":"default"}}`,
		`{"revision":-1,"prompt":{"selected":"default"}}`,
	} {
		req := httptest.NewRequest(http.MethodPatch, "/api/bots/definition/prompt?uid=43", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
		rec := httptest.NewRecorder()
		handler.HandleOwnerPrompt(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "revision is required") {
			t.Fatalf("body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
	if got := db.records[43].Runtime.DesiredRevision; got != 1 {
		t.Fatalf("invalid revision requests changed desired revision to %d", got)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/bots/definition/prompt?uid=43", strings.NewReader(
		`{"revision":1,"prompt":{"selected":"default","customSystemPrompt":"  \n\t"}}`,
	))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleOwnerPrompt(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("whitespace backup status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := db.records[43].Definition.Prompt.CustomSystemPrompt; got != "" {
		t.Fatalf("whitespace backup was not normalized: %q", got)
	}
}

func TestBotDefinitionPromptBodyLimitAllowsEscapedOneMiBPrompt(t *testing.T) {
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
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 1},
				Exists:  true,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models))
	body, err := json.Marshal(botDefinitionPromptPatchRequest{
		Revision: func() *int64 { revision := int64(1); return &revision }(),
		Prompt: types.BotPromptDefinition{
			Selected:           "custom",
			CustomSystemPrompt: strings.Repeat(`<`, maxCustomSystemPromptBytes),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 5*maxCustomSystemPromptBytes {
		t.Fatalf("escaped test body did not exercise the sixfold JSON expansion: %d", len(body))
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/bots/definition/prompt?uid=43", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleOwnerPrompt(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := len([]byte(db.records[43].Definition.Prompt.CustomSystemPrompt)); got != maxCustomSystemPromptBytes {
		t.Fatalf("stored prompt bytes=%d", got)
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

func TestCatalogDefinitionShipsContextWindowTokens(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					// 存储为旧数据：catalog 模型不带 context window，
					// 响应必须从 catalog 权威补全，设备不依赖本地 profile。
					Model: types.BotDefinitionModel{Kind: "catalog", ModelID: "gpt-5.6-sol"},
				},
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 3},
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
	if getRec.Code != http.StatusOK {
		t.Fatalf("runtime get status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), `"modelId":"gpt-5.6-sol"`) ||
		!strings.Contains(getRec.Body.String(), `"contextWindowTokens":256000`) {
		t.Fatalf("catalog definition must ship cloud context window: body=%s", getRec.Body.String())
	}
}

func TestRuntimeDefinitionLeavesUntouchedEmptyRecordUnconfigured(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
				},
				Exists: true,
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
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["configured"] != false || response["revision"] != float64(0) || response["definition"] != nil {
		t.Fatalf("response=%v", response)
	}
}

func TestRuntimeDefinitionNormalizesHistoricalEmptyLocalHandoff(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Prompt: &types.BotPromptDefinition{Selected: "custom", CustomSystemPrompt: "Keep local runtime."},
				},
				Runtime: types.BotDefinitionRuntime{
					DesiredRevision: 3,
					AppliedKind:     botModelKindLocal,
					AppliedModelID:  botModelKindLocal,
				},
				Exists: true,
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
	body := rec.Body.String()
	for _, expected := range []string{
		`"configured":true`,
		`"revision":3`,
		`"kind":"local"`,
		`"modelId":"local"`,
		`"customSystemPrompt":"Keep local runtime."`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
	if db.records[43].Definition.Model.Kind != "" || db.records[43].Definition.Model.ModelID != "" {
		t.Fatalf("response normalization mutated storage: %+v", db.records[43].Definition.Model)
	}

	ackReq := httptest.NewRequest(http.MethodPost, "/api/bot/definition/ack", strings.NewReader(`{"revision":3}`))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ackRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK || !strings.Contains(ackRec.Body.String(), `"kind":"local"`) {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if got := db.records[43].Runtime; got.LastAttemptRevision != 3 || got.AppliedRevision != 3 {
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
		`{"revision":6,"error":"request failed with sk-runtime-secret; total_cny=500; max_tokens=8192"}`,
	))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ackRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if got := db.records[43].Runtime.LastError; got != "request failed with [REDACTED]; total_cny=500; max_tokens=8192" {
		t.Fatalf("last error=%q", got)
	}
	ownerReq := httptest.NewRequest(http.MethodGet, "/api/bots/definition?uid=43", nil)
	ownerReq = ownerReq.WithContext(context.WithValue(ownerReq.Context(), uidKey, int64(7)))
	ownerRec := httptest.NewRecorder()
	handler.HandleOwnerDefinition(ownerRec, ownerReq)
	if ownerRec.Code != http.StatusOK || !strings.Contains(ownerRec.Body.String(), `"lastError":"Bot 配置应用失败"`) {
		t.Fatalf("owner status=%d body=%s", ownerRec.Code, ownerRec.Body.String())
	}
	for _, sensitive := range []string{"total_cny", "max_tokens", "request failed"} {
		if strings.Contains(ownerRec.Body.String(), sensitive) {
			t.Fatalf("owner response leaked %s: %s", sensitive, ownerRec.Body.String())
		}
	}
	if got := db.records[43].Runtime.LastError; got != "request failed with [REDACTED]; total_cny=500; max_tokens=8192" {
		t.Fatalf("owner read changed internal last error=%q", got)
	}
}

func TestOwnerDefinitionErrorsDoNotExposeStoredCustomModelDetails(t *testing.T) {
	enableBotModelEncryption(t)
	db := &botDefinitionTestStore{
		owners:  map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	modelConfig := NewBotModelConfigHandler(db, models)
	invalidStored := cloudCustomModelConfig{
		Protocol: "anthropic", APIBase: "https://models.example.com", Model: "model-a", APIKey: "secret-a",
		ContextWindowTokens: 5000000, MaxTokens: 1000001,
	}
	plaintext, err := json.Marshal(invalidStored)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := modelConfig.secretCodec.encrypt(43, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	db.records[43] = &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "43",
			Model: types.BotDefinitionModel{
				Kind: botModelKindCustom, Protocol: "anthropic", APIBase: "https://models.example.com",
				Model: "model-a", APIKeyCiphertext: ciphertext,
			},
			Prompt: &types.BotPromptDefinition{Selected: "default"},
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 1},
		Exists:  true,
	}
	handler := NewBotDefinitionHandler(db, db, models, modelConfig)

	for _, tc := range []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantError  string
		handle     func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "get", method: http.MethodGet, path: "/api/bots/definition?uid=43",
			wantStatus: http.StatusServiceUnavailable, wantError: "bot definition could not be prepared",
			handle: handler.HandleOwnerDefinition,
		},
		{
			name: "patch", method: http.MethodPatch, path: "/api/bots/definition/model?uid=43",
			body:       `{"revision":1,"model":{"kind":"custom","protocol":"anthropic","apiBase":"https://models.example.com","model":"model-b","apiKey":"secret-b"}}`,
			wantStatus: http.StatusBadRequest, wantError: "bot model definition could not be updated",
			handle: handler.HandleOwnerModel,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
			rec := httptest.NewRecorder()
			tc.handle(rec, req)

			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), `"error":"`+tc.wantError+`"`) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			for _, sensitive := range []string{"context window", "max tokens", "5000000", "1000001", "secret-a"} {
				if strings.Contains(strings.ToLower(rec.Body.String()), sensitive) {
					t.Fatalf("owner response leaked %s: %s", sensitive, rec.Body.String())
				}
			}
		})
	}
	if got := db.records[43].Definition.Model.APIKeyCiphertext; got != ciphertext {
		t.Fatal("owner error handling changed the stored custom model")
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
	if strings.Contains(patchRec.Body.String(), "contextWindowTokens") || strings.Contains(patchRec.Body.String(), "maxTokens") {
		t.Fatalf("owner response leaked token limits: %s", patchRec.Body.String())
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil)
	runtimeReq = runtimeReq.WithContext(context.WithValue(runtimeReq.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeDefinition(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK ||
		!strings.Contains(runtimeRec.Body.String(), `"apiKey":"sk-definition-secret"`) ||
		!strings.Contains(runtimeRec.Body.String(), `"apiBase":"https://models.example.com/v1"`) ||
		!strings.Contains(runtimeRec.Body.String(), `"contextWindowTokens":256000`) {
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

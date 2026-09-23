package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestAdminReapplyBumpsCatalogRevisionWithoutChangingSelection(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{
			43: {
				Kind: botModelKindCatalog, ModelID: "deepseek-flash", ReasoningEffort: "high",
				Revision: 5, UpdatedAt: "2026-09-23T00:00:00Z",
				AppliedKind: botModelKindCatalog, AppliedModelID: "deepseek-flash",
				AppliedReasoning: "high", AppliedRevision: 5,
			},
		},
	}
	handler := NewBotModelConfigHandler(db, db)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=43", nil)
	rec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid response body: %v", err)
	}
	if body["status"] != "reapplied" || body["model_id"] != "deepseek-flash" ||
		body["reasoning_effort"] != "high" || body["revision"] != float64(6) || body["previous_revision"] != float64(5) {
		t.Fatalf("body=%v", body)
	}
	if body["management_enabled"] != true {
		t.Fatalf("management_enabled missing: body=%v", body)
	}
	got := db.models[43]
	if got.Revision != 6 || got.ModelID != "deepseek-flash" || got.ReasoningEffort != "high" || got.Kind != botModelKindCatalog {
		t.Fatalf("config=%+v", got)
	}
	// The previous revision stays applied until the device acknowledges the new one.
	if got.AppliedRevision != 5 {
		t.Fatalf("applied revision must not move before the device applies: %+v", got)
	}
}

func TestAdminReapplyResolvesLegacyCatalogAlias(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{
			43: {Kind: botModelKindCatalog, ModelID: "deepseek-v4-flash", ReasoningEffort: "high", Revision: 2},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=43", nil)
	rec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := db.models[43]; got.ModelID != "deepseek-flash" || got.Revision != 3 {
		t.Fatalf("config=%+v", got)
	}
}

func TestAdminReapplyRejectsNonCatalogSelections(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7, 44: 7, 45: 7},
		models: map[int64]*types.BotModelConfig{
			43: {Kind: botModelKindCustom, ModelID: "custom-model", Revision: 1},
			44: {},
			45: {Kind: botModelKindCatalog, ModelID: "retired-model", Revision: 1},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	for _, tc := range []struct {
		uid  string
		want int
	}{
		{"43", http.StatusConflict},
		{"44", http.StatusConflict},
		{"45", http.StatusConflict},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid="+tc.uid, nil)
		rec := httptest.NewRecorder()
		handler.HandleAdminReapplyModelConfig(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("uid=%s status=%d body=%s, want %d", tc.uid, rec.Code, rec.Body.String(), tc.want)
		}
	}
	if db.models[43].Revision != 1 || db.models[45].Revision != 1 {
		t.Fatalf("rejected reapplies must not touch storage: 43=%+v 45=%+v", db.models[43], db.models[45])
	}
	if db.models[44].Kind != "" || db.models[44].ModelID != "" {
		t.Fatalf("unconfigured bot storage changed: %+v", db.models[44])
	}
}

func TestAdminReapplyValidatesRequest(t *testing.T) {
	db := &botModelConfigTestStore{owners: map[int64]int64{43: 7}, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotModelConfigHandler(db, db)

	getReq := httptest.NewRequest(http.MethodGet, "/api/admin/bots/model-config/reapply?uid=43", nil)
	getRec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(getRec, getReq)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get status=%d, want 405", getRec.Code)
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=abc", nil)
	badRec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad uid status=%d, want 400", badRec.Code)
	}
}

func TestAdminReapplyReturnsNotFoundForUnknownBot(t *testing.T) {
	db := &botModelConfigTestStore{owners: map[int64]int64{43: 7}, models: map[int64]*types.BotModelConfig{}}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=99", nil)
	rec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
}

func TestAdminReapplyUnavailableWithoutModelStore(t *testing.T) {
	handler := NewBotModelConfigHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=43", nil)
	rec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", rec.Code, rec.Body.String())
	}
}

func TestAdminReapplyNormalizesLegacySelectionAndClearsLastError(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{
			43: {ModelID: "deepseek-v4-flash", Revision: 4, LastError: "apply failed", LastAttemptRevision: 4},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=43", nil)
	rec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := db.models[43]
	if got.Kind != botModelKindCatalog || got.ModelID != "deepseek-flash" || got.Revision != 5 {
		t.Fatalf("config=%+v", got)
	}
	if got.LastError != "" || got.LastAttemptRevision != 0 {
		t.Fatalf("retry state must reset: %+v", got)
	}
}

// botModelDefinitionTestStore mirrors the production definition backend: it
// implements store.BotDefinitionStore, so saveDesiredCatalogModelRevision must
// use UpdateBotDefinitionModel (DesiredRevision CAS) rather than the legacy
// bot-config table.
type botModelDefinitionTestStore struct {
	*botModelConfigTestStore
	records map[int64]*types.BotDefinitionRecord
}

func (s *botModelDefinitionTestStore) GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error) {
	if record := s.records[botUID]; record != nil {
		copy := *record
		return &copy, nil
	}
	return &types.BotDefinitionRecord{}, nil
}

func (s *botModelDefinitionTestStore) CreateBotDefinitionIfAbsent(botUID int64, definition types.BotDefinition) (*types.BotDefinitionRecord, error) {
	record := s.records[botUID]
	if record == nil {
		record = &types.BotDefinitionRecord{Definition: definition, Exists: true}
		s.records[botUID] = record
	}
	copy := *record
	return &copy, nil
}

func (s *botModelDefinitionTestStore) UpdateBotDefinitionModel(botUID, expectedRevision int64, model types.BotDefinitionModel) (*types.BotDefinitionRecord, error) {
	record := s.records[botUID]
	if record == nil {
		record = &types.BotDefinitionRecord{}
		s.records[botUID] = record
	}
	if expectedRevision >= 0 && record.Runtime.DesiredRevision != expectedRevision {
		return nil, store.ErrStaleBotModelRevision
	}
	record.Definition.Model = model
	record.Runtime.DesiredRevision++
	record.Runtime.LastAttemptRevision = 0
	record.Runtime.LastAttemptAt = ""
	record.Runtime.LastError = ""
	record.Exists = true
	copy := *record
	return &copy, nil
}

func (s *botModelDefinitionTestStore) UpdateBotDefinitionPrompt(botUID, expectedRevision int64, prompt types.BotPromptDefinition) (*types.BotDefinitionRecord, error) {
	return nil, errors.New("not implemented")
}

func (s *botModelDefinitionTestStore) UpdateBotDefinitionSkills(botUID, expectedRevision int64, skills []types.BotSkillRef) (*types.BotDefinitionRecord, error) {
	return nil, errors.New("not implemented")
}

func (s *botModelDefinitionTestStore) UpdateBotPromptVisibility(botUID int64, visibility types.BotPromptVisibility) (*types.BotDefinitionRecord, error) {
	return nil, errors.New("not implemented")
}

func (s *botModelDefinitionTestStore) ReportBotDefaultPrompt(botUID int64, snapshot types.BotDefaultPromptSnapshot) (*types.BotDefinitionRecord, bool, error) {
	return nil, false, errors.New("not implemented")
}

func (s *botModelDefinitionTestStore) AckBotDefinition(botUID, revision int64, applyError string) (*types.BotDefinitionRecord, error) {
	return nil, errors.New("not implemented")
}

func (s *botModelDefinitionTestStore) GetBotModelConfig(botUID int64) (*types.BotModelConfig, error) {
	if record := s.records[botUID]; record != nil && record.Exists {
		return legacyConfigForDefinition(record), nil
	}
	return s.botModelConfigTestStore.GetBotModelConfig(botUID)
}

func TestAdminReapplyBumpsDefinitionRevisionOnDefinitionBackends(t *testing.T) {
	db := &botModelDefinitionTestStore{
		botModelConfigTestStore: &botModelConfigTestStore{owners: map[int64]int64{43: 7}, models: map[int64]*types.BotModelConfig{}},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Model: types.BotDefinitionModel{Kind: botModelKindCatalog, ModelID: "deepseek-flash", ReasoningEffort: "high"},
				},
				Runtime: types.BotDefinitionRuntime{
					DesiredRevision: 6, AppliedRevision: 6,
					LastAttemptRevision: 6, LastError: "previous failure",
				},
				Exists: true,
			},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=43", nil)
	rec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	record := db.records[43]
	if record.Runtime.DesiredRevision != 7 {
		t.Fatalf("desired revision = %d, want 7", record.Runtime.DesiredRevision)
	}
	if record.Runtime.AppliedRevision != 6 {
		t.Fatalf("applied revision must stay until the device acks: %+v", record.Runtime)
	}
	if record.Runtime.LastError != "" || record.Runtime.LastAttemptRevision != 0 {
		t.Fatalf("retry state must reset: %+v", record.Runtime)
	}
	if record.Definition.Model.ModelID != "deepseek-flash" || record.Definition.Model.ReasoningEffort != "high" {
		t.Fatalf("selection changed: %+v", record.Definition.Model)
	}
}

// staleModelConfigStore returns an outdated model config snapshot so the CAS on
// the definition revision can be exercised: the handler reads the old
// revision while the stored record already moved on.
type staleModelConfigStore struct {
	*botModelDefinitionTestStore
}

func (s *staleModelConfigStore) GetBotModelConfig(botUID int64) (*types.BotModelConfig, error) {
	config, err := s.botModelDefinitionTestStore.GetBotModelConfig(botUID)
	if err != nil {
		return nil, err
	}
	config.Revision--
	return config, nil
}

func TestAdminReapplyRejectsConcurrentSelectionChange(t *testing.T) {
	db := &staleModelConfigStore{
		botModelDefinitionTestStore: &botModelDefinitionTestStore{
			botModelConfigTestStore: &botModelConfigTestStore{owners: map[int64]int64{43: 7}, models: map[int64]*types.BotModelConfig{}},
			records: map[int64]*types.BotDefinitionRecord{
				43: {
					Definition: types.BotDefinition{
						Model: types.BotDefinitionModel{Kind: botModelKindCatalog, ModelID: "deepseek-flash", ReasoningEffort: "high"},
					},
					Runtime: types.BotDefinitionRuntime{DesiredRevision: 7, AppliedRevision: 6},
					Exists:  true,
				},
			},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bots/model-config/reapply?uid=43", nil)
	rec := httptest.NewRecorder()
	handler.HandleAdminReapplyModelConfig(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	// The conflict must come from the revision CAS, not from an unsupported
	// selection, otherwise removing the CAS guard would go unnoticed.
	if !strings.Contains(rec.Body.String(), "retry the reapply") {
		t.Fatalf("conflict must be reported by the revision CAS: body=%s", rec.Body.String())
	}
	got := db.records[43]
	if got.Definition.Model.ModelID != "deepseek-flash" || got.Runtime.DesiredRevision != 7 {
		t.Fatalf("concurrent selection must be preserved: %+v", got)
	}
}

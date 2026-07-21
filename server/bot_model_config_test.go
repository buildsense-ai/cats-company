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

type botModelConfigTestStore struct {
	owners map[int64]int64
	models map[int64]*types.BotModelConfig
}

func (s *botModelConfigTestStore) GetBotOwner(botUID int64) (int64, error) {
	owner, ok := s.owners[botUID]
	if !ok {
		return 0, store.ErrStaleBotModelRevision
	}
	return owner, nil
}

func (s *botModelConfigTestStore) GetBotModelConfig(botUID int64) (*types.BotModelConfig, error) {
	if config := s.models[botUID]; config != nil {
		copy := *config
		return &copy, nil
	}
	return &types.BotModelConfig{}, nil
}

func (s *botModelConfigTestStore) SaveBotDesiredModelConfig(botUID int64, modelID, reasoningEffort string) (*types.BotModelConfig, error) {
	config, _ := s.GetBotModelConfig(botUID)
	config.ModelID = modelID
	config.ReasoningEffort = reasoningEffort
	config.Revision++
	config.LastAttemptRevision = 0
	config.LastError = ""
	s.models[botUID] = config
	return config, nil
}

func (s *botModelConfigTestStore) AckBotModelConfig(botUID, revision int64, modelID, reasoningEffort, applyError string) (*types.BotModelConfig, error) {
	config, _ := s.GetBotModelConfig(botUID)
	if config.Revision != revision {
		return nil, store.ErrStaleBotModelRevision
	}
	config.LastAttemptRevision = revision
	config.LastError = applyError
	if applyError == "" {
		config.AppliedRevision = revision
		config.AppliedModelID = modelID
		config.AppliedReasoning = reasoningEffort
	}
	s.models[botUID] = config
	return config, nil
}

func TestOwnerCanSelectBotModelAndFriendCannot(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	handler := NewBotModelConfigHandler(db, db)

	req := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"model_id":"deepseek-v4-flash","reasoning_effort":"max"}`,
	))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleOwnerConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := db.models[43]; got == nil || got.ModelID != "deepseek-v4-flash" || got.ReasoningEffort != "max" || got.Revision != 1 {
		t.Fatalf("saved config=%+v", got)
	}

	friendReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"model_id":"minimax-m2.7"}`,
	))
	friendReq = friendReq.WithContext(context.WithValue(friendReq.Context(), uidKey, int64(85)))
	friendRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(friendRec, friendReq)
	if friendRec.Code != http.StatusForbidden {
		t.Fatalf("friend status=%d body=%s, want 403", friendRec.Code, friendRec.Body.String())
	}
}

func TestGPT56CatalogUsesRelayReasoningEfforts(t *testing.T) {
	model, effort, ok := normalizeBotModelSelection("gpt-5.6-terra", "xhigh")
	if !ok || model.ID != "gpt-5.6-terra" || model.Provider != "openai" || model.Protocol != "OpenAI Responses" || effort != "xhigh" {
		t.Fatalf("selection model=%+v effort=%q ok=%v", model, effort, ok)
	}

	for _, modelID := range []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"} {
		_, defaultEffort, valid := normalizeBotModelSelection(modelID, "")
		if !valid || defaultEffort != "medium" {
			t.Fatalf("default selection for %s: effort=%q valid=%v", modelID, defaultEffort, valid)
		}
	}

	if _, _, valid := normalizeBotModelSelection("gpt-5.6-terra", "max"); valid {
		t.Fatal("GPT-5.6 must reject DeepSeek-only max effort")
	}
	if _, _, valid := normalizeBotModelSelection("deepseek-v4-flash", "xhigh"); valid {
		t.Fatal("DeepSeek must reject GPT-5.6-only xhigh effort")
	}
}

func TestDefaultModelIsDisplayOnlyUntilOwnerEnablesCloudManagement(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	handler := NewBotModelConfigHandler(db, db)

	getReq := httptest.NewRequest(http.MethodGet, "/api/bots/model-config?uid=43", nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), uidKey, int64(7)))
	getRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(getRec, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"configured":false`) {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	if db.models[43] != nil {
		t.Fatal("reading the displayed default must not enable cloud management")
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"model_id":"minimax-m3"}`,
	))
	patchReq = patchReq.WithContext(context.WithValue(patchReq.Context(), uidKey, int64(7)))
	patchRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(patchRec, patchReq)
	if patchRec.Code != http.StatusOK || !strings.Contains(patchRec.Body.String(), `"configured":true`) {
		t.Fatalf("patch status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	if db.models[43] == nil || db.models[43].Revision != 1 {
		t.Fatalf("saved config=%+v", db.models[43])
	}
}

func TestOwnerCanReturnBotToDeviceLocalModelConfiguration(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{
			43: {ModelID: "minimax-m3", Revision: 2},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"model_id":"local"}`,
	))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleOwnerConfig(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configured":false`) || !strings.Contains(rec.Body.String(), `"model_id":"local"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.models[43].ModelID != "" || db.models[43].Revision != 3 {
		t.Fatalf("saved config=%+v", db.models[43])
	}
}

func TestOwnerRetryCreatesNewRevisionUntilSelectionIsApplied(t *testing.T) {
	tests := []struct {
		name         string
		config       *types.BotModelConfig
		wantRevision int64
	}{
		{
			name: "pending selection",
			config: &types.BotModelConfig{
				ModelID: "deepseek-v4-flash", ReasoningEffort: "high", Revision: 3,
			},
			wantRevision: 4,
		},
		{
			name: "failed selection",
			config: &types.BotModelConfig{
				ModelID: "deepseek-v4-flash", ReasoningEffort: "high", Revision: 3,
				LastAttemptRevision: 3, LastError: "runtime reload failed",
			},
			wantRevision: 4,
		},
		{
			name: "applied selection",
			config: &types.BotModelConfig{
				ModelID: "deepseek-v4-flash", ReasoningEffort: "high", Revision: 3,
				AppliedRevision: 3, AppliedModelID: "deepseek-v4-flash", AppliedReasoning: "high",
			},
			wantRevision: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := &botModelConfigTestStore{
				owners: map[int64]int64{43: 7},
				models: map[int64]*types.BotModelConfig{43: test.config},
			}
			handler := NewBotModelConfigHandler(db, db)
			req := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
				`{"model_id":"deepseek-v4-flash","reasoning_effort":"high"}`,
			))
			req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
			rec := httptest.NewRecorder()
			handler.HandleOwnerConfig(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := db.models[43].Revision; got != test.wantRevision {
				t.Fatalf("revision=%d, want %d", got, test.wantRevision)
			}
		})
	}
}

func TestRuntimeReadsOwnConfigAndAcknowledgesCurrentRevision(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{
			43: {ModelID: "deepseek-v4-flash", ReasoningEffort: "high", Revision: 3},
		},
	}
	handler := NewBotModelConfigHandler(db, db)

	getReq := httptest.NewRequest(http.MethodGet, "/api/bot/model-config", nil)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), uidKey, int64(43)))
	getRec := httptest.NewRecorder()
	handler.HandleRuntimeConfig(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, exposed := body["models"]; exposed {
		t.Fatal("runtime response should not include the owner model catalog")
	}

	ackReq := httptest.NewRequest(http.MethodPost, "/api/bot/model-config/ack", strings.NewReader(
		`{"revision":3,"model_id":"deepseek-v4-flash","reasoning_effort":"high"}`,
	))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ackRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if db.models[43].AppliedRevision != 3 || db.models[43].AppliedModelID != "deepseek-v4-flash" {
		t.Fatalf("ack config=%+v", db.models[43])
	}
}

func TestRuntimeRejectsStaleModelAcknowledgement(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{
			43: {ModelID: "minimax-m3", Revision: 4},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPost, "/api/bot/model-config/ack", strings.NewReader(
		`{"revision":3,"model_id":"deepseek-v4-flash","reasoning_effort":"high"}`,
	))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(43)))
	rec := httptest.NewRecorder()
	handler.HandleRuntimeAck(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
}

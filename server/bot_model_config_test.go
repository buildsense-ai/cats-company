package server

import (
	"context"
	"encoding/base64"
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

func (s *botModelConfigTestStore) MarkBotModelRuntimeProtocol(botUID int64, protocol string) (*types.BotModelConfig, error) {
	config, _ := s.GetBotModelConfig(botUID)
	config.RuntimeProtocol = protocol
	config.RuntimeProtocolSeen = "2026-07-21T00:00:00Z"
	s.models[botUID] = config
	return config, nil
}

func (s *botModelConfigTestStore) SaveBotDesiredModelConfig(botUID int64, kind, modelID, reasoningEffort, customCiphertext string) (*types.BotModelConfig, error) {
	config, _ := s.GetBotModelConfig(botUID)
	config.Kind = kind
	config.ModelID = modelID
	config.ReasoningEffort = reasoningEffort
	if customCiphertext != "" {
		config.CustomCiphertext = customCiphertext
	}
	config.Revision++
	config.LastAttemptRevision = 0
	config.LastError = ""
	s.models[botUID] = config
	return config, nil
}

func (s *botModelConfigTestStore) AckBotModelConfig(botUID, revision int64, kind, modelID, reasoningEffort, applyError string) (*types.BotModelConfig, error) {
	config, _ := s.GetBotModelConfig(botUID)
	if config.Revision != revision {
		return nil, store.ErrStaleBotModelRevision
	}
	config.LastAttemptRevision = revision
	config.LastError = applyError
	if applyError == "" {
		config.AppliedKind = kind
		config.AppliedRevision = revision
		config.AppliedModelID = modelID
		config.AppliedReasoning = reasoningEffort
	}
	s.models[botUID] = config
	return config, nil
}

func enableBotModelEncryption(t *testing.T) {
	t.Helper()
	t.Setenv(botModelEncryptionKeyEnv, base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
}

func markTestBotModelRuntime(t *testing.T, db *botModelConfigTestStore, botUID int64) {
	t.Helper()
	if _, err := db.MarkBotModelRuntimeProtocol(botUID, botModelRuntimeProtocol); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCanSelectBotModelAndFriendCannot(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	markTestBotModelRuntime(t, db, 43)
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

func TestCloudModelRolloutAllowsOnlyConfiguredOwners(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7, 44: 8},
		models: map[int64]*types.BotModelConfig{
			43: {},
			44: {Kind: botModelKindCatalog, ModelID: "minimax-m3", Revision: 2},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	handler.SetRollout(false, map[int64]bool{7: true})

	allowedReq := httptest.NewRequest(http.MethodGet, "/api/bots/model-config?uid=43", nil)
	allowedReq = allowedReq.WithContext(context.WithValue(allowedReq.Context(), uidKey, int64(7)))
	allowedRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusOK || !strings.Contains(allowedRec.Body.String(), `"management_enabled":true`) {
		t.Fatalf("allowed status=%d body=%s", allowedRec.Code, allowedRec.Body.String())
	}

	blockedReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=44", strings.NewReader(
		`{"kind":"catalog","model_id":"gpt-5.6-terra","reasoning_effort":"medium"}`,
	))
	blockedReq = blockedReq.WithContext(context.WithValue(blockedReq.Context(), uidKey, int64(8)))
	blockedRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusForbidden {
		t.Fatalf("blocked status=%d body=%s", blockedRec.Code, blockedRec.Body.String())
	}
	if db.models[44].ModelID != "minimax-m3" || db.models[44].Revision != 2 {
		t.Fatalf("blocked owner changed model config: %+v", db.models[44])
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/api/bot/model-config", nil)
	runtimeReq = runtimeReq.WithContext(context.WithValue(runtimeReq.Context(), uidKey, int64(44)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeConfig(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK ||
		!strings.Contains(runtimeRec.Body.String(), `"management_enabled":false`) ||
		!strings.Contains(runtimeRec.Body.String(), `"configured":false`) {
		t.Fatalf("runtime status=%d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}
}

func TestOldRuntimeCannotSwitchUntilItRegistersCloudModelProtocol(t *testing.T) {
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	handler := NewBotModelConfigHandler(db, db)

	ownerGet := httptest.NewRequest(http.MethodGet, "/api/bots/model-config?uid=43", nil)
	ownerGet = ownerGet.WithContext(context.WithValue(ownerGet.Context(), uidKey, int64(7)))
	ownerGetRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(ownerGetRec, ownerGet)
	if ownerGetRec.Code != http.StatusOK ||
		!strings.Contains(ownerGetRec.Body.String(), `"runtime_supported":false`) ||
		!strings.Contains(ownerGetRec.Body.String(), botModelRuntimeUnavailableReason) {
		t.Fatalf("owner get status=%d body=%s", ownerGetRec.Code, ownerGetRec.Body.String())
	}

	blockedPatch := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"kind":"catalog","model_id":"gpt-5.6-terra","reasoning_effort":"medium"}`,
	))
	blockedPatch = blockedPatch.WithContext(context.WithValue(blockedPatch.Context(), uidKey, int64(7)))
	blockedRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(blockedRec, blockedPatch)
	if blockedRec.Code != http.StatusConflict || db.models[43] != nil {
		t.Fatalf("old runtime patch status=%d body=%s config=%+v", blockedRec.Code, blockedRec.Body.String(), db.models[43])
	}

	runtimeGet := httptest.NewRequest(http.MethodGet, "/api/bot/model-config", nil)
	runtimeGet = runtimeGet.WithContext(context.WithValue(runtimeGet.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeConfig(runtimeRec, runtimeGet)
	if runtimeRec.Code != http.StatusOK || !botModelRuntimeSupported(db.models[43]) || db.models[43].Revision != 0 {
		t.Fatalf("runtime registration status=%d body=%s config=%+v", runtimeRec.Code, runtimeRec.Body.String(), db.models[43])
	}

	allowedPatch := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"kind":"catalog","model_id":"gpt-5.6-terra","reasoning_effort":"medium"}`,
	))
	allowedPatch = allowedPatch.WithContext(context.WithValue(allowedPatch.Context(), uidKey, int64(7)))
	allowedRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(allowedRec, allowedPatch)
	if allowedRec.Code != http.StatusOK || db.models[43].ModelID != "gpt-5.6-terra" || db.models[43].Revision != 1 {
		t.Fatalf("new runtime patch status=%d body=%s config=%+v", allowedRec.Code, allowedRec.Body.String(), db.models[43])
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
	markTestBotModelRuntime(t, db, 43)

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
			43: {ModelID: "minimax-m3", RuntimeProtocol: botModelRuntimeProtocol, Revision: 2},
		},
	}
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"model_id":"local"}`,
	))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleOwnerConfig(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configured":false`) ||
		!strings.Contains(rec.Body.String(), `"model_id":"local"`) || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.models[43].ModelID != "" || db.models[43].Revision != 3 {
		t.Fatalf("saved config=%+v", db.models[43])
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/api/bot/model-config", nil)
	runtimeReq = runtimeReq.WithContext(context.WithValue(runtimeReq.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeConfig(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK || !strings.Contains(runtimeRec.Body.String(), `"configured":false`) ||
		!strings.Contains(runtimeRec.Body.String(), `"revision":3`) || !strings.Contains(runtimeRec.Body.String(), `"status":"pending"`) {
		t.Fatalf("runtime status=%d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}

	ackReq := httptest.NewRequest(http.MethodPost, "/api/bot/model-config/ack", strings.NewReader(
		`{"revision":3,"kind":"local","model_id":"local"}`,
	))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ackRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK || !strings.Contains(ackRec.Body.String(), `"status":"local"`) {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if db.models[43].AppliedRevision != 3 || db.models[43].AppliedKind != "local" || db.models[43].AppliedModelID != "local" {
		t.Fatalf("acked config=%+v", db.models[43])
	}

	repeatReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(`{"model_id":"local"}`))
	repeatReq = repeatReq.WithContext(context.WithValue(repeatReq.Context(), uidKey, int64(7)))
	repeatRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(repeatRec, repeatReq)
	if repeatRec.Code != http.StatusOK || db.models[43].Revision != 3 {
		t.Fatalf("repeat status=%d body=%s config=%+v", repeatRec.Code, repeatRec.Body.String(), db.models[43])
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
				ModelID: "deepseek-v4-flash", ReasoningEffort: "high", RuntimeProtocol: botModelRuntimeProtocol, Revision: 3,
			},
			wantRevision: 4,
		},
		{
			name: "failed selection",
			config: &types.BotModelConfig{
				ModelID: "deepseek-v4-flash", ReasoningEffort: "high", RuntimeProtocol: botModelRuntimeProtocol, Revision: 3,
				LastAttemptRevision: 3, LastError: "runtime reload failed",
			},
			wantRevision: 4,
		},
		{
			name: "applied selection",
			config: &types.BotModelConfig{
				ModelID: "deepseek-v4-flash", ReasoningEffort: "high", RuntimeProtocol: botModelRuntimeProtocol, Revision: 3,
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
	markTestBotModelRuntime(t, db, 43)
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
	markTestBotModelRuntime(t, db, 43)
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

func TestCustomModelSecretIsEncryptedAndOnlyReturnedToBotRuntime(t *testing.T) {
	enableBotModelEncryption(t)
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	markTestBotModelRuntime(t, db, 43)
	handler := NewBotModelConfigHandler(db, db)

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(`{
		"kind":"custom",
		"custom":{
			"protocol":"openai-responses",
			"api_base":"https://models.example.com/v1/",
			"model":"gpt-example",
			"api_key":"sk-super-secret",
			"context_window_tokens":256000,
			"max_tokens":8192,
			"reasoning_effort":"high"
		}
	}`))
	patchReq = patchReq.WithContext(context.WithValue(patchReq.Context(), uidKey, int64(7)))
	patchRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	stored := db.models[43]
	if stored == nil || stored.Kind != botModelKindCustom || stored.ModelID != "gpt-example" || stored.CustomCiphertext == "" {
		t.Fatalf("saved config=%+v", stored)
	}
	if strings.Contains(stored.CustomCiphertext, "sk-super-secret") {
		t.Fatal("plaintext API key was persisted")
	}
	if strings.Contains(patchRec.Body.String(), "sk-super-secret") {
		t.Fatal("owner response exposed plaintext API key")
	}
	if !strings.Contains(patchRec.Body.String(), `"api_key_configured":true`) || !strings.Contains(patchRec.Body.String(), `"api_key_hint":"****cret"`) {
		t.Fatalf("owner response does not contain a safe key hint: %s", patchRec.Body.String())
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/api/bot/model-config", nil)
	runtimeReq = runtimeReq.WithContext(context.WithValue(runtimeReq.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeConfig(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK || !strings.Contains(runtimeRec.Body.String(), "sk-super-secret") {
		t.Fatalf("runtime status=%d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}
}

func TestCustomModelUpdateCanKeepExistingAPIKey(t *testing.T) {
	enableBotModelEncryption(t)
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	markTestBotModelRuntime(t, db, 43)
	handler := NewBotModelConfigHandler(db, db)

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
		rec := httptest.NewRecorder()
		handler.HandleOwnerConfig(rec, req)
		return rec
	}
	first := patch(`{"kind":"custom","custom":{"protocol":"anthropic","api_base":"https://models.example.com","model":"model-a","api_key":"secret-a","context_window_tokens":128000}}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first patch status=%d body=%s", first.Code, first.Body.String())
	}
	second := patch(`{"kind":"custom","custom":{"protocol":"anthropic","api_base":"https://models.example.com","model":"model-b","context_window_tokens":128000}}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second patch status=%d body=%s", second.Code, second.Body.String())
	}

	runtimeReq := httptest.NewRequest(http.MethodGet, "/api/bot/model-config", nil)
	runtimeReq = runtimeReq.WithContext(context.WithValue(runtimeReq.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeConfig(runtimeRec, runtimeReq)
	if runtimeRec.Code != http.StatusOK || !strings.Contains(runtimeRec.Body.String(), `"model":"model-b"`) || !strings.Contains(runtimeRec.Body.String(), `"api_key":"secret-a"`) {
		t.Fatalf("runtime status=%d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}
}

func TestCustomModelRequiresServerEncryptionButCatalogDoesNot(t *testing.T) {
	t.Setenv(botModelEncryptionKeyEnv, "")
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	markTestBotModelRuntime(t, db, 43)
	handler := NewBotModelConfigHandler(db, db)

	customReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"kind":"custom","custom":{"protocol":"anthropic","api_base":"https://models.example.com","model":"model-a","api_key":"secret-a","context_window_tokens":128000}}`,
	))
	customReq = customReq.WithContext(context.WithValue(customReq.Context(), uidKey, int64(7)))
	customRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(customRec, customReq)
	if customRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("custom status=%d body=%s, want 503", customRec.Code, customRec.Body.String())
	}

	catalogReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(`{"kind":"catalog","model_id":"minimax-m3"}`))
	catalogReq = catalogReq.WithContext(context.WithValue(catalogReq.Context(), uidKey, int64(7)))
	catalogRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(catalogRec, catalogReq)
	if catalogRec.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogRec.Code, catalogRec.Body.String())
	}
}

func TestUnreadableSavedCustomSecretDoesNotBlockOfficialModelManagement(t *testing.T) {
	enableBotModelEncryption(t)
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{
			43: {
				Kind: botModelKindCatalog, ModelID: "minimax-m3", Revision: 3,
				CustomCiphertext: "v1:corrupted",
			},
		},
	}
	markTestBotModelRuntime(t, db, 43)
	handler := NewBotModelConfigHandler(db, db)
	req := httptest.NewRequest(http.MethodGet, "/api/bots/model-config?uid=43", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleOwnerConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model_id":"minimax-m3"`) || !strings.Contains(rec.Body.String(), `"custom_unavailable_reason"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "corrupted") {
		t.Fatal("owner response exposed encrypted storage material")
	}
}

func TestCustomModelAcknowledgementTracksKind(t *testing.T) {
	enableBotModelEncryption(t)
	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	markTestBotModelRuntime(t, db, 43)
	handler := NewBotModelConfigHandler(db, db)

	ownerReq := httptest.NewRequest(http.MethodPatch, "/api/bots/model-config?uid=43", strings.NewReader(
		`{"kind":"custom","custom":{"protocol":"openai-chat-completions","api_base":"https://models.example.com/v1","model":"model-a","api_key":"secret-a","context_window_tokens":128000,"reasoning_effort":"medium"}}`,
	))
	ownerReq = ownerReq.WithContext(context.WithValue(ownerReq.Context(), uidKey, int64(7)))
	ownerRec := httptest.NewRecorder()
	handler.HandleOwnerConfig(ownerRec, ownerReq)
	if ownerRec.Code != http.StatusOK {
		t.Fatalf("owner status=%d body=%s", ownerRec.Code, ownerRec.Body.String())
	}

	failureReq := httptest.NewRequest(http.MethodPost, "/api/bot/model-config/ack", strings.NewReader(
		`{"revision":1,"kind":"custom","model_id":"model-a","reasoning_effort":"medium","error":"provider rejected secret-a"}`,
	))
	failureReq = failureReq.WithContext(context.WithValue(failureReq.Context(), uidKey, int64(43)))
	failureRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(failureRec, failureReq)
	if failureRec.Code != http.StatusOK || db.models[43].LastError != "provider rejected [REDACTED]" {
		t.Fatalf("failure status=%d config=%+v", failureRec.Code, db.models[43])
	}

	ackReq := httptest.NewRequest(http.MethodPost, "/api/bot/model-config/ack", strings.NewReader(
		`{"revision":1,"kind":"custom","model_id":"model-a","reasoning_effort":"medium"}`,
	))
	ackReq = ackReq.WithContext(context.WithValue(ackReq.Context(), uidKey, int64(43)))
	ackRec := httptest.NewRecorder()
	handler.HandleRuntimeAck(ackRec, ackReq)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if db.models[43].AppliedKind != botModelKindCustom || db.models[43].AppliedModelID != "model-a" {
		t.Fatalf("ack config=%+v", db.models[43])
	}
}

func TestOwnerModelCatalogIncludesPerModelQuotaFromSingleRelayRequest(t *testing.T) {
	requestCount := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/internal/usage/users" || r.URL.Query().Get("search") != "7" {
			t.Fatalf("unexpected relay request: %s", r.URL.String())
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"users": []map[string]interface{}{{
				"uid": 7, "configured": true,
				"limits": map[string]interface{}{
					"model_limits": []map[string]interface{}{
						{
							"provider": "openai", "model": "gpt-5.6-terra",
							"budget": map[string]interface{}{"max_limit": 100.0, "current_usage": 25.0},
						},
						{
							"provider": "anthropic", "model": "deepseek-v4-flash",
							"budget": map[string]interface{}{"max_limit": 50.0, "current_usage": 45.0},
						},
					},
				},
			}},
		})
	}))
	defer relay.Close()

	db := &botModelConfigTestStore{
		owners: map[int64]int64{43: 7},
		models: map[int64]*types.BotModelConfig{},
	}
	handler := NewBotModelConfigHandler(db, db)
	handler.SetRelayUsageClient(&RelayAdminClient{baseURL: relay.URL, token: "test-token", client: relay.Client()})
	req := httptest.NewRequest(http.MethodGet, "/api/bots/model-config?uid=43&include_usage=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	handler.HandleOwnerConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if requestCount != 1 {
		t.Fatalf("relay request count=%d, want 1", requestCount)
	}
	if !strings.Contains(rec.Body.String(), `"model":"gpt-5.6-terra"`) || !strings.Contains(rec.Body.String(), `"remaining_cny":75`) {
		t.Fatalf("Terra quota missing from response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model":"deepseek-v4-flash"`) || !strings.Contains(rec.Body.String(), `"status":"high"`) {
		t.Fatalf("DeepSeek quota missing from response: %s", rec.Body.String())
	}
}

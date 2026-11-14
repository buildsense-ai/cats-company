package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// recordedBotConfig 是 createBotAccount 写 bot_config 一次调用的记录。
type recordedBotConfig struct {
	uid         int64
	owner       int64
	apiEndpoint string
	model       string
}

// recordBotStore 只实现 createBotAccount 需要的最小存储面，其余走 nil Store。
type recordBotStore struct {
	store.Store
	nextUID      int64
	createdUsers []*types.User
	botConfigs   []recordedBotConfig
	apiKeySaves  int
}

func (s *recordBotStore) GetUserByUsername(string) (*types.User, error) { return nil, nil }

func (s *recordBotStore) CreateUser(user *types.User) (int64, error) {
	s.nextUID++
	user.ID = s.nextUID
	s.createdUsers = append(s.createdUsers, user)
	return user.ID, nil
}

func (s *recordBotStore) SaveBotConfigWithOwner(uid, ownerID int64, apiEndpoint, model string) error {
	s.botConfigs = append(s.botConfigs, recordedBotConfig{uid: uid, owner: ownerID, apiEndpoint: apiEndpoint, model: model})
	return nil
}

func (s *recordBotStore) SaveAPIKey(int64, string) error {
	s.apiKeySaves++
	return nil
}

// creditBotStore 增加 credit 资格读取能力（ModelCreditEligibilityReader）。
type creditBotStore struct {
	*recordBotStore
	enabled   bool
	eligErr   error
	eligCalls int
}

func (s *creditBotStore) GetModelCreditEligibility(_ context.Context, _ int64) (bool, int, int64, error) {
	s.eligCalls++
	if s.eligErr != nil {
		return false, 0, 0, s.eligErr
	}
	return s.enabled, 3, 100, nil
}

// creditModelConfigBotStore 增加 BotModelConfigStore 能力（legacy 适配面）。
type creditModelConfigBotStore struct {
	*creditBotStore
	desired    []types.BotModelConfig
	desiredErr error
}

func (s *creditModelConfigBotStore) GetBotModelConfig(int64) (*types.BotModelConfig, error) {
	return &types.BotModelConfig{}, nil
}

func (s *creditModelConfigBotStore) MarkBotModelRuntimeProtocol(int64, string) (*types.BotModelConfig, error) {
	return &types.BotModelConfig{}, nil
}

func (s *creditModelConfigBotStore) SaveBotDesiredModelConfig(_ int64, kind, modelID, reasoningEffort, ciphertext string) (*types.BotModelConfig, error) {
	if s.desiredErr != nil {
		return nil, s.desiredErr
	}
	config := types.BotModelConfig{Kind: kind, ModelID: modelID, ReasoningEffort: reasoningEffort, CustomCiphertext: ciphertext}
	s.desired = append(s.desired, config)
	return &config, nil
}

func (s *creditModelConfigBotStore) AckBotModelConfig(int64, int64, string, string, string, string) (*types.BotModelConfig, error) {
	return &types.BotModelConfig{}, nil
}

// creditDefinitionBotStore 增加 BotDefinitionStore 能力（正式 definition）。
type creditDefinitionBotStore struct {
	*creditBotStore
	definitionUpdates []types.BotDefinitionModel
	definitionErr     error
}

func (s *creditDefinitionBotStore) GetBotDefinition(int64) (*types.BotDefinitionRecord, error) {
	return nil, nil
}

func (s *creditDefinitionBotStore) CreateBotDefinitionIfAbsent(int64, types.BotDefinition) (*types.BotDefinitionRecord, error) {
	return nil, nil
}

func (s *creditDefinitionBotStore) UpdateBotDefinitionModel(_ int64, _ int64, model types.BotDefinitionModel) (*types.BotDefinitionRecord, error) {
	if s.definitionErr != nil {
		return nil, s.definitionErr
	}
	s.definitionUpdates = append(s.definitionUpdates, model)
	return &types.BotDefinitionRecord{
		Definition: types.BotDefinition{Model: model},
		Exists:     true,
	}, nil
}

func (s *creditDefinitionBotStore) UpdateBotDefinitionPrompt(int64, int64, types.BotPromptDefinition) (*types.BotDefinitionRecord, error) {
	return nil, nil
}

func (s *creditDefinitionBotStore) UpdateBotDefinitionSkills(int64, int64, []types.BotSkillRef) (*types.BotDefinitionRecord, error) {
	return nil, nil
}

func (s *creditDefinitionBotStore) UpdateBotDefinitionServiceAgents(int64, int64, []string) (*types.BotDefinitionRecord, error) {
	return nil, nil
}

func (s *creditDefinitionBotStore) UpdateBotPromptVisibility(int64, types.BotPromptVisibility) (*types.BotDefinitionRecord, error) {
	return nil, nil
}

func (s *creditDefinitionBotStore) ReportBotDefaultPrompt(int64, types.BotDefaultPromptSnapshot) (*types.BotDefinitionRecord, bool, error) {
	return nil, false, nil
}

func (s *creditDefinitionBotStore) AckBotDefinition(int64, int64, string) (*types.BotDefinitionRecord, error) {
	return nil, nil
}

func newCreditBotHandler(db store.Store) *BotHandler {
	return NewBotHandler(db)
}

func TestCreateBotAccountDefaultsCreditOwnerBotToDeepSeekFlash(t *testing.T) {
	db := &creditModelConfigBotStore{creditBotStore: &creditBotStore{recordBotStore: &recordBotStore{}, enabled: true}}
	h := newCreditBotHandler(db)

	result, status, err := h.createBotAccount(42, BotRegisterRequest{Username: "credit-worker"})
	if err != nil {
		t.Fatalf("createBotAccount err=%v status=%d", err, status)
	}
	if result == nil || result.UID == 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(db.botConfigs) != 1 {
		t.Fatalf("bot configs=%d", len(db.botConfigs))
	}
	if len(db.desired) != 1 {
		t.Fatalf("desired model writes=%d want 1", len(db.desired))
	}
	got := db.desired[0]
	if got.Kind != botModelKindCatalog || got.ModelID != "deepseek-flash" || got.ReasoningEffort != "high" {
		t.Fatalf("desired model=%+v want catalog/deepseek-flash/high", got)
	}
	if db.eligCalls != 1 {
		t.Fatalf("eligibility reads=%d want 1", db.eligCalls)
	}
	if db.botConfigs[0].owner != 42 {
		t.Fatalf("owner=%d", db.botConfigs[0].owner)
	}
}

func TestCreateBotAccountInitializesCreditOwnerBotDefinition(t *testing.T) {
	db := &creditDefinitionBotStore{creditBotStore: &creditBotStore{recordBotStore: &recordBotStore{}, enabled: true}}
	h := newCreditBotHandler(db)

	if _, status, err := h.createBotAccount(42, BotRegisterRequest{Username: "credit-worker"}); err != nil {
		t.Fatalf("createBotAccount err=%v status=%d", err, status)
	}
	if len(db.definitionUpdates) != 1 {
		t.Fatalf("definition model updates=%d want 1", len(db.definitionUpdates))
	}
	got := db.definitionUpdates[0]
	if got.Kind != "catalog" || got.ModelID != "deepseek-flash" || got.ReasoningEffort != "high" {
		t.Fatalf("definition model=%+v want catalog/deepseek-flash/high", got)
	}
}

func TestCreateBotAccountKeepsLegacyBotModelWithoutCreditIdentity(t *testing.T) {
	db := &recordBotStore{}
	h := newCreditBotHandler(db)

	if _, status, err := h.createBotAccount(42, BotRegisterRequest{Username: "legacy-bot"}); err != nil {
		t.Fatalf("createBotAccount err=%v status=%d", err, status)
	}
	if len(db.botConfigs) != 1 || db.botConfigs[0].model != "" || db.botConfigs[0].apiEndpoint != "" {
		t.Fatalf("bot configs=%+v want one empty model/endpoint", db.botConfigs)
	}
}

func TestCreateBotAccountKeepsLegacyBotModelForNonCreditOwner(t *testing.T) {
	db := &creditModelConfigBotStore{creditBotStore: &creditBotStore{recordBotStore: &recordBotStore{}, enabled: false}}
	h := newCreditBotHandler(db)

	if _, status, err := h.createBotAccount(42, BotRegisterRequest{Username: "legacy-bot"}); err != nil {
		t.Fatalf("createBotAccount err=%v status=%d", err, status)
	}
	if db.eligCalls != 1 {
		t.Fatalf("eligibility reads=%d want 1", db.eligCalls)
	}
	if len(db.desired) != 0 {
		t.Fatalf("desired model writes=%d want 0", len(db.desired))
	}
	if len(db.botConfigs) != 1 || db.botConfigs[0].model != "" {
		t.Fatalf("bot configs=%+v want one empty model", db.botConfigs)
	}
}

func TestCreateBotAccountKeepsExplicitModelSelectionForCreditOwner(t *testing.T) {
	cases := []struct {
		name string
		req  BotRegisterRequest
	}{
		{name: "explicit model", req: BotRegisterRequest{Username: "custom-bot", Model: "gpt-6.1-sol", APIEndpoint: "https://bot.example"}},
		{name: "custom endpoint only", req: BotRegisterRequest{Username: "custom-bot", APIEndpoint: "https://bot.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &creditDefinitionBotStore{creditBotStore: &creditBotStore{recordBotStore: &recordBotStore{}, enabled: true}}
			h := newCreditBotHandler(db)

			if _, status, err := h.createBotAccount(42, tc.req); err != nil {
				t.Fatalf("createBotAccount err=%v status=%d", err, status)
			}
			if len(db.definitionUpdates) != 0 {
				t.Fatalf("definition model updates=%+v want none", db.definitionUpdates)
			}
			if db.eligCalls != 0 {
				t.Fatalf("eligibility reads=%d want 0", db.eligCalls)
			}
			if len(db.botConfigs) != 1 || db.botConfigs[0].model != tc.req.Model || db.botConfigs[0].apiEndpoint != tc.req.APIEndpoint {
				t.Fatalf("bot configs=%+v want the request values untouched", db.botConfigs)
			}
		})
	}
}

func TestCreateBotAccountFailsClosedWhenCreditIdentityUnreadable(t *testing.T) {
	db := &creditModelConfigBotStore{creditBotStore: &creditBotStore{
		recordBotStore: &recordBotStore{},
		enabled:        true,
		eligErr:        errors.New("eligibility down"),
	}}
	h := newCreditBotHandler(db)

	result, status, err := h.createBotAccount(42, BotRegisterRequest{Username: "credit-worker"})
	if err == nil {
		t.Fatalf("createBotAccount succeeded, want failure")
	}
	if status != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", status, http.StatusInternalServerError)
	}
	if result != nil {
		t.Fatalf("result=%+v want nil", result)
	}
	if len(db.createdUsers) != 0 || len(db.botConfigs) != 0 {
		t.Fatalf("created users=%d configs=%d want no resource created", len(db.createdUsers), len(db.botConfigs))
	}
}

func TestCreateBotAccountFailsWhenModelConfigurationUnsupported(t *testing.T) {
	db := &creditBotStore{recordBotStore: &recordBotStore{}, enabled: true}
	h := newCreditBotHandler(db)

	if _, status, err := h.createBotAccount(42, BotRegisterRequest{Username: "credit-worker"}); err == nil {
		t.Fatalf("createBotAccount succeeded, want failure")
	} else if status != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", status, http.StatusInternalServerError)
	}
	if len(db.createdUsers) != 0 || len(db.botConfigs) != 0 {
		t.Fatalf("created users=%d configs=%d want no resource created", len(db.createdUsers), len(db.botConfigs))
	}
}

func TestCreateBotAccountFailsWhenInitialModelCannotBeSaved(t *testing.T) {
	db := &creditModelConfigBotStore{
		creditBotStore: &creditBotStore{recordBotStore: &recordBotStore{}, enabled: true},
		desiredErr:     errors.New("write failed"),
	}
	h := newCreditBotHandler(db)

	if _, status, err := h.createBotAccount(42, BotRegisterRequest{Username: "credit-worker"}); err == nil {
		t.Fatalf("createBotAccount succeeded, want failure")
	} else if status != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", status, http.StatusInternalServerError)
	}
	if len(db.botConfigs) != 1 {
		t.Fatalf("bot configs=%d want the bot to exist but the call to fail", len(db.botConfigs))
	}
	if len(db.desired) != 0 {
		t.Fatalf("desired model writes=%d want 0", len(db.desired))
	}
}

// TestHandleCreateBotAppliesCreditDefaultModel 走 HTTP 入口，确认 owner 建 bot 的
// 公开路径也拿到同一个 credit 默认模型（自动 cloud-worker 开通共用 createBotAccount）。
func TestHandleCreateBotAppliesCreditDefaultModel(t *testing.T) {
	db := &creditDefinitionBotStore{creditBotStore: &creditBotStore{recordBotStore: &recordBotStore{}, enabled: true}}
	h := NewBotHandler(db)

	body := strings.NewReader(`{"username":"credit-worker","display_name":"Credit Worker"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/bots", body)
	req = req.WithContext(withTestUID(42))
	recorder := httptest.NewRecorder()

	h.HandleCreateBot(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(db.definitionUpdates) != 1 || db.definitionUpdates[0].ModelID != "deepseek-flash" {
		t.Fatalf("definition model updates=%+v want one deepseek-flash write", db.definitionUpdates)
	}
}

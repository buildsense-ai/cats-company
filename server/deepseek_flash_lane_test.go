package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func laneProviderForBot(t *testing.T, botUID int64, modelID string) string {
	t.Helper()
	descriptor := catalogRuntimeDescriptorForBot(botUID, modelID)
	if descriptor == nil {
		t.Fatalf("descriptor for %s is nil", modelID)
	}
	return descriptor.Provider
}

func TestDeepSeekFlashAnthropicLaneDefaultsToOpenAI(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	descriptor := catalogRuntimeDescriptorForBot(42, "deepseek-flash")
	if descriptor == nil || descriptor.Provider != "openai" || descriptor.OpenAIAPIMode != "responses" {
		t.Fatalf("default descriptor = %+v", descriptor)
	}
}

func TestDeepSeekFlashAnthropicLaneInvalidPercentDisables(t *testing.T) {
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	for _, value := range []string{"abc", "-5", "0", "  "} {
		t.Setenv(deepSeekAnthropicLanePercentEnv, value)
		if provider := laneProviderForBot(t, 11, "deepseek-flash"); provider != "openai" {
			t.Fatalf("percent %q provider = %s", value, provider)
		}
	}
}

func TestDeepSeekFlashAnthropicLaneCanaryList(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, " 777 , 888")
	descriptor := catalogRuntimeDescriptorForBot(777, "deepseek-flash")
	if descriptor == nil || descriptor.Provider != "anthropic" || descriptor.OpenAIAPIMode != "" {
		t.Fatalf("canary descriptor = %+v", descriptor)
	}
	if other := catalogRuntimeDescriptorForBot(778, "deepseek-flash"); other == nil || other.Provider != "openai" {
		t.Fatalf("non-canary descriptor = %+v", other)
	}
}

func TestDeepSeekFlashAnthropicLanePercentIsDeterministicAndBounded(t *testing.T) {
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	t.Setenv(deepSeekAnthropicLanePercentEnv, "50")
	anthropic, openai := 0, 0
	for uid := int64(1); uid <= 400; uid++ {
		first := laneProviderForBot(t, uid, "deepseek-flash")
		second := laneProviderForBot(t, uid, "deepseek-flash")
		if first != second {
			t.Fatalf("lane for bot %d changed between calls", uid)
		}
		if first == "anthropic" {
			anthropic++
		} else {
			openai++
		}
	}
	// The hash only needs a roughly even split; keep a wide acceptance band.
	if anthropic < 100 || anthropic > 300 {
		t.Fatalf("unexpected split anthropic=%d openai=%d", anthropic, openai)
	}
}

func TestDeepSeekFlashAnthropicLanePercentFullFlipsAll(t *testing.T) {
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	for uid := int64(1); uid <= 50; uid++ {
		if provider := laneProviderForBot(t, uid, "deepseek-flash"); provider != "anthropic" {
			t.Fatalf("bot %d provider = %s", uid, provider)
		}
	}
}

func TestDeepSeekFlashAnthropicLaneIgnoresOtherModels(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	if provider := laneProviderForBot(t, 5, "gpt-5.6-sol"); provider != "openai" {
		t.Fatalf("gpt descriptor provider = %s", provider)
	}
	if descriptor := catalogRuntimeDescriptorForBot(5, "gpt-5.6-sol"); descriptor == nil || descriptor.OpenAIAPIMode != "responses" {
		t.Fatalf("gpt descriptor lost its api mode: %+v", descriptor)
	}
	if provider := laneProviderForBot(t, 5, "minimax-m3"); provider != "anthropic" {
		t.Fatalf("minimax provider = %s", provider)
	}
}

func TestDeepSeekFlashAnthropicLaneAppliesToLegacyAlias(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	descriptor := catalogRuntimeDescriptorForBot(9, "deepseek-v4-flash")
	if descriptor == nil || descriptor.CatalogModelID != deepSeekPublicModelID || descriptor.Provider != "anthropic" {
		t.Fatalf("legacy alias descriptor = %+v", descriptor)
	}
}

func TestDeepSeekFlashAnthropicLaneDisabledForUnconfiguredBots(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	if provider := laneProviderForBot(t, 0, "deepseek-flash"); provider != "openai" {
		t.Fatalf("bot 0 provider = %s", provider)
	}
	if provider := laneProviderForBot(t, -3, "deepseek-flash"); provider != "openai" {
		t.Fatalf("negative bot provider = %s", provider)
	}
}

func TestDesiredModelConfigRuntimeUsesBotLane(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	desired := desiredModelConfigResponse(321, botModelKindCatalog, "deepseek-flash", "high", &types.BotModelConfig{})
	runtime, ok := desired["runtime"].(*botModelRuntimeDescriptor)
	if !ok || runtime.Provider != "anthropic" || runtime.Model != "deepseek-flash" {
		t.Fatalf("desired runtime = %#v", desired["runtime"])
	}
	if desired["model_id"] != "deepseek-flash" {
		t.Fatalf("desired model_id = %#v", desired["model_id"])
	}
}

func TestDeepSeekFlashAnthropicLaneClampsOverflowPercent(t *testing.T) {
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "")
	for _, value := range []string{"101", "150"} {
		t.Setenv(deepSeekAnthropicLanePercentEnv, value)
		if provider := laneProviderForBot(t, 3, "deepseek-flash"); provider != "anthropic" {
			t.Fatalf("percent %q provider = %s", value, provider)
		}
	}
}

func TestDeepSeekFlashAnthropicLaneRampIsMonotonic(t *testing.T) {
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "")
	lanes := func(percent string) map[int64]bool {
		t.Setenv(deepSeekAnthropicLanePercentEnv, percent)
		result := map[int64]bool{}
		for uid := int64(1); uid <= 200; uid++ {
			result[uid] = laneProviderForBot(t, uid, "deepseek-flash") == "anthropic"
		}
		return result
	}
	quarter, half, full := lanes("25"), lanes("50"), lanes("100")
	for uid := int64(1); uid <= 200; uid++ {
		if quarter[uid] && !half[uid] {
			t.Fatalf("bot %d fell back off the anthropic lane when the share was raised", uid)
		}
		if half[uid] && !full[uid] {
			t.Fatalf("bot %d fell back off the anthropic lane at 100%%", uid)
		}
	}
}

func TestDeepSeekFlashAnthropicLaneExclusionWins(t *testing.T) {
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "777")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "777, 778")
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	if provider := laneProviderForBot(t, 777, "deepseek-flash"); provider != "openai" {
		t.Fatalf("excluded canary provider = %s", provider)
	}
	if provider := laneProviderForBot(t, 778, "deepseek-flash"); provider != "openai" {
		t.Fatalf("excluded bot provider = %s", provider)
	}
	if provider := laneProviderForBot(t, 779, "deepseek-flash"); provider != "anthropic" {
		t.Fatalf("non-excluded bot provider = %s", provider)
	}
}

func TestDeepSeekFlashAnthropicLaneMalformedCanaryListIgnoresBadEntries(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "abc, 777, -4, ")
	if provider := laneProviderForBot(t, 777, "deepseek-flash"); provider != "anthropic" {
		t.Fatalf("canary with malformed neighbours provider = %s", provider)
	}
	if provider := laneProviderForBot(t, 778, "deepseek-flash"); provider != "openai" {
		t.Fatalf("non-canary provider = %s", provider)
	}
}

func TestDeepSeekFlashAnthropicLaneRuntimeJSONOmitsOpenAIMode(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "")
	anthropicRuntime, err := json.Marshal(catalogRuntimeDescriptorForBot(5, "deepseek-flash"))
	if err != nil {
		t.Fatalf("marshal anthropic runtime: %v", err)
	}
	if strings.Contains(string(anthropicRuntime), "openaiApiMode") {
		t.Fatalf("anthropic runtime must omit openaiApiMode: %s", anthropicRuntime)
	}
	if !strings.Contains(string(anthropicRuntime), `"provider":"anthropic"`) {
		t.Fatalf("anthropic runtime provider missing: %s", anthropicRuntime)
	}
	t.Setenv(deepSeekAnthropicLanePercentEnv, "")
	openaiRuntime, err := json.Marshal(catalogRuntimeDescriptorForBot(5, "deepseek-flash"))
	if err != nil {
		t.Fatalf("marshal openai runtime: %v", err)
	}
	if !strings.Contains(string(openaiRuntime), `"openaiApiMode":"responses"`) {
		t.Fatalf("default runtime must keep the responses mode: %s", openaiRuntime)
	}
}

func TestDeepSeekFlashAnthropicLaneCanaryOutranksPercent(t *testing.T) {
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "")
	t.Setenv(deepSeekAnthropicLanePercentEnv, "1")
	openaiUID := int64(0)
	for uid := int64(1); uid <= 2000; uid++ {
		if laneProviderForBot(t, uid, "deepseek-flash") == "openai" {
			openaiUID = uid
			break
		}
	}
	if openaiUID == 0 {
		t.Fatal("no bot stayed on the default lane at 1%")
	}
	t.Setenv(deepSeekAnthropicLaneBotsEnv, strconv.FormatInt(openaiUID, 10))
	if provider := laneProviderForBot(t, openaiUID, "deepseek-flash"); provider != "anthropic" {
		t.Fatalf("canary bot %d stayed on %s despite the canary list", openaiUID, provider)
	}
}

func TestCatalogRuntimeListUsesBotLane(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "100")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "")
	handler := &BotModelConfigHandler{}
	catalog, quotaError := handler.catalogWithUsageForCurrent(context.Background(), 7, 42, false, "")
	if quotaError != "" {
		t.Fatalf("quota error = %s", quotaError)
	}
	for _, item := range catalog {
		if item.ID != "deepseek-flash" {
			continue
		}
		if item.Runtime == nil || item.Runtime.Provider != "anthropic" {
			t.Fatalf("catalog runtime for the bot lane = %+v", item.Runtime)
		}
		return
	}
	t.Fatal("deepseek-flash missing from the catalog")
}

func TestDeepSeekFlashDefinitionShipsAnthropicRuntimeForCanaryBot(t *testing.T) {
	t.Setenv(deepSeekAnthropicLanePercentEnv, "")
	t.Setenv(deepSeekAnthropicLaneBotsEnv, "43")
	t.Setenv(deepSeekAnthropicLaneExcludeEnv, "")
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "deepseek-flash"},
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
	body := getRec.Body.String()
	if !strings.Contains(body, `"catalogRuntime":{"catalogModelId":"deepseek-flash","model":"deepseek-flash","provider":"anthropic"`) {
		t.Fatalf("canary definition must ship the anthropic lane: body=%s", body)
	}
	if strings.Contains(body, "openaiApiMode") {
		t.Fatalf("anthropic lane must not ship openaiApiMode: body=%s", body)
	}
}

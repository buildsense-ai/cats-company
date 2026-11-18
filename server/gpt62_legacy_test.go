package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func gpt62TestSummary(slug string, now time.Time) *types.CommercialSummary {
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	return &types.CommercialSummary{
		TotalCNY: 11000, TotalsByModel: map[string]float64{types.GPT62Sol: 5500, "gpt-6.1-sol": 5500},
		Plans:        []*types.CommercialPlan{{ID: 2, Slug: slug, ModelBudgets: map[string]float64{types.GPT62Sol: 5500}}},
		Entitlements: []*types.CommercialEntitlement{{PlanID: 2, PlanSlug: slug, Source: "operator", SourceRef: "package", State: "active", StartsAt: start, ExpiresAt: &end}},
		Grants:       []*types.CommercialQuotaGrant{{PlanID: 2, Model: types.GPT62Sol, GrantType: "operator_plan", SourceRef: "package", AmountCNY: 5500, EffectiveAt: start, ExpiresAt: &end}},
	}
}

func TestGPT62LegacyQuotaRequiresOwnActivePackage(t *testing.T) {
	now := time.Now()
	for _, slug := range []string{"catsco-personal", "catsco-pro", "catsco-internal-all-models-50k", "catsco-free", "catsco-trial-custom", "catsco-pro-v2", "CATSCO-PRO"} {
		t.Run(slug, func(t *testing.T) {
			want := slug == "catsco-personal" || slug == "catsco-pro" || slug == "catsco-internal-all-models-50k"
			if got := gpt62LegacyQuota(gpt62TestSummary(slug, now), now) > 0; got != want {
				t.Fatalf("allowed=%v want=%v", got, want)
			}
		})
	}
	for name, change := range map[string]func(*types.CommercialSummary){
		"wildcard":            func(s *types.CommercialSummary) { s.Grants[0].Model = "*" },
		"manual":              func(s *types.CommercialSummary) { s.Grants[0].GrantType = "manual" },
		"trial":               func(s *types.CommercialSummary) { s.Grants[0].GrantType = "trial"; s.Entitlements[0].Source = "trial" },
		"credit-only":         func(s *types.CommercialSummary) { s.Grants = nil },
		"unrelated source":    func(s *types.CommercialSummary) { s.Grants[0].SourceRef = "old-package" },
		"expired entitlement": func(s *types.CommercialSummary) { s.Entitlements[0].ExpiresAt = &now },
		"expired grant":       func(s *types.CommercialSummary) { s.Grants[0].ExpiresAt = &now },
		"revoked grant":       func(s *types.CommercialSummary) { s.Grants[0].RevokedAt = &now },
		"inactive plan":       func(s *types.CommercialSummary) { s.Plans[0].State = 1 },
		"removed from plan":   func(s *types.CommercialSummary) { s.Plans[0].ModelBudgets = nil },
	} {
		t.Run(name, func(t *testing.T) {
			summary := gpt62TestSummary("catsco-pro", now)
			change(summary)
			if amount := gpt62LegacyQuota(summary, now); amount != 0 {
				t.Fatalf("unexpected quota %v", amount)
			}
		})
	}
}

func TestGPT62SelectorRuntimeAndRelayPeerFailClosed(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	own := map[int64]int64{43: 7}
	models := &botModelConfigTestStore{owners: own, models: map[int64]*types.BotModelConfig{
		43: {Kind: botModelKindCatalog, ModelID: types.GPT62Sol, Revision: 3, RuntimeProtocol: botModelRuntimeProtocol},
	}}
	defs := &botDefinitionTestStore{owners: own, records: map[int64]*types.BotDefinitionRecord{
		43: {Definition: types.BotDefinition{Schema: types.BotDefinitionSchema, BotID: "43", Model: types.BotDefinitionModel{Kind: "catalog", ModelID: types.GPT62Sol}}, Runtime: types.BotDefinitionRuntime{DesiredRevision: 3}, Exists: true},
	}}
	quota := &creditAwareQuotaStore{enabled: true, summary: gpt62TestSummary("catsco-pro", time.Now())}
	bot := NewBotModelConfigHandler(models, models)
	bot.SetCommercialQuotaSource(quota, false, nil)
	def := NewBotDefinitionHandler(defs, defs, models, bot)
	desktop := NewRelayConfigHandler()
	desktop.SetBotModelConfig(bot)
	peer := NewModelCreditEligibilityHandler(quota)
	send := func(method, path, body string, uid int64, handler http.HandlerFunc) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, uid))
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}
	check := func(t *testing.T, catalog *CommercialModelCatalog, allowed bool) {
		t.Helper()
		bot.SetCommercialModelCatalog(catalog)
		peer.SetCommercialModelCatalog(catalog)
		available := func(models []botModelCatalogItem) bool {
			for _, item := range models {
				if types.IsGPT62Sol(item.ID) {
					if !item.Available || item.Runtime == nil || item.Runtime.OpenAIAPIMode != "responses" {
						t.Fatalf("bad runtime descriptor: %+v", item)
					}
					return true
				}
			}
			return false
		}
		items, _ := bot.catalogWithUsageForCurrent(context.Background(), 7, 43, false, types.GPT62Sol)
		if available(items) != allowed {
			t.Fatalf("selector denied/allowed incorrectly: %+v", items)
		}
		rec := send(http.MethodGet, "/api/relay/config", "", 7, desktop.HandleConfig)
		var conf relayConfigResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &conf); err != nil || available(conf.Models) != allowed {
			t.Fatalf("desktop catalog: %s (%v)", rec.Body, err)
		}
		rec = httptest.NewRecorder()
		peer.ServeHTTP(rec, eligibilityRequest(http.MethodPost, `{"uid":7}`, "Bearer "+secret))
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"gpt-6.2-sol"`) != allowed || !strings.Contains(rec.Body.String(), `"allowed":false`) {
			t.Fatalf("peer response: %d %s", rec.Code, rec.Body)
		}
		for path, handler := range map[string]http.HandlerFunc{"/api/bot/model-config": bot.HandleRuntimeConfig, "/api/bot/definition": def.HandleRuntimeDefinition} {
			rec := send(http.MethodGet, path, "", 43, handler)
			want := http.StatusForbidden
			if allowed {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("%s: status=%d want=%d body=%s", path, rec.Code, want, rec.Body)
			}
		}
		save := send(http.MethodPatch, "/api/bots/model-config?uid=43", `{"kind":"catalog","model_id":"gpt-6.2-sol","reasoning_effort":"high"}`, 7, bot.HandleOwnerConfig)
		want := http.StatusForbidden
		if allowed {
			want = http.StatusOK
		}
		if save.Code != want {
			t.Fatalf("save status=%d want=%d body=%s", save.Code, want, save.Body)
		}
		if models.models[43].ModelID != types.GPT62Sol || defs.records[43].Definition.Model.ModelID != types.GPT62Sol {
			t.Fatal("saved selection was changed")
		}
		syncSummary := commercialRelaySummaryWithGPT62(context.Background(), catalog, quota.summary)
		if (syncSummary.TotalsByModel[types.GPT62Sol] > 0) != allowed || commercialRelaySharedLimit(syncSummary) != 11000 {
			t.Fatalf("sync input mismatch: %+v", syncSummary)
		}
	}
	sellable := []string{"gpt-6.1-sol"}
	private := []string{types.GPT62Sol}
	live, _ := catalogTestServerWithInternal(t, sellable, private, http.StatusOK)
	liveCatalog := newTestCatalog(t, live)
	t.Run("private", func(t *testing.T) { check(t, liveCatalog, true) })
	t.Run("absent", func(t *testing.T) {
		missing, _ := catalogTestServerWithInternal(t, sellable, nil, http.StatusOK)
		check(t, newTestCatalog(t, missing), false)
	})
	t.Run("mixed", func(t *testing.T) {
		mixed, _ := catalogTestServerWithInternal(t, []string{"gpt-6.1-sol", types.GPT62Sol}, private, http.StatusOK)
		check(t, newTestCatalog(t, mixed), false)
	})
	t.Run("snapshot twice", func(t *testing.T) {
		down, _ := catalogTestServerWithInternal(t, sellable, nil, http.StatusServiceUnavailable)
		fallback := newTestCatalog(t, down)
		fallback.snapshotPath = liveCatalog.snapshotPath
		check(t, fallback, false)
		check(t, fallback, false)
	})
	t.Run("expired", func(t *testing.T) {
		now := time.Now()
		quota.summary.Entitlements[0].ExpiresAt = &now
		check(t, liveCatalog, false)
	})
}

func TestGPT62RelayScopeUsesExplicitQuotaAndClearsAfterRemoval(t *testing.T) {
	now := time.Now()
	summary := gpt62TestSummary("catsco-personal", now)
	live, _ := catalogTestServerWithInternal(t, []string{"gpt-6.1-sol"}, []string{types.GPT62Sol}, http.StatusOK)
	catalog := newTestCatalog(t, live)
	user := &commercialRelayUsageUser{}
	user.Limits.ModelLimits = []commercialRelayModelLimit{{Model: types.GPT62Sol, Provider: "openai", AllowedModels: []string{types.GPT62Sol}}}
	allowed := commercialRelaySummaryWithGPT62(context.Background(), catalog, summary)
	updates, managed := commercialRelaySharedManagedPlan(7, allowed, user, nil)
	scopes := commercialRelayModelScopes(allowed, user, managed)
	if len(updates) != 1 || updates[0].MaxLimit != 11000 || len(scopes) != 1 || len(scopes[0].AllowedModels) != 1 {
		t.Fatalf("updates=%+v scopes=%+v", updates, scopes)
	}
	user.Limits.ModelScopes = scopes
	denied := commercialRelaySummaryWithGPT62(context.Background(), nil, summary)
	scopes = commercialRelayModelScopes(denied, user, managed)
	if len(scopes) != 1 || len(scopes[0].AllowedModels) != 0 || len(scopes[0].ManagedModels) != 1 {
		t.Fatalf("did not remove GPT-6.2 access: %+v", scopes)
	}
}

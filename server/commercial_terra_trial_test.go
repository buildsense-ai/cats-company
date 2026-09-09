package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestFreeTerraTrialPackageTransitions(t *testing.T) {
	now := time.Now().UTC()
	expired, future := now.Add(-time.Hour), now.Add(time.Hour)
	free := &types.CommercialEntitlement{PlanSlug: "catsco-free", Source: "free", State: "active", StartsAt: now.Add(-24 * time.Hour)}
	for _, test := range []struct {
		name  string
		other *types.CommercialEntitlement
		want  bool
	}{
		{"free", nil, true},
		{"paid", &types.CommercialEntitlement{PlanSlug: "catsco-personal", State: "active"}, false},
		{"max", &types.CommercialEntitlement{PlanSlug: "catsco-pro", State: "active"}, false},
		{"internal", &types.CommercialEntitlement{PlanSlug: "internal-custom", State: "active"}, false},
		{"legacy", &types.CommercialEntitlement{PlanSlug: "catsco-legacy-custom", Source: "legacy", State: "active"}, false},
		{"expired paid", &types.CommercialEntitlement{PlanSlug: "catsco-personal", State: "active", ExpiresAt: &expired}, true},
		{"future renewal", &types.CommercialEntitlement{PlanSlug: "catsco-pro", State: "active", StartsAt: future}, true},
		{"revoked paid", &types.CommercialEntitlement{PlanSlug: "catsco-pro", State: "revoked"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			summary := &types.CommercialSummary{Entitlements: []*types.CommercialEntitlement{free, test.other}}
			if got := commercialFreeTerraTrialEnabled(summary, now); got != test.want {
				t.Fatalf("enabled=%v, want %v", got, test.want)
			}
		})
	}
}

func TestTerraTrialSyncRestoresPaidAndInternalWithoutRefillingTrial(t *testing.T) {
	free := &types.CommercialEntitlement{PlanSlug: "catsco-free", Source: "free", State: "active"}
	store := &commercialRelaySyncTestStore{summary: &types.CommercialSummary{UID: 38}}
	state := commercialRelayUsageUser{Configured: true, Key: &commercialRelayKeySummary{State: "active"}, Limits: commercialRelayLimits{
		FreeTerraTrial: &commercialRelayTerraTrial{MaxLimit: 100, CurrentUsage: 100, ResetDuration: "never"},
		AvailableModelLimits: []commercialRelayModelLimit{
			{Provider: "gpt", Model: commercialTerraTrialModel, AllowedModels: []string{commercialTerraTrialModel}},
			{Provider: "minimax", Model: "MiniMax-M3", AllowedModels: []string{"MiniMax-M3"}},
		},
	}}
	writes := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writes++
			var body struct {
				Trial   bool                                  `json:"free_terra_trial"`
				Monthly float64                               `json:"monthly_budget"`
				Window  string                                `json:"usage_window_start"`
				Budgets []commercialRelayProviderBudgetUpdate `json:"provider_config_budgets"`
				Scopes  []commercialRelayModelScope           `json:"model_scopes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			state.Limits.FreeTerraTrial.Enabled = body.Trial
			state.Limits.MonthlyBudget = commercialRelayBudget{MaxLimit: body.Monthly, ResetDuration: "1M"}
			state.UsageWindowStart = body.Window
			if body.Scopes != nil {
				state.Limits.ModelScopes = body.Scopes
			}
			for _, update := range body.Budgets {
				for _, model := range update.AllowedModels {
					found := false
					for i := range state.Limits.ModelLimits {
						if state.Limits.ModelLimits[i].Model == model {
							state.Limits.ModelLimits[i].Budget = commercialRelayBudget{MaxLimit: update.MaxLimit, ResetDuration: update.ResetDuration}
							found = true
						}
					}
					if !found {
						state.Limits.ModelLimits = append(state.Limits.ModelLimits, commercialRelayModelLimit{
							Provider: update.Provider, Model: model, AllowedModels: update.AllowedModels,
							Budget: commercialRelayBudget{MaxLimit: update.MaxLimit, ResetDuration: update.ResetDuration}})
					}
				}
			}
		}
		_ = json.NewEncoder(w).Encode(state)
	}))
	defer relay.Close()
	syncer := NewCommercialRelaySyncer(store, &RelayAdminClient{baseURL: relay.URL, token: "fixture", client: relay.Client()}, CommercialRelaySyncerOptions{EnforceEnabled: true})
	for _, plan := range []string{"free", "paid", "free", "internal", "free"} {
		store.summary = &types.CommercialSummary{UID: 38, TotalCNY: 1700, TotalsByModel: map[string]float64{"MiniMax-M3": 1700},
			Entitlements: []*types.CommercialEntitlement{free}, Grants: []*types.CommercialQuotaGrant{{Model: "MiniMax-M3", AmountCNY: 1700, GrantType: "free"}}}
		if plan != "free" {
			store.summary.TotalCNY = 12200
			store.summary.TotalsByModel[commercialTerraTrialModel] = 10500
			store.summary.Entitlements = append(store.summary.Entitlements, &types.CommercialEntitlement{PlanSlug: plan, State: "active"})
			store.summary.Grants = append(store.summary.Grants, &types.CommercialQuotaGrant{Model: commercialTerraTrialModel, AmountCNY: 10500, GrantType: "operator_plan"})
		}
		if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
			t.Fatalf("%s: %v", plan, err)
		}
		if state.Limits.FreeTerraTrial.Enabled != (plan == "free") || state.Limits.FreeTerraTrial.CurrentUsage != 100 {
			t.Fatalf("%s changed trial spend/policy: %#v", plan, state.Limits.FreeTerraTrial)
		}
		if state.Limits.MonthlyBudget.MaxLimit != store.summary.TotalCNY {
			t.Fatalf("%s shared quota changed", plan)
		}
		before := writes
		if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
			t.Fatal(err)
		}
		if writes != before {
			t.Fatalf("%s reconciliation is not idempotent", plan)
		}
	}
}

func TestFreeTerraTrialDoesNotIncreaseOrMutateRecurringPool(t *testing.T) {
	summary := &types.CommercialSummary{TotalCNY: 1700, TotalsByModel: map[string]float64{"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-v4-flash": 100, "glm-5.3-flash": 100}}
	view := commercialSummaryWithTerraTrial(summary)
	if commercialRelaySharedLimit(view) != 1700 {
		t.Fatal("trial inflated the shared pool")
	}
	if summary.TotalsByModel[commercialTerraTrialModel] != 0 || len(summary.Grants) != 0 {
		t.Fatal("trial mutated the commercial ledger")
	}
	if view.TotalsByModel[commercialTerraTrialModel] != 100 || !commercialRelayGovernedGrant("free_trial") {
		t.Fatal("trial cannot grant scoped Terra access")
	}
}

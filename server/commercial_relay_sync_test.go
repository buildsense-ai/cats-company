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

func quotaRateSummary(slug string, points float64) *types.CommercialSummary {
	return &types.CommercialSummary{
		UID:          38,
		TotalCNY:     points,
		Entitlements: []*types.CommercialEntitlement{{PlanSlug: slug, State: "active", StartsAt: time.Now().UTC().Add(-time.Hour)}},
		Plans:        []*types.CommercialPlan{{Slug: slug, MonthlyBudget: points}},
	}
}

func TestCommercialRelayQuotaRate(t *testing.T) {
	for _, test := range []struct {
		name    string
		summary *types.CommercialSummary
		want    float64
	}{
		{"personal", quotaRateSummary("catsco-personal", 11000), 11000.0 / 300.0},
		{"pro", quotaRateSummary("catsco-pro", 33000), 33000.0 / 700.0},
		{"unknown plan falls back to the entry rate", quotaRateSummary("internal-custom", 5000), 11000.0 / 300.0},
		{"nil summary", nil, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := commercialRelayQuotaRate(test.summary); !nearlyEqual(got, test.want) {
				t.Fatalf("rate=%v, want %v", got, test.want)
			}
		})
	}

	// Plan points fall back to the per-model budgets when MonthlyBudget is 0.
	summary := quotaRateSummary("catsco-personal", 11000)
	summary.Plans[0].MonthlyBudget = 0
	summary.Plans[0].ModelBudgets = map[string]float64{"MiniMax-M3": 6000, "deepseek-v4-flash": 5000}
	if got := commercialRelayQuotaRate(summary); !nearlyEqual(got, 11000.0/300.0) {
		t.Fatalf("model budgets fallback rate=%v", got)
	}

	// Summaries rebuilt by the baseline flow no longer carry the plan rows;
	// the static official-package table must still produce the rate.
	noPlans := quotaRateSummary("catsco-pro", 33000)
	noPlans.Plans = nil
	if got := commercialRelayQuotaRate(noPlans); !nearlyEqual(got, 33000.0/700.0) {
		t.Fatalf("static plan points rate=%v", got)
	}

	// Internal/custom plans carry their own real-cost anchor in the plan row.
	custom := quotaRateSummary("catsco-internal-all-models-50k", 50000)
	custom.Plans[0].RealCostCNY = 1000
	if got := commercialRelayQuotaRate(custom); !nearlyEqual(got, 50.0) {
		t.Fatalf("custom plan rate=%v", got)
	}
	// Without an anchor every package shares the entry-level rate so quota
	// still burns at the true cost pace instead of a made-up default.
	unanchored := quotaRateSummary("catsco-internal-all-models-50k", 50000)
	if got := commercialRelayQuotaRate(unanchored); !nearlyEqual(got, 11000.0/300.0) {
		t.Fatalf("unanchored custom plan rate=%v", got)
	}
	free := quotaRateSummary("catsco-free", 1800)
	if got := commercialRelayQuotaRate(free); !nearlyEqual(got, 11000.0/300.0) {
		t.Fatalf("free rate=%v", got)
	}
	legacy := quotaRateSummary("catsco-legacy-custom", 1600)
	if got := commercialRelayQuotaRate(legacy); !nearlyEqual(got, 11000.0/300.0) {
		t.Fatalf("legacy rate=%v", got)
	}
	// An operator anchor on an official plan wins over the static table.
	override := quotaRateSummary("catsco-pro", 33000)
	override.Plans[0].RealCostCNY = 660
	if got := commercialRelayQuotaRate(override); !nearlyEqual(got, 50.0) {
		t.Fatalf("plan anchor override rate=%v", got)
	}

	// A revoked entitlement must not enable a conversion rate.
	revoked := quotaRateSummary("catsco-pro", 33000)
	revoked.Entitlements[0].State = "revoked"
	if got := commercialRelayQuotaRate(revoked); got != 0 {
		t.Fatalf("revoked rate=%v, want 0", got)
	}
}

func TestSyncSendsPlanQuotaRateAndStaysIdempotent(t *testing.T) {
	store := &commercialRelayBaselineTestStore{commercialRelaySyncTestStore: &commercialRelaySyncTestStore{summary: quotaRateSummary("catsco-pro", 33000)}}
	state := commercialRelayUsageUser{Configured: true, Key: &commercialRelayKeySummary{State: "active"}, Limits: commercialRelayLimits{
		MonthlyBudget: commercialRelayBudget{ResetDuration: "1M"},
	}}
	writes, lastRate := 0, 0.0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writes++
			var body struct {
				Monthly float64 `json:"monthly_budget"`
				Quota   float64 `json:"quota_rate"`
				Window  *string `json:"usage_window_start"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			lastRate = body.Quota
			state.Key.QuotaRate = body.Quota
			state.Limits.MonthlyBudget.MaxLimit = body.Monthly
			if body.Window != nil {
				state.UsageWindowStart = *body.Window
			}
		}
		_ = json.NewEncoder(w).Encode(state)
	}))
	defer relay.Close()
	syncer := NewCommercialRelaySyncer(store, &RelayAdminClient{baseURL: relay.URL, token: "fixture", client: relay.Client()}, CommercialRelaySyncerOptions{EnforceEnabled: true})
	if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
		t.Fatal(err)
	}
	if !nearlyEqual(lastRate, 33000.0/700.0) {
		t.Fatalf("quota_rate=%v, want %v", lastRate, 33000.0/700.0)
	}
	before := writes
	if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
		t.Fatal(err)
	}
	if writes != before {
		t.Fatalf("quota_rate sync is not idempotent: %d -> %d", before, writes)
	}
}

func TestSyncResetsQuotaRateWhenLeavingOfficialPackages(t *testing.T) {
	// The user previously held catsco-pro and the relay still bills 47.14;
	// after the package lapses only the free baseline remains, so the sync must
	// push an explicit 0 to reset the relay back to its default rate.
	store := &commercialRelayBaselineTestStore{commercialRelaySyncTestStore: &commercialRelaySyncTestStore{summary: &types.CommercialSummary{
		UID:          38,
		TotalCNY:     1700,
		Entitlements: []*types.CommercialEntitlement{{Source: "free", State: "active", StartsAt: time.Now().UTC().Add(-time.Hour)}},
	}}}
	state := commercialRelayUsageUser{Configured: true, Key: &commercialRelayKeySummary{State: "active", QuotaRate: 33000.0 / 700.0}, Limits: commercialRelayLimits{
		MonthlyBudget:  commercialRelayBudget{ResetDuration: "1M"},
		FreeTerraTrial: &commercialRelayTerraTrial{MaxLimit: 100, ResetDuration: "never"},
		AvailableModelLimits: []commercialRelayModelLimit{
			{Provider: "gpt", Model: commercialTerraTrialModel, AllowedModels: []string{commercialTerraTrialModel}},
		},
	}}
	var rates []float64
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct {
				Monthly float64                               `json:"monthly_budget"`
				Quota   *float64                              `json:"quota_rate"`
				Window  *string                               `json:"usage_window_start"`
				Trial   *bool                                 `json:"free_terra_trial"`
				Scopes  []commercialRelayModelScope           `json:"model_scopes"`
				Budgets []commercialRelayProviderBudgetUpdate `json:"provider_config_budgets"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if body.Quota == nil {
				t.Errorf("quota_rate must be sent on every shared-pool sync")
			} else {
				rates = append(rates, *body.Quota)
				state.Key.QuotaRate = *body.Quota
			}
			state.Limits.MonthlyBudget.MaxLimit = body.Monthly
			if body.Window != nil {
				state.UsageWindowStart = *body.Window
			}
			if body.Trial != nil {
				state.Limits.FreeTerraTrial.Enabled = *body.Trial
			}
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
	if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
		t.Fatal(err)
	}
	if len(rates) == 0 || !nearlyEqual(rates[len(rates)-1], 11000.0/300.0) {
		t.Fatalf("expected the entry-level rate after leaving pro, got %v", rates)
	}
	before := len(rates)
	if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
		t.Fatal(err)
	}
	if len(rates) != before {
		t.Fatalf("rate reset is not idempotent: %v", rates)
	}
}

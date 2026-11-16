package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// relayUserWithBudgets builds the relay-limits shape Relay returns for a key.
func relayUserWithBudgets(monthlyLimit float64, budgets map[string]float64) *commercialRelayUsageUser {
	limits := commercialRelayLimits{
		MonthlyBudget: commercialRelayBudget{MaxLimit: monthlyLimit, ResetDuration: "1M"},
	}
	for model, amount := range budgets {
		limits.ModelLimits = append(limits.ModelLimits, commercialRelayModelLimit{
			Model:  model,
			Budget: commercialRelayBudget{MaxLimit: amount, ResetDuration: "1M"},
		})
	}
	return &commercialRelayUsageUser{UID: 1601, Configured: true, Limits: limits}
}

// newKeyTextOnlyBudgets mirrors what Relay actually seeds for a brand-new key:
// only the single-model provider configs receive a default budget, so the
// multi-model image provider contributes nothing.
var newKeyTextOnlyBudgets = map[string]float64{
	"MiniMax-M2.7":   1000,
	"MiniMax-M3":     500,
	"deepseek-flash": 100,
	"glm-5.3-flash":  100,
}

func TestCommercialRelayBaselineTreatsNewKeySeedAsFree(t *testing.T) {
	// Regression: every signup after the image provider started listing several
	// models reported this four-model ¥1700 seed and was classified as the
	// pre-migration legacy pool, freezing new accounts into "内部保留套餐".
	profile, budgets := commercialRelayBaseline(relayUserWithBudgets(1700, newKeyTextOnlyBudgets))
	if profile != commercialRelayBaselineProfileFree {
		t.Fatalf("profile=%q want %q", profile, commercialRelayBaselineProfileFree)
	}
	if !commercialRelayBudgetsMatch(budgets, commercialRelayFreeBudgets) {
		t.Fatalf("upgraded budgets=%v want the full free pool", budgets)
	}
}

// TestCommercialRelayBaselineUpgradesThe25ImageGeneration pins the 000073 cut:
// a key seeded while the image lane held the 2.5 family reports nine models and
// 2200, and the shared-pool representation of the new generation repeats 2500
// on every model. Both have to stay Free and upgrade to the twelve-model
// baseline instead of being frozen as the legacy "内部保留套餐".
func TestCommercialRelayBaselineUpgradesThe25ImageGeneration(t *testing.T) {
	if len(commercialRelayFreeBudgets) != 12 || commercialRelayBudgetTotal(commercialRelayFreeBudgets) != 2500 {
		t.Fatalf("free pool = %d models / %v, want twelve models / 2500",
			len(commercialRelayFreeBudgets), commercialRelayBudgetTotal(commercialRelayFreeBudgets))
	}
	profile, budgets := commercialRelayBaseline(relayUserWithBudgets(2200, commercialRelayV5EraFreeBudgets))
	if profile != commercialRelayBaselineProfileFree || !commercialRelayBudgetsMatch(budgets, commercialRelayFreeBudgets) {
		t.Fatalf("2.5 era pool: profile=%q budgets=%v", profile, budgets)
	}
	// The shared-pool representation of both generations (every model entry
	// repeats the pool total) is detected before the per-model baseline.
	shared := make(map[string]float64, len(commercialRelayV5EraFreeBudgets))
	for model := range commercialRelayV5EraFreeBudgets {
		shared[model] = 2200
	}
	if upgraded, ok := commercialRelaySharedPoolFreeUpgrade(2200, shared); !ok ||
		!commercialRelayBudgetsMatch(upgraded, commercialRelayFreeBudgets) {
		t.Fatalf("shared 2200 pool: ok=%v budgets=%v", ok, upgraded)
	}
	shared = make(map[string]float64, len(commercialRelayFreeBudgets))
	for model := range commercialRelayFreeBudgets {
		shared[model] = 2500
	}
	if upgraded, ok := commercialRelaySharedPoolFreeUpgrade(2500, shared); !ok ||
		!commercialRelayBudgetsMatch(upgraded, commercialRelayFreeBudgets) {
		t.Fatalf("shared 2500 pool: ok=%v budgets=%v", ok, upgraded)
	}
}

func TestCommercialRelayBaselineUpgradesSeedPlusExtraFreeModels(t *testing.T) {
	// A key may resolve one or two image providers as well; the seed stays the
	// anchor, extra Free models at Free prices do not disqualify it.
	budgets := map[string]float64{
		"MiniMax-M2.7":   1000,
		"MiniMax-M3":     500,
		"deepseek-flash": 100,
		"glm-5.3-flash":  100,
		"gpt-image-2":    100,
		"gpt-image-2.5":  100,
	}
	profile, upgraded := commercialRelayBaseline(relayUserWithBudgets(0, budgets))
	if profile != commercialRelayBaselineProfileFree {
		t.Fatalf("profile=%q want %q", profile, commercialRelayBaselineProfileFree)
	}
	if !commercialRelayBudgetsMatch(upgraded, commercialRelayFreeBudgets) {
		t.Fatalf("upgraded budgets=%v want the full free pool", upgraded)
	}
}

func TestCommercialRelayBaselineUpgradesEveryObservedNewKeySeed(t *testing.T) {
	// Relay only seeds catalog defaults onto single-model provider configs, so
	// what a brand-new key reports depends on how the Free models were split
	// across providers at signup time. Every shape below was observed in
	// production on accounts whose baseline was written within seconds of
	// signup; all of them are the Free pool, not a custom retention package.
	cases := map[string]map[string]float64{
		"two models": {"MiniMax-M2.7": 1000, "MiniMax-M3": 500},
		"three models": {
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "glm-5.3-flash": 100,
		},
		"text seed": {
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500,
			"deepseek-flash": 100, "glm-5.3-flash": 100,
		},
		"seed plus one image": {
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500,
			"deepseek-flash": 100, "glm-5.3-flash": 100, "gpt-image-2": 100,
		},
		"minimax plus image lane": {
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500,
			"gpt-image-2": 100, "gpt-image-2.5": 100, "gpt-image-2.5-flare": 100,
			"gpt-image-2.5-sunburst": 100, "chatgpt-image-latest": 100,
		},
		"minimax image lane plus glm": {
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "glm-5.3-flash": 100,
			"gpt-image-2": 100, "gpt-image-2.5": 100, "gpt-image-2.5-flare": 100,
			"gpt-image-2.5-sunburst": 100, "chatgpt-image-latest": 100,
		},
	}
	for name, budgets := range cases {
		t.Run(name, func(t *testing.T) {
			profile, upgraded := commercialRelayBaseline(relayUserWithBudgets(0, budgets))
			if profile != commercialRelayBaselineProfileFree {
				t.Fatalf("profile=%q want %q (budgets=%v)", profile, commercialRelayBaselineProfileFree, budgets)
			}
			if !commercialRelayBudgetsMatch(upgraded, commercialRelayFreeBudgets) {
				t.Fatalf("upgraded budgets=%v want the full free pool", upgraded)
			}
		})
	}
}

func TestCommercialRelayBaselineKeepsCustomPoolsLegacy(t *testing.T) {
	cases := map[string]map[string]float64{
		// Same model, more money than Free: a real custom grant.
		"higher amount": {"MiniMax-M2.7": 5000},
		// Same count, different price: not a Free pool generation.
		"discounted amount": {"MiniMax-M2.7": 50},
		// Model that never belonged to Free.
		"unknown model":   {"claude-sonnet-5-5": 100},
		"mixed with paid": {"MiniMax-M2.7": 1000, "claude-sonnet-5-5": 100},
	}
	for name, budgets := range cases {
		t.Run(name, func(t *testing.T) {
			profile, preserved := commercialRelayBaseline(relayUserWithBudgets(0, budgets))
			if profile != commercialRelayBaselineProfileLegacy {
				t.Fatalf("profile=%q want %q (budgets=%v)", profile, commercialRelayBaselineProfileLegacy, budgets)
			}
			if len(preserved) == 0 {
				t.Fatalf("legacy budgets must be preserved, got %v", preserved)
			}
		})
	}
}

func TestCommercialRelayBaselineKeepExactFreeAndOlderGenerations(t *testing.T) {
	cases := map[string]struct {
		budgets map[string]float64
	}{
		"current free pool": {budgets: commercialRelayFreeBudgetsCopy()},
		"glm era":           {budgets: commercialRelayGlmEraFreeBudgets},
		"pre image lane":    {budgets: commercialRelayLegacyFreeBudgets},
		"v4 era":            {budgets: commercialRelayV4EraFreeBudgets},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			profile, _ := commercialRelayBaseline(relayUserWithBudgets(0, tc.budgets))
			if profile != commercialRelayBaselineProfileFree {
				t.Fatalf("profile=%q want %q", profile, commercialRelayBaselineProfileFree)
			}
		})
	}
}

func TestCommercialRelayBaselineForSummaryUsesSubsetWhenSummaryEmpty(t *testing.T) {
	// The signup path hits this with a nil/empty summary, which is exactly the
	// case that regressed.
	profile, budgets, err := commercialRelayBaselineForSummary(nil, relayUserWithBudgets(1700, newKeyTextOnlyBudgets))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile != commercialRelayBaselineProfileFree {
		t.Fatalf("profile=%q want %q", profile, commercialRelayBaselineProfileFree)
	}
	if !commercialRelayBudgetsMatch(budgets, commercialRelayFreeBudgets) {
		t.Fatalf("budgets=%v want the full free pool", budgets)
	}
}

// TestCommercialRelayHasBaselineEntitlementGuardsExistingAccounts pins the gate
// SyncUID uses before it ever reaches the classifier: an account that already
// holds an active free/legacy entitlement is never re-classified from the Relay
// snapshot, which is what keeps existing legacy accounts untouched.
func TestCommercialRelayHasBaselineEntitlementGuardsExistingAccounts(t *testing.T) {
	for _, source := range []string{"legacy", "free"} {
		summary := &types.CommercialSummary{
			UID: 1601,
			Entitlements: []*types.CommercialEntitlement{
				{Source: source, State: "active", PlanSlug: "catsco-legacy-custom"},
			},
		}
		if !commercialRelayHasBaselineEntitlement(summary) {
			t.Fatalf("an active %s entitlement must block re-classification", source)
		}
	}
	if commercialRelayHasBaselineEntitlement(&types.CommercialSummary{UID: 1601}) {
		t.Fatal("a summary without entitlements must not claim a baseline")
	}
}

func TestCommercialRelayBudgetsAreFreePoolSubset(t *testing.T) {
	seedPlusImage := map[string]float64{
		"MiniMax-M2.7":   1000,
		"MiniMax-M3":     500,
		"deepseek-flash": 100,
		"glm-5.3-flash":  100,
		"gpt-image-2":    100,
	}
	cases := []struct {
		name    string
		budgets map[string]float64
		want    bool
	}{
		{"empty", nil, false},
		{"full pool", commercialRelayFreeBudgetsCopy(), true},
		{"text seed", newKeyTextOnlyBudgets, true},
		{"seed plus one image", seedPlusImage, true},
		{"single model", map[string]float64{"MiniMax-M3": 500}, true},
		{"image only", map[string]float64{"gpt-image-2": 100}, true},
		{"minimax only", map[string]float64{
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500,
		}, true},
		{"seed missing deepseek", map[string]float64{
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "glm-5.3-flash": 100,
		}, true},
		{"price above free", map[string]float64{
			"MiniMax-M2.7": 1000, "MiniMax-M3": 501, "deepseek-flash": 100, "glm-5.3-flash": 100,
		}, false},
		{"price below free", map[string]float64{
			"MiniMax-M2.7": 1000, "MiniMax-M3": 499, "deepseek-flash": 100, "glm-5.3-flash": 100,
		}, false},
		{"foreign model on top of seed", map[string]float64{
			"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-flash": 100, "glm-5.3-flash": 100,
			"claude-sonnet-5-5": 100,
		}, false},
		{"retired v4 model", map[string]float64{
			"MiniMax-M2.7": 1000, "deepseek-v4-flash": 100,
		}, false},
		{"case insensitive model", map[string]float64{
			"minimax-m2.7": 1000, "minimax-m3": 500, "deepseek-flash": 100, "glm-5.3-flash": 100,
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commercialRelayBudgetsAreFreePoolSubset(tc.budgets); got != tc.want {
				t.Fatalf("commercialRelayBudgetsAreFreePoolSubset(%v)=%v want %v", tc.budgets, got, tc.want)
			}
		})
	}
}

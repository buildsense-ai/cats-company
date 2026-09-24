package postgres

import (
	"math"
	"sort"
	"strings"
	"testing"
)

// reconcileCatalog is the shape the relay publishes after gpt-6-sol landed:
// twelve sellable models, five of them image-lane.
func reconcileCatalog() []string {
	return []string{
		"MiniMax-M2.7", "MiniMax-M3", "chatgpt-image-latest", "deepseek-flash",
		"glm-5.3-flash", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-sol",
		"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst",
	}
}

// reconcilePersonalPlan is the live Personal plan: five chat models at 2100 plus
// five image models at 100. Those per-model amounts are a split of the plan's
// 11000 total, not independent quotas - production shows every model a paid user
// holds reporting the same max_limit.
func reconcilePersonalPlan() map[string]float64 {
	return map[string]float64{
		"MiniMax-M2.7": 2100, "MiniMax-M3": 2100, "deepseek-flash": 2100,
		"glm-5.3-flash": 2100, "gpt-5.6-terra": 2100,
		"gpt-image-2": 100, "gpt-image-2.5": 100, "gpt-image-2.5-flare": 100,
		"gpt-image-2.5-sunburst": 100, "chatgpt-image-latest": 100,
	}
}

func sumBudgets(budgets map[string]float64) float64 {
	total := 0.0
	for _, amount := range budgets {
		total += amount
	}
	return math.Round(total*1e6) / 1e6
}

func TestReconcileAddsNewRelayModelsAndKeepsTheTotal(t *testing.T) {
	// Adding the two relay models the plan is missing must not raise the plan's
	// advertised allowance: the same 11000 is spread over twelve models instead
	// of ten. Image models take part in the same split because they draw on the
	// same pool.
	target := reconcilePlanModelBudgets(reconcilePersonalPlan(), reconcileCatalog(), true)
	if target == nil {
		t.Fatal("reconcile skipped a plan missing a relay model")
	}
	// Every model carries the same share, except one that absorbs the rounding
	// remainder (6-decimal storage cannot divide 11000 by 12 exactly).
	want := 11000.0 / 12.0
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-sol", "MiniMax-M2.7", "gpt-image-2", "chatgpt-image-latest"} {
		if got := target[model]; math.Abs(got-want) > 0.00001 {
			t.Fatalf("%s budget = %v, want ~%v (11000 over twelve models)", model, got, want)
		}
	}
	if got := sumBudgets(target); got != 11000 {
		t.Fatalf("total = %v, want the plan's original 11000 exactly", got)
	}
}

func TestReconcileIsIdempotentWhenThePlanAlreadyMatches(t *testing.T) {
	// A plan that already carries every relay model must produce no write at
	// all, which is what keeps a routine restart free of database churn.
	personal := map[string]float64{}
	for _, model := range reconcileCatalog() {
		personal[model] = 11000.0 / 12.0
	}

	if target := reconcilePlanModelBudgets(personal, reconcileCatalog(), true); target != nil {
		t.Fatalf("reconcile rewrote a matching plan: %v", target)
	}
}

func TestReconcileDropsRetiredModelsAndResharesThePool(t *testing.T) {
	// A model the relay retired leaves the plan and its share returns to the
	// pool, mirroring the DeepSeek V4 retirement (six chat models at 1750 became
	// five at 2100). The plan's total must not move.
	personal := map[string]float64{
		"MiniMax-M2.7": 1750, "MiniMax-M3": 1750, "deepseek-v4-flash": 1750,
		"deepseek-flash": 1750, "glm-5.3-flash": 1750, "gpt-5.6-terra": 1750,
		"gpt-image-2": 100, "gpt-image-2.5": 100, "gpt-image-2.5-flare": 100,
		"gpt-image-2.5-sunburst": 100, "chatgpt-image-latest": 100,
	}

	target := reconcilePlanModelBudgets(personal, []string{
		"MiniMax-M2.7", "MiniMax-M3", "deepseek-flash", "glm-5.3-flash", "gpt-5.6-terra",
		"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "chatgpt-image-latest",
	}, true)
	if target == nil {
		t.Fatal("reconcile skipped a plan holding a retired model")
	}
	if _, ok := target["deepseek-v4-flash"]; ok {
		t.Fatal("reconcile kept a model the relay no longer sells")
	}
	if got := sumBudgets(target); got != 11000 {
		t.Fatalf("total = %v, want 11000 (the retired model's share stays in the pool)", got)
	}
	if got := target["MiniMax-M2.7"]; math.Abs(got-1100.0) > 0.00001 {
		t.Fatalf("MiniMax-M2.7 budget = %v, want ~1100 (11000 over ten models)", got)
	}
}

func TestReconcileRemovesRetiredModelsEvenWhenThePlanIsPinned(t *testing.T) {
	// A pinned plan keeps its model set, but a retired model still goes: leaving
	// it would advertise a model whose requests the relay refuses.
	personal := reconcilePersonalPlan()
	personal["deepseek-v4-flash"] = 100

	target := reconcilePlanModelBudgets(personal, reconcileCatalog(), false)
	if target == nil {
		t.Fatal("reconcile skipped a pinned plan holding a retired model")
	}
	if _, ok := target["deepseek-v4-flash"]; ok {
		t.Fatal("reconcile kept a retired model in a pinned plan")
	}
	if got := sumBudgets(target); got != 11100 {
		t.Fatalf("total = %v, want 11100 (the plan's own total, unchanged)", got)
	}
}

func TestReconcileDoesNotAddModelsToAPinnedPlan(t *testing.T) {
	// auto_update_models=false means "this plan sells a fixed set": a new relay
	// model must not appear, and the plan's own models keep their amounts.
	target := reconcilePlanModelBudgets(reconcilePersonalPlan(), reconcileCatalog(), false)
	if target != nil {
		t.Fatalf("reconcile rewrote a pinned plan that already matched: %v", target)
	}
}

func TestReconcileSharesANewImageModelFromTheSamePool(t *testing.T) {
	// Image models draw on the plan's shared pool like every other model, so a
	// new one must share the total rather than take a separate allowance out of
	// it. Treating the image lane as its own small quota would quietly reduce
	// what the buyer can spend.
	catalog := append(reconcileCatalog(), "gpt-image-3")
	personal := reconcilePersonalPlan()

	target := reconcilePlanModelBudgets(personal, catalog, true)
	if target == nil {
		t.Fatal("reconcile skipped a plan missing a new image model")
	}
	if got := sumBudgets(target); got != 11000 {
		t.Fatalf("total = %v, want 11000 (a new model shares the pool, it does not add to it)", got)
	}
	want := 11000.0 / 13.0
	if got := target["gpt-image-3"]; math.Abs(got-want) > 0.00001 {
		t.Fatalf("gpt-image-3 budget = %v, want ~%v (11000 over thirteen models)", got, want)
	}
	if got := target["MiniMax-M2.7"]; math.Abs(got-want) > 0.00001 {
		t.Fatalf("MiniMax-M2.7 budget = %v, want ~%v", got, want)
	}
}

func TestReconcileSpreadsRoundingRemainderWithoutDriftingTheTotal(t *testing.T) {
	// A pool that does not divide evenly must still sum to the original total,
	// or every restart would quietly change what a plan advertises.
	personal := map[string]float64{"alpha-model": 100, "beta-model": 100, "gamma-model": 100}
	target := reconcilePlanModelBudgets(personal, []string{"alpha-model", "beta-model", "gamma-model", "delta-model"}, true)
	if target == nil {
		t.Fatal("reconcile skipped a plan missing a relay model")
	}
	if got := sumBudgets(target); got != 300 {
		t.Fatalf("total = %v, want exactly 300", got)
	}
	if got := target["delta-model"]; got != 75 {
		t.Fatalf("delta-model budget = %v, want 75", got)
	}
}

func TestReconcileKeepsEveryModelOnTheSameLimit(t *testing.T) {
	// The relay repeats the shared limit on every model entry, and production
	// shows a paid user's image and chat models reporting the same max_limit. The
	// split must therefore be uniform, with at most one model absorbing the
	// rounding remainder that 6-decimal storage cannot divide away.
	target := reconcilePlanModelBudgets(reconcilePersonalPlan(), reconcileCatalog(), true)
	if target == nil {
		t.Fatal("reconcile skipped the plan")
	}
	counts := map[float64]int{}
	for _, amount := range target {
		counts[amount]++
	}
	if len(counts) > 2 {
		t.Fatalf("the pool was split unevenly across %d distinct amounts: %v", len(counts), counts)
	}
	odd := 0
	for amount, count := range counts {
		if math.Abs(amount-11000.0/12.0) > 0.00001 {
			t.Fatalf("a model carries %v, which is not the shared share %v", amount, 11000.0/12.0)
		}
		if count == 1 {
			odd++
		}
	}
	if odd > 1 {
		t.Fatalf("more than one model carries an odd amount: %v", counts)
	}
}

func TestReconcileDeltaReportsAddedAndRemovedModels(t *testing.T) {
	before := map[string]float64{"a-model": 1, "b-model": 1}
	after := map[string]float64{"b-model": 1, "c-model": 1}

	added, removed := reconcileModelDelta(before, after)
	if strings.Join(added, ",") != "c-model" {
		t.Fatalf("added = %v, want [c-model]", added)
	}
	if strings.Join(removed, ",") != "a-model" {
		t.Fatalf("removed = %v, want [a-model]", removed)
	}
	if added, removed := reconcileModelDelta(before, before); len(added) != 0 || len(removed) != 0 {
		t.Fatalf("an unchanged set reported a delta: added=%v removed=%v", added, removed)
	}
}

func TestReconcileModelDeltaIsCaseInsensitive(t *testing.T) {
	before := map[string]float64{"GPT-6-SOL": 100}
	after := map[string]float64{"gpt-6-sol": 100}

	added, removed := reconcileModelDelta(before, after)
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("a casing change was reported as a model change: added=%v removed=%v", added, removed)
	}
}

func TestReconcileNormalizesCatalogInput(t *testing.T) {
	models := normalizeReconcileModels([]string{"  gpt-6-sol  ", "GPT-6-SOL", "", "  ", "*", "deepseek-flash"})
	if strings.Join(models, ",") != "gpt-6-sol,deepseek-flash" {
		t.Fatalf("models = %v, want trimmed case-insensitive de-duplication without wildcards", models)
	}
}

func TestReconcileKeepsRelaySpellingWhenThePlanDiffersInCase(t *testing.T) {
	// The relay's spelling wins so the stored budgets match what the relay
	// publishes; the plan's amount is what carries over.
	personal := map[string]float64{"minimax-m2.7": 2100}
	target := reconcilePlanModelBudgets(personal, []string{"MiniMax-M2.7", "gpt-6-sol"}, true)
	if target == nil {
		t.Fatal("reconcile skipped the plan")
	}
	if _, ok := target["MiniMax-M2.7"]; !ok {
		t.Fatalf("reconcile kept the plan's casing: %v", target)
	}
	if got := sumBudgets(target); got != 2100 {
		t.Fatalf("total = %v, want 2100", got)
	}
}

func TestReconcileRefusesToEmptyAPlan(t *testing.T) {
	// If every model were dropped the plan would sell nothing, so the reconcile
	// leaves the plan alone and lets the caller report the problem. The relay's
	// monthly-pool wildcard is not a sellable model.
	if target := reconcilePlanModelBudgets(reconcilePersonalPlan(), []string{"*"}, true); target != nil {
		t.Fatalf("reconcile emptied a plan: %v", target)
	}
	// A catalog holding only the wildcard plus one real model leaves the plan
	// selling exactly that model, with the plan's total on it.
	target := reconcilePlanModelBudgets(reconcilePersonalPlan(), []string{"*", "gpt-6-sol"}, true)
	if target == nil {
		t.Fatal("reconcile skipped a plan whose catalog shrank to one model")
	}
	if len(target) != 1 || target["gpt-6-sol"] != 11000 {
		t.Fatalf("target = %v, want only gpt-6-sol carrying the plan's 11000", target)
	}
}

func TestReconcileSortedNamesAreDeterministic(t *testing.T) {
	// The rounding remainder lands on the first model, so the order must be
	// stable across runs or the same input could produce different budgets.
	target := reconcilePlanModelBudgets(
		map[string]float64{"zeta-model": 100, "alpha-model": 100},
		[]string{"zeta-model", "alpha-model", "mid-model"},
		true,
	)
	if target == nil {
		t.Fatal("reconcile skipped the plan")
	}
	names := make([]string, 0, len(target))
	for model := range target {
		names = append(names, model)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "alpha-model,mid-model,zeta-model" {
		t.Fatalf("models = %v", names)
	}
	if got := sumBudgets(target); got != 200 {
		t.Fatalf("total = %v, want 200", got)
	}
}

func TestReconcileCoversEveryOfficialPaidPlan(t *testing.T) {
	for _, slug := range commercialReconcilePlanSlugs {
		if commercialOfficialPlanTier(slug) == 0 {
			t.Fatalf("reconcile maintains %s, which is not an official paid plan", slug)
		}
	}
	for _, slug := range []string{commercialPersonalPlanSlug, commercialProPlanSlug} {
		found := false
		for _, candidate := range commercialReconcilePlanSlugs {
			if candidate == slug {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("official paid plan %s is not maintained by the reconcile", slug)
		}
	}
	// The Free plan keeps its own model set; the reconcile must not touch it.
	if commercialOfficialPlanTier(commercialFreePlanSlug) != 0 {
		t.Fatal("the free plan must not be treated as an official paid plan")
	}
}

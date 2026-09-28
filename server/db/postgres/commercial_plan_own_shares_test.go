package postgres

import "testing"

// TestReconcilePlanOwnSharesKeepsThePlansAllocation guards the amount source for
// the fixed-plan grant repair.
//
// The catalog-following pass re-splits a plan's total evenly, so its grants take
// that split. A fixed plan keeps the allocation it was written with, and its
// holders already carry exactly those amounts. Recomputing a share here would
// leave the one repaired package looking unlike every other holder of the same
// plan: Free grants 1000 for MiniMax-M2.7 and 100 for an image model.
func TestReconcilePlanOwnSharesKeepsThePlansAllocation(t *testing.T) {
	// The live Free plan's shape.
	plan := map[string]float64{
		"MiniMax-M2.7": 1000, "MiniMax-M3": 500,
		"deepseek-flash": 100, "glm-5.3-flash": 100,
		"chatgpt-image-latest": 100, "gpt-image-2": 100, "gpt-image-2.5": 100,
		"gpt-image-2.5-flare": 100, "gpt-image-2.5-sunburst": 100,
	}
	models, amounts := reconcilePlanOwnShares(plan)
	if len(models) != 9 {
		t.Fatalf("models=%v, want the plan's nine", models)
	}
	// Sorted for a stable grant order across restarts.
	if models[0] != "MiniMax-M2.7" || models[8] != "gpt-image-2.5-sunburst" {
		t.Fatalf("models are not sorted: %v", models)
	}
	for model, want := range plan {
		if got := amounts[model]; got != want {
			t.Fatalf("%s = %v, want the plan's own %v", model, got, want)
		}
	}
	// An even split would give every model 2200/9 = 244.44 and quietly change
	// what a repaired holder is allowed to spend per model.
	if amounts["MiniMax-M2.7"] == roundBudget(2200.0/9) {
		t.Fatal("the plan's own allocation was replaced by an even split")
	}
}

// TestReconcilePlanOwnSharesSkipsNonSellableEntries covers the wildcard and
// empty rows the encode path can store.
func TestReconcilePlanOwnSharesSkipsNonSellableEntries(t *testing.T) {
	models, amounts := reconcilePlanOwnShares(map[string]float64{
		"*": 5000, "": 10, "MiniMax-M2.7": 1000, "zero": 0, "negative": -5,
	})
	if len(models) != 1 || models[0] != "MiniMax-M2.7" {
		t.Fatalf("models=%v, want only the positive named model", models)
	}
	if len(amounts) != 1 || amounts["MiniMax-M2.7"] != 1000 {
		t.Fatalf("amounts=%v, want only MiniMax-M2.7 at 1000", amounts)
	}
	if models, amounts := reconcilePlanOwnShares(map[string]float64{"*": 1}); models != nil || amounts != nil {
		t.Fatal("a plan with no named model must not produce shares")
	}
}

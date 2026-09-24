package postgres

import (
	"math"
	"sort"
	"testing"
)

// TestReconcileMatchesTheLiveRelayShape pins the fix against the numbers
// observed in production: every model a paid user holds reports the same
// max_limit, equal to the plan's model_budgets sum. Adding the relay's new
// models must keep both properties.
func TestReconcileMatchesTheLiveRelayShape(t *testing.T) {
	// Live Personal plan (id=6): five chat at 2100 + five image at 100 = 11000,
	// and the live catalog adds gpt-5.6-sol and gpt-6-sol.
	live := map[string]float64{
		"MiniMax-M2.7": 2100, "MiniMax-M3": 2100, "deepseek-flash": 2100,
		"glm-5.3-flash": 2100, "gpt-5.6-terra": 2100,
		"gpt-image-2": 100, "gpt-image-2.5": 100, "gpt-image-2.5-flare": 100,
		"gpt-image-2.5-sunburst": 100, "chatgpt-image-latest": 100,
	}
	target := reconcilePlanModelBudgets(live, reconcileCatalog(), true)
	if target == nil {
		t.Fatal("reconcile skipped the live plan shape")
	}

	// The plan total is what a buyer's max_limit is, so it must be unchanged.
	total := 0.0
	for _, amount := range target {
		total += amount
	}
	if math.Abs(total-11000) > 0.000001 {
		t.Fatalf("plan total = %v, want 11000 (a buyer's shared max_limit)", total)
	}

	// Every model must carry the same amount, because the relay repeats one
	// shared limit on every model entry.
	amounts := make([]float64, 0, len(target))
	for _, amount := range target {
		amounts = append(amounts, amount)
	}
	sort.Float64s(amounts)
	if math.Abs(amounts[len(amounts)-1]-amounts[0]) > 0.00001 {
		t.Fatalf("models carry different amounts (%v .. %v); the relay expects one shared limit", amounts[0], amounts[len(amounts)-1])
	}

	// The two new models must be present and carry the shared amount.
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-sol"} {
		if got, ok := target[model]; !ok {
			t.Fatalf("%s missing from the reconciled plan", model)
		} else if math.Abs(got-11000.0/12.0) > 0.00001 {
			t.Fatalf("%s = %v, want the shared share %v", model, got, 11000.0/12.0)
		}
	}
}

// TestReconcileProPlanMatchesTheLiveShape does the same for the Pro plan, whose
// live numbers are five chat at 6300 plus five image at 300.
func TestReconcileProPlanMatchesTheLiveShape(t *testing.T) {
	live := map[string]float64{
		"MiniMax-M2.7": 6300, "MiniMax-M3": 6300, "deepseek-flash": 6300,
		"glm-5.3-flash": 6300, "gpt-5.6-terra": 6300,
		"gpt-image-2": 300, "gpt-image-2.5": 300, "gpt-image-2.5-flare": 300,
		"gpt-image-2.5-sunburst": 300, "chatgpt-image-latest": 300,
	}
	target := reconcilePlanModelBudgets(live, reconcileCatalog(), true)
	if target == nil {
		t.Fatal("reconcile skipped the live Pro plan shape")
	}
	total := 0.0
	for _, amount := range target {
		total += amount
	}
	if math.Abs(total-33000) > 0.000001 {
		t.Fatalf("plan total = %v, want 33000", total)
	}
	if got := target["gpt-6-sol"]; math.Abs(got-33000.0/12.0) > 0.00001 {
		t.Fatalf("gpt-6-sol = %v, want %v", got, 33000.0/12.0)
	}
}

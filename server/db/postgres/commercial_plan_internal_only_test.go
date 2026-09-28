package postgres

import (
	"math"
	"strings"
	"testing"
)

// reconcileInternalCatalog is the live relay's two lists: eleven sellable models
// plus the internal-only names, which route but are deliberately not for sale.
func reconcileInternalCatalog() ([]string, []string) {
	return []string{
			"MiniMax-M2.7", "MiniMax-M3", "chatgpt-image-latest", "deepseek-flash",
			"glm-5.3-flash", "gpt-5.6-terra", "gpt-6-sol",
			"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst",
		}, []string{
			"gpt-5.6-sol",
		}
}

// reconcileInternalPlan is the live internal all-models package: twelve models,
// one of which the relay routes but does not sell.
func reconcileInternalPlan() map[string]float64 {
	return map[string]float64{
		"MiniMax-M2.7": 8333.33, "MiniMax-M3": 8333.33, "glm-5.3-flash": 8333.35,
		"gpt-5.6-sol": 8333.33, "gpt-5.6-terra": 8333.33, "gpt-6-sol": 8333.33,
		"deepseek-flash": 1, "gpt-image-2": 1, "gpt-image-2.5": 1,
		"gpt-image-2.5-flare": 1, "gpt-image-2.5-sunburst": 1, "chatgpt-image-latest": 1,
	}
}

// TestReconcileKeepsInternalOnlyModelsThePlanAlreadyHolds is the guard for the
// regression the coverage change introduced.
//
// The relay's sellable list is what a plan may OFFER; an internal-only model is
// routable and deliberately absent from it. Treating "absent from the sellable
// list" as "the relay retired it" would strip gpt-5.6-sol from the internal
// all-models package that grants it - and rebuild its buyers' grants without it,
// so they would lose a model they had paid nothing extra for but were entitled
// to. The model must survive, and its share must stay in the same shared pool.
func TestReconcileKeepsInternalOnlyModelsThePlanAlreadyHolds(t *testing.T) {
	sellable, internalOnly := reconcileInternalCatalog()
	// Drop one sellable model so the plan's set genuinely differs from the
	// catalog. A test on an already-matching plan would pass on a nil result and
	// prove nothing about the keep rule.
	plan := reconcileInternalPlan()
	delete(plan, "gpt-image-2.5-flare")
	plan["gpt-5.6-sol"] = 8333.33
	totalBefore := sumBudgets(plan)

	target := reconcilePlanModelBudgets(plan, sellable, true, internalOnly...)
	if target == nil {
		t.Fatal("the plan is missing a sellable model, so the reconcile must produce a target")
	}
	if _, kept := target["gpt-5.6-sol"]; !kept {
		t.Fatalf("gpt-5.6-sol was dropped from a plan that grants it: %v", target)
	}
	// The added sellable model plus the kept internal-only one: eleven sellable
	// plus gpt-5.6-sol equals twelve.
	if got := len(target); got != 12 {
		t.Fatalf("target carries %d models, want 12 (11 sellable + 1 kept internal-only)", got)
	}
	if got := sumBudgets(target); got != totalBefore {
		t.Fatalf("target total = %v, want the plan's %v preserved", got, totalBefore)
	}
	// Every model shares one pool, so each carries the same amount. A model kept
	// outside the split would silently take its allowance out of the pool. The
	// one exception is the rounding remainder, which the split deliberately puts
	// on a single model so the total stays exact.
	want := roundBudget(totalBefore / 12)
	offShare := 0
	for model, amount := range target {
		if amount == want {
			continue
		}
		offShare++
		// The remainder is at most the model count times the last stored decimal
		// (six places here), so allow the split's own rounding.
		if math.Abs(amount-want) > 0.0001 {
			t.Fatalf("%s carries %v, want the shared %v", model, amount, want)
		}
	}
	if offShare > 1 {
		t.Fatalf("%d models carry a remainder, want at most one", offShare)
	}
}

// TestReconcileNeverAddsInternalOnlyModels puts the other half of the rule in
// place: an internal-only name is only ever kept, never introduced. Otherwise a
// reconcile could add a model to a plan whose buyers were never sold it.
func TestReconcileNeverAddsInternalOnlyModels(t *testing.T) {
	sellable, internalOnly := reconcileInternalCatalog()
	// A plan that does not carry the internal-only model.
	plan := map[string]float64{
		"MiniMax-M2.7": 1000, "MiniMax-M3": 1000, "gpt-5.6-terra": 1000,
	}
	target := reconcilePlanModelBudgets(plan, sellable, true, internalOnly...)
	if target == nil {
		t.Fatal("the sellable list carries models the plan lacks, so a target is expected")
	}
	if _, added := target["gpt-5.6-sol"]; added {
		t.Fatal("an internal-only model must not be added to a plan that never held it")
	}
}

// TestReconcileStillDropsRetiredModels keeps the removal rule honest. Only the
// internal-only list is exempt; a name the relay genuinely retired has to go,
// pinned plan or not.
func TestReconcileStillDropsRetiredModels(t *testing.T) {
	sellable, internalOnly := reconcileInternalCatalog()
	plan := reconcileInternalPlan()
	// A retired model is neither sellable nor internal-only.
	plan["gpt-5.6-luna"] = 8333.33

	target := reconcilePlanModelBudgets(plan, sellable, true, internalOnly...)
	if target == nil {
		t.Fatal("the plan differs from the catalog, so a target is expected")
	}
	if _, kept := target["gpt-5.6-luna"]; kept {
		t.Fatal("a retired model must leave the plan even when it is not internal-only")
	}
	if _, kept := target["gpt-5.6-sol"]; !kept {
		t.Fatal("the internal-only model must still be kept alongside the retirement")
	}
	// The retired model's share returns to the pool: the total is unchanged and
	// now divided over twelve models rather than thirteen.
	if got := sumBudgets(target); got != 58339.33 {
		t.Fatalf("total = %v, want the plan's own total preserved", got)
	}
}

// TestReconcilePinnedPlanKeepsItsInternalOnlyModels covers the pinned path: the
// switch stops the plan from GAINING relay models, and must not stop it from
// keeping a name the relay still routes.
func TestReconcilePinnedPlanKeepsItsInternalOnlyModels(t *testing.T) {
	_, internalOnly := reconcileInternalCatalog()
	plan := reconcileInternalPlan()
	// A pinned plan whose set the sellable catalog has moved past.
	plan["retired-line"] = 100

	target := reconcilePlanModelBudgets(plan, []string{"MiniMax-M2.7", "MiniMax-M3"}, false, internalOnly...)
	if target == nil {
		t.Fatal("the pinned plan holds a retired model, so a target is expected")
	}
	if _, kept := target["gpt-5.6-sol"]; !kept {
		t.Fatal("a pinned plan must keep the internal-only model it holds")
	}
	if _, kept := target["retired-line"]; kept {
		t.Fatal("a pinned plan must still drop a retired model")
	}
}

// TestReconcileWithoutInternalListStillDropsTheName documents the old, wrong
// behaviour as an explicit case: a caller that passes no internal-only list gets
// the removal rule applied to those names. The entry point always supplies the
// list, so this pins what the list is worth rather than what production does.
func TestReconcileWithoutInternalListStillDropsTheName(t *testing.T) {
	sellable, _ := reconcileInternalCatalog()
	target := reconcilePlanModelBudgets(reconcileInternalPlan(), sellable, true)
	if target == nil {
		t.Fatal("the plan differs from the catalog, so a target is expected")
	}
	if _, kept := target["gpt-5.6-sol"]; kept {
		t.Fatal("with no internal-only list the name is indistinguishable from a retired one")
	}
	var names []string
	for model := range target {
		names = append(names, model)
	}
	if strings.Join(names, ",") == "" {
		t.Fatal("the target must still carry the sellable models")
	}
}

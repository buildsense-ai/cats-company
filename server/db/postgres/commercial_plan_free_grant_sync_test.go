package postgres

import "testing"

// TestReconcileSyncsFreePlanGrants guards the drift an operator-assigned free
// package hits.
//
// createOperatorPlanGrants builds a buyer's grants from the plan as it stands at
// assignment time. Free's model set is maintained by its own migration rather
// than the catalog, so when that migration adds a model - or drops a retired one
// - the package it already handed out keeps the old set. The holder ends up with
// quota for a model the plan no longer sells and none for the ones it gained,
// which is indistinguishable from the reconcile being broken.
//
// Free is deliberately not covered by the catalog-following rule, so the fix is
// separate: its grants follow the plan even though its model set does not follow
// the relay.
func TestReconcileSyncsFreePlanGrants(t *testing.T) {
	if !reconcileSyncsGrantsSeparately(commercialFreePlanSlug) {
		t.Fatal("the Free plan must have its buyer grants aligned with the plan")
	}
	for _, slug := range []string{commercialPersonalPlanSlug, commercialProPlanSlug, "catsco-internal-all-models-50k"} {
		if reconcileSyncsGrantsSeparately(slug) {
			t.Fatalf("%s follows the catalog, so it must not use the fixed-plan grant pass", slug)
		}
	}
}

// TestReconcileGrantSetMatchesIgnoresSpelling keeps the idempotence check honest.
// A stored grant can carry the plan's older spelling while the split carries the
// relay's canonical one; treating that as a difference would rebuild the package
// on every startup, churning the ledger and the grants.
func TestReconcileGrantSetMatchesIgnoresSpelling(t *testing.T) {
	plan := []string{"MiniMax-M2.7", "gpt-6-sol", "gpt-image-2.5"}
	held := []string{"minimax-m2.7", "GPT-6-SOL", " gpt-image-2.5 "}
	if !reconcileGrantSetMatches(held, plan) {
		t.Fatal("a package holding the plan's models in another spelling must count as matching")
	}
	// A missing model, an extra one, and a retired name all mean a rebuild.
	for name, held := range map[string][]string{
		"missing one":  {"MiniMax-M2.7", "gpt-6-sol"},
		"extra one":    {"MiniMax-M2.7", "gpt-6-sol", "gpt-image-2.5", "deepseek-v4-flash"},
		"retired only": {"deepseek-v4-flash", "MiniMax-M2.7", "gpt-6-sol"},
	} {
		if reconcileGrantSetMatches(held, plan) {
			t.Fatalf("%s must not count as matching: %v", name, held)
		}
	}
	if reconcileGrantSetMatches(nil, plan) {
		t.Fatal("a package holding nothing cannot match a plan with models")
	}
}

// TestReconcileFixedPlanPassOnlyCoversFree pins the scope of the forced pass.
// Widening it would rewrite the grant packages of plans the catalog pass already
// handles, and the two passes would fight over the same rows.
func TestReconcileFixedPlanPassOnlyCoversFree(t *testing.T) {
	if reconcileSyncsGrantsSeparately(commercialLegacyPlanSlug) {
		t.Fatal("legacy grants are historical records and must not be rebuilt")
	}
	if reconcileSyncsGrantsSeparately("") {
		t.Fatal("an empty slug must not select a plan")
	}
}

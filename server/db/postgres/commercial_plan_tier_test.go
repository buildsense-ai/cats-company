package postgres

import (
	"testing"
)

// The postgres package used to carry a second copy of the official paid-plan
// model whitelist and its validator, and a test pinned the same ten-model set in
// both places so they could not drift apart. That duplicate is gone: the relay
// catalog is the source of truth and the plan model sets are maintained by
// ReconcileCommercialPlanModels, whose own tests live in
// commercial_plan_reconcile_test.go. What still matters here is that the slugs
// the tier logic recognises stay pinned.
func TestOfficialPaidPlanTierRecognisesTheReconciledPlans(t *testing.T) {
	// Entry packages are ordered, not fixed: adding a cheaper plan only means
	// inserting it into commercialOfficialPlanTiers. The numbers themselves are
	// never stored, so shifting them is safe — but the order must stay strict,
	// because the tier decides which lower package an upgrade supersedes.
	if got := commercialOfficialPlanTier(commercialBasicPlanSlug); got != 1 {
		t.Fatalf("%s must be tier 1, got %d", commercialBasicPlanSlug, got)
	}
	if got := commercialOfficialPlanTier(commercialPersonalPlanSlug); got != 2 {
		t.Fatalf("%s must be tier 2, got %d", commercialPersonalPlanSlug, got)
	}
	if got := commercialOfficialPlanTier(commercialProPlanSlug); got != 3 {
		t.Fatalf("%s must be tier 3, got %d", commercialProPlanSlug, got)
	}
	// The Free plan keeps its own model set; the reconcile must not touch it.
	if commercialOfficialPlanTier(commercialFreePlanSlug) != 0 {
		t.Fatal("the free plan must not be treated as an official paid plan")
	}
}

// An upgrade must supersede every cheaper package, not just the one that used
// to sit directly below Pro. Otherwise the basic package's unspent provisioning
// credit would survive an upgrade and the buyer would end up with two machines.
func TestOfficialPlanUpgradeSupersedesEveryLowerTier(t *testing.T) {
	got := commercialOfficialPlanSlugsBelow(commercialOfficialPlanTier(commercialProPlanSlug))
	want := map[string]bool{commercialBasicPlanSlug: true, commercialPersonalPlanSlug: true}
	if len(got) != len(want) {
		t.Fatalf("pro must supersede %d plans, got %v", len(want), got)
	}
	for _, slug := range got {
		if !want[slug] {
			t.Fatalf("unexpected superseded slug %q (got %v)", slug, got)
		}
	}
	if below := commercialOfficialPlanSlugsBelow(commercialOfficialPlanTier(commercialBasicPlanSlug)); len(below) != 0 {
		t.Fatalf("the entry plan supersedes nothing, got %v", below)
	}
	if below := commercialOfficialPlanSlugsBelow(commercialOfficialPlanTier(commercialPersonalPlanSlug)); len(below) != 1 || below[0] != commercialBasicPlanSlug {
		t.Fatalf("personal must supersede only basic, got %v", below)
	}
}

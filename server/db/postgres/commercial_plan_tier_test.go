package postgres

import (
	"testing"
)

// The postgres package used to carry a second copy of the official paid-plan
// model whitelist and its validator, and a test pinned the same ten-model set in
// both places so they could not drift apart. The whitelist is gone: the relay
// catalog is the source of truth and the plan model sets are maintained by
// ReconcileCommercialPlanModels, whose own tests live in
// commercial_plan_reconcile_test.go. What still matters here is that the plan
// slugs the tier logic recognises stay pinned.
func TestOfficialPaidPlanTierRecognisesTheReconciledPlans(t *testing.T) {
	if commercialOfficialPlanTier(commercialPersonalPlanSlug) != 1 {
		t.Fatalf("%s must be tier 1", commercialPersonalPlanSlug)
	}
	if commercialOfficialPlanTier(commercialProPlanSlug) != 2 {
		t.Fatalf("%s must be tier 2", commercialProPlanSlug)
	}
	// The Free plan keeps its own model set; the reconcile must not touch it.
	if commercialOfficialPlanTier(commercialFreePlanSlug) != 0 {
		t.Fatal("the free plan must not be treated as an official paid plan")
	}
}

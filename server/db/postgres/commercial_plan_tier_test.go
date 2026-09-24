package postgres

import (
	"testing"
)

// The postgres package used to carry a second copy of the official paid-plan
// model whitelist and its validator, and this test pinned the same ten-model set
// in both places so they could not drift apart. The whitelist is gone: the relay
// catalog is the source of truth and the plan model sets are maintained by
// ReconcileCommercialPlanModels. What still matters here is that the plan slugs
// the reconcile owns stay pinned to the slugs the tier logic recognises.
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

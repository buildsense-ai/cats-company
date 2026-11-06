package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// Whether a package buys a cloud worker is a plan attribute now, not a slug
// allow-list. The plan editor does not round-trip every column, so an absent
// field must keep the plan's current decision: reading it as false would strip
// the machine from a package that is already on sale, and the next buyer would
// pay for tokens alone.
func TestResolvePlanIncludesWorkerKeepsExistingOnAbsentField(t *testing.T) {
	existing := &types.CommercialPlan{CloudWorkerIncluded: true}

	if got := resolvePlanIncludesWorker(existing, nil); !got {
		t.Fatal("absent field must keep the plan's machine, got false")
	}
}

func TestResolvePlanIncludesWorkerDefaultsToTokenOnlyForNewPlan(t *testing.T) {
	if got := resolvePlanIncludesWorker(nil, nil); got {
		t.Fatal("a new plan must not mint machines until an operator says so")
	}
}

func TestResolvePlanIncludesWorkerHonoursExplicitValues(t *testing.T) {
	yes := true
	no := false
	existing := &types.CommercialPlan{CloudWorkerIncluded: false}
	if got := resolvePlanIncludesWorker(existing, &yes); !got {
		t.Fatal("an explicit true must win over the stored value")
	}
	existing = &types.CommercialPlan{CloudWorkerIncluded: true}
	if got := resolvePlanIncludesWorker(existing, &no); got {
		t.Fatal("an explicit false must win over the stored value")
	}
}

// The public catalog must not leak which fleet a package buys, nor whether it
// mints a machine: both are capacity decisions, and a buyer who cached the flag
// would expect a machine after an operator edited the plan.
func TestCommercialPlanForUserHidesWorkerInternals(t *testing.T) {
	plan := &types.CommercialPlan{
		Slug:                         "catsco-basic",
		CloudWorkerIncluded:          true,
		CloudWorkerBillingMode:       types.CloudWorkerMonthly,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
		CloudWorkerRetentionDays:     7,
		TokenQuota:                   500_000_000,
		TokenQuotaDuration:           "1M",
	}
	public := commercialPlanForUser(plan)
	if public == nil {
		t.Fatal("expected a public copy")
	}
	if public.CloudWorkerIncluded {
		t.Fatal("cloud_worker_included must not reach the public catalog")
	}
	if public.CloudWorkerDeploymentProfile != "" || public.CloudWorkerBillingMode != "" || public.CloudWorkerRetentionDays != 0 {
		t.Fatalf("worker internals leaked: %+v", public)
	}
	// The token quota stays internal for now as well: the plan chooser renders
	// its own copy of the number. That is a known divergence — a package can
	// advertise "500M" while the enforced quota is something else — and is
	// tracked separately from this change, which only moves the machine decision
	// out of the slug allow-list.
	if public.TokenQuota != 0 || public.TokenQuotaDuration != "" {
		t.Fatalf("token quota is not public yet, got %d/%q", public.TokenQuota, public.TokenQuotaDuration)
	}
	if plan.TokenQuota != 500_000_000 {
		t.Fatal("the test fixture must exercise a non-zero quota")
	}
}

package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// The shared fleet sizes each tenant slot by a runtime tier. Until this field
// travelled with the package, every tier bought the same slot: a package priced
// at three times another delivered identical memory and CPU weight, and the
// density the pricing model assumed (20 / 15 / 10 tenants per node) never
// reached the scheduler. `provision-worker.sh` was called without `--plan` at
// all, so `packed-provision.sh` fell back to its `standard` default.
func TestResolvePlanRuntimePlanDefaultsToEmptyForNewPlan(t *testing.T) {
	got, err := resolvePlanRuntimePlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("a new plan must keep the fleet default, got %q", got)
	}
}

// An absent field keeps what the plan already delivers. The plan editor does not
// round-trip every column, and silently resizing live slots on an unrelated
// edit would change what buyers already paid for.
func TestResolvePlanRuntimePlanKeepsExistingOnAbsentField(t *testing.T) {
	existing := &types.CommercialPlan{CloudWorkerRuntimePlan: types.CloudWorkerRuntimePro}
	got, err := resolvePlanRuntimePlan(existing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != types.CloudWorkerRuntimePro {
		t.Fatalf("absent field must keep the stored tier, got %q", got)
	}
}

func TestResolvePlanRuntimePlanHonoursExplicitValue(t *testing.T) {
	light := types.CloudWorkerRuntimeLight
	got, err := resolvePlanRuntimePlan(&types.CommercialPlan{CloudWorkerRuntimePlan: types.CloudWorkerRuntimePro}, &light)
	if err != nil {
		t.Fatal(err)
	}
	if got != types.CloudWorkerRuntimeLight {
		t.Fatalf("explicit tier must win, got %q", got)
	}
}

// An unknown tier is rejected rather than silently downgraded: a typo would
// otherwise land on the fleet default, and the package would ship the wrong slot
// with no error anywhere.
func TestResolvePlanRuntimePlanRejectsUnknownTier(t *testing.T) {
	bogus := "huge"
	if _, err := resolvePlanRuntimePlan(nil, &bogus); err == nil {
		t.Fatal("an unknown tier must be rejected")
	}
}

// Empty is a real value meaning "fleet default", so an operator must be able to
// clear an explicit tier deliberately.
func TestResolvePlanRuntimePlanAllowsClearing(t *testing.T) {
	empty := ""
	got, err := resolvePlanRuntimePlan(&types.CommercialPlan{CloudWorkerRuntimePlan: types.CloudWorkerRuntimeHeavy}, &empty)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("explicit empty must clear the tier, got %q", got)
	}
}

// The runtime tier is capacity, so the public catalog must not advertise it: a
// buyer who cached "pro" would expect a bigger slot after an operator retuned
// the package.
func TestCommercialPlanForUserHidesRuntimePlan(t *testing.T) {
	plan := &types.CommercialPlan{
		Slug:                   "catsco-pro-tier",
		CloudWorkerRuntimePlan: types.CloudWorkerRuntimePro,
	}
	public := commercialPlanForUser(plan)
	if public == nil {
		t.Fatal("expected a public copy")
	}
	if public.CloudWorkerRuntimePlan != "" {
		t.Fatalf("runtime tier leaked to the public catalog: %q", public.CloudWorkerRuntimePlan)
	}
}

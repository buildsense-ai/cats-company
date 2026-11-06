package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// A plan editor that does not know about cloud_worker_deployment_profile sends
// no field at all. Treating that as "reset to private_nat" would silently move
// every already-sold shared-fleet plan back onto dedicated ECS instances, so an
// absent field must keep whatever the plan already has.
func TestResolvePlanDeploymentProfileKeepsExistingOnAbsentField(t *testing.T) {
	existing := &types.CommercialPlan{CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared}

	got, err := resolvePlanDeploymentProfile(existing, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != types.CloudWorkerPackedShared {
		t.Fatalf("absent field must keep the existing fleet: got %q", got)
	}
}

// A new plan starts on the shared container fleet.
//
// The column default is private_nat, so "no fleet named" used to mean "buy a
// dedicated ECS instance per buyer" — roughly six times the per-seat
// infrastructure cost of a shared slot, with no error and nothing in the console
// to notice. The two mistakes are not symmetric: a buyer who expected a
// dedicated machine files a ticket, while a fleet of accidentally dedicated
// machines is just a bill. Dedicated capacity stays available by asking for it
// explicitly.
func TestResolvePlanDeploymentProfileDefaultsForNewPlan(t *testing.T) {
	got, err := resolvePlanDeploymentProfile(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != types.CloudWorkerPackedShared {
		t.Fatalf("new plan without a fleet must default to the shared fleet: got %q", got)
	}
}

func TestResolvePlanDeploymentProfileHonoursExplicitValue(t *testing.T) {
	requested := types.CloudWorkerPackedShared
	existing := &types.CommercialPlan{CloudWorkerDeploymentProfile: types.CloudWorkerPrivateNAT}

	got, err := resolvePlanDeploymentProfile(existing, &requested)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != types.CloudWorkerPackedShared {
		t.Fatalf("explicit request must win: got %q", got)
	}
}

func TestResolvePlanDeploymentProfileHonoursExplicitEmptyAsDefault(t *testing.T) {
	requested := ""
	existing := &types.CommercialPlan{CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared}

	got, err := resolvePlanDeploymentProfile(existing, &requested)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != types.CloudWorkerPrivateNAT {
		t.Fatalf("an explicit empty string must reset to the default fleet: got %q", got)
	}
}

func TestResolvePlanDeploymentProfileRejectsUnknownFleet(t *testing.T) {
	requested := "kubernetes"

	if _, err := resolvePlanDeploymentProfile(nil, &requested); err == nil {
		t.Fatal("an unknown fleet must be rejected")
	}
}

// The public catalog must not leak which fleet a plan buys.
func TestCommercialPlanForUserStripsDeploymentProfile(t *testing.T) {
	plan := &types.CommercialPlan{
		ID:                           7,
		Slug:                         "catsco-personal",
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
	}

	out := commercialPlanForUser(plan)
	if out.CloudWorkerDeploymentProfile != "" {
		t.Fatalf("public catalog leaked the fleet assignment: %q", out.CloudWorkerDeploymentProfile)
	}
	if plan.CloudWorkerDeploymentProfile != types.CloudWorkerPackedShared {
		t.Fatal("the caller's plan must not be mutated")
	}
}

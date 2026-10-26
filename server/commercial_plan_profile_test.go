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

func TestResolvePlanDeploymentProfileDefaultsForNewPlan(t *testing.T) {
	got, err := resolvePlanDeploymentProfile(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != types.CloudWorkerPrivateNAT {
		t.Fatalf("new plan without a fleet must default to the dedicated instance: got %q", got)
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

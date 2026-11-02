package postgres

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// Which credit gets spent decides what we pay for.
//
// An owner can hold more than one credit: a legacy dedicated-ECS package plus a
// new shared-fleet one. The old ORDER BY put public_ip first unconditionally, so
// an account that had just bought a ¥99 shared package and still had an old
// public-IP credit would silently be given a dedicated ECS — roughly six times
// the monthly cost — for a request that a shared container would satisfy.
//
// The rule is now: spend the credit whose fleet matches the owner's currently
// active plan, then the shared fleet, then monthly before on-demand, then
// oldest first.
func TestPostgresReservePrefersCreditMatchingActivePlan(t *testing.T) {
	db := commercialPolicyTestDB(t)
	uid := createGLM53MigrationUser(t, db, "credit-order")

	sharedPlan, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:                         "order-shared-plan",
		Name:                         "Shared",
		SaleState:                    "hidden",
		CloudWorkerIncluded:          true,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
		CloudWorkerRuntimePlan:       types.CloudWorkerRuntimeStandard,
	})
	if err != nil {
		t.Fatalf("create shared plan: %v", err)
	}
	// The buyer is on the shared package right now.
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at)
		VALUES ($1, $2, 'order', 'order-order-test', 'active', CURRENT_TIMESTAMP - INTERVAL '1 day')`,
		uid, sharedPlan); err != nil {
		t.Fatalf("activate shared entitlement: %v", err)
	}
	// An older public-IP credit is still unspent, and a newer shared one exists.
	expiry := time.Now().AddDate(0, 1, 0)
	if _, err := db.GrantCloudWorkerConfiguredCredits(uid, 1, "legacy-public-ip", &expiry, types.CloudWorkerPublicIP, types.CloudWorkerMonthly); err != nil {
		t.Fatalf("grant public ip credit: %v", err)
	}
	if _, err := db.GrantCloudWorkerConfiguredCredits(uid, 1, "current-shared", &expiry, types.CloudWorkerPackedShared, types.CloudWorkerMonthly); err != nil {
		t.Fatalf("grant shared credit: %v", err)
	}

	selection, reserved, err := db.ReserveCloudWorkerConfiguredCredit(uid, "create-order-1", "", "")
	if err != nil || !reserved {
		t.Fatalf("reserve: reserved=%v err=%v", reserved, err)
	}
	if selection.Profile != types.CloudWorkerPackedShared {
		t.Fatalf("reserved profile=%q want %q (the package the buyer is on)", selection.Profile, types.CloudWorkerPackedShared)
	}
}

// When nothing in the active plan matches, the shared fleet still wins: its
// capacity is already paid for, so it is the cheapest way to satisfy a request
// that did not ask for a specific fleet.
func TestPostgresReservePrefersSharedFleetOverDedicated(t *testing.T) {
	db := commercialPolicyTestDB(t)
	uid := createGLM53MigrationUser(t, db, "credit-order-fallback")

	expiry := time.Now().AddDate(0, 1, 0)
	// The dedicated credit is older, so age alone would pick it.
	if _, err := db.GrantCloudWorkerConfiguredCredits(uid, 1, "older-public-ip", &expiry, types.CloudWorkerPublicIP, types.CloudWorkerMonthly); err != nil {
		t.Fatalf("grant public ip credit: %v", err)
	}
	if _, err := db.GrantCloudWorkerConfiguredCredits(uid, 1, "newer-shared", &expiry, types.CloudWorkerPackedShared, types.CloudWorkerMonthly); err != nil {
		t.Fatalf("grant shared credit: %v", err)
	}

	selection, reserved, err := db.ReserveCloudWorkerConfiguredCredit(uid, "create-order-2", "", "")
	if err != nil || !reserved {
		t.Fatalf("reserve: reserved=%v err=%v", reserved, err)
	}
	if selection.Profile != types.CloudWorkerPackedShared {
		t.Fatalf("reserved profile=%q want %q", selection.Profile, types.CloudWorkerPackedShared)
	}
}

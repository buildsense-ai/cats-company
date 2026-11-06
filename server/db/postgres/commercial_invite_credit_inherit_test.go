package postgres

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// An invite minted for a package that sells a machine must actually hand that
// machine out.
//
// The zero value of CloudWorkerCredits used to be indistinguishable from "the
// caller chose zero", so an operator who created a code for catsco-standard
// without repeating the count produced a fully paid package whose redemption
// provisioned nothing. Nothing failed: the redemption succeeded, the entitlement
// was granted, and the buyer simply had no worker — the one thing the package
// exists to sell.
func TestPostgresInviteCreditsInheritPlan(t *testing.T) {
	db := commercialPolicyTestDB(t)

	withWorker, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:                         "inherit-worker-plan",
		Name:                         "Inherit Worker",
		SaleState:                    "hidden",
		CloudWorkerIncluded:          true,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
	})
	if err != nil {
		t.Fatalf("create worker plan: %v", err)
	}
	tokenOnly, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:                         "inherit-token-plan",
		Name:                         "Inherit Token",
		SaleState:                    "hidden",
		CloudWorkerIncluded:          false,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
	})
	if err != nil {
		t.Fatalf("create token plan: %v", err)
	}

	cases := []struct {
		code    string
		planID  int64
		credits int
		inherit bool
		want    int
	}{
		{"INHERIT-WORKER", withWorker, 0, true, 1},
		{"INHERIT-TOKEN", tokenOnly, 0, true, 0},
		// An explicit zero stays a deliberate "token-only invite", even when the
		// bound plan sells a machine: operators use this to hand out a trial
		// without capacity.
		{"EXPLICIT-ZERO", withWorker, 0, false, 0},
		{"EXPLICIT-TWO", withWorker, 2, false, 2},
	}
	for _, tc := range cases {
		if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
			Code:                      tc.code,
			PlanID:                    tc.planID,
			MaxRedemptions:            1,
			CloudWorkerCredits:        tc.credits,
			CloudWorkerCreditsInherit: tc.inherit,
		}); err != nil {
			t.Fatalf("create invite %s: %v", tc.code, err)
		}
	}

	invites, err := db.ListCommercialInviteCodes(100)
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	byCode := map[string]*types.CommercialInviteCode{}
	for _, invite := range invites {
		byCode[invite.Code] = invite
	}
	for _, tc := range cases {
		got, ok := byCode[tc.code]
		if !ok {
			t.Fatalf("invite %s missing from the listing", tc.code)
		}
		if got.CloudWorkerCredits != tc.want {
			t.Errorf("invite %s credits=%d want %d (inherit=%v plan inclusive=%v)",
				tc.code, got.CloudWorkerCredits, tc.want, tc.inherit, tc.planID == withWorker)
		}
	}
}

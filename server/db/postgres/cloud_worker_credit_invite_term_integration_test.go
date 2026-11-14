package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// creditInviteGiftUnits is the gift a V1 credit invite hands out: 4,000 credit,
// i.e. 4e9 micro-units.
const creditInviteGiftUnits int64 = 4_000 * store.ModelCreditUnitsPerCredit

// seedCreditInvitePlan creates the package shape a credit invite is cut from: a
// duration-day package that sells 4,000 model credit and no machine of its own,
// so the machine comes from the invite's single packed_shared credit.
//
// durationDays -1 is a perpetual package, which chk_commercial_plans_duration
// only accepts while it is hidden and free.
func seedCreditInvitePlan(t *testing.T, db *Adapter, slug string, durationDays, retentionDays, tier int) int64 {
	t.Helper()
	plan := &types.CommercialPlan{
		Slug:                         slug,
		Name:                         slug,
		PriceFen:                     3900,
		Currency:                     "CNY",
		SaleState:                    "hidden",
		DurationDays:                 durationDays,
		PlanTier:                     tier,
		ModelCreditUnits:             creditInviteGiftUnits,
		CloudWorkerIncluded:          false,
		CloudWorkerBillingMode:       types.CloudWorkerMonthly,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
		CloudWorkerRetentionDays:     retentionDays,
	}
	if durationDays == -1 {
		plan.PriceFen = 0
	}
	id, err := db.CreateCommercialPlan(plan)
	if err != nil {
		t.Fatalf("seed credit invite plan %s: %v", slug, err)
	}
	return id
}

// seedCreditInviteCode mints an invite the way an operator does: one machine
// credit for the bound package, with the gift amount stated by the invite (not
// inherited from the package's monthly allowance).
func seedCreditInviteCode(t *testing.T, db *Adapter, code string, planID int64) {
	t.Helper()
	if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
		Code:               code,
		PlanID:             planID,
		MaxRedemptions:     5,
		CloudWorkerCredits: 1,
		CloudWorkerProfile: types.CloudWorkerPackedShared,
		ModelCreditUnits:   creditInviteGiftUnits,
	}); err != nil {
		t.Fatalf("create credit invite %s: %v", code, err)
	}
}

// readInviteEntitlement returns the window the redemption wrote for the model
// permission, plus its id.
func readInviteEntitlement(t *testing.T, db *Adapter, uid int64) (int64, time.Time, sql.NullTime) {
	t.Helper()
	var id int64
	var startsAt time.Time
	var expiresAt sql.NullTime
	if err := db.db.QueryRow(`
		SELECT id, starts_at, expires_at FROM commercial_entitlements
		WHERE uid = $1 AND source = 'invite' AND state = 'active'
		ORDER BY id DESC LIMIT 1`, uid).Scan(&id, &startsAt, &expiresAt); err != nil {
		t.Fatalf("read invite entitlement: %v", err)
	}
	return id, startsAt.UTC(), expiresAt
}

// TestPostgresCreditInviteKeepsMachineWindow pins what a real redemption of a
// credit invite (V1: 4,000 gift credit, no monthly allowance, one
// packed_shared/light/month credit) does to the machine.
//
// The invite's permission is deliberately permanent - the gift is the
// allowance, not a timed package - so the entitlement carries no expires_at.
// The machine it funds is not permanent: the credit it hands out ends with the
// invite's own package window. cloudWorkerMachineTermQuery read e.expires_at
// alone, so a NULL there was answered as "no deadline at all": the commercial
// summary reported a machine that never expires and the redemption's
// settlement wrote package_expires_at = NULL, which no sweeper ever collects.
func TestPostgresCreditInviteKeepsMachineWindow(t *testing.T) {
	db := commercialPolicyTestDB(t)

	t.Run("the redeemed machine ends with the invite's package window", func(t *testing.T) {
		planID := seedCreditInvitePlan(t, db, "credit-invite-v1-window", 30, 3, 1)
		seedCreditInviteCode(t, db, "CREDIT-INVITE-V1-WINDOW", planID)

		owner := createGLM53MigrationUser(t, db, "credit-invite-window")
		// A follow_plan worker that already exists: the redemption's settlement
		// is what re-terms it.
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-credit-invite-window",
			types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")

		summary, err := db.RedeemCommercialInvite(owner, "CREDIT-INVITE-V1-WINDOW")
		if err != nil {
			t.Fatalf("redeem credit invite: %v", err)
		}
		entitlementID, startedAt, entitlementExpires := readInviteEntitlement(t, db, owner)
		if entitlementExpires.Valid {
			t.Fatalf("the credit invite's model permission must be permanent, got expiry %s", entitlementExpires.Time)
		}
		wantEnd := pgMicro(startedAt).AddDate(0, 0, 30)

		// The machine credit the redemption created carries that same window.
		var profile, billing, expiryMode string
		var creditExpires sql.NullTime
		var retainData bool
		if err := db.db.QueryRow(`
			SELECT deployment_profile, billing_mode, expires_at, expiry_mode, retain_data
			FROM cloud_worker_credits WHERE entitlement_id = $1`, entitlementID).Scan(
			&profile, &billing, &creditExpires, &expiryMode, &retainData); err != nil {
			t.Fatalf("read invite machine credit: %v", err)
		}
		if profile != types.CloudWorkerPackedShared || billing != types.CloudWorkerMonthly {
			t.Fatalf("machine credit = %s/%s, want packed_shared/month", profile, billing)
		}
		if expiryMode != types.CloudWorkerExpiryFollowPlan || !retainData {
			t.Fatalf("machine credit must follow the package and keep its data: mode=%q retain=%t", expiryMode, retainData)
		}
		if !creditExpires.Valid || !creditExpires.Time.UTC().Equal(wantEnd) {
			t.Fatalf("machine credit expires at %v, want the invite window %s", creditExpires, wantEnd)
		}

		// The gift is the invite's own 4,000 credit and nothing is monthly.
		account, err := db.GetModelCreditAccount(context.Background(), owner)
		if err != nil {
			t.Fatalf("read model credit account: %v", err)
		}
		if account.GiftUnits != creditInviteGiftUnits {
			t.Fatalf("gift credit = %d units, want %d", account.GiftUnits, creditInviteGiftUnits)
		}
		if account.MonthlyUnits != 0 || account.PaidUnits != 0 {
			t.Fatalf("a credit invite must not grant monthly/paid credit, got monthly=%d paid=%d",
				account.MonthlyUnits, account.PaidUnits)
		}
		enabled, tier, allowance, err := db.GetModelCreditEligibility(context.Background(), owner)
		if err != nil {
			t.Fatalf("read eligibility: %v", err)
		}
		if !enabled || tier != 1 || allowance != creditInviteGiftUnits {
			t.Fatalf("eligibility = (enabled=%t tier=%d allowance=%d), want the permanent invite permission",
				enabled, tier, allowance)
		}
		// The settlement the redemption ran must leave the worker on the invite
		// window: a NULL there is a machine that never expires and nothing ever
		// collects.
		after := lifecycleFor(t, db, "bot-credit-invite-window")
		if after.ID != workerID {
			t.Fatalf("unexpected lifecycle row: %+v", after)
		}
		if after.PackageExpiresAt.IsZero() {
			t.Fatalf("the credit invite left the worker without a deadline: %+v", after)
		}
		if !after.PackageExpiresAt.Equal(wantEnd) {
			t.Fatalf("worker ends %s, want the invite window %s", after.PackageExpiresAt, wantEnd)
		}
		if !after.SyncPending {
			t.Fatalf("the settled term must reach the provider: %+v", after)
		}

		// The machine package the summary and the operator preview read must be
		// the invite's finite window, not a permanent grant.
		term := summary.CloudWorkerPackageTerm
		if term == nil {
			t.Fatal("the redemption's summary dropped the machine package")
		}
		if term.ExpiresAt == nil {
			t.Fatalf("the invite's machine was reported as permanent: %+v", term)
		}
		if !pgMicro(*term.ExpiresAt).Equal(wantEnd) {
			t.Fatalf("summary machine window = %s, want the invite window %s", *term.ExpiresAt, wantEnd)
		}
		direct, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatalf("read machine package term: %v", err)
		}
		if direct == nil || direct.ExpiresAt == nil || !pgMicro(*direct.ExpiresAt).Equal(wantEnd) {
			t.Fatalf("machine package term = %+v, want the invite window %s", direct, wantEnd)
		}
	})

	t.Run("consuming and removing the credit does not move the window", func(t *testing.T) {
		planID := seedCreditInvitePlan(t, db, "credit-invite-v1-consumed", 30, 3, 1)
		seedCreditInviteCode(t, db, "CREDIT-INVITE-V1-CONSUMED", planID)

		owner := createGLM53MigrationUser(t, db, "credit-invite-consumed")
		if _, err := db.RedeemCommercialInvite(owner, "CREDIT-INVITE-V1-CONSUMED"); err != nil {
			t.Fatalf("redeem credit invite: %v", err)
		}
		entitlementID, startedAt, _ := readInviteEntitlement(t, db, owner)
		wantEnd := pgMicro(startedAt).AddDate(0, 0, 30)

		// Provisioning marks the credit consumed; nothing may then read the
		// machine as permanent because the credit is no longer "available".
		if _, err := db.db.Exec(`UPDATE cloud_worker_credits SET state = 'consumed', consumed_at = CURRENT_TIMESTAMP
			WHERE entitlement_id = $1`, entitlementID); err != nil {
			t.Fatalf("consume machine credit: %v", err)
		}
		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatalf("read machine package term after consumption: %v", err)
		}
		if term == nil || term.ExpiresAt == nil || !pgMicro(*term.ExpiresAt).Equal(wantEnd) {
			t.Fatalf("machine package term after consumption = %+v, want the invite window %s", term, wantEnd)
		}

		if _, err := db.db.Exec(`DELETE FROM cloud_worker_credits WHERE entitlement_id = $1`, entitlementID); err != nil {
			t.Fatalf("remove machine credit: %v", err)
		}
		term, err = db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatalf("read machine package term after removal: %v", err)
		}
		if term == nil || term.ExpiresAt == nil || !pgMicro(*term.ExpiresAt).Equal(wantEnd) {
			t.Fatalf("machine package term after removal = %+v, want the invite window %s", term, wantEnd)
		}
	})

	t.Run("a perpetual credit invite keeps a permanent machine", func(t *testing.T) {
		planID := seedCreditInvitePlan(t, db, "credit-invite-v1-perpetual", -1, 7, 1)
		seedCreditInviteCode(t, db, "CREDIT-INVITE-V1-PERPETUAL", planID)

		owner := createGLM53MigrationUser(t, db, "credit-invite-perpetual")
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-credit-invite-perpetual",
			types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		if _, err := db.RedeemCommercialInvite(owner, "CREDIT-INVITE-V1-PERPETUAL"); err != nil {
			t.Fatalf("redeem perpetual credit invite: %v", err)
		}
		if _, _, entitlementExpires := readInviteEntitlement(t, db, owner); entitlementExpires.Valid {
			t.Fatalf("fixture invalid: the perpetual invite expires at %s", entitlementExpires.Time)
		}
		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatalf("read machine package term: %v", err)
		}
		if term == nil || term.ExpiresAt != nil {
			t.Fatalf("a perpetual credit invite must not invent a deadline: %+v", term)
		}
		after := lifecycleFor(t, db, "bot-credit-invite-perpetual")
		if after.ID != workerID || !after.PackageExpiresAt.IsZero() {
			t.Fatalf("a perpetual credit invite must keep the worker open: %+v", after)
		}
	})

	t.Run("a longer paid machine package is not shortened", func(t *testing.T) {
		paidPlan := seedTieredPlan(t, db, "credit-invite-paid-machine", 365, 7, 2, true)
		invitePlan := seedCreditInvitePlan(t, db, "credit-invite-v1-shorter", 30, 3, 1)
		seedCreditInviteCode(t, db, "CREDIT-INVITE-V1-SHORTER", invitePlan)

		owner := createGLM53MigrationUser(t, db, "credit-invite-paid")
		order := buyMachinePackage(t, db, owner, paidPlan, "CREDITINVITEPAID")
		paidEnd := entitlementExpiry(t, db, owner, order.OrderNo)

		if _, err := db.RedeemCommercialInvite(owner, "CREDIT-INVITE-V1-SHORTER"); err != nil {
			t.Fatalf("redeem credit invite: %v", err)
		}
		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatalf("read machine package term: %v", err)
		}
		if term == nil || term.ExpiresAt == nil || !pgMicro(*term.ExpiresAt).Equal(pgMicro(paidEnd)) {
			t.Fatalf("machine package term = %+v, want the paid window %s", term, paidEnd)
		}
	})
}

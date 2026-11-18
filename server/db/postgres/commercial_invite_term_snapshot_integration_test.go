package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// inviteTermUnits is what a public credit package includes for one 30-day term
// (3,000 credit), in integer micro-credit units.
const inviteTermUnits int64 = 3_000 * store.ModelCreditUnitsPerCredit

// seedInviteTermPlan seeds the shape that qualifies for a term snapshot: a
// public, finite package that sells model credit. 30 days is the only duration
// a credit package may be sold with (CreateCommercialPlan enforces it), so the
// term window and the worker-derived window coincide.
func seedInviteTermPlan(t *testing.T, db *Adapter, slug string, units int64, tier int) int64 {
	t.Helper()
	id, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:                         slug,
		Name:                         slug,
		PriceFen:                     3900,
		Currency:                     "CNY",
		SaleState:                    "public",
		DurationDays:                 30,
		PlanTier:                     tier,
		ModelCreditUnits:             units,
		CloudWorkerIncluded:          true,
		CloudWorkerBillingMode:       types.CloudWorkerMonthly,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
		CloudWorkerRetentionDays:     3,
	})
	if err != nil {
		t.Fatalf("seed invite term plan %s: %v", slug, err)
	}
	return id
}

// seedUnqualifiedInvitePlan seeds the packages an invite snapshot must not cover:
// a hidden package, a perpetual package (duration -1 is only legal while hidden
// and free), or one that sells no model credit at all.
func seedUnqualifiedInvitePlan(t *testing.T, db *Adapter, slug, saleState string, durationDays int, units int64) int64 {
	t.Helper()
	plan := &types.CommercialPlan{
		Slug:                         slug,
		Name:                         slug,
		PriceFen:                     3900,
		Currency:                     "CNY",
		SaleState:                    saleState,
		DurationDays:                 durationDays,
		PlanTier:                     1,
		ModelCreditUnits:             units,
		CloudWorkerIncluded:          true,
		CloudWorkerBillingMode:       types.CloudWorkerMonthly,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
		CloudWorkerRetentionDays:     3,
	}
	if durationDays == -1 {
		plan.PriceFen = 0
	}
	id, err := db.CreateCommercialPlan(plan)
	if err != nil {
		t.Fatalf("seed unqualified invite plan %s: %v", slug, err)
	}
	return id
}

// mintInvite mints an invite the way an operator does: one machine credit for the
// bound package, with the gift either inherited from the package (the 20-credit
// default) or stated by the operator (including an explicit 0).
func mintInvite(t *testing.T, db *Adapter, code string, planID, gift int64, inheritGift bool) {
	t.Helper()
	if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
		Code:                    code,
		PlanID:                  planID,
		MaxRedemptions:          1,
		CloudWorkerCredits:      1,
		CloudWorkerProfile:      types.CloudWorkerPackedShared,
		ModelCreditUnits:        gift,
		ModelCreditUnitsInherit: inheritGift,
	}); err != nil {
		t.Fatalf("mint invite %s: %v", code, err)
	}
}

// readInviteSnapshotEntitlement returns the redemption's entitlement row: its id,
// its window, and the term allowance snapshot written for it.
func readInviteSnapshotEntitlement(t *testing.T, db *Adapter, uid int64) (int64, time.Time, sql.NullTime, sql.NullInt64) {
	t.Helper()
	var id int64
	var startsAt time.Time
	var expiresAt sql.NullTime
	var snapshot sql.NullInt64
	if err := db.db.QueryRow(`
		SELECT id, starts_at, expires_at, invite_monthly_units
		FROM commercial_entitlements
		WHERE uid = $1 AND source = 'invite' AND state = 'active'
		ORDER BY id DESC LIMIT 1`, uid).Scan(&id, &startsAt, &expiresAt, &snapshot); err != nil {
		t.Fatalf("read invite entitlement snapshot: %v", err)
	}
	return id, startsAt.UTC(), expiresAt, snapshot
}

// creditBuckets reads the buckets straight from the account row so a test can
// assert "no credit at all" without tripping over the not-found error a missing
// account raises.
func creditBuckets(t *testing.T, db *Adapter, uid int64) (monthly, gift, paid int64) {
	t.Helper()
	err := db.db.QueryRow(`
		SELECT monthly_units, gift_units, paid_units
		FROM model_credit_accounts WHERE uid = $1`, uid).Scan(&monthly, &gift, &paid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0
	}
	if err != nil {
		t.Fatalf("read model credit buckets: %v", err)
	}
	return monthly, gift, paid
}

// monthlyGrantTotals counts the monthly grants booked for uid and the units they
// handed out. "Exactly one issuance per period" is what the ledger has to show,
// whichever entry point booked it.
func monthlyGrantTotals(t *testing.T, db *Adapter, uid int64) (int, int64) {
	t.Helper()
	var count int
	var units int64
	if err := db.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(units), 0)
		FROM model_credit_ledger_events
		WHERE uid = $1 AND bucket = 'monthly' AND event_type = 'grant'`, uid).Scan(&count, &units); err != nil {
		t.Fatalf("read monthly credit grants: %v", err)
	}
	return count, units
}

// inviteCreditRetainData reports whether the machine credit the redemption minted
// kept the "this environment is not deleted" marker.
func inviteCreditRetainData(t *testing.T, db *Adapter, entitlementID int64) (bool, bool) {
	t.Helper()
	var retainData bool
	err := db.db.QueryRow(`
		SELECT retain_data FROM cloud_worker_credits WHERE entitlement_id = $1`, entitlementID).Scan(&retainData)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false
	}
	if err != nil {
		t.Fatalf("read invite machine credit: %v", err)
	}
	return retainData, true
}

// TestPostgresInviteTermSnapshot pins the redemption half of the credit rollover
// rules: a new redemption of a finite, public, enabled package that sells model
// credit delivers that term's included points, in its own snapshot, separately
// from the operator's one-time gift — and it retains data even when the gift is 0.
//
// The gift must never be able to stand in for the term allowance (or the other
// way round): the same package is redeemed with the default 20-credit gift, with
// an operator-chosen gift, and with an explicit 0, and the monthly bucket always
// gets exactly the package's units, once.
func TestPostgresInviteTermSnapshot(t *testing.T) {
	db := commercialPolicyTestDB(t)

	cases := []struct {
		name     string
		gift     int64
		inherit  bool
		wantGift int64
	}{
		{"default gift", 0, true, types.CommercialInviteDefaultCreditUnits},
		{"configured gift", 5 * store.ModelCreditUnitsPerCredit, false, 5 * store.ModelCreditUnitsPerCredit},
		{"zero gift", 0, false, 0},
	}
	for index, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			slug := fmt.Sprintf("invite-snapshot-%d", index)
			code := fmt.Sprintf("INVITE-SNAPSHOT-%d", index)
			planID := seedInviteTermPlan(t, db, slug, inviteTermUnits, 1)
			mintInvite(t, db, code, planID, tc.gift, tc.inherit)

			owner := createGLM53MigrationUser(t, db, slug)
			if _, err := db.RedeemCommercialInvite(owner, code); err != nil {
				t.Fatalf("redeem invite: %v", err)
			}

			// The snapshot is the package's term allowance, stored on the
			// entitlement (not on the invite code, which the operator can edit).
			entitlementID, startsAt, expiresAt, snapshot := readInviteSnapshotEntitlement(t, db, owner)
			if !snapshot.Valid || snapshot.Int64 != inviteTermUnits {
				t.Fatalf("invite_monthly_units = %+v, want the package's %d units", snapshot, inviteTermUnits)
			}
			// A finite snapshot entitlement ends with the term the worker would
			// get: the same duration-derived window, not a permanent grant.
			wantEnd := pgMicro(startsAt).AddDate(0, 0, 30)
			if !expiresAt.Valid || !pgMicro(expiresAt.Time).Equal(wantEnd) {
				t.Fatalf("snapshot entitlement expires at %+v, want the term window %s", expiresAt, wantEnd)
			}

			// The gift is its own amount and is delivered independently.
			monthly, gift, paid := creditBuckets(t, db, owner)
			if gift != tc.wantGift {
				t.Fatalf("gift bucket = %d units, want %d", gift, tc.wantGift)
			}
			if monthly != inviteTermUnits {
				t.Fatalf("monthly bucket = %d units, want the package's %d", monthly, inviteTermUnits)
			}
			if paid != 0 {
				t.Fatalf("an invite must not mint paid credit, got %d units", paid)
			}
			count, units := monthlyGrantTotals(t, db, owner)
			if count != 1 || units != inviteTermUnits {
				t.Fatalf("monthly issuance = %d grants / %d units, want exactly one grant of %d", count, units, inviteTermUnits)
			}

			// The environment the redemption opens is retained: a gift of 0 does
			// not turn a credit invite back into a clean-up-on-expiry package.
			retainData, found := inviteCreditRetainData(t, db, entitlementID)
			if !found {
				t.Fatal("the redemption minted no machine credit")
			}
			if !retainData {
				t.Fatal("a credit invite must keep its environment even when the gift is 0")
			}

			// Recommitting the same period never issues a second allowance.
			if _, err := db.ReconcileModelCreditMonthlyPeriod(context.Background(), owner, time.Now().UTC()); err != nil {
				t.Fatalf("reconcile monthly period: %v", err)
			}
			if count, units := monthlyGrantTotals(t, db, owner); count != 1 || units != inviteTermUnits {
				t.Fatalf("re-reconcile issued again: %d grants / %d units, want one grant of %d", count, units, inviteTermUnits)
			}
			if monthly, _, _ := creditBuckets(t, db, owner); monthly != inviteTermUnits {
				t.Fatalf("re-reconcile moved the monthly bucket to %d units, want %d", monthly, inviteTermUnits)
			}
		})
	}
}

// TestPostgresInviteSnapshotSkipsPaidUser keeps the existing activation safeguard
// in front of the new snapshot: a user who already paid owns their package, so a
// redemption must neither replace it nor book a second allowance for a period that
// was already granted.
func TestPostgresInviteSnapshotSkipsPaidUser(t *testing.T) {
	db := commercialPolicyTestDB(t)
	paidUnits := 5_000 * store.ModelCreditUnitsPerCredit

	paidPlanID := seedInviteTermPlan(t, db, "invite-snapshot-paid", paidUnits, 2)
	invitePlanID := seedInviteTermPlan(t, db, "invite-snapshot-paid-invite", inviteTermUnits, 1)
	const giftUnits int64 = 3 * store.ModelCreditUnitsPerCredit
	mintInvite(t, db, "INVITE-SNAPSHOT-PAID", invitePlanID, giftUnits, false)

	owner := createGLM53MigrationUser(t, db, "invite-snapshot-paid")
	order := buyMachinePackage(t, db, owner, paidPlanID, "INVITESNAPSHOTPAID")
	// Make the paid term end first regardless of the length of this month.
	var paidEnd time.Time
	if err := db.db.QueryRow(`UPDATE commercial_entitlements SET expires_at = starts_at + INTERVAL '1 day'
		WHERE uid = $1 AND source = 'order' AND source_ref = $2 RETURNING expires_at`, owner, order.OrderNo).Scan(&paidEnd); err != nil {
		t.Fatalf("set paid term end: %v", err)
	}
	if count, units := monthlyGrantTotals(t, db, owner); count != 1 || units != paidUnits {
		t.Fatalf("the paid purchase booked %d monthly grants of %d units, want one of %d", count, units, paidUnits)
	}

	if _, err := db.RedeemCommercialInvite(owner, "INVITE-SNAPSHOT-PAID"); err != nil {
		t.Fatalf("redeem invite on a paid account: %v", err)
	}

	// The paid package is not revoked and not replaced.
	var activePaid int
	if err := db.db.QueryRow(`
		SELECT COUNT(*) FROM commercial_entitlements
		WHERE uid = $1 AND source = 'order' AND state = 'active'`, owner).Scan(&activePaid); err != nil {
		t.Fatalf("count active paid entitlements: %v", err)
	}
	if activePaid != 1 {
		t.Fatalf("active paid entitlements = %d, want the purchased package untouched", activePaid)
	}
	id, startsAt, expiresAt, snapshot := readInviteSnapshotEntitlement(t, db, owner)
	if snapshot.Valid {
		t.Fatalf("paid user got a deferred invite allowance: %d units", snapshot.Int64)
	}
	if !expiresAt.Valid || !expiresAt.Time.Equal(startsAt.AddDate(0, 0, 30)) || !paidEnd.After(startsAt) || !paidEnd.Before(expiresAt.Time) {
		t.Fatalf("want a 30-day invite spanning paid expiry %s, got [%s,%+v)", paidEnd, startsAt, expiresAt)
	}
	if retain, found := inviteCreditRetainData(t, db, id); !found || !retain {
		t.Fatalf("invite machine credit: found=%v retain=%v, want both true", found, retain)
	}
	// No second allowance was booked for the period the paid package already covers.
	if count, units := monthlyGrantTotals(t, db, owner); count != 1 || units != paidUnits {
		t.Fatalf("the redemption booked %d monthly grants of %d units, want the paid grant of %d only", count, units, paidUnits)
	}
	monthly, gift, _ := creditBuckets(t, db, owner)
	if monthly != paidUnits {
		t.Fatalf("monthly bucket = %d units, want the paid %d untouched", monthly, paidUnits)
	}
	if gift != giftUnits {
		t.Fatalf("gift bucket = %d units, want the invite's own %d", gift, giftUnits)
	}
	if _, err := db.ReconcileModelCreditMonthlyPeriod(context.Background(), owner, paidEnd); err != nil {
		t.Fatalf("reconcile at paid expiry while invite remains active: %v", err)
	}
	if count, units := monthlyGrantTotals(t, db, owner); count != 1 || units != paidUnits {
		t.Fatalf("paid expiry unlocked invite points: got %d grants / %d units, want one grant of %d", count, units, paidUnits)
	}
}

func TestPostgresInviteSnapshotStackedEarlyRenewals(t *testing.T) {
	db := commercialPolicyTestDB(t)
	planID := seedInviteTermPlan(t, db, "invite-snapshot-renew", inviteTermUnits, 1)
	mintInvite(t, db, "INVITE-SNAPSHOT-RENEW", planID, 0, false)
	owner := createGLM53MigrationUser(t, db, "invite-snapshot-renew")
	if _, err := db.RedeemCommercialInvite(owner, "INVITE-SNAPSHOT-RENEW"); err != nil {
		t.Fatalf("redeem invite: %v", err)
	}
	_, _, expiry, _ := readInviteSnapshotEntitlement(t, db, owner)
	if !expiry.Valid {
		t.Fatal("expected finite invite term")
	}
	// Both payments arrive during the invite. Only the first directly follows
	// it; the second must inherit the first paid term's natural-month anchor.
	wantAnchor := expiry.Time.UTC()
	wantStart := wantAnchor
	for _, orderNo := range []string{"INVITESNAPRENEW1", "INVITESNAPRENEW2"} {
		buyMachinePackage(t, db, owner, planID, orderNo)
		var start, end, anchor time.Time
		if err := db.db.QueryRow(`SELECT starts_at, expires_at, period_anchor_at FROM commercial_entitlements
			WHERE uid = $1 AND source = 'order' AND source_ref = $2`, owner, orderNo).Scan(&start, &end, &anchor); err != nil {
			t.Fatalf("read renewal %s: %v", orderNo, err)
		}
		wantEnd, ok := store.ModelCreditMonthlyNextBoundary(wantAnchor, wantStart)
		if !ok || !start.Equal(wantStart) || !end.Equal(wantEnd) || !anchor.Equal(wantAnchor) {
			t.Fatalf("%s: got [%s,%s) anchor %s, want [%s,%s) anchor %s", orderNo, start, end, anchor, wantStart, wantEnd, wantAnchor)
		}
		wantStart = wantEnd
	}
	if count, units := monthlyGrantTotals(t, db, owner); count != 1 || units != inviteTermUnits {
		t.Fatalf("early payments booked %d grants / %d units, want only the invite grant of %d", count, units, inviteTermUnits)
	}
}

// TestPostgresInviteSnapshotUnqualifiedPathsUnchanged pins the other half of the
// contract: no automatic retroactive grants. Hidden/test packages, perpetual
// packages and packages that sell no model credit keep the historical invite
// behaviour — no snapshot, no monthly allowance, no ledger row at all.
func TestPostgresInviteSnapshotUnqualifiedPathsUnchanged(t *testing.T) {
	db := commercialPolicyTestDB(t)

	t.Run("a hidden package is not on sale, so it snapshots nothing", func(t *testing.T) {
		const giftUnits int64 = 7 * store.ModelCreditUnitsPerCredit
		planID := seedUnqualifiedInvitePlan(t, db, "invite-snapshot-hidden", "hidden", 30, inviteTermUnits)
		mintInvite(t, db, "INVITE-SNAPSHOT-HIDDEN", planID, giftUnits, false)

		owner := createGLM53MigrationUser(t, db, "invite-snapshot-hidden")
		if _, err := db.RedeemCommercialInvite(owner, "INVITE-SNAPSHOT-HIDDEN"); err != nil {
			t.Fatalf("redeem hidden invite: %v", err)
		}
		_, _, expiresAt, snapshot := readInviteSnapshotEntitlement(t, db, owner)
		if snapshot.Valid {
			t.Fatalf("a hidden package must not snapshot a term allowance, got %d", snapshot.Int64)
		}
		if expiresAt.Valid {
			t.Fatalf("the gift-only invite must keep its permanent permission, got expiry %s", expiresAt.Time)
		}
		monthly, gift, _ := creditBuckets(t, db, owner)
		if gift != giftUnits {
			t.Fatalf("gift bucket = %d units, want %d", gift, giftUnits)
		}
		if monthly != 0 {
			t.Fatalf("a hidden package must not grant monthly credit, got %d units", monthly)
		}
		if count, units := monthlyGrantTotals(t, db, owner); count != 0 || units != 0 {
			t.Fatalf("a hidden package booked %d monthly grants of %d units, want none", count, units)
		}
	})

	t.Run("a perpetual package stays a permanent permission", func(t *testing.T) {
		planID := seedUnqualifiedInvitePlan(t, db, "invite-snapshot-perpetual", "hidden", -1, inviteTermUnits)
		mintInvite(t, db, "INVITE-SNAPSHOT-PERPETUAL", planID, 0, false)

		owner := createGLM53MigrationUser(t, db, "invite-snapshot-perpetual")
		if _, err := db.RedeemCommercialInvite(owner, "INVITE-SNAPSHOT-PERPETUAL"); err != nil {
			t.Fatalf("redeem perpetual invite: %v", err)
		}
		entitlementID, _, expiresAt, snapshot := readInviteSnapshotEntitlement(t, db, owner)
		if snapshot.Valid {
			t.Fatalf("a perpetual package must not snapshot a term allowance, got %d", snapshot.Int64)
		}
		if expiresAt.Valid {
			t.Fatalf("a perpetual invite must stay permanent, got expiry %s", expiresAt.Time)
		}
		if monthly, gift, paid := creditBuckets(t, db, owner); monthly != 0 || gift != 0 || paid != 0 {
			t.Fatalf("a credit-less perpetual invite touched the ledger: monthly=%d gift=%d paid=%d", monthly, gift, paid)
		}
		if count, _ := monthlyGrantTotals(t, db, owner); count != 0 {
			t.Fatalf("a perpetual invite booked %d monthly grants, want none", count)
		}
		// A legacy invite keeps the historical cleanup schedule.
		if retainData, found := inviteCreditRetainData(t, db, entitlementID); found && retainData {
			t.Fatal("a credit-less invite must keep the historical retention schedule")
		}
	})

	t.Run("a public package that sells no model credit grants none", func(t *testing.T) {
		planID := seedUnqualifiedInvitePlan(t, db, "invite-snapshot-no-units", "public", 30, 0)
		mintInvite(t, db, "INVITE-SNAPSHOT-NO-UNITS", planID, 0, false)

		owner := createGLM53MigrationUser(t, db, "invite-snapshot-no-units")
		if _, err := db.RedeemCommercialInvite(owner, "INVITE-SNAPSHOT-NO-UNITS"); err != nil {
			t.Fatalf("redeem token-only invite: %v", err)
		}
		_, startsAt, expiresAt, snapshot := readInviteSnapshotEntitlement(t, db, owner)
		if snapshot.Valid {
			t.Fatalf("a package that sells no credit must not snapshot an allowance, got %d", snapshot.Int64)
		}
		// The old path keeps the worker-derived window for a credit-less invite.
		if !expiresAt.Valid || !pgMicro(expiresAt.Time).Equal(pgMicro(startsAt).AddDate(0, 0, 30)) {
			t.Fatalf("token-only invite expiry = %+v, want the 30-day worker window", expiresAt)
		}
		if monthly, gift, paid := creditBuckets(t, db, owner); monthly != 0 || gift != 0 || paid != 0 {
			t.Fatalf("a token-only invite touched the ledger: monthly=%d gift=%d paid=%d", monthly, gift, paid)
		}
		if count, _ := monthlyGrantTotals(t, db, owner); count != 0 {
			t.Fatalf("a token-only invite booked %d monthly grants, want none", count)
		}
	})
}

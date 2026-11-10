package postgres

import (
	"database/sql"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// seedTieredPlan seeds a package that sits on the upgrade ladder (plan_tier >
// 0), which is what makes an early renewal / an invite redemption start its new
// period at the old expiry instead of at payment time. durationDays -1 is a
// perpetual package (hidden, zero-priced, as chk_commercial_plans_duration
// requires).
func seedTieredPlan(t *testing.T, db *Adapter, slug string, durationDays, retentionDays, tier int, included bool) int64 {
	t.Helper()
	saleState := "test"
	var price int64 = 990
	if durationDays == -1 {
		saleState = "hidden"
		price = 0
	}
	var id int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_plans(
			slug, name, price_fen, currency, sale_state, purchase_limit,
			monthly_budget_cny, model_budgets, duration_days, state, sort_order,
			cloud_worker_included, cloud_worker_deployment_profile, cloud_worker_billing_mode,
			cloud_worker_retention_days, plan_tier
		) VALUES ($1, $1, $2, 'CNY', $3, 0, 0, '{"MiniMax-M3":10}'::jsonb, $4, 0, 100,
		          $5, 'packed_shared', 'month', $6, $7)
		RETURNING id`,
		slug, price, saleState, durationDays, included, retentionDays, tier).Scan(&id); err != nil {
		t.Fatalf("seed tiered plan %s: %v", slug, err)
	}
	return id
}

// readOrderEntitlementWindow returns the window one fulfilled order actually
// granted, so a test can assert on the segment the fulfilment path wrote rather
// than on a value it computed itself.
func readOrderEntitlementWindow(t *testing.T, db *Adapter, uid int64, orderNo string) (time.Time, sql.NullTime) {
	t.Helper()
	var startsAt time.Time
	var expiresAt sql.NullTime
	if err := db.db.QueryRow(`
		SELECT starts_at, expires_at FROM commercial_entitlements
		WHERE uid = $1 AND source = 'order' AND source_ref = $2 AND state = 'active'`,
		uid, orderNo).Scan(&startsAt, &expiresAt); err != nil {
		t.Fatalf("read entitlement window for %s: %v", orderNo, err)
	}
	return startsAt.UTC(), expiresAt
}

// lifecycleFor reads the lifecycle row a shared worker owns, so a test asserts
// on what the entry point committed rather than on an intermediate value.
func lifecycleFor(t *testing.T, db *Adapter, username string) *types.CloudWorkerLifecycle {
	t.Helper()
	item, err := db.GetCloudWorkerBillingLifecycle(username)
	if err != nil {
		t.Fatalf("read cloud worker lifecycle %s: %v", username, err)
	}
	return item
}

// TestPostgresCloudWorkerMachineTermChain pins the merged coverage the single
// machine-package read reports.
//
// The read used to answer "the package in force right now, and nothing else",
// so an early renewal - whose new segment starts at the old expiry, i.e. in the
// future - was invisible: the settlement that follows a payment re-termed the
// worker to the *previous* segment and threw away the time the buyer had just
// paid for. The coverage is now the chain of already-bought windows that starts
// at the window in force today, and it stops at the first gap: a window bought
// for next month must not authorize a machine today.
func TestPostgresCloudWorkerMachineTermChain(t *testing.T) {
	db := commercialPolicyTestDB(t)
	now := time.Now().UTC()
	machinePlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "chain-read-machine", Included: true, DurationDays: 30, RetentionDays: 7})
	perpetualPlan := seedPerpetualMachinePlanWithRetention(t, db, "chain-read-perpetual", 7)

	window := func(days int) *time.Time {
		value := pgMicro(now.Add(time.Duration(days) * 24 * time.Hour))
		return &value
	}

	t.Run("consecutive segments merge into one coverage", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-read-contiguous")
		first := seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-READ-1", "active", now.Add(-time.Hour), window(30))
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-READ-2", "active", pgMicro(*window(30)), window(60))
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-READ-3", "active", pgMicro(*window(60)), window(90))

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil {
			t.Fatal("the account holds a machine package")
		}
		if term.EntitlementID != first || term.PlanID != machinePlan || term.RetentionDays != 7 {
			t.Fatalf("the primary machine package selection changed: %+v", term)
		}
		if term.ExpiresAt == nil || !term.ExpiresAt.Equal(*window(90)) {
			t.Fatalf("coverage ends at %v, want the merged chain end %s", term.ExpiresAt, *window(90))
		}
	})

	t.Run("an overlapping segment still merges", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-read-overlap")
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-OVERLAP-1", "active", now.Add(-time.Hour), window(30))
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-OVERLAP-2", "active", pgMicro(*window(10)), window(45))

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil || term.ExpiresAt == nil || !term.ExpiresAt.Equal(*window(45)) {
			t.Fatalf("an overlapping segment must extend the coverage: %+v", term)
		}
	})

	t.Run("a future gap is not authorized early", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-read-gap")
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-GAP-1", "active", now.Add(-time.Hour), window(30))
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-GAP-2", "active", pgMicro(*window(45)), window(75))

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil || term.ExpiresAt == nil || !term.ExpiresAt.Equal(*window(30)) {
			t.Fatalf("a window that starts after a gap was granted early: %+v", term)
		}
	})

	t.Run("a contiguous perpetual segment removes the deadline", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-read-perpetual-next")
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-PERP-1", "active", now.Add(-time.Hour), window(30))
		seedMachineEntitlement(t, db, owner, perpetualPlan, "operator", "CHAIN-PERP-2", "active", pgMicro(*window(30)), nil)

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil {
			t.Fatal("the account holds a machine package")
		}
		if term.ExpiresAt != nil {
			t.Fatalf("a perpetual segment joined to the current window must remove the deadline: %+v", term)
		}
	})

	t.Run("a perpetual grant already in force keeps the dated window preferred", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-read-perpetual-now")
		seedMachineEntitlement(t, db, owner, perpetualPlan, "operator", "CHAIN-PERP-NOW", "active", now.Add(-time.Hour), nil)
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-PERP-DATED", "active", now.Add(-time.Minute), window(30))

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil || term.ExpiresAt == nil || !term.ExpiresAt.Equal(*window(30)) {
			t.Fatalf("the dated package must stay the reported term: %+v", term)
		}
	})

	t.Run("revoking the middle segment recomputes the chain", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-read-revoked-middle")
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-REV-1", "active", now.Add(-time.Hour), window(30))
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-REV-2", "revoked", pgMicro(*window(30)), window(60))
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-REV-3", "active", pgMicro(*window(60)), window(90))

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil || term.ExpiresAt == nil || !term.ExpiresAt.Equal(*window(30)) {
			t.Fatalf("a revoked segment must not bridge the gap: %+v", term)
		}
	})

	t.Run("a free baseline still never grants a machine", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-read-baseline")
		seedMachineEntitlement(t, db, owner, machinePlan, "free", "CHAIN-BASELINE", "active", now.Add(-time.Hour), nil)
		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term != nil {
			t.Fatalf("a free baseline was read as a machine package: %+v", term)
		}
	})
}

// TestPostgresCommercialEarlyRenewalKeepsSharedWorkerTerm drives the real
// payment completion path twice on the same ladder tier.
//
// The second purchase is a renewal: it starts at the first period's expiry. The
// settlement that runs inside the same transaction must keep the worker on the
// merged window, otherwise the machine the buyer has just paid another month
// for is cut back to the old segment a moment later.
func TestPostgresCommercialEarlyRenewalKeepsSharedWorkerTerm(t *testing.T) {
	db := commercialPolicyTestDB(t)
	plan := seedTieredPlan(t, db, "chain-renew-plan", 30, 7, 1, true)

	owner := createGLM53MigrationUser(t, db, "chain-renew")
	_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-renew", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")

	first := buyMachinePackage(t, db, owner, plan, "CHAINRENEW1")
	firstEnd := entitlementExpiry(t, db, owner, first.OrderNo)
	afterFirst := lifecycleFor(t, db, "bot-chain-renew")
	if afterFirst.ID != workerID || !afterFirst.PackageExpiresAt.Equal(firstEnd) {
		t.Fatalf("the first purchase should govern the worker: %+v want %s", afterFirst, firstEnd)
	}

	second := buyMachinePackage(t, db, owner, plan, "CHAINRENEW2")
	secondStart, secondExpires := readOrderEntitlementWindow(t, db, owner, second.OrderNo)
	if !secondExpires.Valid || !secondStart.Equal(firstEnd) {
		t.Fatalf("fixture invalid: the renewal segment starts at %s, want the paid window end %s", secondStart, firstEnd)
	}
	secondEnd := secondExpires.Time.UTC()

	afterSecond := lifecycleFor(t, db, "bot-chain-renew")
	if afterSecond.ID != workerID {
		t.Fatalf("unexpected lifecycle row: %+v", afterSecond)
	}
	if !afterSecond.PackageExpiresAt.Equal(secondEnd) {
		t.Fatalf("the early renewal was cut back to the previous segment: worker ends %s, purchased window %s",
			afterSecond.PackageExpiresAt, secondEnd)
	}
	if !afterSecond.DeleteAfter.Equal(secondEnd.AddDate(0, 0, 7)) {
		t.Fatalf("the retention window must follow the merged coverage: %+v", afterSecond)
	}
	if !afterSecond.SyncPending {
		t.Fatalf("the newer term must reach the provider: %+v", afterSecond)
	}

	third := buyMachinePackage(t, db, owner, plan, "CHAINRENEW3")
	_, thirdExpires := readOrderEntitlementWindow(t, db, owner, third.OrderNo)
	if !thirdExpires.Valid {
		t.Fatalf("fixture invalid: the third segment has no deadline")
	}
	thirdEnd := thirdExpires.Time.UTC()
	afterThird := lifecycleFor(t, db, "bot-chain-renew")
	if !afterThird.PackageExpiresAt.Equal(thirdEnd) {
		t.Fatalf("the second renewal was cut back: worker ends %s, purchased window %s", afterThird.PackageExpiresAt, thirdEnd)
	}
}

// TestPostgresCommercialExtendKeepsSharedWorkerTerm drives the operator renewal
// extension entry point, which appends a segment at the current expiry for the
// same reason a renewal does.
func TestPostgresCommercialExtendKeepsSharedWorkerTerm(t *testing.T) {
	db := commercialPolicyTestDB(t)
	plan := seedTieredPlan(t, db, "chain-extend-plan", 30, 7, 1, true)

	owner := createGLM53MigrationUser(t, db, "chain-extend")
	_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-extend", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
	order := buyMachinePackage(t, db, owner, plan, "CHAINEXTEND1")
	boughtEnd := entitlementExpiry(t, db, owner, order.OrderNo)

	result, err := db.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{
		UID:         owner,
		Action:      commercialAdjustmentExtend,
		OperationID: "chain-extend-1",
		Note:        "early renewal extension",
	})
	if err != nil || result == nil || !result.Applied || result.ExpiresAt == nil {
		t.Fatalf("extend: result=%#v err=%v", result, err)
	}
	extendedEnd := pgMicro(*result.ExpiresAt)
	if !extendedEnd.After(pgMicro(boughtEnd)) {
		t.Fatalf("fixture invalid: the extension did not add time (%s vs %s)", extendedEnd, boughtEnd)
	}

	after := lifecycleFor(t, db, "bot-chain-extend")
	if after.ID != workerID {
		t.Fatalf("unexpected lifecycle row: %+v", after)
	}
	if !after.PackageExpiresAt.Equal(extendedEnd) {
		t.Fatalf("the operator extension was cut back: worker ends %s, extended window %s",
			after.PackageExpiresAt, extendedEnd)
	}
	if !after.DeleteAfter.Equal(extendedEnd.AddDate(0, 0, 7)) {
		t.Fatalf("the retention window must follow the extended coverage: %+v", after)
	}
	if !after.SyncPending {
		t.Fatalf("the extended term must reach the provider: %+v", after)
	}
}

// TestPostgresCommercialInviteRedeemSettlesSharedWorkerTerms drives the real
// invite redemption entry point, whose tier activation revokes the previously
// active package.
//
// Nothing settled the workers after that revocation: the extension that follows
// a redemption only ever grows a window, so a worker whose machine package had
// just been replaced kept the revoked (or the replacement's quota-only) term.
func TestPostgresCommercialInviteRedeemSettlesSharedWorkerTerms(t *testing.T) {
	db := commercialPolicyTestDB(t)
	plan365 := seedTieredPlan(t, db, "chain-invite-365", 365, 7, 1, true)
	shortMachine := seedTieredPlan(t, db, "chain-invite-30", 30, 3, 2, true)
	tokenOnly := seedTieredPlan(t, db, "chain-invite-tokens", 30, 15, 2, false)
	perpetualPlan := seedTieredPlan(t, db, "chain-invite-perpetual", -1, 7, 2, true)

	inviteExpiryOf := func(t *testing.T, owner int64) *time.Time {
		t.Helper()
		var expiresAt sql.NullTime
		if err := db.db.QueryRow(`
			SELECT expires_at FROM commercial_entitlements
			WHERE uid = $1 AND source = 'invite' AND state = 'active'`, owner).Scan(&expiresAt); err != nil {
			t.Fatalf("read invite entitlement: %v", err)
		}
		if !expiresAt.Valid {
			return nil
		}
		value := expiresAt.Time.UTC()
		return &value
	}

	t.Run("an invite that grants no machine expires the worker", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-invite-nomachine")
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-invite-nomachine", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		buyMachinePackage(t, db, owner, plan365, "CHAININVNO")
		seedMachineInviteCode(t, db, "CHAIN-INVITE-TOKENS", tokenOnly, 0)

		startedAt := time.Now().UTC()
		if _, err := db.RedeemCommercialInvite(owner, "CHAIN-INVITE-TOKENS"); err != nil {
			t.Fatalf("redeem token-only invite: %v", err)
		}

		after := lifecycleFor(t, db, "bot-chain-invite-nomachine")
		if after.ID != workerID {
			t.Fatalf("unexpected lifecycle row: %+v", after)
		}
		if after.PackageExpiresAt.IsZero() || after.PackageExpiresAt.After(startedAt.Add(2*time.Minute)) {
			t.Fatalf("a token-only invite must not keep the machine alive: %+v", after)
		}
		if !after.SyncPending || after.State != "active" {
			t.Fatalf("an expired worker must still be synced before it is collected: %+v", after)
		}
		if !after.DeleteAfter.Equal(after.PackageExpiresAt.AddDate(0, 0, 7)) {
			t.Fatalf("the retention window must survive: %+v", after)
		}
	})

	t.Run("an invite with a shorter machine window settles the worker on it", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-invite-short")
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-invite-short", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		buyMachinePackage(t, db, owner, plan365, "CHAININVSHORT")
		seedMachineInviteCode(t, db, "CHAIN-INVITE-30", shortMachine, 1)

		if _, err := db.RedeemCommercialInvite(owner, "CHAIN-INVITE-30"); err != nil {
			t.Fatalf("redeem machine invite: %v", err)
		}

		want := inviteExpiryOf(t, owner)
		if want == nil {
			t.Fatalf("fixture invalid: the machine invite granted no deadline")
		}
		after := lifecycleFor(t, db, "bot-chain-invite-short")
		if after.ID != workerID {
			t.Fatalf("unexpected lifecycle row: %+v", after)
		}
		if !after.PackageExpiresAt.Equal(*want) {
			t.Fatalf("the revoked 365-day window survived the redemption: worker ends %s, invite window %s",
				after.PackageExpiresAt, *want)
		}
		if !after.SyncPending {
			t.Fatalf("the settled term must reach the provider: %+v", after)
		}
	})

	t.Run("a perpetual invite opens the worker and leaves fixed and permanent terms alone", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "chain-invite-perpetual")
		ownWindow := pgMicro(time.Now().UTC().Add(40 * 24 * time.Hour))
		_, followID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-invite-follow", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		_, fixedID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-invite-fixed", types.CloudWorkerExpiryFixed, &ownWindow, ptrTime(ownWindow.AddDate(0, 0, 7)), "active", "")
		_, permanentID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-invite-permanent", types.CloudWorkerExpiryPermanent, nil, nil, "active", "")
		buyMachinePackage(t, db, owner, plan365, "CHAININVPERP")
		if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
			Code:               "CHAIN-INVITE-PERPETUAL",
			PlanID:             perpetualPlan,
			MaxRedemptions:     2,
			CloudWorkerCredits: 1,
			CloudWorkerProfile: types.CloudWorkerPackedShared,
		}); err != nil {
			t.Fatalf("create perpetual invite: %v", err)
		}

		if _, err := db.RedeemCommercialInvite(owner, "CHAIN-INVITE-PERPETUAL"); err != nil {
			t.Fatalf("redeem perpetual invite: %v", err)
		}
		if expiry := inviteExpiryOf(t, owner); expiry != nil {
			t.Fatalf("fixture invalid: the perpetual invite granted %s", expiry)
		}

		follow := lifecycleFor(t, db, "bot-chain-invite-follow")
		if follow.ID != followID || !follow.PackageExpiresAt.IsZero() || !follow.DeleteAfter.IsZero() {
			t.Fatalf("the perpetual invite must keep the worker open: %+v", follow)
		}
		if !follow.SyncPending {
			t.Fatalf("the settled term must reach the provider: %+v", follow)
		}
		fixed := lifecycleFor(t, db, "bot-chain-invite-fixed")
		if fixed.ID != fixedID || !fixed.PackageExpiresAt.Equal(ownWindow) || fixed.SyncPending {
			t.Fatalf("a fixed term was moved by an invite redemption: %+v", fixed)
		}
		permanent := lifecycleFor(t, db, "bot-chain-invite-permanent")
		if permanent.ID != permanentID || !permanent.PackageExpiresAt.IsZero() || permanent.SyncPending {
			t.Fatalf("a permanent grant was touched by an invite redemption: %+v", permanent)
		}
	})
}

// TestPostgresCommercialFutureGapDoesNotExtendWorkerTerm keeps the fix honest
// through a real revocation entry point: a refund settles the worker on the
// coverage that actually survives, and a window that starts after a gap is not
// part of it.
func TestPostgresCommercialFutureGapDoesNotExtendWorkerTerm(t *testing.T) {
	db := commercialPolicyTestDB(t)
	machinePlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "chain-gap-machine", Included: true, DurationDays: 30, RetentionDays: 7})
	tokenPlan := seedTieredPlan(t, db, "chain-gap-tokens", 30, 15, 0, false)

	now := time.Now().UTC()
	window := func(days int) *time.Time {
		value := pgMicro(now.Add(time.Duration(days) * 24 * time.Hour))
		return &value
	}
	end30, start45, end75 := window(30), window(45), window(75)

	owner := createGLM53MigrationUser(t, db, "chain-gap")
	_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-chain-gap", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
	seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-GAP-NOW", "active", now.Add(-time.Hour), end30)
	seedMachineEntitlement(t, db, owner, machinePlan, "order", "CHAIN-GAP-LATER", "active", *start45, end75)

	tokenOrder := buyMachinePackage(t, db, owner, tokenPlan, "CHAINGAPTOKENS")
	refundPackage(t, db, tokenOrder)

	after := lifecycleFor(t, db, "bot-chain-gap")
	if after.ID != workerID {
		t.Fatalf("unexpected lifecycle row: %+v", after)
	}
	if !after.PackageExpiresAt.Equal(*end30) {
		t.Fatalf("a window that starts after a gap authorized the machine early: worker ends %s, want %s",
			after.PackageExpiresAt, *end30)
	}
}

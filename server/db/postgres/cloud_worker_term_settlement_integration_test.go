package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// seedSharedMachinePlan creates a package that really grants a machine on the
// shared fleet, so the settlement logic sees a machine entitlement rather than a
// quota-only one. durationDays -1 is a perpetual package.
func seedSharedMachinePlan(t *testing.T, db *Adapter, slug, name string, durationDays int) int64 {
	t.Helper()
	var id int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_plans(
			slug, name, price_fen, currency, sale_state, purchase_limit,
			monthly_budget_cny, model_budgets, duration_days, state, sort_order,
			cloud_worker_included, cloud_worker_deployment_profile, cloud_worker_billing_mode,
			cloud_worker_retention_days
		) VALUES ($1, $2, 990, 'CNY', 'test', 0, 0, '{"MiniMax-M3":10}'::jsonb, $3, 0, 100,
		          TRUE, 'packed_shared', 'month', 7)
		RETURNING id`, slug, name, durationDays).Scan(&id); err != nil {
		t.Fatalf("seed shared machine plan %s: %v", slug, err)
	}
	return id
}

// seedPerpetualMachinePlan creates the one shape that can express a perpetual
// machine package: duration -1 is only allowed for a hidden, zero-priced plan
// (chk_commercial_plans_duration), which is exactly an operator-issued
// open-ended package.
func seedPerpetualMachinePlan(t *testing.T, db *Adapter, slug, name string) int64 {
	t.Helper()
	var id int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_plans(
			slug, name, price_fen, currency, sale_state, purchase_limit,
			monthly_budget_cny, model_budgets, duration_days, state, sort_order,
			cloud_worker_included, cloud_worker_deployment_profile, cloud_worker_billing_mode,
			cloud_worker_retention_days
		) VALUES ($1, $2, 0, 'CNY', 'hidden', 0, 0, '{"MiniMax-M3":10}'::jsonb, -1, 0, 100,
		          TRUE, 'packed_shared', 'month', 7)
		RETURNING id`, slug, name).Scan(&id); err != nil {
		t.Fatalf("seed perpetual machine plan %s: %v", slug, err)
	}
	return id
}

// seedTokenOnlyPlan creates a package that buys tokens but no machine.
func seedTokenOnlyPlan(t *testing.T, db *Adapter, slug, name string, durationDays int) int64 {
	t.Helper()
	var id int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_plans(
			slug, name, price_fen, currency, sale_state, purchase_limit,
			monthly_budget_cny, model_budgets, duration_days, state, sort_order,
			cloud_worker_included
		) VALUES ($1, $2, 490, 'CNY', 'test', 0, 0, '{"MiniMax-M3":5}'::jsonb, $3, 0, 100, FALSE)
		RETURNING id`, slug, name, durationDays).Scan(&id); err != nil {
		t.Fatalf("seed token-only plan %s: %v", slug, err)
	}
	return id
}

// buyMachinePackage runs a purchase through the real order/payment path so the
// fulfilment code (not the test) decides the entitlement and the worker term.
func buyMachinePackage(t *testing.T, db *Adapter, uid, planID int64, orderNo string) *types.CommercialOrder {
	t.Helper()
	suffix := strings.NewReplacer("-", "", ":", "").Replace(orderNo)
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	created, err := db.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         orderNo,
		UID:             uid,
		PlanID:          planID,
		Channel:         "test",
		ClientRequestID: "termreq-" + suffix,
		ExpiresAt:       &expiresAt,
	})
	if err != nil {
		t.Fatalf("create order %s: %v", orderNo, err)
	}
	if _, claimed, err := db.BeginCommercialOrderPayment(orderNo, expiresAt); err != nil || !claimed {
		t.Fatalf("begin payment %s: claimed=%v err=%v", orderNo, claimed, err)
	}
	fulfilled, changed, err := db.FulfillCommercialOrder(orderNo, &types.CommercialPaymentConfirmation{
		Channel:         "test",
		EventID:         "event-" + suffix,
		ProviderTradeNo: "trade-" + suffix,
		AmountFen:       created.AmountFen,
		Currency:        "CNY",
		PaidAt:          time.Now().UTC(),
		PayloadHash:     strings.Repeat("a", 64),
	})
	if err != nil || !changed || fulfilled.Status != "fulfilled" {
		t.Fatalf("fulfil %s: order=%#v changed=%v err=%v", orderNo, fulfilled, changed, err)
	}
	return fulfilled
}

// refundPackage runs the real refund entry point, the one an operator or a
// provider callback would use.
func refundPackage(t *testing.T, db *Adapter, order *types.CommercialOrder) {
	t.Helper()
	requestNo := "RF-" + order.OrderNo
	if _, claimed, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute); err != nil || !claimed {
		t.Fatalf("begin refund %s: claimed=%v err=%v", order.OrderNo, claimed, err)
	}
	refunded, changed, err := db.CompleteCommercialOrderRefund(order.OrderNo, &types.CommercialRefundConfirmation{
		Channel:         order.Channel,
		EventID:         requestNo,
		ProviderTradeNo: order.ProviderTradeNo,
		RefundRequestNo: requestNo,
		AmountFen:       order.AmountFen,
		Currency:        order.Currency,
		RefundedAt:      time.Now().UTC(),
		PayloadHash:     strings.Repeat("b", 64),
	})
	if err != nil || !changed || refunded.Status != "refunded" {
		t.Fatalf("refund %s: order=%#v changed=%v err=%v", order.OrderNo, refunded, changed, err)
	}
}

// entitlementExpiry reads the window the purchase actually granted.
func entitlementExpiry(t *testing.T, db *Adapter, uid int64, orderNo string) time.Time {
	t.Helper()
	var expiresAt time.Time
	if err := db.db.QueryRow(`
		SELECT expires_at FROM commercial_entitlements
		WHERE uid = $1 AND source = 'order' AND source_ref = $2 AND state = 'active'`,
		uid, orderNo).Scan(&expiresAt); err != nil {
		t.Fatalf("read entitlement expiry for %s: %v", orderNo, err)
	}
	return expiresAt.UTC()
}

// TestPostgresCommercialRefundSettlesSharedWorkerTerms drives the real refund
// entry point and pins what a follow_plan worker ends up following afterwards.
//
// The defect this covers: a refund revoked the paid window, but the worker kept
// the later of "its own current date" and "whatever survived", so refunding a
// 365-day order with 30 days left kept the machine for 365 days. The settlement
// now adopts the surviving machine package's own window instead, and a free or
// legacy baseline (perpetual, quota-only) no longer counts as a machine grant.
func TestPostgresCommercialRefundSettlesSharedWorkerTerms(t *testing.T) {
	db := commercialPolicyTestDB(t)
	plan365 := seedSharedMachinePlan(t, db, "term-refund-365", "Term 365", 365)
	plan30 := seedSharedMachinePlan(t, db, "term-refund-30", "Term 30", 30)
	perpetualPlan := seedPerpetualMachinePlan(t, db, "term-refund-perpetual", "Term perpetual")

	t.Run("a refunded 365-day order does not outlive the remaining 30 days", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "refund-365-30")
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-refund-365-30", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")

		order365 := buyMachinePackage(t, db, owner, plan365, "TERMREFUND365")
		longEnd := entitlementExpiry(t, db, owner, order365.OrderNo)
		order30 := buyMachinePackage(t, db, owner, plan30, "TERMREFUND030")
		shortEnd := entitlementExpiry(t, db, owner, order30.OrderNo)
		if !longEnd.After(shortEnd) {
			t.Fatalf("fixture invalid: 365-day window %s is not after the 30-day one %s", longEnd, shortEnd)
		}
		before, err := db.GetCloudWorkerBillingLifecycle("bot-refund-365-30")
		if err != nil {
			t.Fatal(err)
		}
		if !before.PackageExpiresAt.Equal(longEnd) {
			t.Fatalf("the worker should start on the 365-day window: %+v want %s", before, longEnd)
		}

		refundPackage(t, db, order365)

		after, err := db.GetCloudWorkerBillingLifecycle("bot-refund-365-30")
		if err != nil {
			t.Fatal(err)
		}
		if after.ID != workerID {
			t.Fatalf("unexpected row: %+v", after)
		}
		if !after.PackageExpiresAt.Equal(shortEnd) {
			t.Fatalf("the refunded 365-day window survived the refund: got %s want the surviving 30-day window %s",
				after.PackageExpiresAt, shortEnd)
		}
		if !after.SyncPending || after.State != "active" {
			t.Fatalf("the settled term must reach the provider: %+v", after)
		}
		if !after.DeleteAfter.Equal(shortEnd.AddDate(0, 0, 7)) {
			t.Fatalf("the retention window must follow the surviving package: %+v", after)
		}
	})

	t.Run("a refunded order that leaves only the free baseline expires now", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "refund-baseline")
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-refund-baseline", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		order := buyMachinePackage(t, db, owner, plan365, "TERMREFUNDBASE")
		// The real baseline entry point: an unlimited, perpetual, quota-only
		// entitlement. It must never be read as an unlimited machine grant.
		if applied, err := db.EnsureCommercialRelayBaseline(owner, "free", map[string]float64{"MiniMax-M3": 10}, time.Now().UTC().Add(-time.Hour)); err != nil || !applied {
			t.Fatalf("ensure free baseline: applied=%v err=%v", applied, err)
		}

		startedAt := time.Now().UTC()
		refundPackage(t, db, order)

		after, err := db.GetCloudWorkerBillingLifecycle("bot-refund-baseline")
		if err != nil {
			t.Fatal(err)
		}
		if after.ID != workerID {
			t.Fatalf("unexpected row: %+v", after)
		}
		if after.PackageExpiresAt.IsZero() || after.PackageExpiresAt.After(startedAt.Add(2*time.Minute)) {
			t.Fatalf("a free baseline must not keep the machine alive: %+v", after)
		}
		if after.PackageExpiresAt.Before(startedAt.Add(-5 * time.Minute)) {
			t.Fatalf("the worker should expire at the refund, not earlier: %+v", after)
		}
		if !after.SyncPending || after.State != "active" {
			t.Fatalf("an expired worker must still be synced before it is collected: %+v", after)
		}
		if !after.DeleteAfter.Equal(after.PackageExpiresAt.AddDate(0, 0, 7)) {
			t.Fatalf("the retention window must survive: %+v", after)
		}
	})

	t.Run("a surviving perpetual machine package keeps the worker open", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "refund-perpetual")
		if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
			Code:               "TERM-PERPETUAL-INVITE",
			PlanID:             perpetualPlan,
			MaxRedemptions:     1,
			CloudWorkerCredits: 1,
			CloudWorkerProfile: types.CloudWorkerPackedShared,
		}); err != nil {
			t.Fatalf("create perpetual invite: %v", err)
		}
		if _, err := db.RedeemCommercialInvite(owner, "TERM-PERPETUAL-INVITE"); err != nil {
			t.Fatalf("redeem perpetual invite: %v", err)
		}
		var perpetualExpiry *time.Time
		if err := db.db.QueryRow(`
			SELECT expires_at FROM commercial_entitlements
			WHERE uid = $1 AND source = 'invite' AND state = 'active'`, owner).Scan(&perpetualExpiry); err != nil {
			t.Fatalf("read invite entitlement: %v", err)
		}
		if perpetualExpiry != nil {
			t.Fatalf("fixture invalid: the perpetual package granted an expiry %s", perpetualExpiry)
		}

		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-refund-perpetual", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		order := buyMachinePackage(t, db, owner, plan365, "TERMREFUNDPERP")
		if before, err := db.GetCloudWorkerBillingLifecycle("bot-refund-perpetual"); err != nil {
			t.Fatal(err)
		} else if before.PackageExpiresAt.IsZero() {
			t.Fatalf("the paid window should govern while it is the latest: %+v", before)
		}

		refundPackage(t, db, order)

		after, err := db.GetCloudWorkerBillingLifecycle("bot-refund-perpetual")
		if err != nil {
			t.Fatal(err)
		}
		if after.ID != workerID {
			t.Fatalf("unexpected row: %+v", after)
		}
		if !after.PackageExpiresAt.IsZero() || !after.DeleteAfter.IsZero() {
			t.Fatalf("the surviving perpetual machine package must keep the worker open: %+v", after)
		}
		if !after.SyncPending {
			t.Fatalf("the settled term must reach the provider: %+v", after)
		}
	})

	t.Run("fixed and permanent terms survive a refund untouched", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "refund-terms")
		ownWindow := pgMicro(time.Now().UTC().Add(40 * 24 * time.Hour))
		_, fixedID := seedSharedLifecycleWorker(t, db, owner, "bot-refund-fixed", types.CloudWorkerExpiryFixed, &ownWindow, ptrTime(ownWindow.AddDate(0, 0, 7)), "active", "")
		_, permanentID := seedSharedLifecycleWorker(t, db, owner, "bot-refund-permanent", types.CloudWorkerExpiryPermanent, nil, nil, "active", "")
		_, followID := seedSharedLifecycleWorker(t, db, owner, "bot-refund-follow", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		order := buyMachinePackage(t, db, owner, plan365, "TERMREFUNDMIX")

		refundPackage(t, db, order)

		fixed, err := db.GetCloudWorkerBillingLifecycle("bot-refund-fixed")
		if err != nil {
			t.Fatal(err)
		}
		if fixed.ID != fixedID || !fixed.PackageExpiresAt.Equal(ownWindow) || fixed.SyncPending {
			t.Fatalf("a fixed term was moved by a refund: %+v", fixed)
		}
		permanent, err := db.GetCloudWorkerBillingLifecycle("bot-refund-permanent")
		if err != nil {
			t.Fatal(err)
		}
		if permanent.ID != permanentID || !permanent.PackageExpiresAt.IsZero() || permanent.SyncPending {
			t.Fatalf("a permanent grant was touched by a refund: %+v", permanent)
		}
		follow, err := db.GetCloudWorkerBillingLifecycle("bot-refund-follow")
		if err != nil {
			t.Fatal(err)
		}
		if follow.ID != followID || follow.PackageExpiresAt.IsZero() || follow.PackageExpiresAt.After(time.Now().UTC().Add(2*time.Minute)) {
			t.Fatalf("with nothing left the follow_plan worker must expire now: %+v", follow)
		}
	})
}

// TestPostgresCommercialAdjustmentSettlesSharedWorkerTerms covers the operator
// plan change: it revokes the old entitlement, so the replacement package - not
// the revoked window - has to decide the worker's term.
func TestPostgresCommercialAdjustmentSettlesSharedWorkerTerms(t *testing.T) {
	db := commercialPolicyTestDB(t)
	plan365 := seedSharedMachinePlan(t, db, "term-adjust-365", "Term adjust 365", 365)
	plan30 := seedSharedMachinePlan(t, db, "term-adjust-30", "Term adjust 30", 30)
	tokenOnly := seedTokenOnlyPlan(t, db, "term-adjust-tokens", "Term adjust tokens", 30)

	t.Run("changing to a shorter package settles the worker on it", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "adjust-shorter")
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-adjust-shorter", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		order := buyMachinePackage(t, db, owner, plan365, "TERMADJUST365")
		longEnd := entitlementExpiry(t, db, owner, order.OrderNo)

		result, err := db.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{
			UID:         owner,
			Action:      "change_plan",
			PlanID:      plan30,
			OperationID: "term-adjust-shorter-1",
			Note:        "settle on the replacement package",
		})
		if err != nil || !result.Applied || result.ExpiresAt == nil {
			t.Fatalf("change plan: result=%#v err=%v", result, err)
		}
		after, err := db.GetCloudWorkerBillingLifecycle("bot-adjust-shorter")
		if err != nil {
			t.Fatal(err)
		}
		if after.ID != workerID {
			t.Fatalf("unexpected row: %+v", after)
		}
		if !after.PackageExpiresAt.Equal(pgMicro(*result.ExpiresAt)) {
			t.Fatalf("the revoked 365-day window survived the plan change: got %s want %s", after.PackageExpiresAt, *result.ExpiresAt)
		}
		if !after.PackageExpiresAt.Before(longEnd) {
			t.Fatalf("the worker kept the longer revoked window %s (new %s)", longEnd, *result.ExpiresAt)
		}
		if !after.SyncPending {
			t.Fatalf("the settled term must reach the provider: %+v", after)
		}
	})

	t.Run("changing to a package without a machine expires the worker", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "adjust-tokens")
		_, workerID := seedSharedLifecycleWorker(t, db, owner, "bot-adjust-tokens", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		buyMachinePackage(t, db, owner, plan365, "TERMADJUSTTOK")
		startedAt := time.Now().UTC()
		result, err := db.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{
			UID:         owner,
			Action:      "change_plan",
			PlanID:      tokenOnly,
			OperationID: "term-adjust-tokens-1",
			Note:        "token-only replacement",
		})
		if err != nil || !result.Applied {
			t.Fatalf("change plan: result=%#v err=%v", result, err)
		}
		after, err := db.GetCloudWorkerBillingLifecycle("bot-adjust-tokens")
		if err != nil {
			t.Fatal(err)
		}
		if after.ID != workerID {
			t.Fatalf("unexpected row: %+v", after)
		}
		if after.PackageExpiresAt.IsZero() || after.PackageExpiresAt.After(startedAt.Add(2*time.Minute)) {
			t.Fatalf("a token-only package must not keep the machine alive: %+v", after)
		}
		if !after.SyncPending || after.State != "active" {
			t.Fatalf("an expired worker must still be synced before it is collected: %+v", after)
		}
	})
}

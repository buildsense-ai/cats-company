package postgres

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresInviteTermMonthlyRepair(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	uid := createCreditMonthlyTestUser(t, db, "invite-repair")
	plan := seedInviteTermPlan(t, db, "invite-repair", rolloverAllowance, 1)
	const gift = 20 * store.ModelCreditUnitsPerCredit
	mintInvite(t, db, "INVITE-REPAIR", plan, gift, false)
	if _, err := db.RedeemCommercialInvite(uid, "INVITE-REPAIR"); err != nil {
		t.Fatal(err)
	}
	ent, start, end, _ := readInviteSnapshotEntitlement(t, db, uid)
	// Recreate the old redemption's durable state: a gift and a finite worker
	// credit, but neither a model-access expiry nor a canonical monthly grant.
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DELETE FROM model_credit_monthly_lots WHERE uid=$1`, uid)
	exec(`DELETE FROM model_credit_ledger_events WHERE uid=$1 AND bucket='monthly'`, uid)
	exec(`UPDATE model_credit_accounts SET monthly_units=0,debt_units=100 WHERE uid=$1`, uid)
	exec(`UPDATE commercial_entitlements SET expires_at=NULL,invite_monthly_units=NULL WHERE id=$1`, ent)
	down, err := os.ReadFile("../migrations/postgres/000079_commercial_invite_monthly_repairs.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../migrations/postgres/000079_commercial_invite_monthly_repairs.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(down))
	exec(string(up))
	in := InviteMonthlyRepair{EntitlementID: ent, PlanID: plan, Units: rolloverAllowance, GiftUnits: gift,
		StartsAt: start, TermEndsAt: end.Time, AuditRef: "repair-fixture"}
	if err := db.db.QueryRow(`SELECT current_database(),current_schema()`).Scan(&in.Database, &in.Schema); err != nil {
		t.Fatal(err)
	}
	var workerBefore string
	if err := db.db.QueryRow(`SELECT to_jsonb(w)::text FROM cloud_worker_credits w WHERE entitlement_id=$1`, ent).Scan(&workerBefore); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		name string
		edit func(*InviteMonthlyRepair)
	}{
		{"wrong-database", func(x *InviteMonthlyRepair) { x.Database += "-wrong" }},
		{"wrong-amount", func(x *InviteMonthlyRepair) { x.Units++ }},
		{"wrong-term", func(x *InviteMonthlyRepair) { x.TermEndsAt = x.TermEndsAt.Add(time.Microsecond) }},
		{"possible-compensation", func(x *InviteMonthlyRepair) { x.GiftUnits = x.Units }},
		{"wrong-schema", func(x *InviteMonthlyRepair) { x.Schema += "-wrong" }},
		{"wrong-gift", func(x *InviteMonthlyRepair) { x.GiftUnits++ }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			x := in
			x.Commit = true
			bad.edit(&x)
			if changed, err := db.RepairInviteMonthly(ctx, x); err == nil || changed {
				t.Fatalf("expected fail closed, changed=%v, err=%v", changed, err)
			}
		})
	}
	assertRejected := func() {
		t.Helper()
		x := in
		x.Commit = true
		if changed, err := db.RepairInviteMonthly(ctx, x); changed || err == nil {
			t.Fatalf("expected rejection, changed=%v, err=%v", changed, err)
		}
	}
	t.Run("worker-term-changed", func(t *testing.T) {
		exec(`UPDATE cloud_worker_credits SET expires_at=$2 WHERE entitlement_id=$1`, ent, end.Time.AddDate(0, 0, 1))
		assertRejected()
		exec(`UPDATE cloud_worker_credits SET expires_at=$2 WHERE entitlement_id=$1`, ent, end.Time)
	})
	t.Run("unexplained-monthly-balance", func(t *testing.T) {
		exec(`UPDATE model_credit_accounts SET monthly_units=1 WHERE uid=$1`, uid)
		assertRejected()
		exec(`UPDATE model_credit_accounts SET monthly_units=0 WHERE uid=$1`, uid)
	})
	t.Run("pending-paid-and-refunded-plan-order", func(t *testing.T) {
		expires := time.Now().UTC().Add(time.Hour)
		order, err := db.CreateCommercialOrder(&types.CommercialOrder{
			OrderNo: "INVREPAIRBLOCK", UID: uid, PlanID: plan, Channel: "test", ClientRequestID: "invite-repair-block", ExpiresAt: &expires,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer exec(`DELETE FROM commercial_orders WHERE order_no=$1`, order.OrderNo)
		assertRejected()
		exec(`UPDATE commercial_orders SET status='paid',paid_at=$2 WHERE order_no=$1`, order.OrderNo, start)
		assertRejected()
		exec(`UPDATE commercial_orders SET status='refunded',paid_at=NULL,refunded_at=$2 WHERE order_no=$1`, order.OrderNo, start)
		assertRejected()
	})
	if changed, err := db.RepairInviteMonthly(ctx, in); err != nil || !changed {
		t.Fatalf("preview changed=%v, err=%v", changed, err)
	}
	var repairs int
	if err := db.db.QueryRow(`SELECT count(*) FROM commercial_invite_monthly_repairs WHERE entitlement_id=$1`, ent).Scan(&repairs); err != nil || repairs != 0 {
		t.Fatalf("preview left %d repair rows: %v", repairs, err)
	}
	if n := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); n != 0 {
		t.Fatalf("preview left %d monthly events", n)
	}
	in.Commit = true
	type result struct {
		changed bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := db.RepairInviteMonthly(ctx, in)
			results <- result{changed, err}
		}()
	}
	wg.Wait()
	close(results)
	writes := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.changed {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("concurrent submissions booked %d grants", writes)
	}
	if _, err := db.db.Exec(string(down)); err == nil || !strings.Contains(err.Error(), "cannot roll back recorded invite monthly repairs") {
		t.Fatalf("migration down must protect recorded repairs: %v", err)
	}
	if account := creditSubscriptionAccount(t, db, ctx, uid); account.MonthlyUnits != rolloverAllowance-100 || account.GiftUnits != gift || account.DebtUnits != 0 {
		t.Fatalf("unexpected repaired balance: %+v", account)
	}
	beyondTerm, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, end.Time)
	if err != nil || beyondTerm.MonthlyUnits != 0 || beyondTerm.FrozenMonthlyUnits != rolloverAllowance-100 {
		t.Fatalf("expiry did not freeze the repaired term: %+v, %v", beyondTerm, err)
	}
	if n := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); n != 1 {
		t.Fatalf("got %d monthly grants; want 1", n)
	}
	// Fulfillment runs at the real clock, before the simulated expiry above.
	order := createAndFulfillCommercialTestOrder(t, db, uid, plan, "INVREPAIRRENEW", "invite-repair-renew", "invite-repair-payment")
	paidStart, _ := creditSubscriptionEntitlementWindow(t, db, uid, order.OrderNo)
	if !paidStart.Equal(end.Time) {
		t.Fatalf("early renewal begins %v, want %v", paidStart, end.Time)
	}
	renewed, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, paidStart)
	if err != nil || renewed.MonthlyUnits != 2*rolloverAllowance-100 || renewed.FrozenMonthlyUnits != 0 {
		t.Fatalf("renewal did not preserve and unlock the repaired lot: %+v, %v", renewed, err)
	}
	var originalEnd sql.NullTime
	var originalSnapshot sql.NullInt64
	if err := db.db.QueryRow(`SELECT expires_at,invite_monthly_units FROM commercial_entitlements WHERE id=$1`, ent).Scan(&originalEnd, &originalSnapshot); err != nil || originalEnd.Valid || originalSnapshot.Valid {
		t.Fatalf("original model-access terms changed: %v, %v, %v", originalEnd, originalSnapshot, err)
	}
	var workerAfter string
	if err := db.db.QueryRow(`SELECT to_jsonb(w)::text FROM cloud_worker_credits w WHERE entitlement_id=$1`, ent).Scan(&workerAfter); err != nil || workerAfter != workerBefore {
		t.Fatalf("original worker credit changed: %v", err)
	}
}

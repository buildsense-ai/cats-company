package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresCommercialActionsCreditOnly(t *testing.T) {
	db := commercialPolicyTestDB(t)
	ctx := context.Background()
	const allowance int64 = 22_500_000_000
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "ops-credit-v3", Name: "Credit V3", Currency: "CNY", SaleState: "hidden",
		DurationDays: 30, PlanTier: 3, ModelCreditUnits: allowance,
	})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	expectCode := func(err error, code string) {
		t.Helper()
		var e *types.CommercialAdjustmentError
		if !errors.As(err, &e) || e.Code != code {
			t.Fatalf("want %s, got %v", code, err)
		}
	}
	preview := func(uid int64, intent, operation string) types.CommercialActionRequest {
		t.Helper()
		r := types.CommercialActionRequest{UID: uid, Intent: intent, PlanID: planID,
			WorkerAction: "none", Note: "credit-only operations", OperationID: operation, Preview: true}
		p, err := db.PreviewCommercialAction(r)
		if err != nil || !p.CanApply || p.NextTotalCNY != 0 || p.WorkerEffect != "none" {
			t.Fatalf("preview: %+v %v", p, err)
		}
		r.Preview, r.PreviewToken = false, p.PreviewToken
		return r
	}
	apply := func(r types.CommercialActionRequest) *types.CommercialAction {
		t.Helper()
		v, err := db.ApplyCommercialAction(r)
		if err != nil {
			t.Fatal(err)
		}
		if !v.Operation.PackageApplied || !v.Operation.RelaySynced || v.Operation.WorkerStatus != "none" || v.Result == nil || !v.Result.Applied || v.Result.NextTotalCNY != 0 {
			t.Fatalf("apply: %+v", v)
		}
		return v
	}
	balance := func(uid, monthly int64) {
		t.Helper()
		var m, g, p int64
		if err := db.db.QueryRow(`SELECT monthly_units,gift_units,paid_units FROM model_credit_accounts WHERE uid=$1`, uid).Scan(&m, &g, &p); err != nil {
			t.Fatal(err)
		}
		if m != monthly || g != 0 || p != 0 {
			t.Fatalf("buckets = %d/%d/%d, want %d/0/0", m, g, p, monthly)
		}
	}
	noLegacyOrMachine := func(uid int64) {
		t.Helper()
		for _, table := range []string{"commercial_quota_grants", "commercial_quota_ledger", "cloud_worker_credits", "cloud_worker_lifecycles", "commercial_managed_relay_budgets"} {
			column := "uid"
			if table == "cloud_worker_lifecycles" {
				column = "owner_uid"
			}
			if count(`SELECT COUNT(*) FROM `+table+` WHERE `+column+`=$1`, uid) != 0 {
				t.Fatalf("credit-only action wrote %s", table)
			}
		}
		if count(`SELECT COUNT(*) FROM bot_config WHERE owner_id=$1`, uid) != 0 {
			t.Fatal("credit-only action created a machine")
		}
	}

	t.Run("grant rollback renewal replay and period rollover", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-credit-ledger")
		r := preview(uid, "grant_plan", "credit-grant")
		for _, table := range []string{"model_credit_accounts", "model_credit_ledger_events", "commercial_entitlements", "commercial_ops_actions"} {
			if count(`SELECT COUNT(*) FROM `+table+` WHERE uid=$1`, uid) != 0 {
				t.Fatalf("preview wrote %s", table)
			}
		}
		exec(`ALTER TABLE commercial_ops_actions ADD CONSTRAINT credit_ops_rollback CHECK(operation_id<>'credit-grant')`)
		if _, err := db.ApplyCommercialAction(r); err == nil {
			t.Fatal("journal failure did not abort the credit grant")
		}
		exec(`ALTER TABLE commercial_ops_actions DROP CONSTRAINT credit_ops_rollback`)
		for _, table := range []string{"model_credit_accounts", "model_credit_ledger_events", "commercial_entitlements", "commercial_ops_actions"} {
			if count(`SELECT COUNT(*) FROM `+table+` WHERE uid=$1`, uid) != 0 {
				t.Fatalf("rollback left %s", table)
			}
		}
		first := apply(r)
		balance(uid, allowance)
		var anchor, expiry time.Time
		var source string
		if err := db.db.QueryRow(`SELECT source,period_anchor_at,expires_at FROM commercial_entitlements WHERE uid=$1`, uid).Scan(&source, &anchor, &expiry); err != nil {
			t.Fatal(err)
		}
		if source != "admin" || !expiry.Equal(store.ModelCreditMonthlyBoundary(anchor, 1)) || !first.Preview.ExpiresAt.Equal(expiry) || !first.Result.ExpiresAt.Equal(expiry) {
			t.Fatalf("grant window: source=%s anchor=%s expiry=%s", source, anchor, expiry)
		}
		if count(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid=$1 AND event_key=$2 AND bucket='monthly' AND event_type='grant' AND units=$3`, uid, store.ModelCreditMonthlyGrantKey(uid, planID, anchor), allowance) != 1 {
			t.Fatal("grant did not book exactly one deterministic monthly event")
		}
		// Replaying a durable action must bypass the now-stale preview and must
		// preserve the exact persisted result after an adapter restart.
		r.PreviewToken = "expired-token"
		replayed, err := (&Adapter{db: db.db}).ApplyCommercialAction(r)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(first)
		b, _ := json.Marshal(replayed)
		if string(a) != string(b) {
			t.Fatal("grant replay changed the durable result")
		}
		changed := r
		changed.Note = "changed"
		_, err = db.ApplyCommercialAction(changed)
		expectCode(err, "operation_conflict")
		if decision, err := db.ApplyModelCreditTask(ctx, store.ModelCreditTaskInput{UID: uid, TaskID: "ops-credit-task", Action: store.ModelCreditTaskActionStart}); err != nil || !decision.Allowed {
			t.Fatalf("credit task: %+v %v", decision, err)
		}
		const spent int64 = 1_000_000
		if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "ops-credit-use", RootTaskID: "ops-credit-task", Units: spent}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "ops-credit-use", ActualUnits: spent}); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 2; i++ {
			renew := preview(uid, "renew_plan", fmt.Sprintf("renew-%d", i))
			v := apply(renew)
			if !v.Result.ExpiresAt.Equal(store.ModelCreditMonthlyBoundary(anchor, i+1)) || !v.Preview.ExpiresAt.Equal(*v.Result.ExpiresAt) {
				t.Fatalf("renewal %d did not chain natural months: %+v", i, v.Result)
			}
			renew.PreviewToken = "expired-token"
			apply(renew)
			balance(uid, allowance-spent)
		}
		if count(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid=$1 AND source='admin' AND period_anchor_at=$2`, uid, anchor) != 3 || count(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid=$1 AND event_type='grant'`, uid) != 1 {
			t.Fatal("early renewal duplicated allowance or lost the anchor")
		}
		next, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, expiry)
		if err != nil || next.MonthlyUnits != allowance {
			t.Fatalf("next period: %+v %v", next, err)
		}
		if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, expiry.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if count(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid=$1 AND bucket='monthly' AND event_type='grant'`, uid) != 2 || count(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid=$1 AND bucket='monthly' AND event_type='expire' AND units=$2`, uid, allowance-spent) != 1 {
			t.Fatal("period rollover duplicated grants or carried old allowance")
		}
		noLegacyOrMachine(uid)
	})

	t.Run("anchor survives expired original segment and snapshot changes", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-credit-anchor")
		now := time.Now().UTC().Truncate(time.Microsecond)
		anchor := time.Date(now.Year()-1, time.January, 31, 10, 0, 0, 0, time.UTC)
		start, expiry, ok := store.ModelCreditMonthlyPeriodAt(anchor, now)
		if !ok {
			t.Fatal("invalid fixture period")
		}
		insertCreditMonthlyTestEntitlement(t, db, uid, planID, "admin", "earlier-credit", start, expiry, anchor)
		r := preview(uid, "renew_plan", "anchored-renewal")
		exec(`UPDATE commercial_entitlements SET period_anchor_at=period_anchor_at+INTERVAL '1 hour' WHERE uid=$1`, uid)
		_, err := db.ApplyCommercialAction(r)
		expectCode(err, "stale_preview")
		exec(`UPDATE commercial_entitlements SET period_anchor_at=$2 WHERE uid=$1`, uid, anchor)
		r = preview(uid, "renew_plan", "anchored-renewal")
		exec(`UPDATE commercial_plans SET model_credit_units=model_credit_units+1 WHERE id=$1`, planID)
		_, err = db.ApplyCommercialAction(r)
		expectCode(err, "stale_preview")
		exec(`UPDATE commercial_plans SET model_credit_units=$2 WHERE id=$1`, planID, allowance)
		v := apply(preview(uid, "renew_plan", "anchored-renewal"))
		want, _ := store.ModelCreditMonthlyNextBoundary(anchor, expiry)
		if !v.Result.ExpiresAt.Equal(want) {
			t.Fatalf("renewal re-anchored on clipped expiry: got %s want %s", v.Result.ExpiresAt, want)
		}
		balance(uid, allowance)
		noLegacyOrMachine(uid)
	})

	t.Run("concurrent credit apply", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-credit-concurrent")
		r := preview(uid, "grant_plan", "concurrent-credit")
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := db.ApplyCommercialAction(r); errs <- err }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		balance(uid, allowance)
		for _, table := range []string{"commercial_entitlements", "commercial_ops_actions", "model_credit_ledger_events"} {
			if count(`SELECT COUNT(*) FROM `+table+` WHERE uid=$1`, uid) != 1 {
				t.Fatalf("concurrent grant duplicated %s", table)
			}
		}
		noLegacyOrMachine(uid)
	})

	t.Run("legacy adjustment and invite sources stay guarded", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-credit-legacy-guard")
		_, err := db.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{UID: uid, Action: "change_plan", PlanID: planID, OperationID: "legacy-credit"})
		expectCode(err, "invalid_plan")
		now := time.Now().UTC()
		insertCreditMonthlyTestEntitlement(t, db, uid, planID, "invite", "invite-only", now.Add(-time.Hour), now.AddDate(0, 1, 0), now.Add(-time.Hour))
		if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, now); err != nil {
			t.Fatal(err)
		}
		if count(`SELECT COUNT(*) FROM model_credit_accounts WHERE uid=$1`, uid) != 0 {
			t.Fatal("invite-only entitlement migrated into monthly credit")
		}
		noLegacyOrMachine(uid)
	})
}

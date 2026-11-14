package postgres

import (
	"context"
	"testing"
	"time"
)

func TestModelCreditEligibilityUsesCurrentPrimaryEntitlement(t *testing.T) {
	db := modelCreditDatabase(t)
	ctx := context.Background()
	var uid int64
	if err := db.db.QueryRowContext(ctx, `INSERT INTO users (username, pass_hash) VALUES ($1, $2) RETURNING id`, "credit-eligibility-test", []byte("test-only")).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	var freePlan, paidPlan, futurePlan int64
	for _, plan := range []struct {
		slug, name string
		tier       int
		units      int64
		id         *int64
	}{
		{"eligibility-free", "Free", 0, 0, &freePlan},
		{"eligibility-paid", "Paid", 3, 9000000, &paidPlan},
		{"eligibility-future", "Future", 4, 20000000, &futurePlan},
	} {
		if err := db.db.QueryRowContext(ctx, `INSERT INTO commercial_plans(slug, name, plan_tier, model_credit_units) VALUES ($1, $2, $3, $4) RETURNING id`, plan.slug, plan.name, plan.tier, plan.units).Scan(plan.id); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	// order 来源的权益按订单快照（commercial_orders.plan_model_credit_units）读额度，
	// 所以 fixture 必须落下真实的订单行；没有订单行就 fail closed（额度 0）。
	insertCreditMonthlyTestOrderSnapshot(t, db, uid, paidPlan, "eligibility-paid")
	insertCreditMonthlyTestOrderSnapshot(t, db, uid, futurePlan, "eligibility-future")
	if _, err := db.db.ExecContext(ctx, `INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at) VALUES
		($1, $2, 'free', '', 'active', $3, NULL),
		($1, $4, 'order', 'eligibility-paid', 'active', $3, $3 + interval '30 days'),
		($1, $5, 'order', 'eligibility-future', 'active', $3 + interval '1 day', $3 + interval '60 days')`, uid, freePlan, now, paidPlan, futurePlan); err != nil {
		t.Fatal(err)
	}
	// 身份 + 当期权限：有生效的 credit 套餐时两者都成立。
	enabled, tier, units, err := db.GetModelCreditEligibility(ctx, uid)
	if err != nil || !enabled || tier != 3 || units != 9000000 {
		t.Fatalf("eligibility=(%v,%d,%d,%v), want enabled paid plan", enabled, tier, units, err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE commercial_plans SET model_credit_units = 0 WHERE id = $1`, paidPlan); err != nil {
		t.Fatal(err)
	}
	// 把套餐额度清零：已卖出的订单快照不受影响（改商品不能删掉已付用户的额度），
	// 身份、档位和 primary 选择都不变，也不回落到免费套餐。
	enabled, tier, units, err = db.GetModelCreditEligibility(ctx, uid)
	if err != nil || !enabled || tier != 3 || units != 9000000 {
		t.Fatalf("zeroing a plan rewrote an order snapshot: (%v,%d,%d,%v), want (true,3,9000000)",
			enabled, tier, units, err)
	}
	// 没有订单行的 order 权益 fail closed：即使套餐现值很高，也不能据此给出额度。
	// 这里用更高档（tier 4）的套餐，确认它被选中但额度是 0，而不是套餐现值。
	var orphanPlan int64
	if err := db.db.QueryRowContext(ctx, `INSERT INTO commercial_plans(slug, name, plan_tier, model_credit_units)
		VALUES ($1, $2, 4, $3) RETURNING id`, "eligibility-orphan", "Orphan", int64(7_000_000)).Scan(&orphanPlan); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', 'eligibility-orphan', 'active', $3::timestamptz, $3::timestamptz + interval '30 days')`,
		uid, orphanPlan, now); err != nil {
		t.Fatal(err)
	}
	if enabled, tier, orphanUnits, err := db.GetModelCreditEligibility(ctx, uid); err != nil ||
		!enabled || tier != 4 || orphanUnits != 0 {
		t.Fatalf("an order entitlement without an order row = (%v,%d,%d,%v), want (true,4,0): fail closed",
			enabled, tier, orphanUnits, err)
	}
}

// 只读入口（余额页 / 资格查询）不能给 legacy 老用户建账本账户：账本账户本身就是
// credit 身份的证据，建了它等于把老用户迁进 credit 模型，Relay 再也不会让它走老
// 额度路径。同时锁住「过期不等于 legacy」：过期订阅身份仍在，权限（allowed）没了。
func TestModelCreditReadsDoNotEnrollLegacyUsers(t *testing.T) {
	db := modelCreditDatabase(t)
	ctx := context.Background()
	var uid int64
	if err := db.db.QueryRowContext(ctx, `INSERT INTO users (username, pass_hash) VALUES ($1, $2) RETURNING id`,
		"credit-legacy-reader", []byte("test-only")).Scan(&uid); err != nil {
		t.Fatal(err)
	}

	enabled, tier, units, err := db.GetModelCreditEligibility(ctx, uid)
	if err != nil || enabled || tier != 0 || units != 0 {
		t.Fatalf("legacy eligibility=(%v,%d,%d,%v), want disabled", enabled, tier, units, err)
	}
	if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, time.Now().UTC()); err != nil {
		t.Fatalf("reconcile legacy user: %v", err)
	}
	var accounts, events int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_credit_accounts WHERE uid = $1`, uid).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 {
		t.Fatalf("a read-only reconcile created %d credit account row(s) for a legacy user", accounts)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid = $1`, uid).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("a read-only reconcile wrote %d ledger event(s) for a legacy user", events)
	}
	if taskEnabled, err := db.modelCreditTaskEnabled(ctx, uid); err != nil || taskEnabled {
		t.Fatalf("legacy user enrolled in the credit task gate: enabled=%t err=%v", taskEnabled, err)
	}

	// 过期的 credit 订阅：身份 true，权限 false。
	var expiredPlanID int64
	if err := db.db.QueryRowContext(ctx, `INSERT INTO commercial_plans(slug, name, plan_tier, model_credit_units)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		"eligibility-expired", "Expired", 3, int64(5_000_000)).Scan(&expiredPlanID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insertCreditMonthlyTestOrderSnapshot(t, db, uid, expiredPlanID, "eligibility-expired")
	if _, err := db.db.ExecContext(ctx, `INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', 'eligibility-expired', 'active', $3, $4)`,
		uid, expiredPlanID, now.AddDate(0, -2, 0), now.AddDate(0, -1, 0)); err != nil {
		t.Fatal(err)
	}
	enabled, tier, units, err = db.GetModelCreditEligibility(ctx, uid)
	if err != nil || !enabled || tier != 0 || units != 0 {
		t.Fatalf("expired credit plan eligibility=(%v,%d,%d,%v), want enabled identity without permission", enabled, tier, units, err)
	}
	if taskEnabled, err := db.modelCreditTaskEnabled(ctx, uid); err != nil || !taskEnabled {
		t.Fatalf("an expired credit subscriber must stay inside the credit model: enabled=%t err=%v", taskEnabled, err)
	}
}

func TestModelCreditEligibilityRejectsInvalidUID(t *testing.T) {
	db := &Adapter{}
	if _, _, _, err := db.GetModelCreditEligibility(context.Background(), 0); err == nil {
		t.Fatal("expected invalid uid error")
	}
}

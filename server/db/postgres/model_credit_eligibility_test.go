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
	if _, err := db.db.ExecContext(ctx, `INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at) VALUES
		($1, $2, 'free', '', 'active', $3, NULL),
		($1, $4, 'order', 'eligibility-paid', 'active', $3, $3 + interval '30 days'),
		($1, $5, 'order', 'eligibility-future', 'active', $3 + interval '1 day', $3 + interval '60 days')`, uid, freePlan, now, paidPlan, futurePlan); err != nil {
		t.Fatal(err)
	}
	enabled, tier, units, err := db.GetModelCreditEligibility(ctx, uid)
	if err != nil || !enabled || tier != 3 || units != 9000000 {
		t.Fatalf("eligibility=(%v,%d,%d,%v), want enabled paid plan", enabled, tier, units, err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE commercial_plans SET model_credit_units = 0 WHERE id = $1`, paidPlan); err != nil {
		t.Fatal(err)
	}
	enabled, tier, units, err = db.GetModelCreditEligibility(ctx, uid)
	if err != nil || enabled || tier != 3 || units != 0 {
		t.Fatalf("zero allowance primary must remain disabled without falling back: (%v,%d,%d,%v)", enabled, tier, units, err)
	}
}

func TestModelCreditEligibilityRejectsInvalidUID(t *testing.T) {
	db := &Adapter{}
	if _, _, _, err := db.GetModelCreditEligibility(context.Background(), 0); err == nil {
		t.Fatal("expected invalid uid error")
	}
}

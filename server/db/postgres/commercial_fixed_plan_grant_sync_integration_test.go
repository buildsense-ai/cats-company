package postgres

import (
	"context"
	"testing"
)

// TestPostgresCommercialFixedPlanGrantsFollowThePlan is the end-to-end guard for
// the drift an operator-assigned free package hits.
//
// createOperatorPlanGrants builds a buyer's grants from the plan as it stood at
// assignment time. Free's model set is maintained by its own migration rather
// than the catalog, so when that migration adds a model - or drops a retired one
// - the package already handed out keeps the old set: quota for a model the plan
// no longer sells, and none for the ones it gained. Production carried exactly
// this shape (one holder with four models while the plan sold nine).
//
// The unit tests cover the pieces; this one proves the reconcile pass actually
// reaches such a plan, which is what a missing call in the entry point would
// break silently.
func TestPostgresCommercialFixedPlanGrantsFollowThePlan(t *testing.T) {
	db := commercialPolicyTestDB(t)
	ctx := context.Background()

	// The Free plan is created by its own migration rather than CreateSchema, so
	// the fixture seeds the same shape the live plan has.
	livePlan := `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-flash":100,"glm-5.3-flash":100,"chatgpt-image-latest":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100}`
	planID := seedGLM53MigrationPlan(t, db, commercialFreePlanSlug, "Free", livePlan)
	plan := decodeModelBudgets([]byte(livePlan))
	if len(plan) < 2 {
		t.Fatalf("the fixture needs at least two models: %v", plan)
	}

	const uid = 933001
	// The holder's package as it was assigned: one model missing, one retired
	// name still present. The amounts are the plan's own, as production has.
	models, amounts := reconcilePlanOwnShares(plan)
	if len(models) < 2 {
		t.Fatalf("the Free plan produced %d sellable models", len(models))
	}
	userID := createGLM53MigrationUser(t, db, "fixed-plan-grants")
	sourceRef := "test-free-assignment"
	for _, model := range models[:len(models)-1] {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, source_ref, note
			) VALUES ($1, $2, 'operator_plan', $3, $4, '1M', CURRENT_TIMESTAMP, $5, 'test assignment')`,
			userID, planID, model, amounts[model], sourceRef,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec(`
		INSERT INTO commercial_quota_grants(
			uid, plan_id, grant_type, model, amount_cny, reset_duration,
			effective_at, source_ref, note
		) VALUES ($1, $2, 'operator_plan', 'deepseek-v4-flash', 100, '1M', CURRENT_TIMESTAMP, $3, 'test assignment')`,
		userID, planID, sourceRef,
	); err != nil {
		t.Fatal(err)
	}

	if err := db.ReconcileCommercialPlanModelsWithInternal(ctx, reconcileCatalog(), nil, true); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	rows, err := db.db.Query(`
		SELECT model, amount_cny FROM commercial_quota_grants
		WHERE uid = $1 AND plan_id = $2 AND grant_type = 'operator_plan'
		  AND source_ref = $3 AND revoked_at IS NULL
		ORDER BY model`, userID, planID, sourceRef)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	held := map[string]float64{}
	for rows.Next() {
		var model string
		var amount float64
		if err := rows.Scan(&model, &amount); err != nil {
			t.Fatal(err)
		}
		held[model] = amount
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(held) != len(models) {
		t.Fatalf("holder carries %d models, want the plan's %d: %v", len(held), len(models), held)
	}
	for _, model := range models {
		got, ok := held[model]
		if !ok {
			t.Fatalf("the reconcile did not grant %s, which the plan sells: %v", model, held)
		}
		// The plan's own amount, not an even split: Free grants 1000 for
		// MiniMax-M2.7 and 100 for an image model, and a repaired holder must
		// match every other holder of the same plan.
		if want := amounts[model]; got != want {
			t.Fatalf("%s = %v, want the plan's own %v", model, got, want)
		}
	}
	if _, kept := held["deepseek-v4-flash"]; kept {
		t.Fatal("a retired model must be revoked from the holder's package")
	}
}

// TestPostgresCommercialPlanReconcileLeavesMatchingGrantsAlone keeps the pass
// idempotent. It runs on every startup, so a holder that already matches the
// plan must not be rewritten: that would churn the ledger and the grants and
// make the reconcile look like it keeps changing something.
func TestPostgresCommercialPlanReconcileLeavesMatchingGrantsAlone(t *testing.T) {
	db := commercialPolicyTestDB(t)
	ctx := context.Background()

	livePlan := `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-flash":100,"glm-5.3-flash":100,"chatgpt-image-latest":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100}`
	planID := seedGLM53MigrationPlan(t, db, commercialFreePlanSlug, "Free", livePlan)
	models, amounts := reconcilePlanOwnShares(decodeModelBudgets([]byte(livePlan)))

	userID := createGLM53MigrationUser(t, db, "fixed-plan-matching")
	sourceRef := "test-free-matching"
	for _, model := range models {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, source_ref, note
			) VALUES ($1, $2, 'operator_plan', $3, $4, '1M', CURRENT_TIMESTAMP, $5, 'test assignment')`,
			userID, planID, model, amounts[model], sourceRef,
		); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	if err := db.db.QueryRow(`
		SELECT count(*) FROM commercial_quota_grants
		WHERE uid = $1 AND plan_id = $2 AND source_ref = $3`, userID, planID, sourceRef).Scan(&before); err != nil {
		t.Fatal(err)
	}

	if err := db.ReconcileCommercialPlanModelsWithInternal(ctx, reconcileCatalog(), nil, true); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var after, revoked int
	if err := db.db.QueryRow(`
		SELECT count(*), count(*) FILTER (WHERE revoked_at IS NOT NULL)
		FROM commercial_quota_grants WHERE uid = $1 AND plan_id = $2 AND source_ref = $3`,
		userID, planID, sourceRef).Scan(&after, &revoked); err != nil {
		t.Fatal(err)
	}
	if after != before || revoked != 0 {
		t.Fatalf("a matching package was rewritten: before=%d after=%d revoked=%d", before, after, revoked)
	}
}

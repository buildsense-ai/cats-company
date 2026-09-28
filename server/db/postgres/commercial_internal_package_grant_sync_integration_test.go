package postgres

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// TestPostgresCommercialInternalPackageGrantsFollowThePlan is the end-to-end
// guard for the drift the internal all-models package showed in production.
//
// The plan held twelve models while its two buyers held six: the four grants
// created when the package was assigned, plus nothing for the six models added
// later. The reconcile did not repair it, and could not: keeping gpt-5.6-sol
// (which the relay routes but does not sell) leaves the plan's model set
// unchanged, so the delta guard reported no change and skipped the grant pass
// entirely. Production showed exactly this - plan twelve, buyers six.
//
// The fix runs the grant alignment even when the set did not move. This test
// pins that: a plan matching the catalog with a lagging package must be
// repaired, not skipped.
func TestPostgresCommercialInternalPackageGrantsFollowThePlan(t *testing.T) {
	db := commercialPolicyTestDB(t)
	ctx := context.Background()

	// The internal package as production stores it: everything the relay sells,
	// plus the internal-only model that is routable but not for sale.
	sellable, internalOnly := reconcileInternalCatalog()
	plan := reconcileInternalPlan()
	encoded, err := encodeModelBudgets(plan)
	if err != nil {
		t.Fatal(err)
	}
	planID := seedGLM53MigrationPlan(t, db, "catsco-internal-all-models-50k", "内部全模型", string(encoded))
	// auto_update_models defaults to TRUE, which is what puts this plan through
	// the catalog-following path rather than the fixed-plan one.
	var autoUpdate bool
	if err := db.db.QueryRow(`SELECT auto_update_models FROM commercial_plans WHERE id = $1`, planID).Scan(&autoUpdate); err != nil {
		t.Fatal(err)
	}
	if !autoUpdate {
		t.Fatal("the fixture must opt into following the relay")
	}

	// The buyer's package as it was assigned: only the six models the plan held
	// at that time. deepseek-v4-flash is retired, so it carries its own amount
	// rather than the plan's (which no longer contains it).
	userID := createGLM53MigrationUser(t, db, "internal-package")
	sourceRef := "internal-package-assignment"
	_, planAmounts := reconcilePlanOwnShares(plan)
	assigned := map[string]float64{
		"MiniMax-M2.7": 8333.33, "MiniMax-M3": 8333.33, "glm-5.3-flash": 8333.35,
		"gpt-5.6-sol": 8333.33, "gpt-5.6-terra": 8333.33,
		"deepseek-v4-flash": 100,
	}
	assignedModels := make([]string, 0, len(assigned))
	for model := range assigned {
		assignedModels = append(assignedModels, model)
	}
	sort.Strings(assignedModels)
	for _, model := range assignedModels {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, source_ref, note
			) VALUES ($1, $2, 'operator_plan', $3, $4, '1M', CURRENT_TIMESTAMP, $5, 'test assignment')`,
			userID, planID, model, assigned[model], sourceRef,
		); err != nil {
			t.Fatal(err)
		}
	}
	_ = planAmounts

	if err := db.ReconcileCommercialPlanModelsWithInternal(ctx, sellable, internalOnly, true); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// The plan must keep every model it had, including the internal-only one.
	var planHasInternalOnly bool
	if err := db.db.QueryRow(`SELECT model_budgets ? 'gpt-5.6-sol' FROM commercial_plans WHERE id = $1`, planID).Scan(&planHasInternalOnly); err != nil {
		t.Fatal(err)
	}
	if !planHasInternalOnly {
		t.Fatal("the reconcile removed the internal-only model from a plan that grants it")
	}

	rows, err := db.db.Query(`
		SELECT model FROM commercial_quota_grants
		WHERE uid = $1 AND plan_id = $2 AND grant_type = 'operator_plan'
		  AND source_ref = $3 AND revoked_at IS NULL`, userID, planID, sourceRef)
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		held = append(held, model)
	}
	rows.Close()

	want := make([]string, 0, len(plan))
	for model := range plan {
		want = append(want, model)
	}
	sort.Strings(want)
	sort.Strings(held)
	if strings.Join(held, ",") != strings.Join(want, ",") {
		t.Fatalf("holder carries %d models, want the plan's %d\n got: %v\nwant: %v",
			len(held), len(want), held, want)
	}
	for _, retired := range []string{"deepseek-v4-flash"} {
		for _, model := range held {
			if model == retired {
				t.Fatalf("the retired %s is still granted", retired)
			}
		}
	}
}

// TestPostgresCommercialPackageThatMatchesIsNotRewritten keeps the pass free in
// steady state. It now runs on every startup even when the plan's set did not
// move, so a package that already matches must be left untouched rather than
// revoked and recreated on every restart.
func TestPostgresCommercialPackageThatMatchesIsNotRewritten(t *testing.T) {
	db := commercialPolicyTestDB(t)
	ctx := context.Background()

	sellable, internalOnly := reconcileInternalCatalog()
	plan := reconcileInternalPlan()
	encoded, err := encodeModelBudgets(plan)
	if err != nil {
		t.Fatal(err)
	}
	planID := seedGLM53MigrationPlan(t, db, "catsco-internal-matched", "内部全模型", string(encoded))

	userID := createGLM53MigrationUser(t, db, "internal-matched")
	sourceRef := "internal-matched-assignment"
	models, amounts := reconcilePlanOwnShares(plan)
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

	if err := db.ReconcileCommercialPlanModelsWithInternal(ctx, sellable, internalOnly, true); err != nil {
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

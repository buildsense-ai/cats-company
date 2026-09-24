package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ReconcileCommercialPlanModels keeps the official paid plans' model sets in
// step with the relay catalog.
//
// It replaces the startup migrations that used to hardcode the plan model lists.
// Those migrations had to be edited and redeployed every time the relay
// onboarded a model, and each one re-applied its literal list on every startup,
// so a hand-edited plan was silently reverted on the next restart. Reading the
// catalog instead means a new relay model reaches paid plans with no
// control-plane change.
//
// Two behaviours, both driven by the plan's auto_update_models switch:
//
//   - add: a model that is sellable on the relay but missing from the plan is
//     added, unless the plan pins its model set (auto_update_models = false)
//   - remove: a model the relay no longer offers is dropped from the plan even
//     when it is pinned, because leaving it would advertise a model whose
//     requests the relay refuses
//
// The plan's advertised total never changes: each lane keeps its own allowance
// and is re-split over the models that remain. An existing buyer's grants are
// migrated the same way, so the models they can use match the plan they bought.
//
// The whole pass is one transaction guarded by an advisory lock, so two
// starting instances cannot interleave, and it is idempotent: when a plan's
// model set already matches the catalog it performs no write at all.
func (a *Adapter) ReconcileCommercialPlanModels(ctx context.Context, catalog []string) error {
	models := normalizeReconcileModels(catalog)
	if len(models) == 0 {
		return fmt.Errorf("commercial model catalog is empty")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin commercial plan reconcile: %w", err)
	}
	defer tx.Rollback()

	// Serialize with any other instance starting at the same moment.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('catsco_commercial_plan_models_v1', 0))`,
	); err != nil {
		return fmt.Errorf("lock commercial plan reconcile: %w", err)
	}

	for _, slug := range commercialReconcilePlanSlugs {
		if err := reconcileOfficialPlan(ctx, tx, slug, models); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit commercial plan reconcile: %w", err)
	}
	return nil
}

// commercialReconcilePlanSlugs are the plans whose model sets follow the relay.
// Kept in a fixed order so a failure always reports the same first plan.
var commercialReconcilePlanSlugs = []string{
	commercialPersonalPlanSlug,
	commercialProPlanSlug,
}

func normalizeReconcileModels(catalog []string) []string {
	out := make([]string, 0, len(catalog))
	seen := make(map[string]struct{}, len(catalog))
	for _, model := range catalog {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}

// storedBudget keeps a plan's own spelling of a model alongside its amount, so
// a case-insensitive match still writes back the relay's canonical spelling.
type storedBudget struct {
	spelling string
	amount   float64
}

// reconcileOfficialPlan rewrites one plan's model budgets and, when the model
// set actually changed, migrates the grants of everyone currently holding it.
func reconcileOfficialPlan(ctx context.Context, tx *sql.Tx, slug string, catalog []string) error {
	var (
		planID           int64
		planName         string
		rawBudgets       []byte
		autoUpdateModels bool
	)
	err := tx.QueryRowContext(ctx, `
		SELECT id, name, model_budgets, auto_update_models
		FROM commercial_plans
		WHERE slug = $1 AND archived_at IS NULL
		FOR UPDATE`, slug).Scan(&planID, &planName, &rawBudgets, &autoUpdateModels)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load %s for model reconcile: %w", slug, err)
	}

	current := decodeModelBudgets(rawBudgets)
	target := reconcilePlanModelBudgets(current, catalog, autoUpdateModels)
	if target == nil {
		return nil
	}

	encoded, err := encodeModelBudgets(target)
	if err != nil {
		return fmt.Errorf("encode %s model budgets: %w", slug, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE commercial_plans SET model_budgets = $2::jsonb WHERE id = $1`,
		planID, string(encoded),
	); err != nil {
		return fmt.Errorf("update %s model budgets: %w", slug, err)
	}

	if err := reconcilePlanOrderSnapshots(ctx, tx, slug, target); err != nil {
		return err
	}
	return reconcileActivePlanGrants(ctx, tx, planID, planName, current, target)
}

// reconcilePlanModelBudgets computes the model set a plan should carry. It
// returns nil when the plan already matches, which is what keeps a steady-state
// startup free of writes.
//
// Only the plan's total is meaningful. A plan's models share one pool, and the
// relay repeats the shared limit on every model entry, so the per-model amounts
// in model_budgets are a split of that total rather than independent quotas.
// Verified against production: every model a paid user holds (image models
// included) reports the same max_limit, equal to the plan's model_budgets sum
// plus any grants.
//
// The total is therefore preserved and re-split evenly over the resulting model
// set, so five chat models at 2100 plus five image models at 100 become eleven
// models at 1000 each and the buyer's pool is unchanged. Treating the image
// models as a separate small allowance would be wrong twice over: it would leave
// them out of the split, and a newly added image model would silently take its
// allowance away from the shared pool instead of sharing it.
func reconcilePlanModelBudgets(current map[string]float64, catalog []string, autoUpdateModels bool) map[string]float64 {
	existing := make(map[string]storedBudget, len(current))
	// The plan's present total is the anchor: whatever the model set becomes,
	// this is what the plan advertises and what a buyer's pool must stay at. A
	// retired model's share therefore returns to the pool rather than leaving the
	// plan, which is exactly what the DeepSeek V4 retirement did (six chat models
	// at 1750 became five at 2100).
	total := 0.0
	for model, amount := range current {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" || amount <= 0 {
			continue
		}
		existing[strings.ToLower(model)] = storedBudget{spelling: model, amount: amount}
		total += amount
	}
	if total <= 0 {
		return nil
	}

	out := make(map[string]float64, len(catalog))
	changed := false
	for _, model := range catalog {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" {
			// "*" is the relay's monthly-pool wildcard, not a sellable model.
			continue
		}
		if _, ok := existing[strings.ToLower(model)]; !ok {
			if !autoUpdateModels {
				// The plan pins its model set, so a new relay model is not added.
				continue
			}
			changed = true
		}
		out[model] = 0
	}
	// A model the relay no longer sells leaves the plan even when the plan is
	// pinned: keeping it would advertise a model whose requests the relay
	// refuses.
	for key, stored := range existing {
		if _, ok := out[stored.spelling]; ok {
			continue
		}
		if _, ok := out[key]; ok {
			continue
		}
		changed = true
	}
	if !changed || len(out) == 0 {
		return nil
	}

	names := make([]string, 0, len(out))
	for model := range out {
		names = append(names, model)
	}
	sort.Strings(names)
	share := roundBudget(total / float64(len(names)))
	assigned := 0.0
	for _, model := range names {
		out[model] = share
		assigned += share
	}
	// Push the rounding remainder onto the first model so the sum is exactly the
	// plan's total: a restart must not drift the advertised allowance.
	if remainder := roundBudget(total - assigned); remainder != 0 {
		out[names[0]] = roundBudget(out[names[0]] + remainder)
	}
	return out
}

// roundBudget keeps budgets at the six decimal places the column stores, so a
// computed value and its stored form compare equal.
func roundBudget(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}

// reconcilePlanOrderSnapshots refreshes the model snapshot on orders that have
// not been fulfilled yet. Fulfilled orders keep their recorded snapshot: it is
// the contract those buyers paid against, and rewriting it would falsify
// history. Their grants are handled by the grant pass.
func reconcilePlanOrderSnapshots(ctx context.Context, tx *sql.Tx, slug string, budgets map[string]float64) error {
	encoded, err := encodeModelBudgets(budgets)
	if err != nil {
		return fmt.Errorf("encode %s order model budgets: %w", slug, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE commercial_orders
		SET plan_model_budgets = $2::jsonb
		WHERE plan_slug = $1
		  AND status IN ('created', 'pending', 'paid')
		  AND plan_model_budgets IS DISTINCT FROM $2::jsonb`,
		slug, string(encoded),
	); err != nil {
		return fmt.Errorf("update %s order model snapshots: %w", slug, err)
	}
	return nil
}

// reconcileActivePlanGrants brings the grants of current holders in line with
// the plan they bought.
//
// Grants are created from the plan's budgets at purchase time, so changing the
// plan alone would leave existing buyers without the new model. The package's
// plan-derived grants are therefore rebuilt for the new model set, exactly as
// the model-set migrations did before this reconcile replaced them: the total is
// the same because the same allowance is re-split, and each model carries the
// plan's own per-model amount.
//
// Only grants that came from a plan's model set are touched. Free, manual and
// top-up grants are the operator's own arrangements and stay as they are.
func reconcileActivePlanGrants(ctx context.Context, tx *sql.Tx, planID int64, planName string, before, after map[string]float64) error {
	added, removed := reconcileModelDelta(before, after)
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	packages, err := reconcileGrantPackages(ctx, tx, planID)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return nil
	}
	// The plan's own per-model amount: every model carries the same share of the
	// shared pool, so one lookup covers them all.
	share := 0.0
	for _, amount := range after {
		if amount > 0 {
			share = roundBudget(amount)
			break
		}
	}
	if share <= 0 {
		return nil
	}
	models := make([]string, 0, len(after))
	for model, amount := range after {
		if amount > 0 {
			models = append(models, model)
		}
	}
	sort.Strings(models)

	for _, pkg := range packages {
		if err := revokePackageGrants(ctx, tx, pkg); err != nil {
			return err
		}
		for _, model := range models {
			if err := grantPackageModel(ctx, tx, pkg, planID, planName, model, share); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileModelDelta reports which models the reconcile added and removed.
func reconcileModelDelta(before, after map[string]float64) (added, removed []string) {
	beforeKeys := make(map[string]struct{}, len(before))
	for model := range before {
		beforeKeys[strings.ToLower(strings.TrimSpace(model))] = struct{}{}
	}
	afterKeys := make(map[string]struct{}, len(after))
	for model := range after {
		afterKeys[strings.ToLower(strings.TrimSpace(model))] = struct{}{}
	}
	for model := range after {
		if _, ok := beforeKeys[strings.ToLower(strings.TrimSpace(model))]; !ok {
			added = append(added, model)
		}
	}
	for model := range before {
		if _, ok := afterKeys[strings.ToLower(strings.TrimSpace(model))]; !ok {
			removed = append(removed, model)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// revokePackageGrants retires one buyer's plan-derived grants so the package can
// be rebuilt for the plan's new model set. The ledger keeps a negative entry per
// grant, so the buyer's history explains why the old amounts stopped applying.
func revokePackageGrants(ctx context.Context, tx *sql.Tx, pkg reconcileGrantPackage) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
		SELECT g.uid, g.model, -g.amount_cny, 'revoke', 'plan_model_reconcile', g.id,
		       'paid plan model set updated'
		FROM commercial_quota_grants g
		WHERE g.uid = $1 AND g.plan_id = $2
		  AND g.grant_type = $3 AND g.source_ref = $4
		  AND g.revoked_at IS NULL
		  AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)`,
		pkg.uid, pkg.planID, pkg.grantType, pkg.sourceRef,
	); err != nil {
		return fmt.Errorf("record uid %d package revocation: %w", pkg.uid, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE commercial_quota_grants
		SET revoked_at = CURRENT_TIMESTAMP,
		    expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
		WHERE uid = $1 AND plan_id = $2
		  AND grant_type = $3 AND source_ref = $4
		  AND revoked_at IS NULL`,
		pkg.uid, pkg.planID, pkg.grantType, pkg.sourceRef,
	); err != nil {
		return fmt.Errorf("revoke uid %d package grants: %w", pkg.uid, err)
	}
	return nil
}

// grantPackageModel gives one buyer's package a model at the plan's per-model
// amount. Every model carries the same share of the shared pool, so rebuilding
// the whole set leaves the buyer's total exactly where it was.
func grantPackageModel(ctx context.Context, tx *sql.Tx, pkg reconcileGrantPackage, planID int64, planName, model string, amount float64) error {
	var grantID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO commercial_quota_grants(
			uid, plan_id, invite_code_id, grant_type, model, amount_cny,
			reset_duration, effective_at, expires_at, source_ref, note, operator_uid
		)
		VALUES ($1, $2, NULLIF($3, 0), $4, $5, $6, '1M', $7, $8, $9, $10, $11)
		RETURNING id`,
		pkg.uid, planID, pkg.inviteCodeID, pkg.grantType, model, amount,
		pkg.effectiveAt, pkg.expiresAt, pkg.sourceRef,
		"paid plan model auto-update", pkg.operatorUID,
	).Scan(&grantID); err != nil {
		return fmt.Errorf("grant %s to uid %d: %w", model, pkg.uid, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
		VALUES ($1, $2, $3, 'grant', 'plan_model_reconcile', $4, $5)`,
		pkg.uid, model, amount, grantID, planName,
	); err != nil {
		return fmt.Errorf("record %s grant for uid %d: %w", model, pkg.uid, err)
	}
	return nil
}

// reconcileGrantPackage identifies one buyer's active package: the grants that
// were created together for the same plan from the same source.
type reconcileGrantPackage struct {
	planID       int64
	uid          int64
	grantType    string
	sourceRef    string
	inviteCodeID int64
	operatorUID  *int64
	effectiveAt  sql.NullTime
	expiresAt    sql.NullTime
}

// reconcileGrantPackages lists the active paid packages for a plan. Only grant
// sources created from a plan's model set are considered; free and manual
// grants are the operator's own arrangements and stay untouched.
func reconcileGrantPackages(ctx context.Context, tx *sql.Tx, planID int64) ([]reconcileGrantPackage, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT g.uid, g.grant_type, g.source_ref,
		       COALESCE(MAX(g.invite_code_id), 0),
		       MAX(g.operator_uid),
		       MIN(g.effective_at),
		       MAX(g.expires_at)
		FROM commercial_quota_grants g
		WHERE g.plan_id = $1
		  AND g.grant_type IN ('order', 'invite', 'operator_plan')
		  AND g.revoked_at IS NULL
		  AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
		GROUP BY g.uid, g.grant_type, g.source_ref`, planID)
	if err != nil {
		return nil, fmt.Errorf("list paid plan packages: %w", err)
	}
	defer rows.Close()
	var packages []reconcileGrantPackage
	for rows.Next() {
		pkg := reconcileGrantPackage{planID: planID}
		if err := rows.Scan(
			&pkg.uid, &pkg.grantType, &pkg.sourceRef, &pkg.inviteCodeID,
			&pkg.operatorUID, &pkg.effectiveAt, &pkg.expiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan paid plan package: %w", err)
		}
		packages = append(packages, pkg)
	}
	return packages, rows.Err()
}

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

// commercialImageModelPrefixes identifies the image lane inside a plan's model
// budgets.
//
// A paid plan carries two kinds of model: chat models that share one pool of
// points, and image models that each hold a small fixed allowance because the
// relay bills images per request rather than per token (a plan grants 100 per
// image model against thousands per chat model). The two lanes are re-split
// separately so an image model can never inherit a chat-sized allowance. These
// are name prefixes rather than a model list, so a new image model is
// classified correctly without a code change.
var commercialImageModelPrefixes = []string{"gpt-image-", "chatgpt-image-"}

func isCommercialImageModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range commercialImageModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
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

// storedBudget keeps a plan's own spelling of a model alongside its amount, so
// a case-insensitive match still writes back the relay's canonical spelling.
type storedBudget struct {
	spelling string
	amount   float64
}

// reconcilePlanModelBudgets computes the model set a plan should carry. It
// returns nil when the plan already matches, which is what keeps a steady-state
// startup free of writes.
//
// The plan's advertised total is preserved and the two lanes are re-split
// separately:
//
//   - the chat models keep their combined allowance and share it evenly, so
//     five models at 2100 become six at 1750 and the buyer's pool is unchanged
//   - each image model keeps its own small allowance, and a newly added image
//     model is paid for out of the chat pool so the total still holds
//
// A retired model's share stays in its lane rather than leaving the plan: a
// model dropping out re-splits the remaining allowance, the same way the
// DeepSeek V4 retirement turned six chat models at 1750 into five at 2100.
func reconcilePlanModelBudgets(current map[string]float64, catalog []string, autoUpdateModels bool) map[string]float64 {
	existing := make(map[string]storedBudget, len(current))
	// The plan's present total is the anchor: whatever the model set becomes,
	// this is what the plan advertises and what a buyer's pool must stay at. A
	// retired model's share therefore returns to the flexible lane rather than
	// leaving the plan, which is exactly what the DeepSeek V4 retirement did
	// (six chat models at 1750 became five at 2100).
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
	chatCount := 0
	imageCount := 0
	imageAllowance := 0.0
	changed := false
	for _, model := range catalog {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" {
			// "*" is the relay's monthly-pool wildcard, not a sellable model.
			continue
		}
		stored, ok := existing[strings.ToLower(model)]
		if isCommercialImageModel(model) {
			switch {
			case ok:
				out[model] = stored.amount
				imageAllowance = stored.amount
				imageCount++
			case autoUpdateModels && imageAllowance > 0:
				// Inherit the lane's per-model allowance; the cost is covered by
				// the plan's existing total, so the advertised allowance holds.
				out[model] = imageAllowance
				imageCount++
				changed = true
			}
			continue
		}
		if !ok && !autoUpdateModels {
			// The plan pins its model set, so a new relay model is not added.
			continue
		}
		if !ok {
			changed = true
		}
		out[model] = 0
		chatCount++
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
	if !changed || chatCount == 0 {
		return nil
	}

	// The image lane holds a fixed allowance per model; everything else is the
	// chat pool, shared evenly.
	chatPool := roundBudget(total - imageAllowance*float64(imageCount))
	if chatPool <= 0 {
		return nil
	}
	chatNames := make([]string, 0, chatCount)
	for model := range out {
		if !isCommercialImageModel(model) {
			chatNames = append(chatNames, model)
		}
	}
	sort.Strings(chatNames)
	share := roundBudget(chatPool / float64(len(chatNames)))
	assigned := 0.0
	for _, model := range chatNames {
		out[model] = share
		assigned += share
	}
	// Push the rounding remainder onto the first model so the sum is exactly
	// the pool: a restart must not drift the plan's advertised allowance.
	if remainder := roundBudget(chatPool - assigned); remainder != 0 {
		out[chatNames[0]] = roundBudget(out[chatNames[0]] + remainder)
	}
	// Drop anything left without an allowance rather than storing a zero budget,
	// which the relay reads as "model not configured".
	for model, amount := range out {
		if amount <= 0 {
			delete(out, model)
		}
	}
	if len(out) == 0 {
		return nil
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
// plan alone would leave existing buyers without the new model. Only the models
// that actually changed are touched, so a grant an operator adjusted by hand is
// left alone.
func reconcileActivePlanGrants(ctx context.Context, tx *sql.Tx, planID int64, planName string, before, after map[string]float64) error {
	added, removed := reconcileModelDelta(before, after)
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	if len(removed) > 0 {
		if err := revokeReconciledPlanGrants(ctx, tx, planID, removed); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		if err := grantReconciledPlanModels(ctx, tx, planID, planName, added, after); err != nil {
			return err
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

// revokeReconciledPlanGrants retires grants for models the relay dropped. The
// ledger keeps a negative entry so the buyer's history explains why the model
// disappeared from their package.
func revokeReconciledPlanGrants(ctx context.Context, tx *sql.Tx, planID int64, removed []string) error {
	for _, model := range removed {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			SELECT g.uid, g.model, -g.amount_cny, 'revoke', 'plan_model_reconcile', g.id,
			       'retired relay model removed from paid plan'
			FROM commercial_quota_grants g
			WHERE g.plan_id = $1
			  AND g.model = $2
			  AND g.grant_type IN ('order', 'invite', 'operator_plan')
			  AND g.revoked_at IS NULL
			  AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)`,
			planID, model,
		); err != nil {
			return fmt.Errorf("record %s grant revocation: %w", model, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE commercial_quota_grants
			SET revoked_at = CURRENT_TIMESTAMP,
			    expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
			WHERE plan_id = $1
			  AND model = $2
			  AND grant_type IN ('order', 'invite', 'operator_plan')
			  AND revoked_at IS NULL`,
			planID, model,
		); err != nil {
			return fmt.Errorf("revoke %s grants: %w", model, err)
		}
	}
	return nil
}

// grantReconciledPlanModels gives current holders the models the relay added.
//
// The new model carries the plan's own allowance for it, so a buyer's usable
// quota matches the package they are on. Existing models are left alone: the
// plan's re-split only applies to a new purchase, and rewriting live grants
// would change what a buyer already holds.
func grantReconciledPlanModels(ctx context.Context, tx *sql.Tx, planID int64, planName string, added []string, budgets map[string]float64) error {
	packages, err := reconcileGrantPackages(ctx, tx, planID)
	if err != nil {
		return err
	}
	for _, pkg := range packages {
		for _, model := range added {
			amount := roundBudget(budgets[model])
			if amount <= 0 {
				continue
			}
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
		}
	}
	return nil
}

// reconcileGrantPackage identifies one buyer's active package: the grants that
// were created together for the same plan from the same source.
type reconcileGrantPackage struct {
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
		var pkg reconcileGrantPackage
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

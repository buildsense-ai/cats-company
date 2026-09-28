package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ReconcileCommercialPlanModels keeps every auto-updating plan's model set in
// step with the relay catalog.
//
// It replaces the startup migrations that used to hardcode the plan model lists.
// Those migrations had to be edited and redeployed every time the relay
// onboarded a model, and each one re-applied its literal list on every startup,
// so a hand-edited plan was silently reverted on the next restart. Reading the
// catalog instead means a new relay model reaches a plan with no control-plane
// change.
//
// The plans it covers are chosen by the plan's own switch rather than a list in
// this file: every non-archived plan with auto_update_models = true follows the
// relay. An operator creating an internal all-models package and leaving the
// switch on gets the whole catalog, and an operator who unchecks it pins exactly
// the models they picked. Keeping a slug list here instead meant a plan the
// operator had already opted in was silently skipped, so its buyers held quota
// for models that were never added to their grants.
//
// Two behaviours, both driven by that switch:
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
// ReconcileCommercialPlanModels keeps every auto-updating plan's model set in
// step with the relay catalog, given only the sellable list.
//
// It is the narrow entry point: without the internal-only list the caller cannot
// describe what the relay keeps off the shelf, so the pass is refused rather
// than run blind. Callers that read the catalog should use
// ReconcileCommercialPlanModelsWithInternal.
func (a *Adapter) ReconcileCommercialPlanModels(ctx context.Context, catalog []string) error {
	return a.ReconcileCommercialPlanModelsWithInternal(ctx, catalog, nil, false)
}

// errReconcileCatalogUnknown marks a relay catalog that predates the
// internal-only split. It is returned instead of running the pass so the caller
// can log it as a skipped reconcile rather than a failure.
var errReconcileCatalogUnknown = errors.New("relay model catalog does not classify internal-only models")

// ReconcileCommercialPlanModelsWithInternal is ReconcileCommercialPlanModels
// plus the relay's internal-only list and whether that list is trustworthy.
//
// The extra list is what keeps an internal package's models from being stripped:
// those names are routable but deliberately absent from the sellable catalog, so
// without them the removal rule reads them as retired and drops them from the
// plan that grants them.
//
// internalOnlyKnown says whether the relay actually declared that list. An older
// relay answers without the field, and absence must not be read as "this relay
// sells everything it routes": acting on it would delete a model an internal
// package grants. When the list is unknown the whole pass is skipped - a plan
// keeps the models it has and gains none this startup - because the alternative
// is guessing which absent names are retired. The condition clears as soon as
// the relay reports the field.
func (a *Adapter) ReconcileCommercialPlanModelsWithInternal(ctx context.Context, catalog []string, internalOnly []string, internalOnlyKnown bool) error {
	if !internalOnlyKnown {
		return fmt.Errorf("relay catalog predates the internal-only split: %w", errReconcileCatalogUnknown)
	}
	models := normalizeReconcileModels(catalog)
	if len(models) == 0 {
		return fmt.Errorf("commercial model catalog is empty")
	}
	internalModels := normalizeReconcileModels(internalOnly)
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

	plans, err := reconcilePlanSlugs(ctx, tx)
	if err != nil {
		return err
	}
	for _, slug := range plans {
		if err := reconcileOfficialPlan(ctx, tx, slug, models, internalModels); err != nil {
			return err
		}
	}
	// Plans whose model set is deliberately fixed still need their buyer grants
	// aligned with the plan they bought. An operator-assigned package is built
	// from the plan as it stood at assignment time, so a later change - a model
	// added by the plan's own migration, or one retired - leaves its holder with
	// the old set: quota for a model the plan no longer sells, and none for the
	// ones it gained.
	if err := reconcileFixedPlanGrants(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit commercial plan reconcile: %w", err)
	}
	return nil
}

// reconcileFixedPlanGrants aligns the grants of plans that do not follow the
// catalog. It never rewrites their model budgets: only the link between a plan
// and what its holders were given is repaired.
func reconcileFixedPlanGrants(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, slug, name, model_budgets FROM commercial_plans
		WHERE archived_at IS NULL AND slug = $1
		ORDER BY id FOR UPDATE`, commercialFreePlanSlug)
	if err != nil {
		return fmt.Errorf("list fixed plans for grant reconcile: %w", err)
	}
	// Collect before writing: a tx is one connection, so running an INSERT while
	// this cursor is open fails with "driver: bad connection". The rows are few
	// (one per fixed plan) so buffering them costs nothing.
	type fixedPlan struct {
		id      int64
		slug    string
		name    string
		budgets map[string]float64
	}
	var plans []fixedPlan
	for rows.Next() {
		var (
			planID     int64
			slug       string
			planName   string
			rawBudgets []byte
		)
		if err := rows.Scan(&planID, &slug, &planName, &rawBudgets); err != nil {
			rows.Close()
			return fmt.Errorf("scan fixed plan for grant reconcile: %w", err)
		}
		if !reconcileSyncsGrantsSeparately(slug) {
			continue
		}
		plans = append(plans, fixedPlan{id: planID, slug: slug, name: planName, budgets: decodeModelBudgets(rawBudgets)})
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close fixed plans for grant reconcile: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read fixed plans for grant reconcile: %w", err)
	}

	for _, plan := range plans {
		// The amounts are the plan's own, not an even split: a repaired holder
		// must match every other holder of the same plan.
		models, amounts := reconcilePlanOwnShares(plan.budgets)
		if len(models) == 0 {
			continue
		}
		// The delta guard inside the catalog pass would short-circuit here - the
		// plan's set did not change - so the forced entry point is used instead.
		if err := reconcilePlanGrantsForced(ctx, tx, plan.id, plan.name, models, amounts); err != nil {
			return err
		}
	}
	return nil
}

// reconcileCoversPlan reports whether a plan follows the relay catalog.
//
// The official paid plans always follow it: their model set is a product
// decision, so a switch flipped by mistake must not freeze them. Every other
// plan is decided by its own switch, which is what lets an operator create an
// internal all-models package (switch on, gets the whole catalog) or a pinned
// package (switch off, keeps exactly the models they picked) with no code
// change. Free and legacy are excluded: Free is sold to every account and keeps
// a deliberately small model set, and legacy grants are historical records.
func reconcileCoversPlan(slug string, autoUpdateModels bool) bool {
	slug = strings.TrimSpace(slug)
	if slug == "" || slug == commercialFreePlanSlug || slug == commercialLegacyPlanSlug {
		return false
	}
	if commercialOfficialPlanTier(slug) != 0 {
		return true
	}
	return autoUpdateModels
}

// reconcileSyncsGrantsSeparately reports whether a plan's buyer grants are
// aligned with the plan even though its model set does not follow the catalog.
//
// Free is the case this exists for. Its model set is deliberately small and
// maintained by its own migration, but an operator-assigned free package is
// built from whatever the plan held at assignment time. When the plan later
// gains a model - or drops a retired one - those buyers keep the old set, so
// they hold quota for a model the plan no longer sells and lack the ones it
// gained. That is indistinguishable from the reconcile being broken, so the
// grant pass runs for Free too; only the model-set rewrite is skipped.
func reconcileSyncsGrantsSeparately(slug string) bool {
	return strings.TrimSpace(slug) == commercialFreePlanSlug
}

// reconcilePlanSlugs lists the plans that follow the relay.
//
// The query only narrows the candidate set; the decision itself is made by
// reconcileCoversPlan so the rule exists in one place. Duplicating the switch in
// SQL is how a plan ends up covered by one half and skipped by the other.
func reconcilePlanSlugs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT slug, auto_update_models FROM commercial_plans
		WHERE archived_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list plans for model reconcile: %w", err)
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var (
			slug       string
			autoUpdate bool
		)
		if err := rows.Scan(&slug, &autoUpdate); err != nil {
			return nil, fmt.Errorf("scan plan slug for model reconcile: %w", err)
		}
		if reconcileCoversPlan(slug, autoUpdate) {
			slugs = append(slugs, slug)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read plan slugs for model reconcile: %w", err)
	}
	return slugs, nil
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
func reconcileOfficialPlan(ctx context.Context, tx *sql.Tx, slug string, catalog, internalOnly []string) error {
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
	target := reconcilePlanModelBudgets(current, catalog, autoUpdateModels, internalOnly...)
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
//
// internalOnly names models the relay routes but deliberately does not sell. A
// plan that already holds one keeps it: the model is missing from the sellable
// catalog, and the removal rule below would otherwise read that as "the relay
// retired it" and strip it from the internal package that grants it. They are
// never added - only an operator puts one in a plan.
func reconcilePlanModelBudgets(current map[string]float64, catalog []string, autoUpdateModels bool, internalOnly ...string) map[string]float64 {
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

	// Models that must survive the removal pass even though they are not in the
	// sellable catalog. Only the ones the plan already holds are considered: this
	// keeps an internal package intact without letting a name be invented into a
	// plan that never carried it.
	keep := make(map[string]storedBudget, len(internalOnly))
	for _, model := range internalOnly {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if stored, ok := existing[key]; ok {
			keep[key] = stored
		}
	}

	out := make(map[string]float64, len(catalog)+len(keep))
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
	// Put back the internal-only models the plan already holds. They are absent
	// from the sellable catalog, so the loop above skipped them and the removal
	// pass below would drop them; the relay still routes them and the internal
	// package still grants them, so they stay and keep sharing the same pool.
	for _, stored := range keep {
		if _, ok := out[stored.spelling]; ok {
			continue
		}
		if _, ok := out[strings.ToLower(stored.spelling)]; ok {
			continue
		}
		out[stored.spelling] = 0
	}
	// A model the relay no longer sells leaves the plan even when the plan is
	// pinned: keeping it would advertise a model whose requests the relay
	// refuses. The internal-only models were put back above, so their own
	// presence in out is what exempts them; only a genuinely retired name
	// reaches here.
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
	// Split the plan's total over the new model set exactly the way
	// reconcilePlanModelBudgets split it for the plan, so the buyer's pool stays
	// equal to the plan total. The remainder has to be placed deliberately: when
	// the total does not divide evenly (11000 over 12 models), one model carries
	// the leftover. Reading a per-model amount out of the map instead would pick
	// a different model on every run, because Go randomises map iteration, and
	// the buyer's total would drift by that leftover.
	models, amounts := reconcileGrantShares(after)
	return reconcilePlanGrantsForced(ctx, tx, planID, planName, models, amounts)
}

// reconcilePlanGrantsForced rebuilds a plan's holder grants for the given model
// set, without the "did the set change" short-circuit.
//
// It exists for plans whose model set is fixed: their budgets do not move here,
// so a delta check would always report no change and the drift it is called to
// repair would survive every restart. The rebuild itself is the same pass, which
// keeps one implementation of the revocation.
//
// The amounts come from the caller rather than being recomputed here, because
// the two cases need different ones. A catalog-following plan has just been
// re-split evenly by reconcilePlanModelBudgets, so its grants take that split. A
// fixed plan keeps its own per-model amounts - Free grants 1000 for MiniMax-M2.7
// and 100 for an image model - and its holders already carry exactly those, so
// re-splitting evenly would leave one repaired package looking unlike every
// other holder of the same plan.
func reconcilePlanGrantsForced(ctx context.Context, tx *sql.Tx, planID int64, planName string, models []string, amounts map[string]float64) error {
	if len(models) == 0 {
		return nil
	}
	packages, err := reconcileGrantPackages(ctx, tx, planID)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return nil
	}

	for _, pkg := range packages {
		// A package already holding exactly the plan's set is left alone: this
		// pass runs on every startup, and rewriting a correct package would
		// churn the ledger and the grants for no reason.
		current, err := packageGrantModels(ctx, tx, pkg)
		if err != nil {
			return err
		}
		if reconcileGrantSetMatches(current, models) {
			continue
		}
		if err := revokePackageGrants(ctx, tx, pkg); err != nil {
			return err
		}
		for _, model := range models {
			if err := grantPackageModel(ctx, tx, pkg, planID, planName, model, amounts[model]); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileGrantShares splits a plan's total over its models for the grant
// rebuild, mirroring reconcilePlanModelBudgets. It returns the models in a
// stable order plus each model's amount.
//
// The split must be deterministic: the same plan must produce the same amounts
// on every startup, because it decides what every current buyer holds. Deriving
// it by reading one entry out of the map would not be, since Go randomises map
// iteration, and the model carrying the rounding remainder would change run to
// run.
func reconcileGrantShares(after map[string]float64) ([]string, map[string]float64) {
	models := make([]string, 0, len(after))
	total := 0.0
	for model, amount := range after {
		if amount > 0 {
			models = append(models, model)
			total += amount
		}
	}
	if total <= 0 || len(models) == 0 {
		return nil, nil
	}
	sort.Strings(models)
	share := roundBudget(total / float64(len(models)))
	amounts := make(map[string]float64, len(models))
	assigned := 0.0
	for _, model := range models {
		amounts[model] = share
		assigned += share
	}
	if remainder := roundBudget(total - assigned); remainder != 0 {
		amounts[models[0]] = roundBudget(amounts[models[0]] + remainder)
	}
	return models, amounts
}

// reconcilePlanOwnShares returns a plan's models with the plan's own per-model
// amounts, for the fixed-plan grant pass.
//
// It does not re-split the total. A catalog-following plan has just been
// re-split evenly, so its grants take that even split; a fixed plan keeps the
// allocation it was written with, and its holders already carry exactly those
// amounts. Re-splitting here would leave a repaired package looking unlike every
// other holder of the same plan - Free grants 1000 for MiniMax-M2.7 and 100 for
// an image model, and both must survive the repair.
func reconcilePlanOwnShares(plan map[string]float64) ([]string, map[string]float64) {
	models := make([]string, 0, len(plan))
	amounts := make(map[string]float64, len(plan))
	for model, amount := range plan {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" || amount <= 0 {
			continue
		}
		models = append(models, model)
		amounts[model] = amount
	}
	if len(models) == 0 {
		return nil, nil
	}
	sort.Strings(models)
	return models, amounts
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

// packageGrantModels reads the models one buyer's package currently holds. The
// rebuild compares against it so a package that already matches the plan is left
// untouched: the forced pass runs on every startup, and rewriting a correct
// package would churn both the grants and the ledger for no reason.
func packageGrantModels(ctx context.Context, tx *sql.Tx, pkg reconcileGrantPackage) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT model FROM commercial_quota_grants
		WHERE uid = $1 AND plan_id = $2
		  AND grant_type = $3 AND source_ref = $4
		  AND revoked_at IS NULL`,
		pkg.uid, pkg.planID, pkg.grantType, pkg.sourceRef)
	if err != nil {
		return nil, fmt.Errorf("read uid %d package models: %w", pkg.uid, err)
	}
	defer rows.Close()
	var models []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, fmt.Errorf("scan uid %d package model: %w", pkg.uid, err)
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read uid %d package models: %w", pkg.uid, err)
	}
	return models, nil
}

// reconcileGrantSetMatches reports whether a package already holds exactly the
// plan's models. Comparison is case-insensitive because a stored grant may use
// the plan's older spelling while the split uses the relay's canonical one.
func reconcileGrantSetMatches(current, want []string) bool {
	if len(current) != len(want) {
		return false
	}
	held := make(map[string]struct{}, len(current))
	for _, model := range current {
		held[strings.ToLower(strings.TrimSpace(model))] = struct{}{}
	}
	for _, model := range want {
		if _, ok := held[strings.ToLower(strings.TrimSpace(model))]; !ok {
			return false
		}
	}
	return true
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

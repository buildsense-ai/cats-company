package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	commercialBaselineFreeProfile   = "free"
	commercialBaselineLegacyProfile = "legacy"
	// relayQuotaMigrationRef marks a baseline written from a Relay budget
	// snapshot during the pre-commercial migration. Only these entries can be
	// repaired: an operator-created legacy pool never carries this reference.
	relayQuotaMigrationRef = "relay-quota-migration-v1"
)

type commercialBaselinePlan struct {
	slug        string
	name        string
	description string
	source      string
	sourceRef   string
}

func (a *Adapter) EnsureCommercialRelayBaseline(uid int64, profile string, budgets map[string]float64, startsAt time.Time) (bool, error) {
	if uid <= 0 {
		return false, fmt.Errorf("invalid commercial relay baseline uid")
	}
	normalized, err := normalizeCommercialBaselineBudgets(budgets)
	if err != nil {
		return false, err
	}
	if startsAt.IsZero() || startsAt.After(time.Now().UTC()) {
		startsAt = time.Now().UTC()
	} else {
		startsAt = startsAt.UTC()
	}

	tx, err := a.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin commercial relay baseline: %w", err)
	}
	defer tx.Rollback()

	var lockedUID int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE id = $1 FOR UPDATE`, uid).Scan(&lockedUID); err != nil {
		if err == sql.ErrNoRows {
			return false, store.ErrCommercialRelayUserMissing
		}
		return false, fmt.Errorf("lock commercial relay baseline user: %w", err)
	}

	var baselineExists bool
	if err := tx.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM commercial_entitlements
			WHERE uid = $1 AND source IN ('free','legacy') AND state = 'active'
		)`, uid).Scan(&baselineExists); err != nil {
		return false, fmt.Errorf("check commercial relay baseline: %w", err)
	}
	if baselineExists {
		return false, nil
	}

	var activeManualGrants int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM commercial_quota_grants
		WHERE uid = $1 AND grant_type = 'manual'
		  AND revoked_at IS NULL AND effective_at <= CURRENT_TIMESTAMP
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`, uid).Scan(&activeManualGrants); err != nil {
		return false, fmt.Errorf("check active manual commercial grants: %w", err)
	}
	plan := commercialBaselinePlanFor(profile)
	if plan.source == commercialBaselineLegacyProfile && len(normalized) == 0 && activeManualGrants == 0 {
		return false, fmt.Errorf("commercial relay legacy baseline has no manual quota")
	}
	if plan.source != commercialBaselineLegacyProfile && len(normalized) == 0 {
		return false, fmt.Errorf("commercial relay baseline has no quota")
	}

	budgetsJSON := []byte("{}")
	if plan.source == commercialBaselineFreeProfile {
		budgetsJSON, err = json.Marshal(normalized)
		if err != nil {
			return false, fmt.Errorf("encode commercial relay baseline: %w", err)
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO commercial_plans(
			slug, name, description, price_fen, currency, sale_state, purchase_limit,
			monthly_budget_cny, model_budgets, duration_days, state, sort_order
		) VALUES ($1, $2, $3, 0, 'CNY', 'hidden', 0, 0, $4::jsonb, 30, 0, 1000)
		ON CONFLICT (slug) DO NOTHING`, plan.slug, plan.name, plan.description, string(budgetsJSON)); err != nil {
		return false, fmt.Errorf("ensure commercial relay baseline plan: %w", err)
	}
	var planID, priceFen int64
	var planState int
	var saleState string
	if err := tx.QueryRow(`SELECT id, price_fen, state, sale_state FROM commercial_plans WHERE slug = $1`, plan.slug).Scan(&planID, &priceFen, &planState, &saleState); err != nil {
		return false, fmt.Errorf("load commercial relay baseline plan: %w", err)
	}
	if priceFen != 0 || planState != 0 || saleState != "hidden" {
		return false, fmt.Errorf("commercial relay baseline plan is not safe")
	}
	if _, err := tx.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, $3, $4, 'active', $5, NULL)`, uid, planID, plan.source, plan.sourceRef, startsAt); err != nil {
		return false, fmt.Errorf("create commercial relay baseline entitlement: %w", err)
	}

	models := make([]string, 0, len(normalized))
	for model := range normalized {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		amount := normalized[model]
		var grantID int64
		if err := tx.QueryRow(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, $3, $4, $5, '1M', $6, NULL, $7, $8)
			RETURNING id`, uid, planID, plan.source, model, amount, startsAt, plan.sourceRef, plan.description).Scan(&grantID); err != nil {
			return false, fmt.Errorf("create commercial relay baseline grant: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'grant', $4, $5, $6)`, uid, model, amount, plan.source, grantID, plan.name); err != nil {
			return false, fmt.Errorf("record commercial relay baseline grant: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit commercial relay baseline: %w", err)
	}
	return true, nil
}

func commercialBaselinePlanFor(profile string) commercialBaselinePlan {
	if strings.EqualFold(strings.TrimSpace(profile), commercialBaselineFreeProfile) {
		return commercialBaselinePlan{
			slug:        "catsco-free",
			name:        "免费版",
			description: "CatsCo 默认共享额度",
			source:      commercialBaselineFreeProfile,
			sourceRef:   "catsco-free-v1",
		}
	}
	return commercialBaselinePlan{
		slug:        "catsco-legacy-custom",
		name:        "内部保留套餐",
		description: "全量迁移前已存在的额度，按原值保留",
		source:      commercialBaselineLegacyProfile,
		sourceRef:   relayQuotaMigrationRef,
	}
}

// ListCommercialRelayLegacyBaselines returns every active legacy baseline the
// migration wrote, with its active grants and the signals that disqualify a
// repair: a paid entitlement, an operator manual grant, or a baseline written
// long after the account was created (a real migration baseline, not a
// signup-time Relay seed).
func (a *Adapter) ListCommercialRelayLegacyBaselines() ([]*types.CommercialRelayBaselineCandidate, error) {
	rows, err := a.db.Query(`
		SELECT e.uid, e.plan_id, COALESCE(p.slug, ''), e.source_ref, e.starts_at, u.created_at,
		       (SELECT count(*) FROM commercial_entitlements paid
		         WHERE paid.uid = e.uid AND paid.state = 'active'
		           AND paid.source NOT IN ('free','legacy')),
		       (SELECT count(*) FROM commercial_quota_grants manual
		         WHERE manual.uid = e.uid AND manual.revoked_at IS NULL
		           AND lower(manual.grant_type) = 'manual'
		           AND manual.effective_at <= CURRENT_TIMESTAMP
		           AND (manual.expires_at IS NULL OR manual.expires_at > CURRENT_TIMESTAMP)),
		       g.model, g.amount_cny
		FROM commercial_entitlements e
		JOIN users u ON u.id = e.uid
		LEFT JOIN commercial_plans p ON p.id = e.plan_id
		LEFT JOIN commercial_quota_grants g
		       ON g.uid = e.uid AND g.plan_id = e.plan_id AND g.revoked_at IS NULL
		      AND g.effective_at <= CURRENT_TIMESTAMP
		      AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
		WHERE e.state = 'active' AND e.source = 'legacy'
		  AND e.source_ref = $1
		ORDER BY e.uid, e.id, g.model`, relayQuotaMigrationRef)
	if err != nil {
		return nil, fmt.Errorf("list commercial relay legacy baselines: %w", err)
	}
	defer rows.Close()
	byUID := map[int64]*types.CommercialRelayBaselineCandidate{}
	order := make([]int64, 0)
	for rows.Next() {
		var (
			uid, planID              int64
			slug, sourceRef          string
			startsAt, userCreatedAt  time.Time
			paidActive, manualGrants int
			model                    sql.NullString
			amount                   sql.NullFloat64
		)
		if err := rows.Scan(&uid, &planID, &slug, &sourceRef, &startsAt, &userCreatedAt, &paidActive, &manualGrants, &model, &amount); err != nil {
			return nil, fmt.Errorf("scan commercial relay legacy baseline: %w", err)
		}
		candidate, ok := byUID[uid]
		if !ok {
			candidate = &types.CommercialRelayBaselineCandidate{
				UID:           uid,
				PlanID:        planID,
				PlanSlug:      slug,
				SourceRef:     sourceRef,
				StartsAt:      startsAt,
				UserCreatedAt: userCreatedAt,
				Budgets:       map[string]float64{},
				PaidActive:    paidActive,
				ManualGrants:  manualGrants,
			}
			byUID[uid] = candidate
			order = append(order, uid)
		}
		// A model may carry more than one active grant; only the summed amount
		// is meaningful, and a duplicate must not be silently overwritten.
		if model.Valid && model.String != "" && amount.Valid && amount.Float64 > 0 {
			candidate.Budgets[model.String] += amount.Float64
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate commercial relay legacy baselines: %w", err)
	}
	out := make([]*types.CommercialRelayBaselineCandidate, 0, len(order))
	for _, uid := range order {
		out = append(out, byUID[uid])
	}
	return out, nil
}

// RevokeCommercialRelayLegacyBaseline revokes the misclassified legacy
// baseline in one transaction: the active grants are offset in the ledger,
// then revoked, then the entitlement itself is revoked. The caller re-runs the
// Relay sync afterwards, which recreates the Free baseline because the account
// no longer holds an active free/legacy entitlement.
//
// It returns false when nothing matched, so retries are harmless.
func (a *Adapter) RevokeCommercialRelayLegacyBaseline(uid int64, note string) (bool, error) {
	if uid <= 0 {
		return false, fmt.Errorf("invalid commercial relay baseline repair uid")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin commercial relay baseline repair: %w", err)
	}
	defer tx.Rollback()

	var lockedUID int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE id = $1 FOR UPDATE`, uid).Scan(&lockedUID); err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("commercial relay baseline repair user not found")
		}
		return false, fmt.Errorf("lock commercial relay baseline repair user: %w", err)
	}

	var entitlementID, planID int64
	entitlements, err := tx.Query(`
		SELECT id, plan_id FROM commercial_entitlements
		WHERE uid = $1 AND state = 'active' AND source = 'legacy' AND source_ref = $2
		ORDER BY id
		FOR UPDATE`, uid, relayQuotaMigrationRef)
	if err != nil {
		return false, fmt.Errorf("lock commercial relay legacy baseline: %w", err)
	}
	type entitlementRow struct {
		id     int64
		planID int64
	}
	var rowsLocked []entitlementRow
	for entitlements.Next() {
		var item entitlementRow
		if err := entitlements.Scan(&item.id, &item.planID); err != nil {
			entitlements.Close()
			return false, fmt.Errorf("scan commercial relay legacy baseline: %w", err)
		}
		rowsLocked = append(rowsLocked, item)
	}
	if err := entitlements.Close(); err != nil {
		return false, fmt.Errorf("close commercial relay legacy baselines: %w", err)
	}
	if len(rowsLocked) == 0 {
		return false, nil
	}
	if len(rowsLocked) > 1 {
		// Ambiguous state: revoking one row would leave the others active with
		// no grants, which zeroes the account's Relay quota. Refuse instead.
		return false, fmt.Errorf("account has %d active legacy baselines; resolve manually", len(rowsLocked))
	}
	entitlementID, planID = rowsLocked[0].id, rowsLocked[0].planID

	// Re-check the disqualifiers inside the transaction so a concurrent
	// purchase or operator grant cannot be revoked by a stale preview.
	var paidActive int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM commercial_entitlements
		WHERE uid = $1 AND state = 'active' AND source NOT IN ('free','legacy')`, uid).Scan(&paidActive); err != nil {
		return false, fmt.Errorf("check paid entitlements before repair: %w", err)
	}
	if paidActive > 0 {
		return false, fmt.Errorf("account holds a paid entitlement")
	}
	var manualGrants int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM commercial_quota_grants
		WHERE uid = $1 AND revoked_at IS NULL AND lower(grant_type) = 'manual'
		  AND effective_at <= CURRENT_TIMESTAMP
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`, uid).Scan(&manualGrants); err != nil {
		return false, fmt.Errorf("check manual grants before repair: %w", err)
	}
	if manualGrants > 0 {
		return false, fmt.Errorf("account holds a manual quota grant")
	}

	rows, err := tx.Query(`
		SELECT id, model, amount_cny FROM commercial_quota_grants
		WHERE uid = $1 AND plan_id = $2 AND revoked_at IS NULL
		ORDER BY id
		FOR UPDATE`, uid, planID)
	if err != nil {
		return false, fmt.Errorf("lock repaired commercial grants: %w", err)
	}
	type grantRow struct {
		id     int64
		model  string
		amount float64
	}
	var grants []grantRow
	for rows.Next() {
		var item grantRow
		if err := rows.Scan(&item.id, &item.model, &item.amount); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan repaired commercial grant: %w", err)
		}
		grants = append(grants, item)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close repaired commercial grants: %w", err)
	}
	ids := make([]int64, 0, len(grants))
	for _, grant := range grants {
		ids = append(ids, grant.id)
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'revoke', 'relay_baseline_repair', $4, $5)`,
			uid, grant.model, -grant.amount, grant.id, strings.TrimSpace(note)); err != nil {
			return false, fmt.Errorf("record repaired commercial grant: %w", err)
		}
	}
	now := time.Now().UTC()
	if len(ids) > 0 {
		// Revoke exactly the rows whose reversal was just written, so a grant
		// inserted after the snapshot cannot lose its quota without an audit
		// entry.
		if _, err := tx.Exec(`
			UPDATE commercial_quota_grants
			SET revoked_at = $2, expires_at = LEAST(COALESCE(expires_at, $2), $2)
			WHERE id = ANY($1) AND revoked_at IS NULL`, ids, now); err != nil {
			return false, fmt.Errorf("revoke repaired commercial grants: %w", err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE commercial_entitlements SET state = 'revoked'
		WHERE id = $1 AND uid = $2 AND state = 'active'`, entitlementID, uid); err != nil {
		return false, fmt.Errorf("revoke repaired commercial entitlement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit commercial relay baseline repair: %w", err)
	}
	return true, nil
}

func normalizeCommercialBaselineBudgets(budgets map[string]float64) (map[string]float64, error) {
	normalized := map[string]float64{}
	for model, amount := range budgets {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" || amount <= 0 {
			continue
		}
		if math.IsNaN(amount) || math.IsInf(amount, 0) {
			return nil, fmt.Errorf("invalid commercial relay baseline amount")
		}
		normalized[model] = amount
	}
	return normalized, nil
}

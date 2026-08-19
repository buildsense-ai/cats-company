package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	commercialBaselineFreeProfile   = "free"
	commercialBaselineLegacyProfile = "legacy"
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
			return false, fmt.Errorf("commercial relay baseline user not found")
		}
		return false, fmt.Errorf("lock commercial relay baseline user: %w", err)
	}

	var baselineExists bool
	if err := tx.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM commercial_entitlements
			WHERE uid = $1 AND source IN ('free','legacy')
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
		sourceRef:   "relay-quota-migration-v1",
	}
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

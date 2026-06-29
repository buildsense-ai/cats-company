package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func encodeModelBudgets(value map[string]float64) ([]byte, error) {
	if value == nil {
		value = map[string]float64{}
	}
	return json.Marshal(value)
}

func decodeModelBudgets(raw []byte) map[string]float64 {
	if len(raw) == 0 {
		return map[string]float64{}
	}
	var out map[string]float64
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]float64{}
	}
	return out
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	v := value.Time
	return &v
}

func scanCommercialPlan(scanner interface {
	Scan(dest ...interface{}) error
}) (*types.CommercialPlan, error) {
	var plan types.CommercialPlan
	var budgets []byte
	if err := scanner.Scan(
		&plan.ID,
		&plan.Slug,
		&plan.Name,
		&plan.Description,
		&plan.MonthlyBudget,
		&budgets,
		&plan.DurationDays,
		&plan.State,
		&plan.SortOrder,
		&plan.CreatedAt,
		&plan.UpdatedAt,
	); err != nil {
		return nil, err
	}
	plan.ModelBudgets = decodeModelBudgets(budgets)
	return &plan, nil
}

func (a *Adapter) ListCommercialPlans(includeDisabled bool) ([]*types.CommercialPlan, error) {
	where := ""
	if !includeDisabled {
		where = "WHERE state = 0"
	}
	rows, err := a.db.Query(`
		SELECT id, slug, name, description, monthly_budget_cny, model_budgets, duration_days, state, sort_order, created_at, updated_at
		FROM commercial_plans
		` + where + `
		ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list commercial plans: %w", err)
	}
	defer rows.Close()
	var plans []*types.CommercialPlan
	for rows.Next() {
		plan, err := scanCommercialPlan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan commercial plan: %w", err)
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (a *Adapter) CreateCommercialPlan(plan *types.CommercialPlan) (int64, error) {
	if plan == nil {
		return 0, fmt.Errorf("commercial plan is nil")
	}
	budgets, err := encodeModelBudgets(plan.ModelBudgets)
	if err != nil {
		return 0, fmt.Errorf("encode model budgets: %w", err)
	}
	durationDays := plan.DurationDays
	if durationDays <= 0 {
		durationDays = 30
	}
	sortOrder := plan.SortOrder
	if sortOrder == 0 {
		sortOrder = 100
	}
	var id int64
	err = a.db.QueryRow(`
		INSERT INTO commercial_plans(slug, name, description, monthly_budget_cny, model_budgets, duration_days, state, sort_order)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		ON CONFLICT(slug) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			monthly_budget_cny = EXCLUDED.monthly_budget_cny,
			model_budgets = EXCLUDED.model_budgets,
			duration_days = EXCLUDED.duration_days,
			state = EXCLUDED.state,
			sort_order = EXCLUDED.sort_order
		RETURNING id`,
		strings.TrimSpace(plan.Slug),
		strings.TrimSpace(plan.Name),
		strings.TrimSpace(plan.Description),
		plan.MonthlyBudget,
		string(budgets),
		durationDays,
		plan.State,
		sortOrder,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create commercial plan: %w", err)
	}
	return id, nil
}

func scanCommercialInvite(scanner interface {
	Scan(dest ...interface{}) error
}) (*types.CommercialInviteCode, error) {
	var invite types.CommercialInviteCode
	var expiresAt sql.NullTime
	if err := scanner.Scan(
		&invite.ID,
		&invite.Code,
		&invite.PlanID,
		&invite.PlanSlug,
		&invite.PlanName,
		&invite.MaxRedemptions,
		&invite.RedeemedCount,
		&invite.State,
		&expiresAt,
		&invite.Note,
		&invite.CreatedByUID,
		&invite.CreatedAt,
		&invite.UpdatedAt,
	); err != nil {
		return nil, err
	}
	invite.ExpiresAt = nullableTime(expiresAt)
	return &invite, nil
}

func (a *Adapter) ListCommercialInviteCodes(limit int) ([]*types.CommercialInviteCode, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.db.Query(`
		SELECT c.id, c.code, c.plan_id, p.slug, p.name, c.max_redemptions, c.redeemed_count, c.state,
		       c.expires_at, c.note, COALESCE(c.created_by_uid, 0), c.created_at, c.updated_at
		FROM commercial_invite_codes c
		JOIN commercial_plans p ON p.id = c.plan_id
		ORDER BY c.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list commercial invite codes: %w", err)
	}
	defer rows.Close()
	var invites []*types.CommercialInviteCode
	for rows.Next() {
		invite, err := scanCommercialInvite(rows)
		if err != nil {
			return nil, fmt.Errorf("scan commercial invite code: %w", err)
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func (a *Adapter) CreateCommercialInviteCode(invite *types.CommercialInviteCode) (int64, error) {
	if invite == nil {
		return 0, fmt.Errorf("commercial invite code is nil")
	}
	maxRedemptions := invite.MaxRedemptions
	if maxRedemptions <= 0 {
		maxRedemptions = 1
	}
	var id int64
	err := a.db.QueryRow(`
		INSERT INTO commercial_invite_codes(code, plan_id, max_redemptions, state, expires_at, note, created_by_uid)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, 0))
		ON CONFLICT(code) DO UPDATE SET
			plan_id = EXCLUDED.plan_id,
			max_redemptions = EXCLUDED.max_redemptions,
			state = EXCLUDED.state,
			expires_at = EXCLUDED.expires_at,
			note = EXCLUDED.note,
			created_by_uid = EXCLUDED.created_by_uid
		RETURNING id`,
		strings.ToUpper(strings.TrimSpace(invite.Code)),
		invite.PlanID,
		maxRedemptions,
		invite.State,
		invite.ExpiresAt,
		strings.TrimSpace(invite.Note),
		invite.CreatedByUID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create commercial invite code: %w", err)
	}
	return id, nil
}

func (a *Adapter) GrantCommercialQuota(grant *types.CommercialQuotaGrant) (*types.CommercialQuotaGrant, error) {
	if grant == nil {
		return nil, fmt.Errorf("commercial quota grant is nil")
	}
	model := strings.TrimSpace(grant.Model)
	if model == "" {
		model = "*"
	}
	grantType := strings.TrimSpace(grant.GrantType)
	if grantType == "" {
		grantType = "manual"
	}
	resetDuration := strings.TrimSpace(grant.ResetDuration)
	if resetDuration == "" {
		resetDuration = "1M"
	}
	effectiveAt := grant.EffectiveAt
	if effectiveAt.IsZero() {
		effectiveAt = time.Now().UTC()
	}
	var out types.CommercialQuotaGrant
	var expiresAt sql.NullTime
	err := a.db.QueryRow(`
		WITH created AS (
			INSERT INTO commercial_quota_grants(uid, plan_id, invite_code_id, grant_type, model, amount_cny, reset_duration, effective_at, expires_at, note, operator_uid)
			VALUES ($1, NULLIF($2, 0), NULLIF($3, 0), $4, $5, $6, $7, $8, $9, $10, NULLIF($11, 0))
			RETURNING id, uid, COALESCE(plan_id, 0), COALESCE(invite_code_id, 0), grant_type, model, amount_cny, reset_duration, effective_at, expires_at, note, COALESCE(operator_uid, 0), created_at
		), ledger AS (
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			SELECT uid, model, amount_cny, 'grant', grant_type, id, note FROM created
		)
		SELECT * FROM created`,
		grant.UID,
		grant.PlanID,
		grant.InviteCodeID,
		grantType,
		model,
		grant.AmountCNY,
		resetDuration,
		effectiveAt,
		grant.ExpiresAt,
		strings.TrimSpace(grant.Note),
		grant.OperatorUID,
	).Scan(
		&out.ID,
		&out.UID,
		&out.PlanID,
		&out.InviteCodeID,
		&out.GrantType,
		&out.Model,
		&out.AmountCNY,
		&out.ResetDuration,
		&out.EffectiveAt,
		&expiresAt,
		&out.Note,
		&out.OperatorUID,
		&out.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("grant commercial quota: %w", err)
	}
	out.ExpiresAt = nullableTime(expiresAt)
	return &out, nil
}

func (a *Adapter) RedeemCommercialInvite(uid int64, code string) (*types.CommercialSummary, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if uid <= 0 || code == "" {
		return nil, fmt.Errorf("invalid invite redemption")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin invite redemption: %w", err)
	}
	defer tx.Rollback()

	var inviteID, planID int64
	var maxRedemptions, redeemedCount, inviteState, planState, durationDays int
	var expiresAt sql.NullTime
	var planSlug, planName string
	var monthlyBudget float64
	var budgetsRaw []byte
	err = tx.QueryRow(`
		SELECT c.id, c.plan_id, c.max_redemptions, c.redeemed_count, c.state, c.expires_at,
		       p.slug, p.name, p.monthly_budget_cny, p.model_budgets, p.duration_days, p.state
		FROM commercial_invite_codes c
		JOIN commercial_plans p ON p.id = c.plan_id
		WHERE lower(c.code) = lower($1)
		FOR UPDATE OF c`, code).Scan(
		&inviteID,
		&planID,
		&maxRedemptions,
		&redeemedCount,
		&inviteState,
		&expiresAt,
		&planSlug,
		&planName,
		&monthlyBudget,
		&budgetsRaw,
		&durationDays,
		&planState,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("invite code not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load invite code: %w", err)
	}
	if inviteState != 0 || planState != 0 {
		return nil, fmt.Errorf("invite code is disabled")
	}
	if expiresAt.Valid && time.Now().After(expiresAt.Time) {
		return nil, fmt.Errorf("invite code has expired")
	}
	if redeemedCount >= maxRedemptions {
		return nil, fmt.Errorf("invite code has no remaining redemptions")
	}
	var duplicate int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM commercial_entitlements
		WHERE uid = $1 AND source = 'invite' AND source_ref = $2`, uid, code).Scan(&duplicate); err != nil {
		return nil, fmt.Errorf("check invite redemption: %w", err)
	}
	if duplicate > 0 {
		return nil, fmt.Errorf("invite code already redeemed")
	}

	startsAt := time.Now().UTC()
	entitlementExpires := startsAt.AddDate(0, 0, durationDays)
	var entitlementID int64
	if err := tx.QueryRow(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'invite', $3, 'active', $4, $5)
		RETURNING id`, uid, planID, code, startsAt, entitlementExpires).Scan(&entitlementID); err != nil {
		return nil, fmt.Errorf("create commercial entitlement: %w", err)
	}
	modelBudgets := decodeModelBudgets(budgetsRaw)
	if monthlyBudget > 0 {
		modelBudgets["*"] = monthlyBudget
	}
	for model, amount := range modelBudgets {
		model = strings.TrimSpace(model)
		if model == "" || amount <= 0 {
			continue
		}
		var grantID int64
		if err := tx.QueryRow(`
			INSERT INTO commercial_quota_grants(uid, plan_id, invite_code_id, grant_type, model, amount_cny, reset_duration, effective_at, expires_at, note)
			VALUES ($1, $2, $3, 'invite', $4, $5, '1M', $6, $7, $8)
			RETURNING id`, uid, planID, inviteID, model, amount, startsAt, entitlementExpires, "invite "+code).Scan(&grantID); err != nil {
			return nil, fmt.Errorf("create invite quota grant: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'grant', 'invite', $4, $5)`, uid, model, amount, grantID, planName); err != nil {
			return nil, fmt.Errorf("create invite quota ledger: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE commercial_invite_codes SET redeemed_count = redeemed_count + 1 WHERE id = $1`, inviteID); err != nil {
		return nil, fmt.Errorf("update invite redemption count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit invite redemption: %w", err)
	}
	_ = entitlementID
	_ = planSlug
	return a.GetCommercialSummary(uid)
}

func scanCommercialEntitlement(rows *sql.Rows) (*types.CommercialEntitlement, error) {
	var item types.CommercialEntitlement
	var expiresAt sql.NullTime
	if err := rows.Scan(
		&item.ID,
		&item.UID,
		&item.PlanID,
		&item.PlanSlug,
		&item.PlanName,
		&item.Source,
		&item.SourceRef,
		&item.State,
		&item.StartsAt,
		&expiresAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	item.ExpiresAt = nullableTime(expiresAt)
	return &item, nil
}

func scanCommercialQuotaGrant(rows *sql.Rows) (*types.CommercialQuotaGrant, error) {
	var item types.CommercialQuotaGrant
	var expiresAt sql.NullTime
	if err := rows.Scan(
		&item.ID,
		&item.UID,
		&item.PlanID,
		&item.InviteCodeID,
		&item.GrantType,
		&item.Model,
		&item.AmountCNY,
		&item.ResetDuration,
		&item.EffectiveAt,
		&expiresAt,
		&item.Note,
		&item.OperatorUID,
		&item.CreatedAt,
	); err != nil {
		return nil, err
	}
	item.ExpiresAt = nullableTime(expiresAt)
	return &item, nil
}

func scanCommercialLedgerEntry(rows *sql.Rows) (*types.CommercialLedgerEntry, error) {
	var item types.CommercialLedgerEntry
	if err := rows.Scan(
		&item.ID,
		&item.UID,
		&item.Model,
		&item.AmountCNY,
		&item.EntryType,
		&item.SourceType,
		&item.SourceID,
		&item.Note,
		&item.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &item, nil
}

func (a *Adapter) GetCommercialSummary(uid int64) (*types.CommercialSummary, error) {
	plans, err := a.ListCommercialPlans(false)
	if err != nil {
		return nil, err
	}
	summary := &types.CommercialSummary{
		UID:           uid,
		Plans:         plans,
		TotalsByModel: map[string]float64{},
	}
	entitlementRows, err := a.db.Query(`
		SELECT e.id, e.uid, e.plan_id, p.slug, p.name, e.source, e.source_ref, e.state, e.starts_at, e.expires_at, e.created_at, e.updated_at
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		WHERE e.uid = $1 AND e.state = 'active' AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
		ORDER BY e.expires_at NULLS LAST, e.id DESC`, uid)
	if err != nil {
		return nil, fmt.Errorf("list commercial entitlements: %w", err)
	}
	for entitlementRows.Next() {
		item, err := scanCommercialEntitlement(entitlementRows)
		if err != nil {
			entitlementRows.Close()
			return nil, fmt.Errorf("scan commercial entitlement: %w", err)
		}
		summary.Entitlements = append(summary.Entitlements, item)
	}
	if err := entitlementRows.Close(); err != nil {
		return nil, err
	}

	grantRows, err := a.db.Query(`
		SELECT id, uid, COALESCE(plan_id, 0), COALESCE(invite_code_id, 0), grant_type, model, amount_cny,
		       reset_duration, effective_at, expires_at, note, COALESCE(operator_uid, 0), created_at
		FROM commercial_quota_grants
		WHERE uid = $1
		  AND effective_at <= CURRENT_TIMESTAMP
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		ORDER BY created_at DESC`, uid)
	if err != nil {
		return nil, fmt.Errorf("list commercial quota grants: %w", err)
	}
	for grantRows.Next() {
		item, err := scanCommercialQuotaGrant(grantRows)
		if err != nil {
			grantRows.Close()
			return nil, fmt.Errorf("scan commercial quota grant: %w", err)
		}
		summary.Grants = append(summary.Grants, item)
		summary.TotalsByModel[item.Model] += item.AmountCNY
		summary.TotalCNY += item.AmountCNY
	}
	if err := grantRows.Close(); err != nil {
		return nil, err
	}

	ledgerRows, err := a.db.Query(`
		SELECT id, uid, model, amount_cny, entry_type, source_type, COALESCE(source_id, 0), note, created_at
		FROM commercial_quota_ledger
		WHERE uid = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 20`, uid)
	if err != nil {
		return nil, fmt.Errorf("list commercial quota ledger: %w", err)
	}
	for ledgerRows.Next() {
		item, err := scanCommercialLedgerEntry(ledgerRows)
		if err != nil {
			ledgerRows.Close()
			return nil, fmt.Errorf("scan commercial quota ledger: %w", err)
		}
		summary.Ledger = append(summary.Ledger, item)
	}
	if err := ledgerRows.Close(); err != nil {
		return nil, err
	}
	return summary, nil
}

package postgres

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	commercialAdjustmentCredit = "increase"
	commercialAdjustmentDebit  = "decrease"
	commercialAdjustmentPlan   = "change_plan"
)

func (a *Adapter) RecordCommercialCycleReset(uid int64, operationID, note string, effectiveAt time.Time) (bool, time.Time, error) {
	operationID = strings.TrimSpace(operationID)
	if uid <= 0 || operationID == "" || len(operationID) > 128 {
		return false, time.Time{}, commercialAdjustmentError("invalid_request", "invalid commercial cycle reset")
	}
	if effectiveAt.IsZero() {
		effectiveAt = time.Now().UTC()
	} else {
		effectiveAt = effectiveAt.UTC()
	}
	tx, err := a.db.Begin()
	if err != nil {
		return false, time.Time{}, fmt.Errorf("begin commercial cycle reset: %w", err)
	}
	defer tx.Rollback()
	var lockedUID int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE id = $1 FOR UPDATE`, uid).Scan(&lockedUID); err == sql.ErrNoRows {
		return false, time.Time{}, commercialAdjustmentError("not_found", "commercial user not found")
	} else if err != nil {
		return false, time.Time{}, fmt.Errorf("lock commercial cycle reset user: %w", err)
	}
	marker := operationID + " | "
	var recordedAt time.Time
	err = tx.QueryRow(`
		SELECT created_at FROM commercial_quota_ledger
		WHERE uid = $1 AND source_type = 'operator_reset'
		  AND split_part(note, ' | ', 1) = $2
		ORDER BY id DESC LIMIT 1`, uid, operationID).Scan(&recordedAt)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return false, time.Time{}, fmt.Errorf("commit repeated commercial cycle reset: %w", err)
		}
		return false, recordedAt.UTC(), nil
	}
	if err != sql.ErrNoRows {
		return false, time.Time{}, fmt.Errorf("check repeated commercial cycle reset: %w", err)
	}
	if err := tx.QueryRow(`
		INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, note, created_at)
		VALUES ($1, '*', 0, 'reset', 'operator_reset', $2, $3)
		RETURNING created_at`, uid, marker+strings.TrimSpace(note), effectiveAt).Scan(&recordedAt); err != nil {
		return false, time.Time{}, fmt.Errorf("record commercial cycle reset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, time.Time{}, fmt.Errorf("commit commercial cycle reset: %w", err)
	}
	return true, recordedAt.UTC(), nil
}

func (a *Adapter) ApplyCommercialAccountAdjustment(adjustment *types.CommercialAccountAdjustment) (*types.CommercialAccountAdjustmentResult, error) {
	if adjustment == nil || adjustment.UID <= 0 {
		return nil, commercialAdjustmentError("invalid_request", "invalid commercial adjustment")
	}
	action := strings.ToLower(strings.TrimSpace(adjustment.Action))
	operationID := strings.TrimSpace(adjustment.OperationID)
	if operationID == "" || len(operationID) > 128 {
		return nil, commercialAdjustmentError("invalid_request", "operation_id is required")
	}
	now := adjustment.EffectiveAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin commercial account adjustment: %w", err)
	}
	defer tx.Rollback()

	var lockedUID int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE id = $1 FOR UPDATE`, adjustment.UID).Scan(&lockedUID); err == sql.ErrNoRows {
		return nil, commercialAdjustmentError("not_found", "commercial user not found")
	} else if err != nil {
		return nil, fmt.Errorf("lock commercial user: %w", err)
	}

	previousTotal, err := commercialActiveQuotaTotal(tx, adjustment.UID, now)
	if err != nil {
		return nil, err
	}
	result := &types.CommercialAccountAdjustmentResult{
		Action:           action,
		OperationID:      operationID,
		PreviousTotalCNY: previousTotal,
		NextTotalCNY:     previousTotal,
	}

	switch action {
	case commercialAdjustmentCredit, commercialAdjustmentDebit:
		if adjustment.AmountCNY <= 0 || math.IsNaN(adjustment.AmountCNY) || math.IsInf(adjustment.AmountCNY, 0) {
			return nil, commercialAdjustmentError("invalid_request", "adjustment amount must be positive")
		}
		grantType := "adjustment_credit"
		signedAmount := adjustment.AmountCNY
		entryType := "credit"
		if action == commercialAdjustmentDebit {
			grantType = "adjustment_debit"
			signedAmount = -adjustment.AmountCNY
			entryType = "debit"
		}
		if previousTotal+signedAmount < -0.0000005 {
			return nil, commercialAdjustmentError("insufficient_quota", "shared quota cannot be reduced below zero")
		}

		var existingID int64
		err = tx.QueryRow(`
			SELECT id FROM commercial_quota_grants
			WHERE uid = $1 AND grant_type = $2 AND source_ref = $3
			ORDER BY id DESC LIMIT 1`, adjustment.UID, grantType, operationID).Scan(&existingID)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("check repeated commercial adjustment: %w", err)
		}
		if existingID != 0 {
			result.NextTotalCNY = previousTotal
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit repeated commercial adjustment: %w", err)
			}
			return result, nil
		}
		if adjustment.ExpectedTotalCNY != nil && !commercialAmountEqual(previousTotal, *adjustment.ExpectedTotalCNY) {
			return nil, commercialAdjustmentError("stale_total", "shared quota changed after preview; refresh and try again")
		}

		expiresAt, err := commercialPrimaryPackageExpiry(tx, adjustment.UID, now)
		if err != nil {
			return nil, err
		}
		var grantID int64
		if err := tx.QueryRow(`
			INSERT INTO commercial_quota_grants(
				uid, grant_type, model, amount_cny, reset_duration, effective_at,
				expires_at, source_ref, note
			) VALUES ($1, $2, '*', $3, '1M', $4, $5, $6, $7)
			RETURNING id`, adjustment.UID, grantType, adjustment.AmountCNY, now, expiresAt, operationID, strings.TrimSpace(adjustment.Note)).Scan(&grantID); err != nil {
			return nil, fmt.Errorf("create commercial shared quota adjustment: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, '*', $2, $3, 'operator_adjustment', $4, $5)`,
			adjustment.UID, signedAmount, entryType, grantID, strings.TrimSpace(adjustment.Note)); err != nil {
			return nil, fmt.Errorf("record commercial shared quota adjustment: %w", err)
		}
		result.Applied = true
		result.NextTotalCNY = math.Max(0, previousTotal+signedAmount)
		result.ExpiresAt = expiresAt

	case commercialAdjustmentPlan:
		if adjustment.PlanID <= 0 {
			return nil, commercialAdjustmentError("invalid_request", "plan_id is required")
		}
		var repeatedStartsAt time.Time
		var repeatedExpiresAt sql.NullTime
		err := tx.QueryRow(`
			SELECT starts_at, expires_at FROM commercial_entitlements
			WHERE uid = $1 AND source = 'operator' AND source_ref = $2
			ORDER BY id DESC LIMIT 1`, adjustment.UID, operationID).Scan(&repeatedStartsAt, &repeatedExpiresAt)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("check repeated commercial plan change: %w", err)
		}
		if err == nil {
			repeatedStartsAt = repeatedStartsAt.UTC()
			result.CycleStartedAt = &repeatedStartsAt
			result.ExpiresAt = nullableTime(repeatedExpiresAt)
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit repeated commercial plan change: %w", err)
			}
			return result, nil
		}
		if adjustment.ExpectedTotalCNY != nil && !commercialAmountEqual(previousTotal, *adjustment.ExpectedTotalCNY) {
			return nil, commercialAdjustmentError("stale_total", "shared quota changed after preview; refresh and try again")
		}

		plan, err := scanCommercialPlan(tx.QueryRow(`
			SELECT `+commercialPlanColumns+` FROM commercial_plans
			WHERE id = $1 AND state = 0 FOR SHARE`, adjustment.PlanID))
		if err == sql.ErrNoRows {
			return nil, commercialAdjustmentError("invalid_plan", "selected commercial plan is unavailable")
		}
		if err != nil {
			return nil, fmt.Errorf("load selected commercial plan: %w", err)
		}
		if !commercialPlanHasQuota(plan) {
			return nil, commercialAdjustmentError("invalid_plan", "selected commercial plan has no quota")
		}
		var samePlan bool
		if err := tx.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM commercial_entitlements
				WHERE uid = $1 AND plan_id = $2 AND state = 'active'
				  AND starts_at <= $3 AND (expires_at IS NULL OR expires_at > $3)
			)`, adjustment.UID, adjustment.PlanID, now).Scan(&samePlan); err != nil {
			return nil, fmt.Errorf("check active commercial plan: %w", err)
		}
		if samePlan {
			return nil, commercialAdjustmentError("same_plan", "selected commercial plan is already active")
		}

		if err := revokeCommercialPackageState(tx, adjustment.UID, operationID, strings.TrimSpace(adjustment.Note), now); err != nil {
			return nil, err
		}
		expiresAt := commercialOperatorPlanExpiry(plan, now)
		if _, err := tx.Exec(`
			UPDATE commercial_quota_grants
			SET expires_at = $3
			WHERE uid = $1 AND plan_id IS NULL
			  AND grant_type IN ('adjustment_credit', 'adjustment_debit')
			  AND revoked_at IS NULL AND effective_at <= $2
			  AND (expires_at IS NULL OR expires_at > $2)`, adjustment.UID, now, expiresAt); err != nil {
			return nil, fmt.Errorf("align shared adjustments with replacement plan: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
			VALUES ($1, $2, 'operator', $3, 'active', $4, $5)`,
			adjustment.UID, plan.ID, operationID, now, expiresAt); err != nil {
			return nil, fmt.Errorf("create operator commercial entitlement: %w", err)
		}
		if err := createOperatorPlanGrants(tx, adjustment.UID, plan, operationID, strings.TrimSpace(adjustment.Note), now, expiresAt); err != nil {
			return nil, err
		}
		nextTotal, err := commercialActiveQuotaTotal(tx, adjustment.UID, now)
		if err != nil {
			return nil, err
		}
		result.Applied = true
		result.NextTotalCNY = nextTotal
		result.CycleStartedAt = &now
		result.ExpiresAt = expiresAt

	default:
		return nil, commercialAdjustmentError("invalid_request", "unsupported commercial adjustment action")
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit commercial account adjustment: %w", err)
	}
	return result, nil
}

func commercialAdjustmentError(code, message string) error {
	return &types.CommercialAdjustmentError{Code: code, Message: message}
}

func commercialAmountEqual(left, right float64) bool {
	return math.Abs(left-right) <= 0.0000005
}

func commercialQuotaGrantSignedAmount(grant *types.CommercialQuotaGrant) float64 {
	if grant == nil {
		return 0
	}
	if strings.EqualFold(strings.TrimSpace(grant.GrantType), "adjustment_debit") {
		return -grant.AmountCNY
	}
	return grant.AmountCNY
}

func commercialActiveQuotaTotal(tx *sql.Tx, uid int64, now time.Time) (float64, error) {
	var total float64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(
			CASE WHEN grant_type = 'adjustment_debit' THEN -amount_cny ELSE amount_cny END
		), 0)
		FROM commercial_quota_grants
		WHERE uid = $1 AND effective_at <= $2 AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > $2)`, uid, now).Scan(&total); err != nil {
		return 0, fmt.Errorf("load active commercial quota total: %w", err)
	}
	return total, nil
}

func commercialPrimaryPackageExpiry(tx *sql.Tx, uid int64, now time.Time) (*time.Time, error) {
	var expiresAt sql.NullTime
	err := tx.QueryRow(`
		SELECT e.expires_at
		FROM commercial_entitlements e
		WHERE e.uid = $1 AND e.state = 'active' AND e.starts_at <= $2
		  AND (e.expires_at IS NULL OR e.expires_at > $2)
		ORDER BY CASE WHEN e.source IN ('free', 'legacy') THEN 1 ELSE 0 END,
		         e.expires_at DESC NULLS LAST, e.starts_at DESC, e.id DESC
		LIMIT 1`, uid, now).Scan(&expiresAt)
	if err == sql.ErrNoRows {
		return nil, commercialAdjustmentError("no_package", "an active package is required")
	}
	if err != nil {
		return nil, fmt.Errorf("load active commercial package expiry: %w", err)
	}
	return nullableTime(expiresAt), nil
}

func commercialOperatorPlanExpiry(plan *types.CommercialPlan, now time.Time) *time.Time {
	if plan == nil || plan.Slug == "catsco-free" || plan.Slug == "catsco-legacy-custom" {
		return nil
	}
	days := plan.DurationDays
	if days <= 0 {
		days = 30
	}
	expiresAt := now.AddDate(0, 0, days)
	return &expiresAt
}

func revokeCommercialPackageState(tx *sql.Tx, uid int64, operationID, note string, now time.Time) error {
	rows, err := tx.Query(`
		SELECT id, model, amount_cny
		FROM commercial_quota_grants
		WHERE uid = $1 AND plan_id IS NOT NULL AND revoked_at IS NULL
		  AND effective_at <= $2 AND (expires_at IS NULL OR expires_at > $2)
		FOR UPDATE`, uid, now)
	if err != nil {
		return fmt.Errorf("lock replaced commercial package grants: %w", err)
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
			return fmt.Errorf("scan replaced commercial package grant: %w", err)
		}
		grants = append(grants, item)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close replaced commercial package grants: %w", err)
	}
	for _, grant := range grants {
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'revoke', 'operator_plan_change', $4, $5)`,
			uid, grant.model, -grant.amount, grant.id, strings.TrimSpace(note+" ["+operationID+"]")); err != nil {
			return fmt.Errorf("record replaced commercial package grant: %w", err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE commercial_quota_grants
		SET revoked_at = $2, expires_at = LEAST(COALESCE(expires_at, $2), $2)
		WHERE uid = $1 AND plan_id IS NOT NULL AND revoked_at IS NULL`, uid, now); err != nil {
		return fmt.Errorf("revoke replaced commercial package grants: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE commercial_entitlements
		SET state = 'revoked'
		WHERE uid = $1 AND state = 'active'`, uid); err != nil {
		return fmt.Errorf("revoke replaced commercial entitlements: %w", err)
	}
	return nil
}

func createOperatorPlanGrants(tx *sql.Tx, uid int64, plan *types.CommercialPlan, operationID, note string, startsAt time.Time, expiresAt *time.Time) error {
	budgets := make(map[string]float64, len(plan.ModelBudgets)+1)
	for model, amount := range plan.ModelBudgets {
		if model = strings.TrimSpace(model); model != "" && amount > 0 {
			budgets[model] = amount
		}
	}
	if plan.MonthlyBudget > 0 {
		budgets["*"] = plan.MonthlyBudget
	}
	models := make([]string, 0, len(budgets))
	for model := range budgets {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		amount := budgets[model]
		var grantID int64
		if err := tx.QueryRow(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, 'operator_plan', $3, $4, '1M', $5, $6, $7, $8)
			RETURNING id`, uid, plan.ID, model, amount, startsAt, expiresAt, operationID, note).Scan(&grantID); err != nil {
			return fmt.Errorf("create operator plan quota grant: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'grant', 'operator_plan', $4, $5)`, uid, model, amount, grantID, note); err != nil {
			return fmt.Errorf("record operator plan quota grant: %w", err)
		}
	}
	return nil
}

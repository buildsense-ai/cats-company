package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

func (a *Adapter) GetModelCreditEligibility(ctx context.Context, uid int64) (bool, int, int64, error) {
	if uid <= 0 {
		return false, 0, 0, fmt.Errorf("invalid uid")
	}
	var tier int
	var allowance int64
	err := a.db.QueryRowContext(ctx, `
		SELECT p.plan_tier, p.model_credit_units
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		WHERE e.uid = $1
		  AND e.state = 'active'
		  AND e.starts_at <= CURRENT_TIMESTAMP
		  AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
		ORDER BY
		  CASE WHEN e.source IN ('free', 'legacy') THEN 1 ELSE 0 END ASC,
		  e.expires_at DESC NULLS LAST,
		  e.starts_at DESC,
		  e.id DESC
		LIMIT 1`, uid).Scan(&tier, &allowance)
	if err == sql.ErrNoRows {
		return false, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, fmt.Errorf("load primary model credit eligibility: %w", err)
	}
	return allowance > 0, tier, allowance, nil
}

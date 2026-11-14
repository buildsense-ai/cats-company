package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GetModelCreditEligibility 返回 (enabled, plan_tier, allowance)：
//
//   - enabled 是「credit 身份」：这个用户是否进过 credit 体系（买过/被发过带 model
//     credit 的套餐，或账本里已经有记录）。套餐过期不改变身份，真正的 legacy 老用户
//     才是 false。Relay 侧依赖这个字段防止把 credit 用户放回老额度路径，所以它不能
//     再用「当下有没有可花额度」来表示。
//   - allowance 是当下主套餐的月额度；allowance == 0 表示此刻没有可用的 credit 权限
//     （过期/退订），调用方据此得到 allowed=false，但 enabled 仍然是 true。
func (a *Adapter) GetModelCreditEligibility(ctx context.Context, uid int64) (bool, int, int64, error) {
	if uid <= 0 {
		return false, 0, 0, fmt.Errorf("invalid uid")
	}
	// Bring the monthly bucket to the period `now` falls in before answering:
	// the relay must never be told that a stale period's allowance is spendable.
	// The reconcile is idempotent, so repeated eligibility polls are no-ops.
	if _, err := a.ReconcileModelCreditMonthlyPeriod(ctx, uid, time.Now().UTC()); err != nil {
		return false, 0, 0, fmt.Errorf("reconcile model credit monthly period: %w", err)
	}
	enabled, err := a.modelCreditTaskEnabled(ctx, uid)
	if err != nil {
		return false, 0, 0, fmt.Errorf("load model credit identity: %w", err)
	}
	var tier int
	var allowance int64
	err = a.db.QueryRowContext(ctx, `
		SELECT p.plan_tier, `+modelCreditEntitlementUnitsSQL+`
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id`+modelCreditEntitlementOrderJoinSQL+`
		WHERE e.uid = $1
		  AND e.state = 'active'
		  AND e.starts_at <= CURRENT_TIMESTAMP
		  AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
		-- Paid packages own the primary slot: an invite code (or a free/legacy
		-- baseline) may add gift credit but must never displace an active paid
		-- V2/V3 tier. Within the same class the highest tier wins, then the
		-- longest remaining period.
		--
		-- The allowance is the order snapshot for an order-sourced entitlement
		-- (modelCreditEntitlementUnitsSQL), never the plan's live column: zeroing
		-- or editing a package must not cancel an allowance the buyer already paid
		-- for, and must not turn them into a legacy account.
		ORDER BY
		  CASE WHEN e.source IN ('order','admin','internal') THEN 0 ELSE 1 END ASC,
		  p.plan_tier DESC,
		  e.expires_at DESC NULLS LAST,
		  e.starts_at DESC,
		  e.id DESC
		LIMIT 1`, uid).Scan(&tier, &allowance)
	if err == sql.ErrNoRows {
		// 没有生效中的订阅：权限没了，但身份还在（enabled 保持原值）。
		return enabled, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, fmt.Errorf("load primary model credit eligibility: %w", err)
	}
	return enabled, tier, allowance, nil
}

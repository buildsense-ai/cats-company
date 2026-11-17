package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
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
		  AND (`+modelCreditEntitlementUnitsSQL+` > 0 OR (e.source = 'order' AND o.id IS NULL))
		-- Only a confirmed zero-credit legacy entitlement may be skipped for
		-- top-up-only access. A missing order snapshot must keep its primary
		-- slot with zero allowance so damaged order data fails closed.
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
		// 没有生效中的订阅：先看账本里有没有「花钱买来的」点数（独立充值）。
		// 充值是永久可花的余额，不是月额度，所以它能在没有任何套餐的情况下
		// 单独给出一份权限；但只有 paid 桶算数 —— 赠送额度、以及一个从没进过
		// credit 体系的 legacy 用户（没有账户行、paid = 0）都不受影响，
		// 这样「legacy 不花钱也能拿到 credit 权限」不可能发生。
		paid, paidErr := a.modelCreditPaidUnits(ctx, uid)
		if paidErr != nil {
			return false, 0, 0, fmt.Errorf("load paid model credit for eligibility: %w", paidErr)
		}
		if paid > 0 {
			// 档位：充值不买套餐，所以按 Relay 梯度默认的第 1 档计价；它不授予
			// 任何机器，也不参与套餐主槽位的排序。
			return true, store.ModelCreditTaskPlanTierMin, paid, nil
		}
		// 权限没了，但身份还在（enabled 保持原值）。
		return enabled, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, fmt.Errorf("load primary model credit eligibility: %w", err)
	}
	return enabled, tier, allowance, nil
}

// modelCreditPaidUnits 读 paid 桶余额，不建账户行也不加锁：资格查询是只读入口，
// 一次读取绝不能把老用户迁进 credit 账本。
func (a *Adapter) modelCreditPaidUnits(ctx context.Context, uid int64) (int64, error) {
	var paid int64
	if err := a.db.QueryRowContext(ctx,
		`SELECT COALESCE(paid_units, 0) FROM model_credit_accounts WHERE uid = $1`, uid).Scan(&paid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return paid, nil
}

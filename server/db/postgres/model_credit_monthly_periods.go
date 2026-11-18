package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Subscription grants are idempotent by exact period start. A public finite
// invite snapshot can fund just its first term. Rollover preserves unspent
// units, but only an active qualifying subscription can make them spendable.

// Migration 000070, retained for fresh schemas and historical expire events.

const migrateModelCreditMonthlyPeriods = `
-- 月额度周期锚点：订单履约把月付套餐的到期时间对齐到自然月边界，日锚点保存在
-- 订阅上并在续费时沿用，避免 1/31 → 2/28 → 3/28 的永久漂移。
ALTER TABLE commercial_entitlements ADD COLUMN IF NOT EXISTS period_anchor_at TIMESTAMPTZ;

-- 邀请赠送额度是邀请自己的数量（默认 20 credit），不再从套餐 model_credit_units
-- 继承（V3 的 22500 不是邀请的面额）。
ALTER TABLE commercial_invite_codes ADD COLUMN IF NOT EXISTS model_credit_units BIGINT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF to_regclass('commercial_invite_codes') IS NULL THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'chk_commercial_invites_model_credit_units' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE commercial_invite_codes ADD CONSTRAINT chk_commercial_invites_model_credit_units CHECK (model_credit_units >= 0);
    END IF;
END $$;

-- 到期作废是独立事件：月额度当期有效、不结转，余额被清零这件事必须留痕，不能
-- 靠「重新发放一次」掩盖。expire 只带正数，且不含 request_id。
DO $$
BEGIN
    IF to_regclass('model_credit_ledger_events') IS NULL THEN
        RETURN;
    END IF;
    ALTER TABLE model_credit_ledger_events DROP CONSTRAINT IF EXISTS ck_model_credit_ledger_events_event_type;
    ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_event_type
        CHECK (event_type IN ('grant','reserve','settle','release','debt_accrued','debt_repaid','refund','expire'));
    ALTER TABLE model_credit_ledger_events DROP CONSTRAINT IF EXISTS ck_model_credit_ledger_events_request_scope;
    ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_request_scope
        CHECK (request_id IS NOT NULL OR event_type IN ('grant','debt_accrued','debt_repaid','refund','expire'));
END $$;

CREATE INDEX IF NOT EXISTS idx_commercial_entitlements_period_anchor
    ON commercial_entitlements (uid, period_anchor_at)
    WHERE period_anchor_at IS NOT NULL;
`

const modelCreditMonthlyGrantableSourcesSQL = `('order','admin','internal')`

const modelCreditMonthlySourceSQL = `(e.source IN ` + modelCreditMonthlyGrantableSourcesSQL + `
	OR (e.source = 'invite' AND e.invite_monthly_units IS NOT NULL AND e.expires_at IS NOT NULL))`

// Orders and qualifying invites must use their immutable entitlement snapshots.
// Legacy invites still derive model access (not monthly issuance) from the plan.
const modelCreditEntitlementUnitsSQL = `CASE
			WHEN e.source = 'order' THEN COALESCE(o.plan_model_credit_units, 0)
			WHEN e.source = 'invite' THEN COALESCE(e.invite_monthly_units, p.model_credit_units)
			ELSE p.model_credit_units END`

// modelCreditEntitlementOrderJoinSQL 把订单快照挂到 order 来源的权益上。uid/plan_id
// 也参与匹配：source_ref 只有在同一个买家、同一个套餐下才被当作订单号。
const modelCreditEntitlementOrderJoinSQL = `
		LEFT JOIN commercial_orders o
		       ON e.source = 'order' AND o.order_no = e.source_ref
		      AND o.uid = e.uid AND o.plan_id = e.plan_id`

type modelCreditMonthlyEntitlement struct {
	id            int64
	planID        int64
	tier          int
	units         int64
	startsAt      time.Time
	anchorAt      time.Time
	expiresAt     sql.NullTime
	firstTermOnly bool
}

// Paid subscriptions take precedence over invite terms while both are active.
func loadModelCreditMonthlyEntitlement(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (*modelCreditMonthlyEntitlement, error) {
	ent := modelCreditMonthlyEntitlement{}
	err := tx.QueryRowContext(ctx, `
		SELECT e.id, e.plan_id, p.plan_tier, `+modelCreditEntitlementUnitsSQL+`, e.starts_at,
		       COALESCE(e.period_anchor_at, e.starts_at), e.expires_at, e.source = 'invite'
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id`+modelCreditEntitlementOrderJoinSQL+`
		WHERE e.uid = $1
		  AND e.state = 'active'
		  AND e.starts_at <= $2
		  AND (e.expires_at IS NULL OR e.expires_at > $2)
		  AND `+modelCreditMonthlySourceSQL+`
		  AND `+modelCreditEntitlementUnitsSQL+` > 0
		ORDER BY (e.source = 'invite'), p.plan_tier DESC,
		         CASE WHEN e.source = 'invite' THEN e.starts_at END ASC,
		         e.expires_at DESC NULLS LAST, e.starts_at DESC, e.id DESC
		LIMIT 1`, uid, now.UTC()).Scan(
		&ent.id, &ent.planID, &ent.tier, &ent.units, &ent.startsAt, &ent.anchorAt, &ent.expiresAt, &ent.firstTermOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load model credit monthly entitlement: %w", err)
	}
	return &ent, nil
}

func modelCreditLedgerEventExists(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM model_credit_ledger_events WHERE event_key = $1)`, key).Scan(&exists); err != nil {
		return false, fmt.Errorf("check model credit ledger event %s: %w", key, err)
	}
	return exists, nil
}

func modelCreditEntitlementPeriod(ent *modelCreditMonthlyEntitlement, now time.Time) (time.Time, time.Time, bool) {
	if ent.firstTermOnly {
		return ent.startsAt, ent.expiresAt.Time, true
	}
	return store.ModelCreditMonthlyPeriodAt(ent.anchorAt, now)
}

// Reconciliation is serialized by the account row. In particular, do not read
// the current entitlement or last grant before waiting for an existing row lock.
// The no-account path must leave users who have no credit entitlement alone.
func reconcileModelCreditMonthlyPeriod(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (*store.ModelCreditAccount, bool, error) {
	if uid <= 0 {
		return nil, false, store.ErrModelCreditInvalid
	}
	now = now.UTC()
	account, exists, err := readModelCreditAccountForUpdate(ctx, tx, uid)
	if err != nil {
		return nil, false, err
	}
	ent, err := loadModelCreditMonthlyEntitlement(ctx, tx, uid, now)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		if ent == nil {
			return account, false, nil
		}
		account, err = lockModelCreditAccount(ctx, tx, uid)
		if err != nil {
			return nil, false, err
		}
		ent, err = loadModelCreditMonthlyEntitlement(ctx, tx, uid, now)
		if err != nil {
			return nil, false, err
		}
	}
	last, err := loadLastModelCreditMonthlyGrant(ctx, tx, uid, nil)
	if err != nil {
		return nil, false, err
	}
	if last != nil {
		if err := adoptModelCreditMonthlyLot(ctx, tx, account, last, true); err != nil {
			return nil, false, err
		}
	}
	changed, err := moveModelCreditMonthlyBalance(account, ent == nil && last != nil)
	if err != nil {
		return nil, false, err
	}
	if ent == nil {
		if !changed {
			return account, false, nil
		}
		return account, true, saveModelCreditAccount(ctx, tx, account, now)
	}
	periodStart, _, ok := modelCreditEntitlementPeriod(ent, now)
	if !ok || (last != nil && !periodStart.After(last.period)) {
		if changed {
			err = saveModelCreditAccount(ctx, tx, account, now)
		}
		return account, changed, err
	}
	key := store.ModelCreditMonthlyGrantKey(uid, ent.planID, periodStart)
	// The canonical key is a second guard against replay, including out-of-order
	// reconciliation where the latest event is not the latest calendar period.
	booked, err := modelCreditLedgerEventExists(ctx, tx, key)
	if err != nil {
		return nil, false, err
	}
	if booked {
		if changed {
			err = saveModelCreditAccount(ctx, tx, account, now)
		}
		return account, changed, err
	}
	repay := min(ent.units, account.DebtUnits)
	if err := addModelCreditMonthlyBalance(account, ent.units-repay, false); err != nil {
		return nil, false, err
	}
	account.DebtUnits -= repay
	if err := insertModelCreditMonthlyLot(ctx, tx, uid, key, ent.id, periodStart, ent.units-repay); err != nil {
		return nil, false, err
	}
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, false, err
	}
	if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventGrant,
		store.ModelCreditBucketMonthly, ent.units, nil); err != nil {
		return nil, false, err
	}
	if repay > 0 {
		debtKey := store.ModelCreditDebtRepaymentKey(fmt.Sprintf("monthly:%d:%d:%s",
			uid, ent.planID, store.ModelCreditMonthlyPeriodKeyDate(periodStart)))
		if err := recordModelCreditEvent(ctx, tx, debtKey, account, store.ModelCreditEventDebtRepaid,
			store.ModelCreditBucketDebt, repay, nil); err != nil {
			return nil, false, err
		}
	}
	return account, true, nil
}

// ReconcileModelCreditMonthlyPeriod 在独立事务里对账一个用户的月额度。资格查询、
// 余额查询在返回前调用它，确保响应里不会出现上一个周期的旧额度。
func (a *Adapter) ReconcileModelCreditMonthlyPeriod(ctx context.Context, uid int64, now time.Time) (*store.ModelCreditAccount, error) {
	if uid <= 0 || now.IsZero() {
		return nil, store.ErrModelCreditInvalid
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model credit monthly reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	account, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, uid, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model credit monthly reconcile: %w", err)
	}
	return account, nil
}

// ReconcileModelCreditMonthlyPeriods 是后台周期任务：只扫有付费 credit 订阅或
// 还有月额度余额的用户，逐个小事务对账，单用户失败不影响整批。
func (a *Adapter) ReconcileModelCreditMonthlyPeriods(ctx context.Context, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit <= 0 {
		return 0, store.ErrModelCreditInvalid
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT uid FROM (
			SELECT e.uid AS uid
			FROM commercial_entitlements e
			JOIN commercial_plans p ON p.id = e.plan_id`+modelCreditEntitlementOrderJoinSQL+`
			WHERE e.state = 'active'
			  AND e.starts_at <= $1
			  AND (e.expires_at IS NULL OR e.expires_at > $1)
			  AND `+modelCreditMonthlySourceSQL+`
			  AND `+modelCreditEntitlementUnitsSQL+` > 0
			UNION
			SELECT ac.uid FROM model_credit_accounts ac WHERE ac.monthly_units > 0
		) candidates
		ORDER BY uid
		LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("list model credit monthly reconcile candidates: %w", err)
	}
	uids := make([]int64, 0, limit)
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan model credit monthly reconcile candidate: %w", err)
		}
		uids = append(uids, uid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate model credit monthly reconcile candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close model credit monthly reconcile candidates: %w", err)
	}
	reconciled := 0
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return reconciled, err
		}
		changed, err := a.reconcileModelCreditMonthlyPeriodOne(ctx, uid, now)
		if err != nil {
			return reconciled, err
		}
		if changed {
			reconciled++
		}
	}
	return reconciled, nil
}

func (a *Adapter) reconcileModelCreditMonthlyPeriodOne(ctx context.Context, uid int64, now time.Time) (bool, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin model credit monthly reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, changed, err := reconcileModelCreditMonthlyPeriod(ctx, tx, uid, now)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit model credit monthly reconcile: %w", err)
	}
	return changed, nil
}

// activeCommercialCreditPackageAnchor returns the natural-month anchor the user's
// current period of this credit package was sold on. A same-package renewal keeps
// the original day-of-month instead of re-anchoring on the previous expiry, which
// is what would make 1/31 → 2/28 → 3/28 drift.
func activeCommercialCreditPackageAnchor(tx *sql.Tx, uid, planID int64, now time.Time) (time.Time, bool, error) {
	var anchor sql.NullTime
	err := tx.QueryRow(`
		SELECT COALESCE(period_anchor_at, starts_at)
		FROM commercial_entitlements
		WHERE uid = $1 AND plan_id = $2 AND state = 'active'
		  AND starts_at <= $3
		  AND (expires_at IS NULL OR expires_at >= $3)
		ORDER BY expires_at DESC NULLS LAST, id DESC
		LIMIT 1`, uid, planID, now.UTC()).Scan(&anchor)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load credit package period anchor: %w", err)
	}
	if !anchor.Valid {
		return time.Time{}, false, nil
	}
	return anchor.Time, true, nil
}

// GetModelCreditPeriod 回答 GET /api/relay/credits 需要的两个事实：用户此刻所在的
// 月额度窗口，以及下一期额度是否已经被某条已付订阅覆盖。
//
// 它是只读的：不建账户、不发额度、不改权益，也不做对账。窗口来自
// loadModelCreditMonthlyEntitlement 的口径（已开始、未过期、可发月额度的来源），
// 周期用这条订阅自己的锚点算（store.ModelCreditMonthlyPeriodAt），所以和真正发放
// 额度用的是同一套边界，1/31 → 2/28 → 3/31 不会漂移。
//
// 提前续费写的是「将来才开始」的权益行，loadModelCreditMonthlyEntitlement 看不到
// 它，因此 SubscriptionExpiresAt 永远是当下这一段的到期时间，而不是续费之后的；
// 续费只让 NextRefreshAt 变成可以确定。没有月订阅（邀请/免费/legacy）时返回
// (nil, nil)，不编造周期。
func (a *Adapter) GetModelCreditPeriod(ctx context.Context, uid int64, now time.Time) (*store.ModelCreditPeriod, error) {
	if uid <= 0 {
		return nil, store.ErrModelCreditInvalid
	}
	now = now.UTC()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin model credit period read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ent, err := loadModelCreditMonthlyEntitlement(ctx, tx, uid, now)
	if err != nil {
		return nil, err
	}
	if ent == nil {
		return nil, nil
	}
	periodStart, periodEnd, ok := modelCreditEntitlementPeriod(ent, now)
	if !ok {
		return nil, nil
	}
	period := &store.ModelCreditPeriod{
		MonthlyPeriodStartsAt: periodStart.UTC(),
		MonthlyPeriodEndsAt:   periodEnd.UTC(),
	}
	if ent.expiresAt.Valid {
		expiresAt := ent.expiresAt.Time.UTC()
		period.SubscriptionExpiresAt = &expiresAt
	}
	covered, err := commercialMonthlySubscriptionCovers(ctx, tx, uid, periodEnd)
	if err != nil {
		return nil, err
	}
	if covered {
		refreshAt := periodEnd.UTC()
		period.NextRefreshAt = &refreshAt
	}
	return period, nil
}

// commercialMonthlySubscriptionCovers 判断 at 时刻是否有一条付过钱的月订阅在生效：
// 来源集合与发放一致（order/admin/internal），套餐卖 model credit，且窗口覆盖 at。
// 恰好在本期边界到期的订阅不算覆盖——那正是「要先续费才会刷新」的情形。
func commercialMonthlySubscriptionCovers(ctx context.Context, tx *sql.Tx, uid int64, at time.Time) (bool, error) {
	var covers bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM commercial_entitlements e
			JOIN commercial_plans p ON p.id = e.plan_id`+modelCreditEntitlementOrderJoinSQL+`
			WHERE e.uid = $1
			  AND e.state = 'active'
			  AND `+modelCreditMonthlySourceSQL+`
			  AND e.starts_at <= $2
			  AND (e.expires_at IS NULL OR e.expires_at > $2)
			  AND `+modelCreditEntitlementUnitsSQL+` > 0
		)`, uid, at.UTC()).Scan(&covers); err != nil {
		return false, fmt.Errorf("check model credit monthly refresh coverage: %w", err)
	}
	return covers, nil
}

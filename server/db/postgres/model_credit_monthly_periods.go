package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 月额度周期（docs/credit-plan-rules-20261109.md P0-2/P0-5）：
//
//   - 只有真正付过钱的订阅（order/admin/internal）发月额度；邀请码只发一次 gift，
//     不继承套餐的 model_credit_units，也不会把历史免费用户迁进 credit 账本。
//   - 周期是锚定最初订阅时刻的自然月（store.ModelCreditMonthlyPeriodAt），提前续费
//     只延长 expires_at，不会提前发下一期额度；下一期开始时才发。
//   - 发放是幂等的：账本事件键 grant:monthly:<uid>:<planID>:<周期起点 UTC 瞬时>
//     （store.ModelCreditMonthlyGrantKey，精确到纳秒，形如
//     20270331T100000.000000000Z）把「哪个周期」写进主键，重复执行只会命中同一行。
//     HEAD 之前的账本没有月额度发放，所以线上不存在「只有日期、没有时刻」的历史键；
//     万一读到解析不了的同前缀键，lastModelCreditMonthlyGrant 会跳过它继续找。
//   - 不结转：跨周期时把上一个周期剩下的月额度作废（expire 事件），清零后才发新
//     周期，避免用户拿旧周期的额度当新额度花。
//   - 跨期释放不挪用：请求在旧周期预留、在新周期才结算/释放时，退回的月额度不是
//     本周期的钱，直接作废而不是塞回 monthly。
//
// 这套 SQL 是迁移 000070 的内联副本（CreateSchema 不执行迁移文件）。

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

// modelCreditMonthlyGrantableSourcesSQL 必须与 store.ModelCreditMonthlyGrantableSource
// 保持一致：邀请/免费/历史基线都不发月额度。
const modelCreditMonthlyGrantableSourcesSQL = `('order','admin','internal')`

type modelCreditMonthlyEntitlement struct {
	id        int64
	planID    int64
	tier      int
	units     int64
	startsAt  time.Time
	anchorAt  time.Time
	expiresAt sql.NullTime
}

// loadModelCreditMonthlyEntitlement 取用户当前应发月额度的订阅（最高档优先）。
// 邀请码/免费订阅被排除：它们只是权限，不是月额度来源。
func loadModelCreditMonthlyEntitlement(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (*modelCreditMonthlyEntitlement, error) {
	ent := modelCreditMonthlyEntitlement{}
	err := tx.QueryRowContext(ctx, `
		SELECT e.id, e.plan_id, p.plan_tier, p.model_credit_units, e.starts_at,
		       COALESCE(e.period_anchor_at, e.starts_at), e.expires_at
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		WHERE e.uid = $1
		  AND e.state = 'active'
		  AND e.starts_at <= $2
		  AND (e.expires_at IS NULL OR e.expires_at > $2)
		  AND p.model_credit_units > 0
		  AND e.source IN `+modelCreditMonthlyGrantableSourcesSQL+`
		ORDER BY p.plan_tier DESC, e.expires_at DESC NULLS LAST, e.starts_at DESC, e.id DESC
		LIMIT 1`, uid, now.UTC()).Scan(
		&ent.id, &ent.planID, &ent.tier, &ent.units, &ent.startsAt, &ent.anchorAt, &ent.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load model credit monthly entitlement: %w", err)
	}
	return &ent, nil
}

// lastModelCreditMonthlyGrant 返回该用户最近一次月额度发放所属的周期起点。
//
// 同前缀里如果混进解析不了的键（历史格式、人为写坏的行），不能就此认为「从来没发
// 过」：那会在同一个周期里再发一次月额度。所以这里按时间倒序逐条解析，跳过解析失败
// 的键，取第一条能解析的。
func lastModelCreditMonthlyGrant(ctx context.Context, tx *sql.Tx, uid int64) (time.Time, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT event_key FROM model_credit_ledger_events
		WHERE uid = $1 AND bucket = 'monthly' AND event_type = 'grant'
		  AND event_key LIKE $2
		ORDER BY created_at DESC, event_key DESC
		LIMIT 50`, uid, store.ModelCreditMonthlyGrantKeyPrefix(uid)+"%")
	if err != nil {
		return time.Time{}, false, fmt.Errorf("load last model credit monthly grant: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return time.Time{}, false, fmt.Errorf("scan model credit monthly grant key: %w", err)
		}
		periodStart, ok := store.ParseModelCreditMonthlyGrantKey(uid, key)
		if !ok {
			continue
		}
		return periodStart, true, nil
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, fmt.Errorf("iterate model credit monthly grant keys: %w", err)
	}
	return time.Time{}, false, nil
}

// modelCreditMonthlyLedgerEntry 是一条待写入的账本变更。
type modelCreditMonthlyLedgerEntry struct {
	key       string
	eventType string
	bucket    string
	units     int64
}

func modelCreditLedgerEventExists(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM model_credit_ledger_events WHERE event_key = $1)`, key).Scan(&exists); err != nil {
		return false, fmt.Errorf("check model credit ledger event %s: %w", key, err)
	}
	return exists, nil
}

// expireModelCreditMonthlyBalance 作废月额度余额：余额清零并写一条 expire 事件。
// 键把周期起始日编在里面，重复执行不会再清零一次。
func expireModelCreditMonthlyBalance(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount,
	uid int64, periodStart, now time.Time) (bool, error) {
	if account == nil || account.MonthlyUnits <= 0 {
		return false, nil
	}
	key := store.ModelCreditMonthlyExpireKey(uid, periodStart)
	exists, err := modelCreditLedgerEventExists(ctx, tx, key)
	if err != nil || exists {
		return false, err
	}
	units := account.MonthlyUnits
	account.MonthlyUnits = 0
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return false, err
	}
	if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventExpire,
		store.ModelCreditBucketMonthly, units, nil); err != nil {
		return false, err
	}
	return true, nil
}

// reconcileModelCreditMonthlyPeriod 把用户的月额度对齐到 now 所在的周期：
// 作废旧周期剩余额度、发放本周期额度。发放/作废都用确定性事件键，所以后台扫描、
// 资格查询和预留并发跑也只会记账一次。返回最新账户与是否改动了账本。
//
// 只读入口（余额页 / 资格查询）也会走到这里，所以「有没有东西要对账」必须先判断：
// lockModelCreditAccount 是 upsert，如果无条件调用，一个从没进过 credit 体系的老
// 用户只要打开一次额度页面就会多出一行 model_credit_accounts —— 而 credit 任务门禁
// 正是拿这行账本账户当「进过 credit 体系」的证据，等于把老用户误迁进 credit 模型。
// 因此没有任何月度期限订阅、也没有任何月额度发放记录的用户，这里只读已有账户（没有
// 行就返回零值），不建账户、不写事件 —— 但该拿的行锁照拿，和并发的预留/结算串行。
// 调用方拿到的一定是非 nil 账户（旧行为），只是它可能还没落库。
func reconcileModelCreditMonthlyPeriod(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (*store.ModelCreditAccount, bool, error) {
	if uid <= 0 {
		return nil, false, store.ErrModelCreditInvalid
	}
	now = now.UTC()
	ent, err := loadModelCreditMonthlyEntitlement(ctx, tx, uid, now)
	if err != nil {
		return nil, false, err
	}
	lastPeriod, hasLast, err := lastModelCreditMonthlyGrant(ctx, tx, uid)
	if err != nil {
		return nil, false, err
	}
	if ent == nil && !hasLast {
		// 既没有付费 credit 订阅，也没有周期发放记录：没有可对账的东西，也不建账户。
		// 计划外来源（人工补发/买断）进来的余额同样不在这里静默清零，所以只读账户即可。
		account, _, err := readModelCreditAccountForUpdate(ctx, tx, uid)
		return account, false, err
	}
	account, err := lockModelCreditAccount(ctx, tx, uid)
	if err != nil {
		return nil, false, err
	}
	if ent == nil {
		// 没有有效的付费 credit 订阅（到期/退订）：剩下的月额度不能再花，也不结转。
		changed, err := expireModelCreditMonthlyBalance(ctx, tx, account, uid, lastPeriod, now)
		if err != nil {
			return nil, false, err
		}
		return account, changed, nil
	}
	periodStart, _, ok := store.ModelCreditMonthlyPeriodAt(ent.anchorAt, now)
	if !ok {
		return account, false, nil
	}
	if hasLast && store.ModelCreditMonthlyPeriodKeyDate(lastPeriod) >= store.ModelCreditMonthlyPeriodKeyDate(periodStart) {
		// 本周期已经发过：重复扫描、提前续费都不会再发一次额度。
		return account, false, nil
	}
	entries := make([]modelCreditMonthlyLedgerEntry, 0, 2)
	if account.MonthlyUnits > 0 && hasLast {
		key := store.ModelCreditMonthlyExpireKey(uid, lastPeriod)
		exists, err := modelCreditLedgerEventExists(ctx, tx, key)
		if err != nil {
			return nil, false, err
		}
		if !exists {
			entries = append(entries, modelCreditMonthlyLedgerEntry{
				key: key, eventType: store.ModelCreditEventExpire,
				bucket: store.ModelCreditBucketMonthly, units: account.MonthlyUnits,
			})
			account.MonthlyUnits = 0
		}
	}
	if ent.units > 0 {
		key := store.ModelCreditMonthlyGrantKey(uid, ent.planID, periodStart)
		exists, err := modelCreditLedgerEventExists(ctx, tx, key)
		if err != nil {
			return nil, false, err
		}
		if !exists {
			granted, ok := store.ModelCreditAddUnits(account.MonthlyUnits, ent.units)
			if !ok {
				return nil, false, fmt.Errorf("monthly credit grant overflows uid %d: %w", uid, store.ErrModelCreditInvalid)
			}
			account.MonthlyUnits = granted
			entries = append(entries, modelCreditMonthlyLedgerEntry{
				key: key, eventType: store.ModelCreditEventGrant,
				bucket: store.ModelCreditBucketMonthly, units: ent.units,
			})
			// 本期月额度先抵欠款（P0-2.4：月额度可以抵债，赠送额度不行）。抵债事件用
			// 「uid + plan + 周期」自己的键，重复对账不会再还一次。
			if repay := min(ent.units, account.DebtUnits); repay > 0 {
				debtKey := store.ModelCreditDebtRepaymentKey(fmt.Sprintf("monthly:%d:%d:%s",
					uid, ent.planID, store.ModelCreditMonthlyPeriodKeyDate(periodStart)))
				entries = append(entries, modelCreditMonthlyLedgerEntry{
					key: debtKey, eventType: store.ModelCreditEventDebtRepaid,
					bucket: store.ModelCreditBucketDebt, units: repay,
				})
				account.DebtUnits -= repay
				account.MonthlyUnits -= repay
			}
		}
	}
	if len(entries) == 0 {
		return account, false, nil
	}
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, false, err
	}
	for _, entry := range entries {
		if err := recordModelCreditEvent(ctx, tx, entry.key, account, entry.eventType, entry.bucket, entry.units, nil); err != nil {
			return nil, false, err
		}
	}
	return account, true, nil
}

// currentModelCreditMonthlyPeriodStart 返回请求发生时用户所在周期的起始日。
// 跨周期结算/释放时用它判断预留的月额度是否还属于当期。
func currentModelCreditMonthlyPeriodStart(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (time.Time, bool, error) {
	ent, err := loadModelCreditMonthlyEntitlement(ctx, tx, uid, now.UTC())
	if err != nil || ent == nil {
		return time.Time{}, false, err
	}
	start, _, ok := store.ModelCreditMonthlyPeriodAt(ent.anchorAt, now.UTC())
	return start, ok, nil
}

// Manual monthly grants without subscription-period history have no automatic
// expiry. Subscription holds return only within their original period.
func modelCreditMonthlyHoldExpired(ctx context.Context, tx *sql.Tx, uid int64, createdAt, now time.Time) (bool, error) {
	start, active, err := currentModelCreditMonthlyPeriodStart(ctx, tx, uid, now)
	if err != nil {
		return false, err
	}
	if active {
		return createdAt.Before(start), nil
	}
	_, subscriptionGrant, err := lastModelCreditMonthlyGrant(ctx, tx, uid)
	return subscriptionGrant, err
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
			JOIN commercial_plans p ON p.id = e.plan_id
			WHERE e.state = 'active'
			  AND e.starts_at <= $1
			  AND (e.expires_at IS NULL OR e.expires_at > $1)
			  AND p.model_credit_units > 0
			  AND e.source IN `+modelCreditMonthlyGrantableSourcesSQL+`
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

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.ModelCreditLedgerStore = (*Adapter)(nil)

// modelCreditAccountQuery reads one account row in bucket order.
const modelCreditAccountQuery = `SELECT monthly_units, gift_units, paid_units, debt_units,
	COALESCE(retained_task_id, ''), updated_at
	FROM model_credit_accounts WHERE uid = $1`

const modelCreditReservationQuery = `SELECT reserved_units, monthly_units, gift_units, paid_units,
	COALESCE(root_task_id, ''), unfunded_units,
	settled_units, state, created_at, lease_expires_at, no_usage_confirmed_at, settled_at, released_at
	FROM model_credit_reservations WHERE uid = $1 AND request_id = $2`

type modelCreditQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// lockModelCreditAccount creates the account row if needed and then holds its
// row lock for the rest of the transaction, which is what makes concurrent
// reserves/settles of one user serialize. Insert-then-lock instead of
// select-then-insert: the loser of ON CONFLICT DO NOTHING waits for the winner
// to commit rather than both creating a balance out of nothing.
func lockModelCreditAccount(ctx context.Context, tx *sql.Tx, uid int64) (*store.ModelCreditAccount, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO model_credit_accounts (uid) VALUES ($1) ON CONFLICT (uid) DO NOTHING`, uid); err != nil {
		return nil, fmt.Errorf("create model credit account: %w", err)
	}
	account := store.ModelCreditAccount{UID: uid}
	if err := tx.QueryRowContext(ctx, modelCreditAccountQuery+` FOR UPDATE`, uid).Scan(
		&account.MonthlyUnits, &account.GiftUnits, &account.PaidUnits, &account.DebtUnits,
		&account.RetainedTaskID, &account.UpdatedAt); err != nil {
		return nil, fmt.Errorf("lock model credit account: %w", err)
	}
	if err := account.Validate(); err != nil {
		return nil, fmt.Errorf("model credit account %d is inconsistent: %w", uid, err)
	}
	return &account, nil
}

func saveModelCreditAccount(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount, now time.Time) error {
	account.UpdatedAt = now.UTC()
	var retainedTaskID any
	if account.RetainedTaskID != "" {
		retainedTaskID = account.RetainedTaskID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_credit_accounts
		SET monthly_units = $2, gift_units = $3, paid_units = $4, debt_units = $5,
		    retained_task_id = $6, updated_at = $7
		WHERE uid = $1`,
		account.UID, account.MonthlyUnits, account.GiftUnits, account.PaidUnits, account.DebtUnits,
		retainedTaskID, account.UpdatedAt); err != nil {
		return fmt.Errorf("store model credit account: %w", err)
	}
	return nil
}

// readModelCreditAccountForUpdate 读并锁住已经存在的账户行，没有行时返回零值账户
// （UID 已填）而不插入。只读入口（余额页 / 资格查询）用它：既要和并发的预留/结算
// 串行，又不能因为一次读取就把老用户迁进 credit 账本。
func readModelCreditAccountForUpdate(ctx context.Context, tx *sql.Tx, uid int64) (*store.ModelCreditAccount, bool, error) {
	account := store.ModelCreditAccount{UID: uid}
	err := tx.QueryRowContext(ctx, modelCreditAccountQuery+` FOR UPDATE`, uid).Scan(
		&account.MonthlyUnits, &account.GiftUnits, &account.PaidUnits, &account.DebtUnits,
		&account.RetainedTaskID, &account.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &account, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock model credit account: %w", err)
	}
	if err := account.Validate(); err != nil {
		return nil, false, fmt.Errorf("model credit account %d is inconsistent: %w", uid, err)
	}
	return &account, true, nil
}

// recordModelCreditEvent appends one idempotent ledger row. The key is the
// primary key, so a repeated operation fails loudly instead of double booking;
// the balance columns are the account snapshot after the whole operation.
func recordModelCreditEvent(ctx context.Context, tx *sql.Tx, key string, account *store.ModelCreditAccount,
	eventType, bucket string, units int64, requestID *string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_credit_ledger_events
		(event_key, uid, bucket, event_type, units, request_id,
		 monthly_balance, gift_balance, paid_balance, debt_balance)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		key, account.UID, bucket, eventType, units, requestID,
		account.MonthlyUnits, account.GiftUnits, account.PaidUnits, account.DebtUnits); err != nil {
		return fmt.Errorf("record model credit event: %w", err)
	}
	return nil
}

func insertModelCreditReservation(ctx context.Context, tx *sql.Tx, reservation *store.ModelCreditReservation) error {
	var rootTaskID any
	if reservation.RootTaskID != "" {
		rootTaskID = reservation.RootTaskID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_credit_reservations
		(uid, request_id, root_task_id, reserved_units, monthly_units, gift_units, paid_units,
		 unfunded_units, state, created_at, lease_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		reservation.UID, reservation.RequestID, rootTaskID, reservation.ReservedUnits,
		reservation.Split.Monthly, reservation.Split.Gift, reservation.Split.Paid,
		reservation.UnfundedUnits, reservation.State, reservation.CreatedAt, reservation.LeaseExpiresAt); err != nil {
		return fmt.Errorf("insert model credit reservation: %w", err)
	}
	return nil
}

func readModelCreditReservation(ctx context.Context, q modelCreditQuerier, uid int64, requestID string, lock bool) (*store.ModelCreditReservation, error) {
	reservation := store.ModelCreditReservation{UID: uid, RequestID: requestID}
	query := modelCreditReservationQuery
	if lock {
		query += ` FOR UPDATE`
	}
	var settledUnits sql.NullInt64
	var leaseExpiresAt time.Time
	var noUsageConfirmedAt sql.NullTime
	var settledAt sql.NullTime
	var releasedAt sql.NullTime
	if err := q.QueryRowContext(ctx, query, uid, requestID).Scan(
		&reservation.ReservedUnits, &reservation.Split.Monthly, &reservation.Split.Gift, &reservation.Split.Paid,
		&reservation.RootTaskID, &reservation.UnfundedUnits,
		&settledUnits, &reservation.State, &reservation.CreatedAt, &leaseExpiresAt, &noUsageConfirmedAt,
		&settledAt, &releasedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrModelCreditNotFound
		}
		return nil, fmt.Errorf("read model credit reservation: %w", err)
	}
	reservation.LeaseExpiresAt = leaseExpiresAt
	if noUsageConfirmedAt.Valid {
		at := noUsageConfirmedAt.Time
		reservation.NoUsageConfirmedAt = &at
	}
	if settledUnits.Valid {
		units := settledUnits.Int64
		reservation.SettledUnits = &units
	}
	if settledAt.Valid {
		at := settledAt.Time
		reservation.SettledAt = &at
	}
	if releasedAt.Valid {
		at := releasedAt.Time
		reservation.ReleasedAt = &at
	}
	if err := reservation.Validate(); err != nil {
		return nil, fmt.Errorf("model credit reservation %q is inconsistent: %w", requestID, err)
	}
	return &reservation, nil
}

func settleModelCreditReservation(ctx context.Context, tx *sql.Tx, reservation *store.ModelCreditReservation, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE model_credit_reservations
		SET settled_units = $3, state = $4, settled_at = $5
		WHERE uid = $1 AND request_id = $2`,
		reservation.UID, reservation.RequestID, *reservation.SettledUnits,
		store.ModelCreditReservationSettled, now.UTC())
	if err != nil {
		return fmt.Errorf("settle model credit reservation: %w", err)
	}
	return nil
}

func releaseModelCreditReservation(ctx context.Context, tx *sql.Tx, reservation *store.ModelCreditReservation, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE model_credit_reservations
		SET state = $3, released_at = $4
		WHERE uid = $1 AND request_id = $2`,
		reservation.UID, reservation.RequestID, store.ModelCreditReservationReleased, now.UTC())
	if err != nil {
		return fmt.Errorf("release model credit reservation: %w", err)
	}
	return nil
}
func modelCreditBucketParts(split store.ModelCreditSplit) [3]struct {
	bucket string
	units  int64
} {
	return [3]struct {
		bucket string
		units  int64
	}{
		{store.ModelCreditBucketMonthly, split.Monthly},
		{store.ModelCreditBucketGift, split.Gift},
		{store.ModelCreditBucketPaid, split.Paid},
	}
}

// GrantModelCredits credits one bucket with a caller keyed, idempotent event.
// It is the only way money enters the ledger.
func (a *Adapter) GrantModelCredits(ctx context.Context, in store.ModelCreditGrantInput) (*store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	bucket, _ := store.NormalizeModelCreditGrantBucket(in.Bucket)
	key := store.ModelCreditGrantEventKey(in.IdempotencyKey)
	if len(key) > store.ModelCreditEventKeyMaxLen {
		return nil, store.ErrModelCreditInvalid
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model credit grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	account, err := lockModelCreditAccount(ctx, tx, in.UID)
	if err != nil {
		return nil, err
	}
	var bookedUID, bookedUnits int64
	var bookedBucket string
	switch err := tx.QueryRowContext(ctx,
		`SELECT uid, bucket, units FROM model_credit_ledger_events WHERE event_key = $1`, key).
		Scan(&bookedUID, &bookedBucket, &bookedUnits); {
	case err == nil:
		if bookedUID != in.UID || bookedBucket != bucket || bookedUnits != in.Units {
			return nil, store.ErrModelCreditConflict
		}
		// Replay: the credit is already booked, so the balance is unchanged.
		return account, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return nil, fmt.Errorf("read model credit grant event: %w", err)
	}
	var ok bool
	switch bucket {
	case store.ModelCreditBucketMonthly:
		account.MonthlyUnits, ok = store.ModelCreditAddUnits(account.MonthlyUnits, in.Units)
	case store.ModelCreditBucketGift:
		account.GiftUnits, ok = store.ModelCreditAddUnits(account.GiftUnits, in.Units)
	default:
		account.PaidUnits, ok = store.ModelCreditAddUnits(account.PaidUnits, in.Units)
	}
	if !ok {
		return nil, store.ErrModelCreditInvalid
	}
	// 充值/发额度先抵欠款（P0-2.4）：monthly 与 paid 先还债，gift 不抵债（P0-2.5）。
	// 抵债事件用自己的幂等键（共享入账键），所以重复回调不会把同一笔欠款还两次。
	repay, _, ok := store.PlanModelCreditGrant(*account, bucket, in.Units)
	if !ok || repay > account.DebtUnits {
		return nil, store.ErrModelCreditInvalid
	}
	var debtKey string
	if repay > 0 {
		debtKey = store.ModelCreditDebtRepaymentKey(in.IdempotencyKey)
		if len(debtKey) > store.ModelCreditEventKeyMaxLen {
			return nil, store.ErrModelCreditInvalid
		}
		// 抵债是从这个桶里搬走：桶里只留下真正可花的部分（P0-2.4）。事件仍然记录完整
		// 的入账额 + 单独一笔 debt_repaid，所以账对得上：进 50、还 20、可花 30。
		account.DebtUnits -= repay
		switch bucket {
		case store.ModelCreditBucketMonthly:
			account.MonthlyUnits -= repay
		case store.ModelCreditBucketGift:
			account.GiftUnits -= repay
		default:
			account.PaidUnits -= repay
		}
	}
	now := time.Now().UTC()
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, err
	}
	if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventGrant, bucket, in.Units, nil); err != nil {
		return nil, err
	}
	if repay > 0 {
		if err := recordModelCreditEvent(ctx, tx, debtKey, account,
			store.ModelCreditEventDebtRepaid, store.ModelCreditBucketDebt, repay, nil); err != nil {
			return nil, err
		}
	}
	// 补款/发额度不自动恢复任务：这里只让透支名额跟余额保持一致。
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model credit grant: %w", err)
	}
	return account, nil
}

// ReserveModelCredits holds units for one request in the fixed order monthly ->
// gift -> paid, or reports ErrModelCreditInsufficient. The hold is keyed by
// request id, so a replay returns the stored hold and a replay with a different
// amount is rejected as a conflict.
//
// 每笔预留都必须带 root_task_id，并且那个 root 必须已经 start 且当前 running：没有
// root、或 root 已被暂停/结束的调用会拿到明确错误，而不是绕过门禁继续花钱
// （P0-3.6/P0-3.9）。唯一的例外是「保留任务」：只有它可以把预占拆成「余额兜底的
// 部分」+「没有余额兜底的 unfunded 部分」，后者在结算时才落成真实欠款。
func (a *Adapter) ReserveModelCredits(ctx context.Context, in store.ModelCreditReserveInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin model credit reserve: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Before holding anything, bring the monthly bucket to the period `now` falls
	// in: a stale period's leftover is expired and the current period is granted,
	// so a request can never spend last month's allowance or miss this month's.
	// 对账同时拿到账户行锁，下面读任务状态因此不会和 resume/finish 交错。
	now := time.Now().UTC()
	account, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, in.UID, now)
	if err != nil {
		return nil, nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	switch {
	case err == nil:
		if reservation.State != store.ModelCreditReservationReserved || reservation.ReservedUnits != in.Units ||
			reservation.RootTaskID != in.RootTaskID {
			return nil, nil, store.ErrModelCreditConflict
		}
		// Replay: the hold already exists, so nothing is held twice.
		return reservation, account, nil
	case errors.Is(err, store.ErrModelCreditNotFound):
	default:
		return nil, nil, err
	}
	task, err := modelCreditTaskByID(ctx, tx, in.UID, in.RootTaskID)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, store.ErrModelCreditTaskNotFound
	}
	if task.State != store.ModelCreditTaskRunning {
		return nil, nil, store.ErrModelCreditTaskNotRunning
	}
	// 档位快照算不出费率（0，只可能来自本列上线前写下的旧行）：running 也不能继续花钱，
	// 失败关闭。绝不拿「现在的套餐」替旧任务临时猜一个档位。
	if !store.ModelCreditTaskPlanTierTrusted(task.PlanTier) {
		return nil, nil, store.ErrModelCreditTaskTierUnavailable
	}
	split, funded := store.AllocateModelCreditUnits(*account, in.Units)
	unfunded := int64(0)
	if !funded {
		// 额度不够：只有保留任务可以继续，它透支的部分先记成 unfunded 预占。
		if account.RetainedTaskID != in.RootTaskID {
			return nil, nil, store.ErrModelCreditInsufficient
		}
		partial, remaining := store.AllocateModelCreditUnitsPartial(*account, in.Units)
		split, unfunded = partial, remaining
		if unfunded <= 0 {
			// 余额刚好够（并发下可能在上面的判定之后补上），按正常预占处理。
			unfunded = 0
		}
	}
	account.MonthlyUnits -= split.Monthly
	account.GiftUnits -= split.Gift
	account.PaidUnits -= split.Paid
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	reservation = &store.ModelCreditReservation{
		UID:            in.UID,
		RequestID:      in.RequestID,
		RootTaskID:     in.RootTaskID,
		ReservedUnits:  in.Units,
		Split:          split,
		UnfundedUnits:  unfunded,
		State:          store.ModelCreditReservationReserved,
		CreatedAt:      now,
		LeaseExpiresAt: now.Add(store.ModelCreditReservationLease),
	}
	if err := reservation.Validate(); err != nil {
		return nil, nil, err
	}
	if err := insertModelCreditReservation(ctx, tx, reservation); err != nil {
		return nil, nil, err
	}
	for _, part := range modelCreditBucketParts(split) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventReserve, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventReserve, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	// 这一笔可能刚好把额度用光（含全被 hold 占住）：只有最近开始的 running 任务
	// 留下继续透支，其余立刻暂停（P0-3.1/P0-3.5）。
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit model credit reserve: %w", err)
	}
	return reservation, account, nil
}

// SettleModelCredits charges what the request actually cost. The unused part of
// the hold goes back to the buckets it came from (a cross-period monthly
// remainder is expired instead of carried).
//
// 已经派发出去的调用必须能结算（P0-3.2）：超出预占的部分先从当下真实余额扣，扣不动
// 的、以及保留任务用掉的 unfunded 预占，一律落成真实欠款 debt_accrued，而不是返回
// 错误让这笔钱凭空消失。重复结算（同一 request id）在状态判定处就返回，绝不会重复
// 记债。
func (a *Adapter) SettleModelCredits(ctx context.Context, in store.ModelCreditSettleInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin model credit settle: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Settling must see the period `now` falls in, not a stale bucket: an overage
	// is otherwise taken from last period's allowance, and a released hold could
	// be handed back into a period it never belonged to.
	now := time.Now().UTC()
	account, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, in.UID, now)
	if err != nil {
		return nil, nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	if err != nil {
		return nil, nil, err
	}
	if reservation.State == store.ModelCreditReservationSettled {
		if reservation.SettledUnits == nil || *reservation.SettledUnits != in.ActualUnits {
			return nil, nil, store.ErrModelCreditConflict
		}
		// Replay: the request is already settled for this exact amount.
		return reservation, account, nil
	}
	if reservation.State != store.ModelCreditReservationReserved {
		return nil, nil, store.ErrModelCreditConflict
	}
	consumed := store.ConsumeModelCreditSplit(reservation.Split, in.ActualUnits)
	released := store.ModelCreditSplit{
		Monthly: reservation.Split.Monthly - consumed.Monthly,
		Gift:    reservation.Split.Gift - consumed.Gift,
		Paid:    reservation.Split.Paid - consumed.Paid,
	}
	// A hold taken in one period and settled in the next must not hand last
	// period's monthly units back into the current period: they are expired, not
	// returned, so credit is neither reused across periods nor moved to someone
	// else's allowance.
	expiredMonthly := int64(0)
	if released.Monthly > 0 {
		expired, periodErr := modelCreditMonthlyHoldExpired(ctx, tx, in.UID, reservation.CreatedAt, now)
		if periodErr != nil {
			return nil, nil, periodErr
		}
		if expired {
			expiredMonthly, released.Monthly = released.Monthly, 0
		}
	}
	// 已派发的调用必须结算，所以这里不再因为余额不够而报错：先从当下真实余额扣，
	// 扣不动的部分（含保留任务用掉的 unfunded 预占）落成真实欠款。
	fundedTotal := reservation.Split.Total()
	unfundedUsed := min(reservation.UnfundedUnits, max(0, in.ActualUnits-fundedTotal))
	excess := max(0, in.ActualUnits-reservation.ReservedUnits)
	overage, uncovered := store.AllocateModelCreditUnitsPartial(*account, excess)
	debtAccrued := unfundedUsed + uncovered
	var ok bool
	if account.MonthlyUnits, ok = store.ModelCreditAddUnits(account.MonthlyUnits, released.Monthly); !ok {
		return nil, nil, store.ErrModelCreditInvalid
	}
	if account.GiftUnits, ok = store.ModelCreditAddUnits(account.GiftUnits, released.Gift); !ok {
		return nil, nil, store.ErrModelCreditInvalid
	}
	if account.PaidUnits, ok = store.ModelCreditAddUnits(account.PaidUnits, released.Paid); !ok {
		return nil, nil, store.ErrModelCreditInvalid
	}
	account.MonthlyUnits -= overage.Monthly
	account.GiftUnits -= overage.Gift
	account.PaidUnits -= overage.Paid
	if debtAccrued > 0 {
		if account.DebtUnits, ok = store.ModelCreditAddUnits(account.DebtUnits, debtAccrued); !ok {
			return nil, nil, store.ErrModelCreditInvalid
		}
	}
	settledUnits := in.ActualUnits
	reservation.SettledUnits = &settledUnits
	reservation.State = store.ModelCreditReservationSettled
	reservation.SettledAt = &now
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	if err := settleModelCreditReservation(ctx, tx, reservation, now); err != nil {
		return nil, nil, err
	}
	charged := store.ModelCreditSplit{
		Monthly: consumed.Monthly + overage.Monthly,
		Gift:    consumed.Gift + overage.Gift,
		Paid:    consumed.Paid + overage.Paid,
	}
	for _, part := range modelCreditBucketParts(charged) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventSettle, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventSettle, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	for _, part := range modelCreditBucketParts(released) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventRelease, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventRelease, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	if expiredMonthly > 0 {
		key := store.ModelCreditEventKey(store.ModelCreditEventExpire, in.UID, in.RequestID, store.ModelCreditBucketMonthly)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventExpire, store.ModelCreditBucketMonthly, expiredMonthly, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	if debtAccrued > 0 {
		key := store.ModelCreditDebtAccrualKey(in.UID, in.RequestID)
		if err := recordModelCreditEvent(ctx, tx, key, account,
			store.ModelCreditEventDebtAccrued, store.ModelCreditBucketDebt, debtAccrued, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	// 结算把余额清空（含全被 hold 占住）时同样要收敛到「只保留一个任务」。
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit model credit settle: %w", err)
	}
	return reservation, account, nil
}

// ReleaseModelCredits returns the complete hold only after zero usage has
// been confirmed. A settled reservation cannot be released.
func (a *Adapter) ReleaseModelCredits(ctx context.Context, in store.ModelCreditReleaseInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error) {
	if err := in.Validate(); err != nil {
		return nil, nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin model credit release: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Same as settle: reconcile first so a release never returns units into a
	// period the hold did not belong to.
	now := time.Now().UTC()
	account, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, in.UID, now)
	if err != nil {
		return nil, nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	if err != nil {
		return nil, nil, err
	}
	if reservation.State == store.ModelCreditReservationReleased {
		return reservation, account, nil
	}
	if reservation.State != store.ModelCreditReservationReserved {
		return nil, nil, store.ErrModelCreditConflict
	}
	if reservation.NoUsageConfirmedAt == nil {
		return nil, nil, store.ErrModelCreditConflict
	}
	// Same cross-period rule as settle: the unused monthly part of a hold taken
	// in a previous period is expired instead of being returned to this period.
	released := reservation.Split
	expiredMonthly := int64(0)
	if released.Monthly > 0 {
		expired, periodErr := modelCreditMonthlyHoldExpired(ctx, tx, in.UID, reservation.CreatedAt, now)
		if periodErr != nil {
			return nil, nil, periodErr
		}
		if expired {
			expiredMonthly, released.Monthly = released.Monthly, 0
		}
	}
	for _, part := range modelCreditBucketParts(released) {
		var ok bool
		switch part.bucket {
		case store.ModelCreditBucketMonthly:
			account.MonthlyUnits, ok = store.ModelCreditAddUnits(account.MonthlyUnits, part.units)
		case store.ModelCreditBucketGift:
			account.GiftUnits, ok = store.ModelCreditAddUnits(account.GiftUnits, part.units)
		case store.ModelCreditBucketPaid:
			account.PaidUnits, ok = store.ModelCreditAddUnits(account.PaidUnits, part.units)
		}
		if !ok {
			return nil, nil, store.ErrModelCreditInvalid
		}
	}
	reservation.State = store.ModelCreditReservationReleased
	// 释放把额度还给用户，但如果账户仍然是被耗尽的，保留名额必须按 running 任务重算，
	// 不能让一个已经不可花的账户继续挂着透支名额。
	reservation.ReleasedAt = &now
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	if err := releaseModelCreditReservation(ctx, tx, reservation, now); err != nil {
		return nil, nil, err
	}
	for _, part := range modelCreditBucketParts(released) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventRelease, in.UID, in.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventRelease, part.bucket, part.units, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	if expiredMonthly > 0 {
		key := store.ModelCreditEventKey(store.ModelCreditEventExpire, in.UID, in.RequestID, store.ModelCreditBucketMonthly)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventExpire, store.ModelCreditBucketMonthly, expiredMonthly, &in.RequestID); err != nil {
			return nil, nil, err
		}
	}
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit model credit release: %w", err)
	}
	return reservation, account, nil
}

// GetModelCreditAccount reads the buckets. A missing row is
// ErrModelCreditNotFound; reading never creates credit.
func (a *Adapter) GetModelCreditAccount(ctx context.Context, uid int64) (*store.ModelCreditAccount, error) {
	if uid <= 0 {
		return nil, store.ErrModelCreditInvalid
	}
	account := store.ModelCreditAccount{UID: uid}
	if err := a.db.QueryRowContext(ctx, modelCreditAccountQuery, uid).Scan(
		&account.MonthlyUnits, &account.GiftUnits, &account.PaidUnits, &account.DebtUnits,
		&account.RetainedTaskID, &account.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrModelCreditNotFound
		}
		return nil, fmt.Errorf("read model credit account: %w", err)
	}
	if err := account.Validate(); err != nil {
		return nil, fmt.Errorf("model credit account %d is inconsistent: %w", uid, err)
	}
	return &account, nil
}

// GetModelCreditReservation reads one hold of one user.
func (a *Adapter) GetModelCreditReservation(ctx context.Context, uid int64, requestID string) (*store.ModelCreditReservation, error) {
	if uid <= 0 {
		return nil, store.ErrModelCreditInvalid
	}
	if err := store.ValidateModelCreditRequestID(requestID); err != nil {
		return nil, err
	}
	return readModelCreditReservation(ctx, a.db, uid, requestID, false)
}

// MarkModelCreditNoUsage records terminal failure evidence. It does not release
// funds; the 30-minute reservation lease remains a quarantine window for delayed
// usage delivery.
func (a *Adapter) MarkModelCreditNoUsage(ctx context.Context, in store.ModelCreditNoUsageConfirmation) (*store.ModelCreditReservation, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin no-usage confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockModelCreditAccount(ctx, tx, in.UID); err != nil {
		return nil, err
	}
	reservation, err := readModelCreditReservation(ctx, tx, in.UID, in.RequestID, true)
	if err != nil {
		return nil, err
	}
	// Replaying a successful zero-usage completion after release must stay
	// idempotent, including when the peer lost the release response.
	if reservation.State == store.ModelCreditReservationReleased && reservation.NoUsageConfirmedAt != nil {
		return reservation, nil
	}
	if reservation.State != store.ModelCreditReservationReserved {
		return nil, store.ErrModelCreditConflict
	}
	if reservation.NoUsageConfirmedAt == nil {
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE model_credit_reservations
			SET no_usage_confirmed_at = $3 WHERE uid = $1 AND request_id = $2`,
			in.UID, in.RequestID, now); err != nil {
			return nil, fmt.Errorf("mark model credit request no-usage: %w", err)
		}
		reservation.NoUsageConfirmedAt = &now
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit no-usage confirmation: %w", err)
	}
	return reservation, nil
}

// ReleaseExpiredModelCredits scans candidates without row locks, then releases
// each through the account-first transaction used by settle. This avoids lock
// inversion; a late settle wins or loses atomically, never both.
func (a *Adapter) ReleaseExpiredModelCredits(ctx context.Context, now time.Time, limit int) (int, error) {
	if now.IsZero() || limit <= 0 {
		return 0, store.ErrModelCreditInvalid
	}
	rows, err := a.db.QueryContext(ctx, `SELECT uid, request_id
		FROM model_credit_reservations
		WHERE state = 'reserved' AND no_usage_confirmed_at IS NOT NULL AND lease_expires_at <= $1
		ORDER BY lease_expires_at, uid, request_id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("list expired model credit reservations: %w", err)
	}
	type candidate struct {
		uid       int64
		requestID string
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.uid, &item.requestID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired model credit reservation: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate expired model credit reservations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close expired model credit reservations: %w", err)
	}
	released := 0
	for _, item := range candidates {
		_, _, err := a.ReleaseModelCredits(ctx, store.ModelCreditReleaseInput{UID: item.uid, RequestID: item.requestID})
		if errors.Is(err, store.ErrModelCreditConflict) {
			continue
		}
		if err != nil {
			return released, err
		}
		released++
	}
	return released, nil
}

func (a *Adapter) ListModelCreditLedgerEvents(ctx context.Context, uid int64, limit, offset int) ([]store.ModelCreditLedgerEvent, error) {
	if uid <= 0 || limit <= 0 || limit > 100 || offset < 0 {
		return nil, store.ErrModelCreditInvalid
	}
	rows, err := a.db.QueryContext(ctx, `SELECT event_key, bucket, event_type, units, request_id,
		monthly_balance, gift_balance, paid_balance, debt_balance, created_at
		FROM model_credit_ledger_events WHERE uid = $1
		ORDER BY created_at DESC, event_key DESC LIMIT $2 OFFSET $3`, uid, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list model credit ledger events: %w", err)
	}
	defer rows.Close()
	events := make([]store.ModelCreditLedgerEvent, 0, limit)
	for rows.Next() {
		var event store.ModelCreditLedgerEvent
		var requestID sql.NullString
		if err := rows.Scan(&event.EventKey, &event.Bucket, &event.EventType, &event.Units, &requestID,
			&event.MonthlyBalance, &event.GiftBalance, &event.PaidBalance, &event.DebtBalance, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan model credit ledger event: %w", err)
		}
		if requestID.Valid {
			event.RequestID = &requestID.String
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model credit ledger events: %w", err)
	}
	return events, nil
}

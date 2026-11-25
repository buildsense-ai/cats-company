package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 预付费资源（云机器升配、Cats 算力任务）共用的点数通道：预占 → 结算/释放。
//
// 两种商品卖的是同一种钱：先从共享的 model credit 账本里预占一个上限，确认实际
// 用量后结算，没用掉的部分按预占时的批次原路退回。钱的规则必须逐字一致，否则
// 「升配不能白送」和「算力任务不能白送」两条承诺会各自漂移，所以这里只有一套
// 实现，两个业务只填参数（prepaidResourceHold）。
//
// 规则（与云机器升配既有实现完全相同，不因为换了业务线而放宽）：
//   - 只能花 monthly → paid。gift 是邀请奖励，只能用于模型调用，换了资源等于把
//     赠送额度变成免费机器；欠债账户直接拒绝，因为「已透支还买资源」就是把欠款
//     变成免费额度。
//   - 月额度按批次扣：hold_lots 记住这笔钱来自哪一期哪一批，退回只回到还活着的
//     那一批；已过期（frozen）和被退款作废（revoked）的批次不可花、不复活。
//   - 账本事件用 request id 作幂等键（reserve/settle/release/expire 各带 bucket），
//     同一笔操作重放不会二次记账；账户行锁把同一用户的所有资金变动串行化。

// prepaidResourceHold 是一次预付费资源占用。三个字段就是两个业务之间的全部差异。
type prepaidResourceHold struct {
	// Label 只出现在错误信息里（"machine upgrade" / "compute task"）。
	Label     string
	UID       int64
	RequestID string // 稳定 id：账本 request id，也是 hold_lots 的归属键
	Units     int64  // 预占总额度（报价上限）
}

func (h prepaidResourceHold) describe() string {
	return h.Label + " " + h.RequestID
}

// reservePrepaidResourceCredits 从买家账户预占 hold.Units 点，顺序 monthly → paid，
// 绝不碰 gift，欠债账户直接拒绝。
//
// 先对账月额度：跨期的旧余额进 frozen（不可花），新的一期在扣费前发出，否则会
// 出现「报价看着够、扣费却不够」。扣不到就是余额不足，此时什么都没动。
func reservePrepaidResourceCredits(ctx context.Context, tx *sql.Tx, hold prepaidResourceHold, now time.Time) error {
	if hold.UID <= 0 || hold.RequestID == "" {
		return fmt.Errorf("%s has no owner or request id", hold.Label)
	}
	if hold.Units <= 0 {
		return fmt.Errorf("%s has no cost to hold", hold.describe())
	}
	if _, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, hold.UID, now.UTC()); err != nil {
		return err
	}
	account, err := lockModelCreditAccount(ctx, tx, hold.UID)
	if err != nil {
		return err
	}
	if account.DebtUnits > 0 {
		return fmt.Errorf("%w: account %d is in arrears", store.ErrModelCreditInsufficient, hold.UID)
	}
	split, ok := allocateMachineUpgradeUnits(*account, hold.Units)
	if !ok {
		return fmt.Errorf("%w: need %d units, spendable balance %d",
			store.ErrModelCreditInsufficient, hold.Units, machineUpgradeSpendableUnits(*account))
	}
	// Attribute the monthly slice to the exact lots it came from. The request id
	// is the same key the reserve ledger event uses, so a replay reads back the
	// same hold and a settle/release consumes the same rows.
	if err := debitModelCreditMonthlyLots(ctx, tx, hold.UID, hold.RequestID, split.Monthly); err != nil {
		return err
	}
	account.MonthlyUnits -= split.Monthly
	account.GiftUnits -= split.Gift
	account.PaidUnits -= split.Paid
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return err
	}
	requestID := hold.RequestID
	for _, part := range modelCreditBucketParts(split) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventReserve, hold.UID, hold.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventReserve,
			part.bucket, part.units, &requestID); err != nil {
			return err
		}
	}
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return err
	}
	return nil
}

// settlePrepaidResourceCredits 结算 consumed 点，把没用掉的部分原路退回。
//
// 余额在预占时就已经扣过了，所以这里只把「实际消费」记账成 settle，剩下的按批次
// 退回：月额度回 monthly（当期已结束时回 frozen）、paid 回 paid。退出前先清掉
// hold_lots，避免账本里留下永远还不掉的悬挂批次。
//
// reconcileMonthly 让调用方声明「结算前必须先对账订阅周期」：任务可能在跨月之后
// 才有终态，退回的月额度必须按当下这一期的状态决定是 monthly 还是 frozen。云机器
// 升配的预占窗口很短，沿用旧行为（false），保证行为不变。
func settlePrepaidResourceCredits(ctx context.Context, tx *sql.Tx, hold prepaidResourceHold, consumed int64, reconcileMonthly bool, now time.Time) error {
	if hold.UID <= 0 || hold.RequestID == "" {
		return fmt.Errorf("%s has no owner or request id", hold.Label)
	}
	if consumed < 0 || consumed > hold.Units {
		return fmt.Errorf("%s cannot settle %d of %d units: %w",
			hold.describe(), consumed, hold.Units, store.ErrModelCreditConflict)
	}
	buckets, err := machineUpgradeHeldBuckets(ctx, tx, hold.UID, hold.RequestID)
	if err != nil {
		return err
	}
	if len(buckets) == 0 {
		return fmt.Errorf("%s has no recorded hold: %w", hold.describe(), store.ErrModelCreditConflict)
	}
	var monthlyHeld, paidHeld int64
	for _, part := range buckets {
		switch part.bucket {
		case store.ModelCreditBucketMonthly:
			monthlyHeld += part.units
		case store.ModelCreditBucketPaid:
			paidHeld += part.units
		case store.ModelCreditBucketGift:
			// 预占里出现 gift 只可能是账本被污染：结算它就等于承认一次不合规的
			// gift 消费，所以卡住等人工核对，而不是把钱悄悄记成已花。
			return fmt.Errorf("%s holds gift units: %w", hold.describe(), store.ErrModelCreditInvalid)
		default:
			return fmt.Errorf("%s hold has an unknown bucket %q: %w",
				hold.describe(), part.bucket, store.ErrModelCreditInvalid)
		}
	}
	if monthlyHeld+paidHeld != hold.Units {
		return fmt.Errorf("%s holds %d units, not the reserved %d: %w",
			hold.describe(), monthlyHeld+paidHeld, hold.Units, store.ErrModelCreditConflict)
	}
	if reconcileMonthly {
		if _, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, hold.UID, now.UTC()); err != nil {
			return err
		}
	}
	account, err := lockModelCreditAccount(ctx, tx, hold.UID)
	if err != nil {
		return err
	}
	// Consume the hold in its original order (monthly then paid); whatever is
	// left goes back to the bucket it came from.
	consumedMonthly := min(consumed, monthlyHeld)
	consumedPaid := consumed - consumedMonthly
	releasedMonthly := monthlyHeld - consumedMonthly
	releasedPaid := paidHeld - consumedPaid
	monthlyReturned, err := returnModelCreditMonthlyHold(ctx, tx, account, hold.RequestID,
		monthlyHeld, consumedMonthly, now)
	if err != nil {
		return err
	}
	// A refunded/expired lot cannot be revived: the returned monthly units are
	// the difference between what the hold gave back and what is still alive.
	expiredMonthly := releasedMonthly - monthlyReturned
	if releasedPaid > 0 {
		updated, ok := store.ModelCreditAddUnits(account.PaidUnits, releasedPaid)
		if !ok {
			return fmt.Errorf("%s hold is inconsistent: %w", hold.describe(), store.ErrModelCreditInvalid)
		}
		account.PaidUnits = updated
	}
	requestID := hold.RequestID
	charged := store.ModelCreditSplit{Monthly: consumedMonthly, Paid: consumedPaid}
	for _, part := range modelCreditBucketParts(charged) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventSettle, hold.UID, hold.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventSettle,
			part.bucket, part.units, &requestID); err != nil {
			return err
		}
	}
	released := store.ModelCreditSplit{Monthly: monthlyReturned, Paid: releasedPaid}
	for _, part := range modelCreditBucketParts(released) {
		if part.units == 0 {
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventRelease, hold.UID, hold.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventRelease,
			part.bucket, part.units, &requestID); err != nil {
			return err
		}
	}
	if expiredMonthly > 0 {
		// 这一期已经作废/结束，退回的月额度一单位也不会变成可花余额，必须留一条
		// expire 记录（和 SettleModelCredits 的口径一致），否则余额里少掉的这笔钱
		// 在账本上找不到解释。
		key := store.ModelCreditEventKey(store.ModelCreditEventExpire, hold.UID, hold.RequestID, store.ModelCreditBucketMonthly)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventExpire,
			store.ModelCreditBucketMonthly, expiredMonthly, &requestID); err != nil {
			return err
		}
	}
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return err
	}
	if monthlyReturned > 0 || releasedPaid > 0 {
		// 只有真正把钱还给账户时才需要重算保留名额；整笔消费的结算不动余额。
		if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
			return err
		}
	}
	return nil
}

// releasePrepaidResourceCredits 整笔退回预占（确认零用量）。它先对账月额度，确定
// 当下订阅状态并接管还存在的旧月额度，再按批次退回：活批次回 monthly，过期批次
// 进 frozen，被退款作废的批次一单位也不复活（也不写一笔假的退款事件）。
func releasePrepaidResourceCredits(ctx context.Context, tx *sql.Tx, hold prepaidResourceHold, now time.Time) error {
	if hold.UID <= 0 || hold.RequestID == "" {
		return fmt.Errorf("%s has no owner or request id", hold.Label)
	}
	buckets, err := machineUpgradeHeldBuckets(ctx, tx, hold.UID, hold.RequestID)
	if err != nil {
		return err
	}
	if _, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, hold.UID, now.UTC()); err != nil {
		return err
	}
	account, err := lockModelCreditAccount(ctx, tx, hold.UID)
	if err != nil {
		return err
	}
	var monthlyHeld int64
	for _, part := range buckets {
		if part.bucket == store.ModelCreditBucketMonthly {
			monthlyHeld += part.units
		}
	}
	monthlyReturned, err := returnModelCreditMonthlyHold(ctx, tx, account, hold.RequestID,
		monthlyHeld, 0, now)
	if err != nil {
		return err
	}
	requestID := hold.RequestID
	for _, part := range buckets {
		var ok bool
		released := part.units
		switch part.bucket {
		case store.ModelCreditBucketMonthly:
			// returnModelCreditMonthlyHold 已经把退回来的月额度记到账户上（活批次进
			// monthly，过期批次进 frozen），所以这里不能再加一次。
			released = monthlyReturned
			ok = true
		case store.ModelCreditBucketGift:
			account.GiftUnits, ok = store.ModelCreditAddUnits(account.GiftUnits, part.units)
		case store.ModelCreditBucketPaid:
			account.PaidUnits, ok = store.ModelCreditAddUnits(account.PaidUnits, part.units)
		default:
			ok = false
		}
		if !ok {
			return fmt.Errorf("%s hold is inconsistent: %w", hold.describe(), store.ErrModelCreditInvalid)
		}
		if released == 0 {
			// 一个单位都没退回来（批次被作废或整批过期）：不写一笔假退款事件。
			continue
		}
		key := store.ModelCreditEventKey(store.ModelCreditEventRelease, hold.UID, hold.RequestID, part.bucket)
		if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventRelease,
			part.bucket, released, &requestID); err != nil {
			return err
		}
	}
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return err
	}
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return err
	}
	return nil
}

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// An order may sell paid model credit in addition to (or instead of) the legacy
// CNY/token budget. What an order sells is snapshotted on the order, so the
// ledger is driven by what the buyer really paid for and never by the plan's
// current value.
//
// A credit package is a subscription: its allowance belongs to a natural-month
// period and is booked into the monthly bucket by
// reconcileModelCreditMonthlyPeriod, once per period, inside the payment
// transaction. Charging the order's units into the paid bucket as well gave
// every new buyer the amount twice and made an early renewal top up
// immediately, so no order books paid credit any more.
//
// The refund direction therefore only sees the legacy image: orders fulfilled
// before that change did book paid credit, and their reversal stays exactly as
// conservative as it was. A subscription order is refunded by revoking its
// entitlement and reconciling the monthly permission that survives.
//
// Both directions run inside the caller's payment/refund transaction and use a
// deterministic event key, which is the ledger primary key: a replayed callback
// can never book the same credit or the same reversal twice.
var (
	// ErrCommercialOrderCreditSpent refuses a refund whose purchased credit was
	// already consumed or is currently held by a request. Reversing it would
	// turn used credit into free credit, so the refund has to stay incomplete
	// and be resolved by an operator instead.
	ErrCommercialOrderCreditSpent = errors.New("order model credit was already consumed and cannot be reversed")
	// ErrCommercialOrderCreditGrantMissing reports a paid order whose credit was
	// never booked. Refunding in that state would silently book a reversal of
	// credit the buyer never received.
	ErrCommercialOrderCreditGrantMissing = errors.New("order model credit grant is missing from the ledger")
	// ErrCommercialOrderCreditShort reports a paid bucket that no longer holds
	// the purchased amount even though no settle of this order is visible, which
	// means the balance was changed by something this reversal must not touch.
	ErrCommercialOrderCreditShort = errors.New("order model credit balance is lower than the purchased amount")
)

// commercialOrderCreditGrantKey books the purchase: grant:order:<orderNo>.
func commercialOrderCreditGrantKey(orderNo string) string {
	return store.ModelCreditEventGrant + ":order:" + strings.TrimSpace(orderNo)
}

// commercialOrderCreditRefundKey books the reversal: refund:order:<orderNo>.
func commercialOrderCreditRefundKey(orderNo string) string {
	return store.ModelCreditEventRefund + ":order:" + strings.TrimSpace(orderNo)
}

// commercialOrderArrearsRepaymentKey is the ledger key a grant of this order
// used when it repaid arrears. Repaying debt spends the bought credit for good
// — the debt is gone — so a refund must treat that row as consumption instead
// of handing the money back (and it never re-opens the debt).
func commercialOrderArrearsRepaymentKey(orderNo string) string {
	return store.ModelCreditDebtRepaymentKey("order:" + strings.TrimSpace(orderNo))
}

// commercialOrderCreditGrantBooked reports whether this order booked paid
// credit, which is what decides how a refund releases it. Legacy orders put the
// sold units straight into the paid bucket; a subscription order only receives
// the monthly allowance of its period, so it has no paid grant to reverse and a
// refund must not demand one.
func commercialOrderCreditGrantBooked(ctx context.Context, tx *sql.Tx, orderNo string) (bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return false, nil
	}
	key := commercialOrderCreditGrantKey(orderNo)
	if len(key) > store.ModelCreditEventKeyMaxLen {
		return false, fmt.Errorf("order model credit grant key is too long: %w", store.ErrModelCreditInvalid)
	}
	var booked bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM model_credit_ledger_events WHERE event_key = $1)`, key).Scan(&booked); err != nil {
		return false, fmt.Errorf("read order model credit grant %s: %w", key, err)
	}
	return booked, nil
}

// commercialOrderPaidCreditRefusal is the single gate that decides whether the
// paid credit an order booked can still be taken back, and the only thing that
// authorises the refund freeze. It must run after lockModelCreditAccount, so
// every question below and the caller's write share the same account row lock:
//   - is the purchase grant really booked;
//   - has any paid credit been consumed since then;
//   - does a model-call reservation still hold paid credit;
//   - does a machine upgrade still hold paid credit (its hold is booked in
//     the same paid ledger, so it is spent money just like a reservation);
//   - did this very order repay arrears — repaying debt is consumption, the
//     credit is gone and reversing it would hand back money the buyer
//     already used.
//
// Any of them refusing keeps the refund incomplete for an operator instead of
// turning spent credit into free credit. Nothing here restores arrears: a refund
// only ever moves the paid bucket, so a top-up that paid off debt cannot be
// refunded at all.
func commercialOrderPaidCreditRefusal(ctx context.Context, tx *sql.Tx, order *types.CommercialOrder, account *store.ModelCreditAccount) error {
	if order == nil || order.PlanModelCreditUnits <= 0 {
		return nil
	}
	if account == nil {
		return fmt.Errorf("%w: order %s has no credit account", ErrCommercialOrderCreditGrantMissing, order.OrderNo)
	}
	var settledSinceGrant, openPaidHolds, openMachineUpgradeHolds, repaidArrears int64
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM model_credit_ledger_events e
			 WHERE e.uid = $1 AND e.bucket = 'paid' AND e.event_type = 'settle'
			   AND e.created_at >= g.created_at),
			(SELECT COUNT(*) FROM model_credit_reservations r
			 WHERE r.uid = $1 AND r.state = 'reserved' AND r.paid_units > 0),
			(SELECT COUNT(*) FROM cloud_worker_machine_upgrades u
			 WHERE u.uid = $1 AND u.held AND NOT u.settled
			   AND EXISTS (SELECT 1 FROM model_credit_ledger_events m
			               WHERE m.uid = u.uid AND m.request_id = u.operation_id
			                 AND m.event_type = 'reserve' AND m.bucket = 'paid')),
			(SELECT COUNT(*) FROM model_credit_ledger_events d
			 WHERE d.uid = $1 AND d.event_key = $3)
		FROM model_credit_ledger_events g
		WHERE g.event_key = $2`, order.UID, commercialOrderCreditGrantKey(order.OrderNo),
		commercialOrderArrearsRepaymentKey(order.OrderNo)).
		Scan(&settledSinceGrant, &openPaidHolds, &openMachineUpgradeHolds, &repaidArrears); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: order %s", ErrCommercialOrderCreditGrantMissing, order.OrderNo)
		}
		return fmt.Errorf("read order model credit consumption: %w", err)
	}
	if settledSinceGrant > 0 {
		return fmt.Errorf("%w: %d paid settle event(s) after the purchase grant of order %s",
			ErrCommercialOrderCreditSpent, settledSinceGrant, order.OrderNo)
	}
	if openPaidHolds > 0 {
		return fmt.Errorf("%w: %d open reservation(s) still hold paid credit of uid %d",
			ErrCommercialOrderCreditSpent, openPaidHolds, order.UID)
	}
	if openMachineUpgradeHolds > 0 {
		return fmt.Errorf("%w: %d machine upgrade(s) still hold paid credit of uid %d",
			ErrCommercialOrderCreditSpent, openMachineUpgradeHolds, order.UID)
	}
	if repaidArrears > 0 {
		return fmt.Errorf("%w: order %s already repaid arrears, so its credit is spent",
			ErrCommercialOrderCreditSpent, order.OrderNo)
	}
	if account.PaidUnits < order.PlanModelCreditUnits {
		return fmt.Errorf("%w: paid bucket holds %d units, order %s sold %d",
			ErrCommercialOrderCreditShort, account.PaidUnits, order.OrderNo, order.PlanModelCreditUnits)
	}
	return nil
}

// One refund may have to reverse the paid credit its order sold, and it may only
// do that once the money is really gone. The hold is the record of the opposite
// order: the units are moved out of the spendable paid bucket BEFORE the payment
// channel is called, and completion consumes the hold.
//
// The row itself is load-bearing evidence. An order that is already 'refunding'
// without one was claimed by a build that refunded first, so the channel may
// already have paid the money out; such a refund fails closed and is left to an
// operator instead of being replayed.
const (
	commercialRefundHoldHeld     = "held"
	commercialRefundHoldConsumed = "consumed"
)

var (
	// errCommercialRefundHoldMissing refuses an order that is already
	// 'refunding' without a local hold: the provider outcome is unknown, so the
	// only safe move is to stop and reconcile.
	errCommercialRefundHoldMissing = errors.New("commercial refund hold is missing")
	// errCommercialRefundHoldMismatch refuses a hold that belongs to another
	// claim, or one that was already consumed.
	errCommercialRefundHoldMismatch = errors.New("commercial refund hold does not match the claim")
)

// commercialRefundHold is one order's frozen paid credit.
type commercialRefundHold struct {
	OrderNo         string
	UID             int64
	State           string
	Units           int64
	RefundRequestNo string
	ReserveEventKey string
}

func readCommercialRefundHold(ctx context.Context, tx *sql.Tx, orderNo string, lock bool) (*commercialRefundHold, error) {
	query := `SELECT order_no, uid, state, units, refund_request_no, reserve_event_key
		FROM commercial_refund_holds WHERE order_no = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	hold := commercialRefundHold{}
	if err := tx.QueryRowContext(ctx, query, strings.TrimSpace(orderNo)).Scan(
		&hold.OrderNo, &hold.UID, &hold.State, &hold.Units,
		&hold.RefundRequestNo, &hold.ReserveEventKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read commercial refund hold: %w", err)
	}
	return &hold, nil
}

// freezeCommercialOrderRefundHold moves the paid credit the order may have to
// reverse out of the spendable bucket and records the hold. It runs in the
// caller's transaction, after the order row lock and under the buyer's account
// row lock, so a concurrent model call, settle or second refund either waits or
// sees the reduced balance.
//
// Repeating it for the same claim never freezes twice: an existing matching hold
// is returned as is.
func freezeCommercialOrderRefundHold(ctx context.Context, tx *sql.Tx, order *types.CommercialOrder,
	refundRequestNo string, now time.Time) (*commercialRefundHold, error) {
	if order == nil || strings.TrimSpace(order.OrderNo) == "" || order.UID <= 0 {
		return nil, fmt.Errorf("invalid commercial refund hold: %w", store.ErrModelCreditInvalid)
	}
	refundRequestNo = strings.TrimSpace(refundRequestNo)
	if store.ValidateModelCreditRequestID(refundRequestNo) != nil {
		return nil, fmt.Errorf("invalid commercial refund request number %q: %w", refundRequestNo, store.ErrModelCreditInvalid)
	}
	reserveKey := store.ModelCreditEventKey(store.ModelCreditEventReserve, order.UID, refundRequestNo, store.ModelCreditBucketPaid)
	if len(reserveKey) > store.ModelCreditEventKeyMaxLen {
		return nil, fmt.Errorf("commercial refund hold key is too long: %w", store.ErrModelCreditInvalid)
	}
	existing, err := readCommercialRefundHold(ctx, tx, order.OrderNo, true)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.RefundRequestNo != refundRequestNo || existing.State != commercialRefundHoldHeld {
			return nil, fmt.Errorf("%w: order %s already holds %d units for claim %q in state %q",
				errCommercialRefundHoldMismatch, order.OrderNo, existing.Units, existing.RefundRequestNo, existing.State)
		}
		return existing, nil
	}
	bookedPaidCredit, err := commercialOrderCreditGrantBooked(ctx, tx, order.OrderNo)
	if err != nil {
		return nil, err
	}
	hold := &commercialRefundHold{
		OrderNo:         order.OrderNo,
		UID:             order.UID,
		State:           commercialRefundHoldHeld,
		RefundRequestNo: refundRequestNo,
	}
	if bookedPaidCredit && order.PlanModelCreditUnits > 0 {
		account, err := lockModelCreditAccount(ctx, tx, order.UID)
		if err != nil {
			return nil, err
		}
		if err := commercialOrderPaidCreditRefusal(ctx, tx, order, account); err != nil {
			return nil, err
		}
		hold.Units = order.PlanModelCreditUnits
		account.PaidUnits -= hold.Units
		if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
			return nil, err
		}
		requestID := refundRequestNo
		if err := recordModelCreditEvent(ctx, tx, reserveKey, account, store.ModelCreditEventReserve,
			store.ModelCreditBucketPaid, hold.Units, &requestID); err != nil {
			return nil, err
		}
		hold.ReserveEventKey = reserveKey
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commercial_refund_holds
		(order_no, uid, state, units, refund_request_no, reserve_event_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		hold.OrderNo, hold.UID, hold.State, hold.Units, hold.RefundRequestNo,
		hold.ReserveEventKey, now.UTC()); err != nil {
		return nil, fmt.Errorf("record commercial refund hold: %w", err)
	}
	return hold, nil
}

// consumeCommercialRefundHold records the release of the reservation and the
// order's negative refund together. Both events snapshot the final account;
// the frozen units never re-enter spendable balance, so unrelated grants cannot
// make refund completion overflow an artificial intermediate balance.
func consumeCommercialRefundHold(ctx context.Context, tx *sql.Tx, order *types.CommercialOrder,
	hold *commercialRefundHold, now time.Time) error {
	if order == nil || hold == nil || order.UID <= 0 {
		return fmt.Errorf("invalid commercial refund hold: %w", store.ErrModelCreditInvalid)
	}
	if hold.State != commercialRefundHoldHeld {
		return fmt.Errorf("%w: order %s hold is %s", errCommercialRefundHoldMismatch, order.OrderNo, hold.State)
	}
	if hold.Units > 0 {
		account, err := lockModelCreditAccount(ctx, tx, order.UID)
		if err != nil {
			return err
		}
		// The freeze already reduced paid_units. Release + refund have zero
		// net effect, so snapshot the final account without adding the hold back.
		requestID := hold.RefundRequestNo
		releaseKey := store.ModelCreditEventKey(store.ModelCreditEventRelease, order.UID, hold.RefundRequestNo, store.ModelCreditBucketPaid)
		if err := recordModelCreditEvent(ctx, tx, releaseKey, account, store.ModelCreditEventRelease,
			store.ModelCreditBucketPaid, hold.Units, &requestID); err != nil {
			return err
		}
		refundKey := commercialOrderCreditRefundKey(order.OrderNo)
		if booked, err := readCommercialOrderCreditEvent(ctx, tx, refundKey); err == nil {
			return fmt.Errorf("order model credit refund %s is already booked for uid %d: %w",
				refundKey, booked.uid, store.ErrModelCreditConflict)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := recordModelCreditEvent(ctx, tx, refundKey, account, store.ModelCreditEventRefund,
			store.ModelCreditBucketPaid, -hold.Units, nil); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE commercial_refund_holds
		SET state = $2, consumed_at = $3, updated_at = $3
		WHERE order_no = $1 AND state = $4`,
		order.OrderNo, commercialRefundHoldConsumed, now.UTC(), commercialRefundHoldHeld)
	if err != nil {
		return fmt.Errorf("settle commercial refund hold: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read settled commercial refund hold: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: order %s hold changed while it was consumed", errCommercialRefundHoldMismatch, order.OrderNo)
	}
	return nil
}

// commercialOrderCreditEvent is the small slice of a ledger row these helpers
// compare a replay against.
type commercialOrderCreditEvent struct {
	uid       int64
	bucket    string
	eventType string
	units     int64
}

func readCommercialOrderCreditEvent(ctx context.Context, tx *sql.Tx, key string) (commercialOrderCreditEvent, error) {
	var event commercialOrderCreditEvent
	if err := tx.QueryRowContext(ctx,
		`SELECT uid, bucket, event_type, units FROM model_credit_ledger_events WHERE event_key = $1`, key).
		Scan(&event.uid, &event.bucket, &event.eventType, &event.units); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return commercialOrderCreditEvent{}, sql.ErrNoRows
		}
		return commercialOrderCreditEvent{}, fmt.Errorf("read model credit order event %s: %w", key, err)
	}
	return event, nil
}
func commercialInviteCreditGrantKey(uid int64, code string) string {
	return store.ModelCreditEventGrant + ":invite:" + fmt.Sprintf("%d", uid) + ":" + strings.ToUpper(strings.TrimSpace(code))
}

func grantCommercialInviteModelCredit(ctx context.Context, tx *sql.Tx, uid int64, code string, units int64, now time.Time) error {
	if units <= 0 {
		return nil
	}
	key := commercialInviteCreditGrantKey(uid, code)
	if uid <= 0 || len(key) > store.ModelCreditEventKeyMaxLen {
		return fmt.Errorf("invalid invite model credit grant: %w", store.ErrModelCreditInvalid)
	}
	account, err := lockModelCreditAccount(ctx, tx, uid)
	if err != nil {
		return err
	}
	booked, err := readCommercialOrderCreditEvent(ctx, tx, key)
	switch {
	case err == nil:
		if booked.uid != uid || booked.bucket != store.ModelCreditBucketGift ||
			booked.eventType != store.ModelCreditEventGrant || booked.units != units {
			return fmt.Errorf("invite model credit grant %s conflicts with the booked event: %w", key, store.ErrModelCreditConflict)
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	gift, ok := store.ModelCreditAddUnits(account.GiftUnits, units)
	if !ok {
		return fmt.Errorf("invite model credit grant overflows the gift bucket: %w", store.ErrModelCreditInvalid)
	}
	account.GiftUnits = gift
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return err
	}
	return recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventGrant,
		store.ModelCreditBucketGift, units, nil)
}

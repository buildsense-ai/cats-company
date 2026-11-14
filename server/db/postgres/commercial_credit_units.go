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

// reverseCommercialOrderModelCredit takes paid credit that a legacy order booked
// back out of the paid bucket. It is deliberately conservative: once any of that
// credit could have been spent, the refund is refused instead of clawing back
// money the buyer no longer holds.
func reverseCommercialOrderModelCredit(ctx context.Context, tx *sql.Tx, order *types.CommercialOrder, now time.Time) error {
	if order == nil || order.PlanModelCreditUnits <= 0 {
		return nil
	}
	key := commercialOrderCreditRefundKey(order.OrderNo)
	if len(key) > store.ModelCreditEventKeyMaxLen {
		return fmt.Errorf("order model credit refund key is too long: %w", store.ErrModelCreditInvalid)
	}
	account, err := lockModelCreditAccount(ctx, tx, order.UID)
	if err != nil {
		return err
	}
	booked, err := readCommercialOrderCreditEvent(ctx, tx, key)
	switch {
	case err == nil:
		if booked.uid != order.UID || booked.bucket != store.ModelCreditBucketPaid ||
			booked.eventType != store.ModelCreditEventRefund || booked.units != -order.PlanModelCreditUnits {
			return fmt.Errorf("order model credit refund %s conflicts with the booked event: %w", key, store.ErrModelCreditConflict)
		}
		// Replay: the credit was already taken back.
		return nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	// One query answers both questions the reversal has to ask before it may
	// touch the balance: is the purchase grant really booked, and has any paid
	// credit been consumed or held since then.
	var settledSinceGrant, openPaidHolds int64
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM model_credit_ledger_events e
			 WHERE e.uid = $1 AND e.bucket = 'paid' AND e.event_type = 'settle'
			   AND e.created_at >= g.created_at),
			(SELECT COUNT(*) FROM model_credit_reservations r
			 WHERE r.uid = $1 AND r.state = 'reserved' AND r.paid_units > 0)
		FROM model_credit_ledger_events g
		WHERE g.event_key = $2`, order.UID, commercialOrderCreditGrantKey(order.OrderNo)).
		Scan(&settledSinceGrant, &openPaidHolds); err != nil {
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
	if account.PaidUnits < order.PlanModelCreditUnits {
		return fmt.Errorf("%w: paid bucket holds %d units, order %s sold %d",
			ErrCommercialOrderCreditShort, account.PaidUnits, order.OrderNo, order.PlanModelCreditUnits)
	}
	account.PaidUnits -= order.PlanModelCreditUnits
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return err
	}
	return recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventRefund,
		store.ModelCreditBucketPaid, -order.PlanModelCreditUnits, nil)
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

package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Credit top-up: money in, spendable credit out — and nothing else.
//
// A top-up is deliberately not a subscription. It books units into the paid
// bucket of the model-credit ledger and stops there: no entitlement row, no
// quota grant, no trial, no provisioning credit, and no write to any package
// window the buyer already holds. That is what lets a user with no package at
// all spend what they bought, and what keeps a package renewal from being
// confused with a purchase.
//
// Exact rate (产品合同): 1 fen = 1 credit = 1_000_000 micro units. There is no
// bonus, no rounding and no per-amount product row: the amount on the order is
// the whole product, and the unit snapshot next to it is what fulfilment books
// and what a refund may reverse.

// CommercialTopupUnitsPerFen is the fixed rate used by both the API and the
// ledger. Exported so the catalog and the order path cannot drift apart.
const CommercialTopupUnitsPerFen int64 = 1_000_000

// CommercialTopupOrderSlug labels a top-up order in history reads. It is not a
// commercial_plans row: a top-up must never appear in the plan catalog and must
// never be resolvable as a purchasable package.
const CommercialTopupOrderSlug = "credit_topup"

// CommercialTopupOrderName is what the order history shows as the product.
const CommercialTopupOrderName = "点数充值"

// CommercialTopupUnits converts a CNY price in fen into ledger micro units.
// It returns 0 for a non-positive amount so callers can reject it before any
// row exists.
func CommercialTopupUnits(amountFen int64) int64 {
	if amountFen <= 0 || amountFen > int64(^uint64(0)>>1)/CommercialTopupUnitsPerFen {
		return 0
	}
	return amountFen * CommercialTopupUnitsPerFen
}

// CreateCommercialTopupOrder creates (or replays) a standalone credit order.
//
// The buyer's own client_request_id is the idempotency key, exactly like a
// package order: replaying it returns the stored order. Reusing the same id for
// a different amount is a conflict rather than a silent second product, which
// is what stops a retry from turning 10 CNY into 100 CNY.
func (a *Adapter) CreateCommercialTopupOrder(uid, amountFen int64, channel, clientRequestID string, expiresAt *time.Time) (*types.CommercialOrder, error) {
	if uid <= 0 || amountFen <= 0 || strings.TrimSpace(channel) == "" || strings.TrimSpace(clientRequestID) == "" {
		return nil, fmt.Errorf("invalid credit top-up order")
	}
	units := CommercialTopupUnits(amountFen)
	if units <= 0 {
		return nil, fmt.Errorf("invalid credit top-up amount")
	}
	channel = strings.TrimSpace(channel)
	clientRequestID = strings.TrimSpace(clientRequestID)
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin credit top-up order: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("commercial_order:%d:%s", uid, clientRequestID)); err != nil {
		return nil, fmt.Errorf("lock credit top-up idempotency: %w", err)
	}
	existing, err := scanCommercialOrder(tx.QueryRow(
		`SELECT `+commercialOrderColumns+` FROM commercial_orders WHERE uid = $1 AND client_request_id = $2`,
		uid, clientRequestID))
	if err == nil {
		if existing.Kind != types.CommercialOrderKindCreditTopup || existing.AmountFen != amountFen || existing.Channel != channel {
			return nil, fmt.Errorf("client request id is already used by another order")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check credit top-up idempotency: %w", err)
	}
	existing, err = scanCommercialOrder(tx.QueryRow(`
		SELECT `+commercialOrderColumns+` FROM commercial_orders
		WHERE order_no = (
			SELECT order_no FROM commercial_order_request_ids
			WHERE uid = $1 AND client_request_id = $2
		)`, uid, clientRequestID))
	if err == nil {
		if existing.Kind != types.CommercialOrderKindCreditTopup || existing.AmountFen != amountFen || existing.Channel != channel {
			return nil, fmt.Errorf("client request id is already used by another order")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check credit top-up request alias: %w", err)
	}
	expiry := expiresAt
	if expiry == nil {
		value := time.Now().UTC().Add(20 * time.Minute)
		expiry = &value
	}
	created, err := scanCommercialOrder(tx.QueryRow(`
		INSERT INTO commercial_orders(
			order_no, uid, plan_id, kind, plan_slug, plan_name, plan_description, plan_duration_days,
			plan_monthly_budget_cny, plan_model_budgets, amount_fen, currency, channel, status,
			client_request_id, expires_at, plan_model_credit_units
		) VALUES ($1, $2, NULL, $3, $4, $5, '', 0, 0, '{}'::jsonb, $6, 'CNY', $7, 'created', $8, $9, $10)
		RETURNING `+commercialOrderColumns,
		newCommercialTopupOrderNo(),
		uid,
		types.CommercialOrderKindCreditTopup,
		CommercialTopupOrderSlug,
		CommercialTopupOrderName,
		amountFen,
		channel,
		clientRequestID,
		expiry,
		units,
	))
	if err != nil {
		return nil, fmt.Errorf("create credit top-up order: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO commercial_order_request_ids(uid, client_request_id, order_no)
		VALUES ($1, $2, $3)`, uid, clientRequestID, created.OrderNo); err != nil {
		return nil, fmt.Errorf("save credit top-up request id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit credit top-up order: %w", err)
	}
	return created, nil
}

// fulfillCommercialTopupOrder finishes a paid top-up inside the payment
// transaction and commits it.
//
// Two things can be replayed here and both must stay idempotent: the provider
// callback (blocked by the payment-event unique key before this runs) and the
// ledger booking itself (keyed grant:order:<orderNo>). A second delivery
// therefore books nothing twice, and a callback that arrives after a manual
// reconcile cannot double the balance.
func (a *Adapter) fulfillCommercialTopupOrder(tx *sql.Tx, order *types.CommercialOrder, paidAt time.Time) (*types.CommercialOrder, bool, error) {
	if order == nil {
		return nil, false, fmt.Errorf("invalid credit top-up order")
	}
	if order.PlanModelCreditUnits <= 0 || order.PlanModelCreditUnits != CommercialTopupUnits(order.AmountFen) {
		return nil, false, fmt.Errorf("credit top-up order %s carries %d units, want %d for %d fen",
			order.OrderNo, order.PlanModelCreditUnits, CommercialTopupUnits(order.AmountFen), order.AmountFen)
	}
	if err := bookCommercialTopupCredit(context.Background(), tx, order, paidAt); err != nil {
		return nil, false, err
	}
	fulfilled, err := scanCommercialOrder(tx.QueryRow(`
		UPDATE commercial_orders
		SET status = 'fulfilled', provider_trade_no = $2, paid_at = $3, fulfilled_at = CURRENT_TIMESTAMP,
		    checkout_url = '', last_error = ''
		WHERE order_no = $1
		RETURNING `+commercialOrderColumns, order.OrderNo, strings.TrimSpace(order.ProviderTradeNo), paidAt.UTC()))
	if err != nil {
		return nil, false, fmt.Errorf("complete credit top-up order: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit credit top-up fulfilment: %w", err)
	}
	return fulfilled, true, nil
}

// bookCommercialTopupCredit books the purchased units into the paid bucket with
// the same event key a package order uses (grant:order:<orderNo>), so the
// existing refund reversal — including its "already consumed cannot be
// reversed" rule — applies unchanged to a top-up.
//
// Arrears are repaid first, exactly like every other grant: buying credit must
// not leave an account that already overspent pretending to be solvent.
func bookCommercialTopupCredit(ctx context.Context, tx *sql.Tx, order *types.CommercialOrder, now time.Time) error {
	units := order.PlanModelCreditUnits
	if units <= 0 {
		return fmt.Errorf("credit top-up order %s has no units to book: %w", order.OrderNo, store.ErrModelCreditInvalid)
	}
	key := commercialOrderCreditGrantKey(order.OrderNo)
	if len(key) > store.ModelCreditEventKeyMaxLen {
		return fmt.Errorf("credit top-up grant key is too long: %w", store.ErrModelCreditInvalid)
	}
	account, err := lockModelCreditAccount(ctx, tx, order.UID)
	if err != nil {
		return err
	}
	booked, err := readCommercialOrderCreditEvent(ctx, tx, key)
	switch {
	case err == nil:
		if booked.uid != order.UID || booked.bucket != store.ModelCreditBucketPaid ||
			booked.eventType != store.ModelCreditEventGrant || booked.units != units {
			return fmt.Errorf("credit top-up grant %s conflicts with the booked event: %w", key, store.ErrModelCreditConflict)
		}
		// Replay: the credit is already booked, so the balance is unchanged.
		return nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}
	paid, ok := store.ModelCreditAddUnits(account.PaidUnits, units)
	if !ok {
		return fmt.Errorf("credit top-up overflows the paid bucket: %w", store.ErrModelCreditInvalid)
	}
	account.PaidUnits = paid
	repay, _, ok := store.PlanModelCreditGrant(*account, store.ModelCreditBucketPaid, units)
	if !ok || repay > account.DebtUnits || repay > account.PaidUnits {
		return fmt.Errorf("credit top-up cannot repay arrears: %w", store.ErrModelCreditInvalid)
	}
	var debtKey string
	if repay > 0 {
		debtKey = commercialOrderArrearsRepaymentKey(order.OrderNo)
		if len(debtKey) > store.ModelCreditEventKeyMaxLen {
			return fmt.Errorf("credit top-up debt key is too long: %w", store.ErrModelCreditInvalid)
		}
		account.DebtUnits -= repay
		account.PaidUnits -= repay
	}
	if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
		return err
	}
	if err := recordModelCreditEvent(ctx, tx, key, account, store.ModelCreditEventGrant,
		store.ModelCreditBucketPaid, units, nil); err != nil {
		return err
	}
	if repay > 0 {
		if err := recordModelCreditEvent(ctx, tx, debtKey, account,
			store.ModelCreditEventDebtRepaid, store.ModelCreditBucketDebt, repay, nil); err != nil {
			return err
		}
	}
	// A top-up restores what the account may spend, so the retention slot has to
	// follow the new balance instead of staying clamped to the old one.
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return err
	}
	return nil
}

// newCommercialTopupOrderNo keeps top-up orders visually distinct from package
// orders (CT vs CC) in support logs while staying inside the 40-char column.
func newCommercialTopupOrderNo() string {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		copy(random, sum[:8])
	}
	return "CT" + time.Now().UTC().Format("20060102150405") + strings.ToUpper(hex.EncodeToString(random))
}

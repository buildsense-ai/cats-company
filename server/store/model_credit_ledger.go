package store

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"
)

// Model credit is booked in integer micro-units: one credit is exactly
// 1_000_000 units. Every amount in this file is an int64 count of units; no
// float ever represents money here.
const ModelCreditUnitsPerCredit int64 = 1_000_000
const ModelCreditReservationLease = 30 * time.Minute

// Account buckets. Debt is arrears the user already owes; it is never a
// spending source and never receives credit from a reserve/settle operation.
const (
	ModelCreditBucketMonthly = "monthly"
	ModelCreditBucketGift    = "gift"
	ModelCreditBucketPaid    = "paid"
	ModelCreditBucketDebt    = "debt"
)

// Ledger event types. grant/reserve/release change a spending bucket balance,
// settle records what a reservation actually consumed, debt_* only ever touch
// the debt bucket.
const (
	ModelCreditEventGrant       = "grant"
	ModelCreditEventReserve     = "reserve"
	ModelCreditEventSettle      = "settle"
	ModelCreditEventRelease     = "release"
	ModelCreditEventDebtAccrued = "debt_accrued"
	ModelCreditEventDebtRepaid  = "debt_repaid"
	// ModelCreditEventRefund is the single reversal of one purchase. It is the
	// only event whose units are negative, so a refund can never look like a
	// grant in the ledger.
	ModelCreditEventRefund = "refund"
)

const (
	ModelCreditReservationReserved = "reserved"
	ModelCreditReservationSettled  = "settled"
	ModelCreditReservationReleased = "released"
)

const (
	ModelCreditRequestIDMaxLen = 128
	// ModelCreditIdempotencyKeyMaxLen bounds a caller supplied grant key.
	ModelCreditIdempotencyKeyMaxLen = 100
	// ModelCreditEventKeyMaxLen mirrors model_credit_ledger_events.event_key.
	ModelCreditEventKeyMaxLen = 192
)

var (
	ErrModelCreditInvalid      = errors.New("model credit ledger input is invalid")
	ErrModelCreditConflict     = errors.New("model credit ledger request conflicts with a previous one")
	ErrModelCreditInsufficient = errors.New("model credit balance is insufficient")
	ErrModelCreditNotFound     = errors.New("model credit reservation not found")
)

// Keys are embedded in the ledger event key, so they stay inside a small ASCII
// alphabet that keeps the composed key within ModelCreditEventKeyMaxLen.
var modelCreditToken = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

func validModelCreditToken(value string, maxLen int) bool {
	return len(value) > 0 && len(value) <= maxLen && modelCreditToken.MatchString(value)
}

// NormalizeModelCreditGrantBucket accepts only the buckets a grant may credit.
// Debt is written by arrears accounting, never by a grant.
func NormalizeModelCreditGrantBucket(bucket string) (string, bool) {
	switch bucket {
	case ModelCreditBucketMonthly, ModelCreditBucketGift, ModelCreditBucketPaid:
		return bucket, true
	default:
		return "", false
	}
}

// ModelCreditEventKey builds the unique ledger key for one bucket of one
// request-scoped operation. The key is deterministic, so replaying the same
// operation can never post a second event.
func ModelCreditEventKey(eventType string, uid int64, requestID, bucket string) string {
	return eventType + ":" + strconv.FormatInt(uid, 10) + ":" + requestID + ":" + bucket
}

// ModelCreditGrantEventKey builds the ledger key for a caller keyed grant.
func ModelCreditGrantEventKey(idempotencyKey string) string {
	return ModelCreditEventGrant + ":" + idempotencyKey
}

// ModelCreditAddUnits returns balance+delta, refusing to overflow. A zero delta
// returns the balance unchanged.
func ModelCreditAddUnits(balance, delta int64) (int64, bool) {
	if balance < 0 || delta < 0 || balance > int64(^uint64(0)>>1)-delta {
		return 0, false
	}
	return balance + delta, true
}

// One row per user holds all four buckets. Buckets are columns rather than
// per-bucket rows so one FOR UPDATE on the account row serializes every
// mutation for that user.
type ModelCreditAccount struct {
	UID          int64
	MonthlyUnits int64
	GiftUnits    int64
	PaidUnits    int64
	DebtUnits    int64
	UpdatedAt    time.Time
}

func (a ModelCreditAccount) Validate() error {
	if a.UID <= 0 || a.MonthlyUnits < 0 || a.GiftUnits < 0 || a.PaidUnits < 0 || a.DebtUnits < 0 {
		return ErrModelCreditInvalid
	}
	return nil
}

// SpendableUnits is everything a request may hold right now. Debt is excluded:
// arrears are not a balance.
func (a ModelCreditAccount) SpendableUnits() int64 {
	return a.MonthlyUnits + a.GiftUnits + a.PaidUnits
}

// ValidateModelCreditRequestID reports whether requestID can key a reservation
// and be embedded in the ledger event keys.
func ValidateModelCreditRequestID(requestID string) error {
	if !validModelCreditToken(requestID, ModelCreditRequestIDMaxLen) {
		return ErrModelCreditInvalid
	}
	return nil
}

// ModelCreditSplit is how one amount is drawn from the three spending buckets.
type ModelCreditSplit struct {
	Monthly int64
	Gift    int64
	Paid    int64
}

func (s ModelCreditSplit) Total() int64 { return s.Monthly + s.Gift + s.Paid }

// AllocateModelCreditUnits draws units in the fixed order monthly -> gift ->
// paid, which is the only allowed consumption order. Gift credits come from
// invite gifts and must never be spent on arrears, so a debt-carrying account
// cannot draw gift units at all; only the monthly and paid buckets may cover
// the request, and a shortfall is reported as insufficient rather than
// becoming new debt.
func AllocateModelCreditUnits(account ModelCreditAccount, units int64) (ModelCreditSplit, bool) {
	if units <= 0 || account.Validate() != nil {
		return ModelCreditSplit{}, false
	}
	var split ModelCreditSplit
	remaining := units
	if take := min(remaining, account.MonthlyUnits); take > 0 {
		split.Monthly = take
		remaining -= take
	}
	if account.DebtUnits == 0 {
		if take := min(remaining, account.GiftUnits); take > 0 {
			split.Gift = take
			remaining -= take
		}
	}
	if take := min(remaining, account.PaidUnits); take > 0 {
		split.Paid = take
		remaining -= take
	}
	if remaining > 0 {
		return ModelCreditSplit{}, false
	}
	return split, true
}

// ConsumeModelCreditSplit takes units from an existing hold in the same
// monthly -> gift -> paid order the hold was reserved in. Units beyond the hold
// are ignored here; the caller accounts for that overage separately.
func ConsumeModelCreditSplit(split ModelCreditSplit, units int64) ModelCreditSplit {
	var consumed ModelCreditSplit
	if units <= 0 {
		return consumed
	}
	remaining := units
	if take := min(remaining, split.Monthly); take > 0 {
		consumed.Monthly = take
		remaining -= take
	}
	if take := min(remaining, split.Gift); take > 0 {
		consumed.Gift = take
		remaining -= take
	}
	if take := min(remaining, split.Paid); take > 0 {
		consumed.Paid = take
		remaining -= take
	}
	return consumed
}

// ModelCreditReservation is the per-request hold. SettledUnits records the
// amount consumed; the lease is a quarantine, not proof that a request ended.
type ModelCreditReservation struct {
	UID                int64
	RequestID          string
	ReservedUnits      int64
	Split              ModelCreditSplit
	SettledUnits       *int64
	State              string
	CreatedAt          time.Time
	LeaseExpiresAt     time.Time
	NoUsageConfirmedAt *time.Time
	SettledAt          *time.Time
	ReleasedAt         *time.Time
}

func (r ModelCreditReservation) Validate() error {
	if r.UID <= 0 || !validModelCreditToken(r.RequestID, ModelCreditRequestIDMaxLen) ||
		r.ReservedUnits <= 0 || r.Split.Total() != r.ReservedUnits ||
		r.Split.Monthly < 0 || r.Split.Gift < 0 || r.Split.Paid < 0 ||
		r.LeaseExpiresAt.IsZero() || !r.LeaseExpiresAt.After(r.CreatedAt) {
		return ErrModelCreditInvalid
	}
	switch r.State {
	case ModelCreditReservationReserved:
		if r.SettledUnits != nil || r.SettledAt != nil || r.ReleasedAt != nil {
			return ErrModelCreditInvalid
		}
	case ModelCreditReservationSettled:
		if r.SettledUnits == nil || *r.SettledUnits < 0 || r.SettledAt == nil || r.ReleasedAt != nil {
			return ErrModelCreditInvalid
		}
	case ModelCreditReservationReleased:
		if r.SettledUnits != nil || r.SettledAt != nil || r.ReleasedAt == nil {
			return ErrModelCreditInvalid
		}
	default:
		return ErrModelCreditInvalid
	}
	return nil
}

// GrantModelCreditInput credits one spending bucket. The caller supplied
// idempotency key makes a repeated grant a no-op.
type ModelCreditGrantInput struct {
	UID            int64
	Bucket         string
	Units          int64
	IdempotencyKey string
}

func (in ModelCreditGrantInput) Validate() error {
	if in.UID <= 0 || in.Units <= 0 || !validModelCreditToken(in.IdempotencyKey, ModelCreditIdempotencyKeyMaxLen) {
		return ErrModelCreditInvalid
	}
	if _, ok := NormalizeModelCreditGrantBucket(in.Bucket); !ok {
		return ErrModelCreditInvalid
	}
	return nil
}

// ReserveModelCreditInput holds Units for one request. The request id is the
// idempotency key of the hold itself.
type ModelCreditReserveInput struct {
	UID       int64
	RequestID string
	Units     int64
}

func (in ModelCreditReserveInput) Validate() error {
	if in.UID <= 0 || in.Units <= 0 || !validModelCreditToken(in.RequestID, ModelCreditRequestIDMaxLen) {
		return ErrModelCreditInvalid
	}
	return nil
}

// ModelCreditSettleInput settles a hold against what the request really cost.
// ActualUnits may be zero (a request that consumed nothing) and may exceed the
// hold; the excess is only taken when the balance really covers it.
type ModelCreditSettleInput struct {
	UID         int64
	RequestID   string
	ActualUnits int64
}

func (in ModelCreditSettleInput) Validate() error {
	if in.UID <= 0 || in.ActualUnits < 0 || !validModelCreditToken(in.RequestID, ModelCreditRequestIDMaxLen) {
		return ErrModelCreditInvalid
	}
	return nil
}

// ModelCreditReleaseInput returns a reservation in full after a request that
// consumed no billable usage failed before settlement.
type ModelCreditReleaseInput struct {
	UID       int64
	RequestID string
}

func (in ModelCreditReleaseInput) Validate() error {
	if in.UID <= 0 || !validModelCreditToken(in.RequestID, ModelCreditRequestIDMaxLen) {
		return ErrModelCreditInvalid
	}
	return nil
}

// ModelCreditNoUsageConfirmation is only recorded after the request reached a
// terminal failure and the caller has authoritative evidence of zero usage.
type ModelCreditNoUsageConfirmation struct {
	UID       int64
	RequestID string
}

func (in ModelCreditNoUsageConfirmation) Validate() error {
	if in.UID <= 0 || !validModelCreditToken(in.RequestID, ModelCreditRequestIDMaxLen) {
		return ErrModelCreditInvalid
	}
	return nil
}

// PostgreSQL only, internal ledger foundation: no registered route, no
// product/payment/relay wiring and no change to the legacy commercial
// quota/token paths. Every mutation runs in one database transaction and takes
// a FOR UPDATE lock on the account row, so concurrent reserves/settles of one
// user are serialized and the balance can never be over-drawn.
//
// Reserve holds monthly -> gift -> paid and returns ErrModelCreditInsufficient
// instead of over-drawing. Settle releases the unused hold back to the exact
// buckets it came from; an overage is deducted only when the balance covers it,
// otherwise settle fails with ErrModelCreditInsufficient and writes nothing.
// Settle never creates debt: arrears accounting and the over-draft policy are
// not wired in this phase, so only an explicit debt entry can raise debt_units.
//
// Replays are idempotent: the same request id returns the stored reservation,
// the same grant key returns the account unchanged, and the same settle with a
// different actual amount is rejected with ErrModelCreditConflict.
type ModelCreditLedgerEvent struct {
	EventKey       string
	Bucket         string
	EventType      string
	Units          int64
	RequestID      *string
	MonthlyBalance int64
	GiftBalance    int64
	PaidBalance    int64
	DebtBalance    int64
	CreatedAt      time.Time
}

type ModelCreditLedgerStore interface {
	GrantModelCredits(context.Context, ModelCreditGrantInput) (*ModelCreditAccount, error)
	ReserveModelCredits(context.Context, ModelCreditReserveInput) (*ModelCreditReservation, *ModelCreditAccount, error)
	SettleModelCredits(context.Context, ModelCreditSettleInput) (*ModelCreditReservation, *ModelCreditAccount, error)
	ReleaseModelCredits(context.Context, ModelCreditReleaseInput) (*ModelCreditReservation, *ModelCreditAccount, error)
	MarkModelCreditNoUsage(context.Context, ModelCreditNoUsageConfirmation) (*ModelCreditReservation, error)
	ReleaseExpiredModelCredits(context.Context, time.Time, int) (int, error)
	GetModelCreditAccount(context.Context, int64) (*ModelCreditAccount, error)
	GetModelCreditReservation(context.Context, int64, string) (*ModelCreditReservation, error)
	ListModelCreditLedgerEvents(context.Context, int64, int, int) ([]ModelCreditLedgerEvent, error)
}

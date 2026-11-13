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
	// ModelCreditEventExpire 是月额度跨周期作废：套餐月额度当期有效、不结转，
	// 余额被清零必须留一条可审计的记录。
	ModelCreditEventExpire = "expire"
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

// ModelCreditDebtRepaymentKey builds the ledger key for repaying arrears out of
// one grant. It shares the grant's idempotency key, so a replayed grant cannot
// repay the same debt twice.
func ModelCreditDebtRepaymentKey(idempotencyKey string) string {
	return ModelCreditEventDebtRepaid + ":" + idempotencyKey
}

// ModelCreditDebtAccrualKey builds the ledger key for the arrears one request
// really ran up. Request ids are unique per user, so a replayed settle can never
// book the same debt again.
func ModelCreditDebtAccrualKey(uid int64, requestID string) string {
	return ModelCreditEventKey(ModelCreditEventDebtAccrued, uid, requestID, ModelCreditBucketDebt)
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
//
// RetainedTaskID 是全用户唯一的「透支名额」：额度耗尽时只保留最近开始的 running
// root 任务，它后续的预留可以超出余额（超出部分记真实欠款）。它是账户行上的一列，
// 因此和余额一起被同一把行锁保护。
type ModelCreditAccount struct {
	UID            int64
	MonthlyUnits   int64
	GiftUnits      int64
	PaidUnits      int64
	DebtUnits      int64
	RetainedTaskID string
	UpdatedAt      time.Time
}

func (a ModelCreditAccount) Validate() error {
	if a.UID <= 0 || a.MonthlyUnits < 0 || a.GiftUnits < 0 || a.PaidUnits < 0 || a.DebtUnits < 0 {
		return ErrModelCreditInvalid
	}
	if a.RetainedTaskID != "" && ValidateModelCreditTaskID(a.RetainedTaskID) != nil {
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

// AllocateModelCreditUnitsPartial 是 AllocateModelCreditUnits 的「能给多少给多少」
// 版本：透支预算已经发出去的调用只能按当下真实可用的额度扣，剩下的一分钱也不能
// 凭空消失，必须由调用方记成欠款（docs P0-2.3）。顺序仍然是 monthly -> gift -> paid，
// 有欠款时 gift 仍然不可用。
func AllocateModelCreditUnitsPartial(account ModelCreditAccount, units int64) (ModelCreditSplit, int64) {
	if units <= 0 || account.Validate() != nil {
		return ModelCreditSplit{}, 0
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
	return split, remaining
}

// PlanModelCreditGrant 决定一笔入账里有多少先拿去还欠款（P0-2.4）：monthly 与 paid
// 先抵债，gift 永不抵债（P0-2.5）。返回还给欠款的额度和真正进入该桶的额度。
func PlanModelCreditGrant(account ModelCreditAccount, bucket string, units int64) (repay, credited int64, ok bool) {
	if units <= 0 || account.Validate() != nil {
		return 0, 0, false
	}
	normalized, valid := NormalizeModelCreditGrantBucket(bucket)
	if !valid {
		return 0, 0, false
	}
	if normalized != ModelCreditBucketGift {
		repay = min(units, account.DebtUnits)
	}
	return repay, units - repay, true
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
//
// RootTaskID 是这笔调用所属的原始任务；UnfundedUnits 是保留任务透支时「没有余额
// 兜底」的那部分预占：它只有在调用真的用掉时才变成欠款，没用掉就随结算消失。
type ModelCreditReservation struct {
	UID                int64
	RequestID          string
	RootTaskID         string
	ReservedUnits      int64
	Split              ModelCreditSplit
	UnfundedUnits      int64
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
		r.ReservedUnits <= 0 || r.Split.Total()+r.UnfundedUnits != r.ReservedUnits ||
		r.Split.Monthly < 0 || r.Split.Gift < 0 || r.Split.Paid < 0 || r.UnfundedUnits < 0 ||
		r.LeaseExpiresAt.IsZero() || !r.LeaseExpiresAt.After(r.CreatedAt) {
		return ErrModelCreditInvalid
	}
	if r.RootTaskID != "" && ValidateModelCreditTaskID(r.RootTaskID) != nil {
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
//
// RootTaskID 是必填项：每一笔模型调用都必须挂在已经 start 且当时 allowed 的原始
// 任务上（P0-3.9），缺了它就是硬错误而不是绕过门禁。旧探针调用同样要补上它。
type ModelCreditReserveInput struct {
	UID        int64
	RequestID  string
	RootTaskID string
	Units      int64
}

func (in ModelCreditReserveInput) Validate() error {
	if in.UID <= 0 || in.Units <= 0 || !validModelCreditToken(in.RequestID, ModelCreditRequestIDMaxLen) {
		return ErrModelCreditInvalid
	}
	if in.RootTaskID == "" {
		return ErrModelCreditTaskRequired
	}
	return ValidateModelCreditTaskID(in.RootTaskID)
}

// ModelCreditSettleInput settles a hold against what the request really cost.
// ActualUnits may be zero (a request that consumed nothing) and may exceed the
// hold: the excess is taken from whatever balance is really available now, and
// anything still uncovered becomes real arrears (P0-2.3/P0-3.2 — a dispatched
// call must always settle, and the overdraft is the ledger's debt, not a hole).
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

// PostgreSQL only ledger foundation. Every mutation runs in one database
// transaction and takes a FOR UPDATE lock on the account row, so concurrent
// reserves/settles of one user are serialized and the balance cannot be
// over-drawn by two writers at once.
//
// Reserve requires a started root task (RootTaskID) and holds monthly -> gift ->
// paid, returning ErrModelCreditInsufficient when the request does not fit. The
// single retained root task of a drained account is the only one allowed to hold
// more than the balance: the part it cannot fund is stored as unfunded hold units.
// Settle releases the unused funded hold back to the exact buckets it came from,
// takes an overage from what is really available, and books whatever remains
// uncovered — plus any funded shortfall — as real debt (debt_accrued). Grants to
// monthly/paid repay arrears first; gift never repays.
//
// Replays are idempotent: the same request id returns the stored reservation and
// never books debt twice, the same grant key returns the account unchanged, and
// the same settle with a different actual amount is rejected with
// ErrModelCreditConflict.
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

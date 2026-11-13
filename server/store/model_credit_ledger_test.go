package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestModelCreditUnitsAreIntegralMicroCredits(t *testing.T) {
	if ModelCreditUnitsPerCredit != 1_000_000 {
		t.Fatalf("one credit must be exactly one million units, got %d", ModelCreditUnitsPerCredit)
	}
	account := ModelCreditAccount{UID: 7, PaidUnits: ModelCreditUnitsPerCredit}
	split, ok := AllocateModelCreditUnits(account, ModelCreditUnitsPerCredit)
	if !ok || split.Paid != ModelCreditUnitsPerCredit || split.Total() != ModelCreditUnitsPerCredit {
		t.Fatalf("one credit is not one million units: %v %v", split, ok)
	}
}

func TestAllocateModelCreditUnitsOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account ModelCreditAccount
		units   int64
		want    ModelCreditSplit
		ok      bool
	}{
		{"monthly_first", ModelCreditAccount{UID: 1, MonthlyUnits: 5, GiftUnits: 3, PaidUnits: 2}, 5, ModelCreditSplit{Monthly: 5}, true},
		{"then_gift", ModelCreditAccount{UID: 1, MonthlyUnits: 5, GiftUnits: 3, PaidUnits: 2}, 7, ModelCreditSplit{Monthly: 5, Gift: 2}, true},
		{"then_paid", ModelCreditAccount{UID: 1, MonthlyUnits: 5, GiftUnits: 3, PaidUnits: 2}, 10, ModelCreditSplit{Monthly: 5, Gift: 3, Paid: 2}, true},
		{"insufficient", ModelCreditAccount{UID: 1, MonthlyUnits: 5, GiftUnits: 3, PaidUnits: 2}, 11, ModelCreditSplit{}, false},
		{"zero_is_invalid", ModelCreditAccount{UID: 1, PaidUnits: 10}, 0, ModelCreditSplit{}, false},
		{"negative_is_invalid", ModelCreditAccount{UID: 1, PaidUnits: 10}, -1, ModelCreditSplit{}, false},
		{"missing_uid_is_invalid", ModelCreditAccount{MonthlyUnits: 10}, 1, ModelCreditSplit{}, false},
		// Gift credits must never be spent while the account owes arrears.
		{"gift_skipped_under_debt", ModelCreditAccount{UID: 1, MonthlyUnits: 1, GiftUnits: 5, PaidUnits: 5, DebtUnits: 100}, 3, ModelCreditSplit{Monthly: 1, Paid: 2}, true},
		{"gift_alone_cannot_cover_debt", ModelCreditAccount{UID: 1, GiftUnits: 5, DebtUnits: 1}, 1, ModelCreditSplit{}, false},
		{"debt_is_not_balance", ModelCreditAccount{UID: 1, DebtUnits: 1000}, 1, ModelCreditSplit{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			split, ok := AllocateModelCreditUnits(tc.account, tc.units)
			if ok != tc.ok || split != tc.want {
				t.Fatalf("allocate %d from %+v = %+v,%v want %+v,%v", tc.units, tc.account, split, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestConsumeModelCreditSplitNeverExceedsHold(t *testing.T) {
	hold := ModelCreditSplit{Monthly: 4, Gift: 2, Paid: 1}
	for _, tc := range []struct {
		units int64
		want  ModelCreditSplit
	}{
		{0, ModelCreditSplit{}},
		{-3, ModelCreditSplit{}},
		{4, ModelCreditSplit{Monthly: 4}},
		{5, ModelCreditSplit{Monthly: 4, Gift: 1}},
		{7, hold},
		{99, hold},
	} {
		if got := ConsumeModelCreditSplit(hold, tc.units); got != tc.want {
			t.Fatalf("consume %d from %+v = %+v want %+v", tc.units, hold, got, tc.want)
		}
	}
}

func TestModelCreditGrantInputValidation(t *testing.T) {
	valid := ModelCreditGrantInput{UID: 42, Bucket: ModelCreditBucketMonthly, Units: 1_000_000, IdempotencyKey: "grant:2026-11"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
	for _, bucket := range []string{ModelCreditBucketPaid, ModelCreditBucketGift} {
		in := valid
		in.Bucket = bucket
		if err := in.Validate(); err != nil {
			t.Fatalf("bucket %q rejected: %v", bucket, err)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(ModelCreditGrantInput) ModelCreditGrantInput
	}{
		{"zero_uid", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.UID = 0; return in }},
		{"negative_uid", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.UID = -1; return in }},
		{"zero_units", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.Units = 0; return in }},
		{"negative_units", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.Units = -5; return in }},
		{"empty_key", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.IdempotencyKey = ""; return in }},
		{"long_key", func(in ModelCreditGrantInput) ModelCreditGrantInput {
			in.IdempotencyKey = strings.Repeat("k", ModelCreditIdempotencyKeyMaxLen+1)
			return in
		}},
		{"key_with_space", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.IdempotencyKey = "grant 1"; return in }},
		{"key_with_unicode", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.IdempotencyKey = "邀请"; return in }},
		{"debt_bucket", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.Bucket = ModelCreditBucketDebt; return in }},
		{"unknown_bucket", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.Bucket = "bonus"; return in }},
		{"empty_bucket", func(in ModelCreditGrantInput) ModelCreditGrantInput { in.Bucket = ""; return in }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.mutate(valid).Validate(); !errors.Is(err, ErrModelCreditInvalid) {
				t.Fatalf("invalid grant accepted: %v", err)
			}
		})
	}
}

func TestModelCreditReserveAndSettleInputValidation(t *testing.T) {
	reserve := ModelCreditReserveInput{UID: 3, RequestID: "req-123", RootTaskID: "root-123", Units: 7}
	if err := reserve.Validate(); err != nil {
		t.Fatalf("valid reserve rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(ModelCreditReserveInput) ModelCreditReserveInput
	}{
		{"zero_uid", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.UID = 0; return in }},
		{"zero_units", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.Units = 0; return in }},
		{"negative_units", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.Units = -2; return in }},
		{"empty_request", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.RequestID = ""; return in }},
		{"long_request", func(in ModelCreditReserveInput) ModelCreditReserveInput {
			in.RequestID = strings.Repeat("r", ModelCreditRequestIDMaxLen+1)
			return in
		}},
		{"request_with_space", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.RequestID = "req 1"; return in }},
		{"request_with_unicode", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.RequestID = "请求1"; return in }},
		{"missing_root", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.RootTaskID = ""; return in }},
		{"root_with_space", func(in ModelCreditReserveInput) ModelCreditReserveInput { in.RootTaskID = "root 1"; return in }},
		{"long_root", func(in ModelCreditReserveInput) ModelCreditReserveInput {
			in.RootTaskID = strings.Repeat("r", ModelCreditTaskIDMaxLen+1)
			return in
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 缺 root 是「必填」错误而不是「格式非法」：两者都是 400，但语义不同。
			if err := tc.mutate(reserve).Validate(); !errors.Is(err, ErrModelCreditInvalid) && !errors.Is(err, ErrModelCreditTaskRequired) {
				t.Fatalf("invalid reserve accepted: %v", err)
			}
		})
	}
	settle := ModelCreditSettleInput{UID: 3, RequestID: "req-123", ActualUnits: 0}
	if err := settle.Validate(); err != nil {
		t.Fatalf("zero actual units must settle: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(ModelCreditSettleInput) ModelCreditSettleInput
	}{
		{"zero_uid", func(in ModelCreditSettleInput) ModelCreditSettleInput { in.UID = 0; return in }},
		{"negative_actual", func(in ModelCreditSettleInput) ModelCreditSettleInput { in.ActualUnits = -1; return in }},
		{"empty_request", func(in ModelCreditSettleInput) ModelCreditSettleInput { in.RequestID = ""; return in }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.mutate(settle).Validate(); !errors.Is(err, ErrModelCreditInvalid) {
				t.Fatalf("invalid settle accepted: %v", err)
			}
		})
	}
}

func TestModelCreditEventKeysStayUniqueAndBounded(t *testing.T) {
	longestRequest := strings.Repeat("r", ModelCreditRequestIDMaxLen)
	for _, eventType := range []string{
		ModelCreditEventGrant, ModelCreditEventReserve, ModelCreditEventSettle,
		ModelCreditEventRelease, ModelCreditEventDebtAccrued, ModelCreditEventDebtRepaid,
	} {
		for _, bucket := range []string{
			ModelCreditBucketMonthly, ModelCreditBucketGift, ModelCreditBucketPaid, ModelCreditBucketDebt,
		} {
			key := ModelCreditEventKey(eventType, int64(^uint64(0)>>1), longestRequest, bucket)
			if len(key) > ModelCreditEventKeyMaxLen {
				t.Fatalf("event key %q is %d bytes, above %d", eventType, len(key), ModelCreditEventKeyMaxLen)
			}
		}
	}
	if key := ModelCreditGrantEventKey(strings.Repeat("k", ModelCreditIdempotencyKeyMaxLen)); len(key) > ModelCreditEventKeyMaxLen {
		t.Fatalf("grant key is %d bytes, above %d", len(key), ModelCreditEventKeyMaxLen)
	}
	reserveKey := ModelCreditEventKey(ModelCreditEventReserve, 1, "req-1", ModelCreditBucketPaid)
	settleKey := ModelCreditEventKey(ModelCreditEventSettle, 1, "req-1", ModelCreditBucketPaid)
	if reserveKey == settleKey {
		t.Fatalf("reserve and settle share the event key %q", reserveKey)
	}
	otherUser := ModelCreditEventKey(ModelCreditEventReserve, 2, "req-1", ModelCreditBucketPaid)
	if reserveKey == otherUser {
		t.Fatalf("request event key is not user-scoped: %q", reserveKey)
	}
}

func TestModelCreditAccountAndReservationValidation(t *testing.T) {
	account := ModelCreditAccount{UID: 5, MonthlyUnits: 2, GiftUnits: 3, PaidUnits: 4, DebtUnits: 6}
	if err := account.Validate(); err != nil {
		t.Fatalf("valid account rejected: %v", err)
	}
	if account.SpendableUnits() != 9 {
		t.Fatalf("spendable units must exclude debt, got %d", account.SpendableUnits())
	}
	for _, mutate := range []func(*ModelCreditAccount){
		func(a *ModelCreditAccount) { a.UID = 0 },
		func(a *ModelCreditAccount) { a.MonthlyUnits = -1 },
		func(a *ModelCreditAccount) { a.GiftUnits = -1 },
		func(a *ModelCreditAccount) { a.PaidUnits = -1 },
		func(a *ModelCreditAccount) { a.DebtUnits = -1 },
	} {
		copy := account
		mutate(&copy)
		if err := copy.Validate(); !errors.Is(err, ErrModelCreditInvalid) {
			t.Fatalf("invalid account accepted: %+v", copy)
		}
	}
	settled := int64(4)
	now := time.Now().UTC()
	reservation := ModelCreditReservation{
		UID: 5, RequestID: "req-9", ReservedUnits: 6, Split: ModelCreditSplit{Monthly: 6},
		State: ModelCreditReservationReserved, CreatedAt: now, LeaseExpiresAt: now.Add(ModelCreditReservationLease),
	}
	if err := reservation.Validate(); err != nil {
		t.Fatalf("valid reservation rejected: %v", err)
	}
	bad := []ModelCreditReservation{
		{UID: 5, RequestID: "req-9", ReservedUnits: 6, Split: ModelCreditSplit{Monthly: 5}, State: ModelCreditReservationReserved},
		{UID: 5, RequestID: "req-9", ReservedUnits: 6, Split: ModelCreditSplit{Monthly: 6}, State: "cancelled"},
		{UID: 5, RequestID: "req-9", ReservedUnits: 6, Split: ModelCreditSplit{Monthly: 6}, State: ModelCreditReservationSettled},
		{UID: 0, RequestID: "req-9", ReservedUnits: 6, Split: ModelCreditSplit{Monthly: 6}, State: ModelCreditReservationReserved},
		{UID: 5, RequestID: "", ReservedUnits: 6, Split: ModelCreditSplit{Monthly: 6}, State: ModelCreditReservationReserved},
	}
	for _, r := range bad {
		if err := r.Validate(); !errors.Is(err, ErrModelCreditInvalid) {
			t.Fatalf("invalid reservation accepted: %+v", r)
		}
	}
	if err := (ModelCreditReservation{
		UID: 5, RequestID: "req-9", ReservedUnits: 6, Split: ModelCreditSplit{Monthly: 6},
		SettledUnits: &settled, State: ModelCreditReservationSettled,
		CreatedAt: now, LeaseExpiresAt: now.Add(ModelCreditReservationLease),
	}).Validate(); !errors.Is(err, ErrModelCreditInvalid) {
		t.Fatal("settled reservation without settled_at accepted")
	}
}

func TestModelCreditReleasedReservationValidation(t *testing.T) {
	releasedAt := time.Now().UTC()
	createdAt := releasedAt.Add(-time.Minute)
	released := ModelCreditReservation{
		UID: 5, RequestID: "req-release", ReservedUnits: 3,
		Split: ModelCreditSplit{Monthly: 1, Gift: 1, Paid: 1},
		State: ModelCreditReservationReleased, CreatedAt: createdAt,
		LeaseExpiresAt: createdAt.Add(ModelCreditReservationLease), ReleasedAt: &releasedAt,
	}
	if err := released.Validate(); err != nil {
		t.Fatalf("valid released reservation rejected: %v", err)
	}
	invalid := released
	invalid.ReleasedAt = nil
	if err := invalid.Validate(); !errors.Is(err, ErrModelCreditInvalid) {
		t.Fatalf("released reservation without released_at accepted: %v", err)
	}
	invalid = released
	settledUnits := int64(1)
	invalid.SettledUnits = &settledUnits
	if err := invalid.Validate(); !errors.Is(err, ErrModelCreditInvalid) {
		t.Fatalf("released reservation with settled units accepted: %v", err)
	}
}
func TestModelCreditAddUnitsRejectsOverflow(t *testing.T) {
	if got, ok := ModelCreditAddUnits(5, 0); !ok || got != 5 {
		t.Fatalf("zero delta must keep the balance, got %d,%v", got, ok)
	}
	if got, ok := ModelCreditAddUnits(5, 7); !ok || got != 12 {
		t.Fatalf("add = %d,%v", got, ok)
	}
	max := int64(^uint64(0) >> 1)
	for _, tc := range []struct {
		balance, delta int64
	}{
		{-1, 5}, {5, -1},
		{max, 1}, {max - 1, 2},
	} {
		if _, ok := ModelCreditAddUnits(tc.balance, tc.delta); ok {
			t.Fatalf("%d+%d must not overflow", tc.balance, tc.delta)
		}
	}
	if got, ok := ModelCreditAddUnits(max, 0); !ok || got != max {
		t.Fatalf("max balance with zero delta = %d,%v", got, ok)
	}
}

// 欠款只能由月额度和付费余额偿还：赠送额度永不抵债（P0-2.5）。
func TestModelCreditGrantRepaysArrearsOnlyFromMonthlyAndPaid(t *testing.T) {
	account := ModelCreditAccount{UID: 5, MonthlyUnits: 1_000_000, GiftUnits: 2_000_000, PaidUnits: 3_000_000, DebtUnits: 4_000_000}
	if _, credited, ok := PlanModelCreditGrant(account, ModelCreditBucketGift, 2_500_000); !ok || credited != 2_500_000 {
		t.Fatalf("gift grant repaid arrears: ok=%v credited=%d", ok, credited)
	}
	repay, credited, ok := PlanModelCreditGrant(account, ModelCreditBucketPaid, 2_500_000)
	if !ok || repay != 2_500_000 || credited != 0 {
		t.Fatalf("paid grant must repay arrears first: ok=%v repay=%d credited=%d", ok, repay, credited)
	}
	repay, credited, ok = PlanModelCreditGrant(account, ModelCreditBucketMonthly, 9_000_000)
	if !ok || repay != 4_000_000 || credited != 5_000_000 {
		t.Fatalf("monthly grant above arrears must split: ok=%v repay=%d credited=%d", ok, repay, credited)
	}
	if _, _, ok := PlanModelCreditGrant(account, ModelCreditBucketDebt, 1); ok {
		t.Fatal("debt bucket accepted a grant")
	}
}

// 透支预留只扣当下真实可用的部分，剩余必须显式报给调用方去记欠款。
func TestModelCreditPartialAllocationReportsTheUnfundedRemainder(t *testing.T) {
	account := ModelCreditAccount{UID: 5, MonthlyUnits: 2, GiftUnits: 3, PaidUnits: 4}
	if split, remaining := AllocateModelCreditUnitsPartial(account, 6); remaining != 0 || split != (ModelCreditSplit{Monthly: 2, Gift: 3, Paid: 1}) {
		t.Fatalf("covered allocation split=%+v remaining=%d", split, remaining)
	}
	if split, remaining := AllocateModelCreditUnitsPartial(account, 10); remaining != 1 || split != (ModelCreditSplit{Monthly: 2, Gift: 3, Paid: 4}) {
		t.Fatalf("partial allocation split=%+v remaining=%d", split, remaining)
	}
	// 有欠款时赠送额度不可用，缺口全部留给欠款。
	indebted := ModelCreditAccount{UID: 5, GiftUnits: 5, DebtUnits: 1}
	if split, remaining := AllocateModelCreditUnitsPartial(indebted, 4); remaining != 4 || split.Total() != 0 {
		t.Fatalf("gift covered arrears while indebted: split=%+v remaining=%d", split, remaining)
	}
	if _, remaining := AllocateModelCreditUnitsPartial(account, 0); remaining != 0 {
		t.Fatalf("zero request reported a remainder: %d", remaining)
	}
}

func TestModelCreditDebtKeysAreRequestScoped(t *testing.T) {
	if got := ModelCreditDebtRepaymentKey("grant-1"); got != "debt_repaid:grant-1" {
		t.Fatalf("repayment key %q", got)
	}
	if got := ModelCreditDebtAccrualKey(9, "req-1"); got != "debt_accrued:9:req-1:debt" {
		t.Fatalf("accrual key %q", got)
	}
	if ModelCreditDebtAccrualKey(9, "req-1") == ModelCreditDebtAccrualKey(9, "req-2") {
		t.Fatal("two requests share one debt key")
	}
}

func TestModelCreditTaskIDValidation(t *testing.T) {
	if err := ValidateModelCreditTaskID("0f8fad5b-d9cb-469f-a165-70867728950e"); err != nil {
		t.Fatalf("uuid root rejected: %v", err)
	}
	for _, bad := range []string{"", "root 1", "根任务", strings.Repeat("r", ModelCreditTaskIDMaxLen+1)} {
		if err := ValidateModelCreditTaskID(bad); !errors.Is(err, ErrModelCreditInvalid) {
			t.Fatalf("invalid root accepted: %q", bad)
		}
	}
}

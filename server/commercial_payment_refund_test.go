package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// observingRefundProvider records what the order looked like when the refund
// reached the channel, which is the only place that proves the claim (and the
// credit freeze) happened before the money moved.
type observingRefundProvider struct {
	mu         sync.Mutex
	statuses   []string
	requestNos []string
	err        error
}

func (p *observingRefundProvider) Channel() string { return commercialPaymentChannelAlipayPage }
func (p *observingRefundProvider) Label() string   { return "refund observer" }
func (p *observingRefundProvider) CreatePayment(context.Context, *types.CommercialOrder) (*CommercialPaymentIntent, error) {
	return nil, context.Canceled
}

func (p *observingRefundProvider) ParseNotification(context.Context, *http.Request) (string, *types.CommercialPaymentConfirmation, error) {
	return "", nil, context.Canceled
}
func (p *observingRefundProvider) RefundPayment(_ context.Context, order *types.CommercialOrder, requestNo, _ string) (*types.CommercialRefundConfirmation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses = append(p.statuses, order.Status)
	p.requestNos = append(p.requestNos, requestNo)
	if p.err != nil {
		return nil, p.err
	}
	return &types.CommercialRefundConfirmation{
		Channel:         order.Channel,
		EventID:         requestNo,
		ProviderTradeNo: order.ProviderTradeNo,
		RefundRequestNo: requestNo,
		AmountFen:       order.AmountFen,
		Currency:        order.Currency,
		RefundedAt:      time.Now().UTC(),
	}, nil
}

func (p *observingRefundProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.statuses)
}

func (p *observingRefundProvider) seenStatus(state int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if state >= len(p.statuses) {
		return ""
	}
	return p.statuses[state]
}

// serialRefundStore serializes the store calls the handler makes, the way one
// database transaction per claim does, so the concurrency test exercises the
// claim protocol instead of a data race in the fake.
type serialRefundStore struct {
	*commercialPaymentTestStore
	mu sync.Mutex
}

func (s *serialRefundStore) GetCommercialOrder(uid int64, orderNo string) (*types.CommercialOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commercialPaymentTestStore.GetCommercialOrder(uid, orderNo)
}

func (s *serialRefundStore) BeginCommercialOrderRefund(orderNo, refundRequestNo string, staleAfter time.Duration) (*types.CommercialOrder, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commercialPaymentTestStore.BeginCommercialOrderRefund(orderNo, refundRequestNo, staleAfter)
}

func (s *serialRefundStore) FailCommercialOrderRefund(orderNo, refundRequestNo, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commercialPaymentTestStore.FailCommercialOrderRefund(orderNo, refundRequestNo, message)
}

func (s *serialRefundStore) CompleteCommercialOrderRefund(orderNo string, confirmation *types.CommercialRefundConfirmation) (*types.CommercialOrder, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commercialPaymentTestStore.CompleteCommercialOrderRefund(orderNo, confirmation)
}

// The order must already be claimed — status 'refunding' and its paid credit
// frozen — by the time the channel is asked for the refund. Otherwise a refund
// that fails locally has already paid the money out against credit the buyer can
// still spend.
func TestCommercialRefundClaimsAndFreezesCreditBeforeProviderCall(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-REFUND-FREEZE"] = &types.CommercialOrder{
		OrderNo: "CC-REFUND-FREEZE", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "fulfilled", ProviderTradeNo: "trade-refund-freeze", AmountFen: 1, Currency: "CNY",
		PlanModelCreditUnits: 5_000_000,
	}
	provider := &observingRefundProvider{}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})

	refunded, changed, err := handler.RefundOrder(context.Background(), "CC-REFUND-FREEZE", "operator refund")
	if err != nil || !changed || refunded == nil || refunded.Status != "refunded" {
		t.Fatalf("refund=%#v changed=%v err=%v", refunded, changed, err)
	}
	if got := provider.seenStatus(0); got != "refunding" {
		t.Fatalf("the provider was called with status %q, want refunding", got)
	}
	if got := provider.requestNos[0]; got != commercialRefundRequestNo("CC-REFUND-FREEZE") {
		t.Fatalf("refund request number=%q, want the deterministic CCRF- order number", got)
	}
	if store.refundFreezes["CC-REFUND-FREEZE"] != 1 {
		t.Fatalf("the claim froze credit %d times, want once", store.refundFreezes["CC-REFUND-FREEZE"])
	}
	// Completion consumes the hold; it must not survive a finished refund.
	if _, held := store.refundHolds["CC-REFUND-FREEZE"]; held {
		t.Fatal("a completed refund kept its hold")
	}
}

// Every reason the paid credit is not reversible today has to refuse the claim
// before the channel is touched: paid credit already spent, still held by an
// open model call, held by a machine upgrade, or spent on arrears.
func TestCommercialRefundRefusesBeforeProviderWhenCreditIsNotReversible(t *testing.T) {
	cases := map[string]error{
		"consumed":              errors.New("order model credit was already consumed and cannot be reversed: 1 paid settle event"),
		"arrears repayment":     errors.New("order model credit was already consumed and cannot be reversed: order already repaid arrears"),
		"open model call hold":  errors.New("order model credit was already consumed and cannot be reversed: 1 open reservation"),
		"machine upgrade hold":  errors.New("order model credit was already consumed and cannot be reversed: 1 machine upgrade"),
		"balance short":         errors.New("order model credit balance is lower than the purchased amount"),
		"legacy claim w/o hold": errors.New("commercial refund hold is missing"),
	}
	for name, refusal := range cases {
		t.Run(name, func(t *testing.T) {
			store := newCommercialPaymentTestStore()
			orderNo := "CC-REFUND-REFUSED"
			store.orders[orderNo] = &types.CommercialOrder{
				OrderNo: orderNo, UID: 38, Channel: commercialPaymentChannelAlipayPage,
				Status: "fulfilled", ProviderTradeNo: "trade-refund-refused", AmountFen: 1, Currency: "CNY",
				PlanModelCreditUnits: 5_000_000,
			}
			store.refundClaims[orderNo] = refusal
			provider := &observingRefundProvider{}
			handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})

			refunded, changed, err := handler.RefundOrder(context.Background(), orderNo, "operator refund")
			if err == nil || changed {
				t.Fatalf("refund was accepted: order=%#v changed=%v err=%v", refunded, changed, err)
			}
			if !errors.Is(err, errCommercialRefundConflict) {
				t.Fatalf("a refused claim must be reported as a conflict, got %v", err)
			}
			if provider.calls() != 0 {
				t.Fatalf("a refused claim reached the provider %d time(s)", provider.calls())
			}
			order := store.orders[orderNo]
			if order.Status != "fulfilled" {
				t.Fatalf("a refused claim changed the order: %#v", order)
			}
			if _, held := store.refundHolds[orderNo]; held {
				t.Fatal("a refused claim froze credit")
			}
		})
	}
}

// Different attempts to refund the same order must serialize on the order: only
// one of them may take the claim and call the channel, the rest see it in
// progress.
func TestCommercialRefundConcurrentClaimsSerialize(t *testing.T) {
	base := newCommercialPaymentTestStore()
	orderNo := "CC-REFUND-CONCURRENT"
	base.orders[orderNo] = &types.CommercialOrder{
		OrderNo: orderNo, UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "fulfilled", ProviderTradeNo: "trade-refund-concurrent", AmountFen: 1, Currency: "CNY",
		PlanModelCreditUnits: 5_000_000,
	}
	store := &serialRefundStore{commercialPaymentTestStore: base}
	provider := &observingRefundProvider{}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})

	const attempts = 8
	var wg sync.WaitGroup
	results := make([]bool, attempts)
	errs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, changed, err := handler.RefundOrder(context.Background(), orderNo, "concurrent refund")
			results[slot], errs[slot] = changed, err
		}(i)
	}
	wg.Wait()

	completed := 0
	for slot := 0; slot < attempts; slot++ {
		if results[slot] {
			completed++
		}
		if errs[slot] != nil && !errors.Is(errs[slot], errCommercialRefundConflict) {
			t.Fatalf("attempt %d failed with an unexpected error: %v", slot, errs[slot])
		}
	}
	if completed != 1 {
		t.Fatalf("%d concurrent attempts completed the refund, want exactly 1", completed)
	}
	if provider.calls() != 1 {
		t.Fatalf("the provider was called %d times for one order, want 1", provider.calls())
	}
	if base.refundFreezes[orderNo] != 1 {
		t.Fatalf("the claim froze credit %d times, want once", base.refundFreezes[orderNo])
	}
	if order := base.orders[orderNo]; order.Status != "refunded" {
		t.Fatalf("final order status=%q, want refunded", order.Status)
	}
}

// A subscription order never booked paid credit — its allowance belongs to a
// monthly period — so the refund has nothing to freeze and follows the monthly
// entitlement handling as before. The hold row is still written, with zero units,
// because its presence is what tells a safe claim from a legacy one.
func TestCommercialRefundWithoutPaidCreditHoldsNothingAndCompletes(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-REFUND-MONTHLY"] = &types.CommercialOrder{
		OrderNo: "CC-REFUND-MONTHLY", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "fulfilled", ProviderTradeNo: "trade-refund-monthly", AmountFen: 2990, Currency: "CNY",
		PlanSlug: "personal-30d", PlanDurationDays: 30,
	}
	provider := &observingRefundProvider{}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})

	refunded, changed, err := handler.RefundOrder(context.Background(), "CC-REFUND-MONTHLY", "subscription refund")
	if err != nil || !changed || refunded == nil || refunded.Status != "refunded" || provider.calls() != 1 {
		t.Fatalf("refund=%#v changed=%v calls=%d err=%v", refunded, changed, provider.calls(), err)
	}
	if units, held := store.refundHolds["CC-REFUND-MONTHLY"]; held {
		t.Fatalf("a refund without paid credit left a hold of %d units", units)
	}
}

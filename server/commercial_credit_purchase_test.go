package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const creditPurchaseTestPeerSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// enableCreditPlanSale opens both credit sale gates for one test: the operator
// switch and the relay credit settlement secret.
func enableCreditPlanSale(t *testing.T) {
	t.Helper()
	t.Setenv("CATSCO_CREDIT_PLAN_SALE_ENABLED", "true")
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", creditPurchaseTestPeerSecret)
}

// countingRelayRecorder is the relay stand-in for the credit tests. Every hit is
// counted, because a credit purchase must not talk to the relay at all: the
// buyer may be brand new and have no key yet.
type countingRelayRecorder struct {
	calls   int32
	handler func(w http.ResponseWriter, r *http.Request)
}

func (c *countingRelayRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c.calls, 1)
		if c.handler != nil {
			c.handler(w, r)
			return
		}
		t.Errorf("credit purchase called the relay: %s %s", r.Method, r.URL.RequestURI())
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(relay.Close)
	return relay
}

func (c *countingRelayRecorder) admin(relay *httptest.Server) *RelayAdminClient {
	return &RelayAdminClient{baseURL: relay.URL, token: "test-token", client: relay.Client()}
}

// TestCommercialValidatePurchaseAcceptsCreditOnlyPlanWithoutRelayKey is the
// production blocker: a legal credit-only package has no legacy model budgets
// and its buyer (often brand new) has no relay key, yet the purchase must be
// admitted instead of failing with a 503.
func TestCommercialValidatePurchaseAcceptsCreditOnlyPlanWithoutRelayKey(t *testing.T) {
	enableCreditPlanSale(t)
	relay := &countingRelayRecorder{}
	server := relay.server(t)
	syncer := NewCommercialRelaySyncer(
		&commercialRelaySyncTestStore{},
		relay.admin(server),
		CommercialRelaySyncerOptions{EnforceUIDs: map[int64]bool{9001: true}},
	)
	plan := creditOnlyTestPlan()
	if !commercialCreditOnlyPlan(plan) || commercialModelBudgetsConfigured(plan.ModelBudgets) {
		t.Fatalf("fixture is not a credit-only plan: %#v", plan)
	}
	if err := syncer.ValidatePurchase(context.Background(), 9001, plan); err != nil {
		t.Fatalf("credit-only plan was refused: %v", err)
	}
	if calls := atomic.LoadInt32(&relay.calls); calls != 0 {
		t.Fatalf("credit-only purchase read the relay %d time(s); a new buyer has no key", calls)
	}
}

// TestCommercialValidatePurchaseKeepsLegacyModelBudgetContract pins the legacy
// path: the relay key and every plan model mapping are still required, and those
// failures stay retryable (they are not plan-configuration errors).
func TestCommercialValidatePurchaseKeepsLegacyModelBudgetContract(t *testing.T) {
	relay := &countingRelayRecorder{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/users/38/key/limits" {
			t.Errorf("unexpected relay call: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(commercialRelayUsageUser{
			Configured: true,
			Key:        &commercialRelayKeySummary{State: "active"},
			Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
				Provider: "openai", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra"},
			}}},
		})
	}}
	server := relay.server(t)
	syncer := NewCommercialRelaySyncer(
		&commercialRelaySyncTestStore{},
		relay.admin(server),
		CommercialRelaySyncerOptions{EnforceUIDs: map[int64]bool{38: true}},
	)

	missingMapping := legacyBudgetTestPlan()
	if err := syncer.ValidatePurchase(context.Background(), 38, missingMapping); err == nil {
		t.Fatal("legacy plan with an unmapped relay model was accepted")
	} else if errors.Is(err, errCommercialPlanNotPurchasable) {
		t.Fatalf("a relay mapping drift must stay a retryable relay error, got %v", err)
	}

	mapped := legacyBudgetTestPlan()
	mapped.ModelBudgets = map[string]float64{"gpt-5.6-terra": 20}
	if err := syncer.ValidatePurchase(context.Background(), 38, mapped); err != nil {
		t.Fatalf("legacy plan with a live model mapping was refused: %v", err)
	}

	noBudgets := legacyBudgetTestPlan()
	noBudgets.ModelBudgets = nil
	noBudgets.MonthlyBudget = 10
	if err := syncer.ValidatePurchase(context.Background(), 38, noBudgets); err == nil || !strings.Contains(err.Error(), "no relay model budgets") {
		t.Fatalf("legacy plan without model budgets was accepted: %v", err)
	}
}

// TestCommercialValidatePurchaseFailsClosedOnBrokenCreditPlans keeps the new
// credit branch from being a blanket allow: a mixed package (credit plus CNY
// budgets), a tier the relay cannot price, an amount the ledger cannot book and
// a package without a billing period are all permanent configuration errors.
func TestCommercialValidatePurchaseFailsClosedOnBrokenCreditPlans(t *testing.T) {
	enableCreditPlanSale(t)
	relay := &countingRelayRecorder{}
	server := relay.server(t)
	syncer := NewCommercialRelaySyncer(
		&commercialRelaySyncTestStore{},
		relay.admin(server),
		CommercialRelaySyncerOptions{EnforceUIDs: map[int64]bool{9002: true}},
	)
	for name, test := range map[string]struct {
		mutate  func(*types.CommercialPlan)
		message string
	}{
		"credit plus monthly budget": {
			mutate:  func(plan *types.CommercialPlan) { plan.MonthlyBudget = 5 },
			message: "mixes model credit",
		},
		"credit plus model budgets": {
			mutate:  func(plan *types.CommercialPlan) { plan.ModelBudgets = map[string]float64{"deepseek-flash": 20} },
			message: "mixes model credit",
		},
		"tier off the ladder": {
			mutate:  func(plan *types.CommercialPlan) { plan.PlanTier = 0 },
			message: "not on the priced ladder",
		},
		"tier above the ladder": {
			mutate:  func(plan *types.CommercialPlan) { plan.PlanTier = store.ModelCreditTaskPlanTierMax + 1 },
			message: "not on the priced ladder",
		},
		"no billing period": {
			mutate:  func(plan *types.CommercialPlan) { plan.DurationDays = 0 },
			message: "no billing period",
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := creditOnlyTestPlan()
			test.mutate(plan)
			err := syncer.ValidatePurchase(context.Background(), 9002, plan)
			if err == nil {
				t.Fatalf("broken credit plan was admitted: %#v", plan)
			}
			if !errors.Is(err, errCommercialPlanNotPurchasable) {
				t.Fatalf("broken credit plan must be a permanent refusal, got %v", err)
			}
			if !strings.Contains(err.Error(), test.message) {
				t.Fatalf("refusal %q does not explain %q", err, test.message)
			}
		})
	}
	if calls := atomic.LoadInt32(&relay.calls); calls != 0 {
		t.Fatalf("credit validation read the relay %d time(s)", calls)
	}
}

// TestCommercialValidatePurchaseRefusesCreditWithoutSettlementChannel keeps the
// credit branch closed while the relay has no credit settlement contract: selling
// it then would charge the buyer for credit nobody can spend.
func TestCommercialValidatePurchaseRefusesCreditWithoutSettlementChannel(t *testing.T) {
	t.Setenv("CATSCO_CREDIT_PLAN_SALE_ENABLED", "true")
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", "")
	relay := &countingRelayRecorder{}
	server := relay.server(t)
	syncer := NewCommercialRelaySyncer(
		&commercialRelaySyncTestStore{},
		relay.admin(server),
		CommercialRelaySyncerOptions{EnforceUIDs: map[int64]bool{9003: true}},
	)
	err := syncer.ValidatePurchase(context.Background(), 9003, creditOnlyTestPlan())
	if err == nil || !errors.Is(err, errCommercialPlanNotPurchasable) {
		t.Fatalf("credit was sellable without the relay settlement channel: %v", err)
	}
	if calls := atomic.LoadInt32(&relay.calls); calls != 0 {
		t.Fatalf("credit validation read the relay %d time(s)", calls)
	}
}

// creditPurchaseHandler builds the real payment handler used by the order tests.
func creditPurchaseHandler(
	t *testing.T,
	paymentStore *commercialPaymentTestStore,
	provider *queryCommercialPaymentProvider,
	syncer *CommercialRelaySyncer,
) *CommercialPaymentHandler {
	t.Helper()
	return NewCommercialPaymentHandler(paymentStore, CommercialPaymentHandlerOptions{
		PublicEnabled: true,
		Providers:     []CommercialPaymentProvider{provider},
		SaleChannels:  map[string]bool{commercialPaymentChannelAlipayPage: true},
		Syncer:        syncer,
	})
}

// TestCommercialCreditOnlyOrderCreatesRealPaymentWithoutRelayKey drives the real
// order-creation handler for a credit-only package and requires it to reach the
// payment provider: no legacy model budgets, no relay key, no relay traffic.
func TestCommercialCreditOnlyOrderCreatesRealPaymentWithoutRelayKey(t *testing.T) {
	enableCreditPlanSale(t)
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{creditOnlyTestPlan()}
	store.plans[0].ID = 11
	provider := &queryCommercialPaymentProvider{intent: &CommercialPaymentIntent{
		CheckoutURL: "https://openapi.alipay.test/pay", ExpiresAt: time.Now().UTC().Add(20 * time.Minute),
	}}
	relay := &countingRelayRecorder{}
	server := relay.server(t)
	syncer := NewCommercialRelaySyncer(
		&commercialRelaySyncTestStore{},
		relay.admin(server),
		CommercialRelaySyncerOptions{EnforceEnabled: true},
	)
	handler := creditPurchaseHandler(t, store, provider, syncer)

	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders",
		`{"plan_id":11,"channel":"alipay_page","client_request_id":"credit_order_request_123"}`, 51))
	if recorder.Code != http.StatusOK {
		t.Fatalf("credit-only order status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Order *types.CommercialOrder `json:"order"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Order == nil || payload.Order.Status != "pending" || payload.Order.CheckoutURL == "" {
		t.Fatalf("credit-only order never reached the provider: %#v", payload.Order)
	}
	if payload.Order.AmountFen != 3900 || payload.Order.PlanMonthlyBudget != 0 || len(payload.Order.PlanModelBudgets) != 0 {
		t.Fatalf("unexpected credit order snapshot: %#v", payload.Order)
	}
	if provider.createCalls != 1 {
		t.Fatalf("provider create called %d time(s), want 1", provider.createCalls)
	}
	if calls := atomic.LoadInt32(&relay.calls); calls != 0 {
		t.Fatalf("credit-only order read the relay %d time(s)", calls)
	}
}

// TestCommercialBrokenCreditPlanOrderFailsClosedBeforePayment is the negative
// half of the handler test: a package whose tier the relay cannot price, and a
// package mixing credit with legacy model budgets, are refused before any order
// or provider call exists.
func TestCommercialBrokenCreditPlanOrderFailsClosedBeforePayment(t *testing.T) {
	for name, mutate := range map[string]func(*types.CommercialPlan){
		"tier off the ladder":       func(plan *types.CommercialPlan) { plan.PlanTier = 0 },
		"credit plus model budgets": func(plan *types.CommercialPlan) { plan.ModelBudgets = map[string]float64{"deepseek-flash": 20} },
	} {
		t.Run(name, func(t *testing.T) {
			enableCreditPlanSale(t)
			store := newCommercialPaymentTestStore()
			store.plans = []*types.CommercialPlan{creditOnlyTestPlan()}
			store.plans[0].ID = 12
			mutate(store.plans[0])
			provider := &queryCommercialPaymentProvider{intent: &CommercialPaymentIntent{
				CheckoutURL: "https://openapi.alipay.test/pay", ExpiresAt: time.Now().UTC().Add(20 * time.Minute),
			}}
			relay := &countingRelayRecorder{}
			server := relay.server(t)
			syncer := NewCommercialRelaySyncer(
				&commercialRelaySyncTestStore{},
				relay.admin(server),
				CommercialRelaySyncerOptions{EnforceEnabled: true},
			)
			handler := creditPurchaseHandler(t, store, provider, syncer)

			recorder := httptest.NewRecorder()
			handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders",
				`{"plan_id":12,"channel":"alipay_page","client_request_id":"broken_credit_request_1"}`, 52))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("broken credit plan order status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if len(store.orders) != 0 || provider.createCalls != 0 {
				t.Fatalf("broken credit plan created an order or payment: orders=%d create=%d", len(store.orders), provider.createCalls)
			}
			if calls := atomic.LoadInt32(&relay.calls); calls != 0 {
				t.Fatalf("broken credit plan read the relay %d time(s)", calls)
			}
		})
	}
}

// TestCommercialLegacyOrderStaysRetryableWhenRelayMappingDrifts pins the legacy
// answer: a relay/model-mapping failure is transient and must keep returning 503
// with no order, not the permanent "plan is unavailable" refusal.
func TestCommercialLegacyOrderStaysRetryableWhenRelayMappingDrifts(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{legacyBudgetTestPlan()}
	store.plans[0].ID = 13
	provider := &queryCommercialPaymentProvider{intent: &CommercialPaymentIntent{
		CheckoutURL: "https://openapi.alipay.test/pay", ExpiresAt: time.Now().UTC().Add(20 * time.Minute),
	}}
	relay := &countingRelayRecorder{handler: func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(commercialRelayUsageUser{
			Configured: true,
			Key:        &commercialRelayKeySummary{State: "active"},
			Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
				Provider: "openai", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra"},
			}}},
		})
	}}
	server := relay.server(t)
	syncer := NewCommercialRelaySyncer(
		&commercialRelaySyncTestStore{},
		relay.admin(server),
		CommercialRelaySyncerOptions{EnforceEnabled: true},
	)
	handler := creditPurchaseHandler(t, store, provider, syncer)

	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders",
		`{"plan_id":13,"channel":"alipay_page","client_request_id":"legacy_mapping_request_1"}`, 53))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy mapping drift status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(store.orders) != 0 || provider.createCalls != 0 {
		t.Fatalf("legacy mapping drift created an order or payment: orders=%d create=%d", len(store.orders), provider.createCalls)
	}
	if calls := atomic.LoadInt32(&relay.calls); calls == 0 {
		t.Fatal("legacy purchase did not validate the buyer's relay key")
	}
}

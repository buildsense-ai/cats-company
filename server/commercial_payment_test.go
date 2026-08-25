package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
	"github.com/smartwalle/alipay/v3"
)

type commercialPaymentTestStore struct {
	*commercialTestStore
	orders      map[string]*types.CommercialOrder
	requestIDs  map[string]string
	trialClaims map[int64]bool
}

type commercialRelaySyncTestStore struct {
	summary       *types.CommercialSummary
	managed       []*types.CommercialManagedRelayBudget
	replaced      []*types.CommercialManagedRelayBudget
	reconcileUIDs []int64
	replacedCh    chan struct{}
	replacedUIDCh chan int64
}

type commercialRelayBaselineTestStore struct {
	*commercialRelaySyncTestStore
	profile  string
	budgets  map[string]float64
	startsAt time.Time
	created  int
}

func (s *commercialRelayBaselineTestStore) EnsureCommercialRelayBaseline(_ int64, profile string, budgets map[string]float64, startsAt time.Time) (bool, error) {
	s.profile = profile
	s.budgets = budgets
	s.startsAt = startsAt
	s.created++
	var grants []*types.CommercialQuotaGrant
	var entitlements []*types.CommercialEntitlement
	totals := map[string]float64{}
	total := 0.0
	if s.summary != nil {
		grants = append(grants, s.summary.Grants...)
		entitlements = append(entitlements, s.summary.Entitlements...)
		total = s.summary.TotalCNY
		for model, amount := range s.summary.TotalsByModel {
			totals[model] = amount
		}
	}
	for model, amount := range budgets {
		grants = append(grants, &types.CommercialQuotaGrant{GrantType: profile, Model: model, AmountCNY: amount})
		totals[model] += amount
		total += amount
	}
	entitlements = append(entitlements, &types.CommercialEntitlement{Source: profile, State: "active", StartsAt: startsAt})
	s.summary = &types.CommercialSummary{
		UID:           38,
		TotalCNY:      total,
		TotalsByModel: totals,
		Grants:        grants,
		Entitlements:  entitlements,
	}
	return true, nil
}

func (s *commercialRelaySyncTestStore) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	return s.summary, nil
}

func (s *commercialRelaySyncTestStore) ListCommercialManagedRelayBudgets(int64) ([]*types.CommercialManagedRelayBudget, error) {
	return s.managed, nil
}

func (s *commercialRelaySyncTestStore) ReplaceCommercialManagedRelayBudgets(uid int64, budgets []*types.CommercialManagedRelayBudget) error {
	s.replaced = budgets
	if s.replacedCh != nil {
		select {
		case s.replacedCh <- struct{}{}:
		default:
		}
	}
	if s.replacedUIDCh != nil {
		select {
		case s.replacedUIDCh <- uid:
		default:
		}
	}
	return nil
}

func (s *commercialRelaySyncTestStore) CommercialRelaySyncRequired(int64) (bool, error) {
	return true, nil
}

func (s *commercialRelaySyncTestStore) ListCommercialReconcileUIDs(afterUID int64, limit int) ([]int64, error) {
	uids := make([]int64, 0, len(s.reconcileUIDs))
	for _, uid := range s.reconcileUIDs {
		if uid <= afterUID {
			continue
		}
		uids = append(uids, uid)
		if len(uids) == limit {
			break
		}
	}
	return uids, nil
}

type queryCommercialPaymentProvider struct {
	paid         bool
	confirmation *types.CommercialPaymentConfirmation
	calls        int
	intent       *CommercialPaymentIntent
	createCalls  int
	closeErr     error
	closeCalls   int
	refund       *types.CommercialRefundConfirmation
	refundErr    error
	refundCalls  int
}

func (p *queryCommercialPaymentProvider) Channel() string {
	return commercialPaymentChannelAlipayPage
}
func (p *queryCommercialPaymentProvider) Label() string { return "支付宝" }
func (p *queryCommercialPaymentProvider) CreatePayment(context.Context, *types.CommercialOrder) (*CommercialPaymentIntent, error) {
	p.createCalls++
	if p.intent == nil {
		return nil, context.Canceled
	}
	return p.intent, nil
}
func (p *queryCommercialPaymentProvider) ParseNotification(context.Context, *http.Request) (string, *types.CommercialPaymentConfirmation, error) {
	return "", nil, context.Canceled
}
func (p *queryCommercialPaymentProvider) QueryPayment(context.Context, *types.CommercialOrder) (*types.CommercialPaymentConfirmation, bool, error) {
	p.calls++
	return p.confirmation, p.paid, nil
}
func (p *queryCommercialPaymentProvider) ClosePayment(context.Context, *types.CommercialOrder) error {
	p.closeCalls++
	return p.closeErr
}
func (p *queryCommercialPaymentProvider) RefundPayment(context.Context, *types.CommercialOrder, string, string) (*types.CommercialRefundConfirmation, error) {
	p.refundCalls++
	return p.refund, p.refundErr
}

type fakeAlipayPaymentClient struct {
	pageURL          *url.URL
	pageErr          error
	query            *alipay.TradeQueryRsp
	queryErr         error
	close            *alipay.TradeCloseRsp
	closeErr         error
	refund           *alipay.TradeRefundRsp
	refundErr        error
	notification     *alipay.Notification
	notificationErr  error
	lastPagePay      alipay.TradePagePay
	lastQuery        alipay.TradeQuery
	lastClose        alipay.TradeClose
	lastRefund       alipay.TradeRefund
	lastNotification url.Values
}

func (c *fakeAlipayPaymentClient) TradePagePay(request alipay.TradePagePay) (*url.URL, error) {
	c.lastPagePay = request
	return c.pageURL, c.pageErr
}

func (c *fakeAlipayPaymentClient) TradeQuery(_ context.Context, request alipay.TradeQuery) (*alipay.TradeQueryRsp, error) {
	c.lastQuery = request
	return c.query, c.queryErr
}

func (c *fakeAlipayPaymentClient) TradeClose(_ context.Context, request alipay.TradeClose) (*alipay.TradeCloseRsp, error) {
	c.lastClose = request
	return c.close, c.closeErr
}

func (c *fakeAlipayPaymentClient) TradeRefund(_ context.Context, request alipay.TradeRefund) (*alipay.TradeRefundRsp, error) {
	c.lastRefund = request
	return c.refund, c.refundErr
}

func (c *fakeAlipayPaymentClient) DecodeNotification(_ context.Context, values url.Values) (*alipay.Notification, error) {
	c.lastNotification = values
	return c.notification, c.notificationErr
}

func newCommercialPaymentTestStore() *commercialPaymentTestStore {
	return &commercialPaymentTestStore{
		commercialTestStore: &commercialTestStore{},
		orders:              map[string]*types.CommercialOrder{},
		requestIDs:          map[string]string{},
		trialClaims:         map[int64]bool{},
	}
}

func (s *commercialPaymentTestStore) GetCommercialPlan(id int64) (*types.CommercialPlan, error) {
	for _, plan := range s.plans {
		if plan.ID == id {
			copy := *plan
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *commercialPaymentTestStore) GetCommercialPlanBySlug(slug string) (*types.CommercialPlan, error) {
	for _, plan := range s.plans {
		if plan.Slug == slug {
			copy := *plan
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *commercialPaymentTestStore) CreateCommercialOrder(order *types.CommercialOrder) (*types.CommercialOrder, error) {
	key := string(rune(order.UID)) + ":" + order.ClientRequestID
	if orderNo := s.requestIDs[key]; orderNo != "" {
		copy := *s.orders[orderNo]
		return &copy, nil
	}
	plan, _ := s.GetCommercialPlan(order.PlanID)
	copy := *order
	copy.ID = int64(len(s.orders) + 1)
	copy.PlanSlug = plan.Slug
	copy.PlanName = plan.Name
	copy.PlanDescription = plan.Description
	copy.PlanDurationDays = plan.DurationDays
	copy.PlanMonthlyBudget = plan.MonthlyBudget
	copy.PlanModelBudgets = plan.ModelBudgets
	copy.AmountFen = plan.PriceFen
	copy.Currency = plan.Currency
	copy.Status = "created"
	copy.CreatedAt = time.Now().UTC()
	copy.UpdatedAt = copy.CreatedAt
	s.orders[copy.OrderNo] = &copy
	s.requestIDs[key] = copy.OrderNo
	return &copy, nil
}

func (s *commercialPaymentTestStore) SetCommercialOrderPaymentIntent(orderNo, checkoutURL string, expiresAt time.Time) (*types.CommercialOrder, error) {
	order := s.orders[orderNo]
	order.Status = "pending"
	order.CheckoutURL = checkoutURL
	order.ExpiresAt = &expiresAt
	copy := *order
	return &copy, nil
}

func (s *commercialPaymentTestStore) BeginCommercialOrderPayment(orderNo string, expiresAt time.Time) (*types.CommercialOrder, bool, error) {
	order := s.orders[orderNo]
	if order == nil {
		return nil, false, context.Canceled
	}
	stalePending := order.Status == "pending" && order.CheckoutURL == "" && order.UpdatedAt.Before(time.Now().UTC().Add(-30*time.Second))
	if order.Status != "created" && order.Status != "failed" && !stalePending {
		copy := *order
		return &copy, false, nil
	}
	order.Status = "pending"
	order.CheckoutURL = ""
	order.ExpiresAt = &expiresAt
	order.UpdatedAt = time.Now().UTC()
	copy := *order
	return &copy, true, nil
}

func (s *commercialPaymentTestStore) FailCommercialOrder(orderNo, message string) error {
	s.orders[orderNo].Status = "failed"
	s.orders[orderNo].LastError = message
	return nil
}

func (s *commercialPaymentTestStore) GetCommercialOrder(uid int64, orderNo string) (*types.CommercialOrder, error) {
	order := s.orders[orderNo]
	if order == nil || (uid > 0 && order.UID != uid) {
		return nil, nil
	}
	copy := *order
	return &copy, nil
}

func (s *commercialPaymentTestStore) ListCommercialOrders(uid int64, limit int) ([]*types.CommercialOrder, error) {
	orders := []*types.CommercialOrder{}
	for _, order := range s.orders {
		if uid == 0 || order.UID == uid {
			copy := *order
			orders = append(orders, &copy)
		}
	}
	return orders, nil
}

func (s *commercialPaymentTestStore) CancelCommercialOrder(uid int64, orderNo, reason string) (*types.CommercialOrder, bool, error) {
	order := s.orders[orderNo]
	if order == nil || order.UID != uid {
		return nil, false, nil
	}
	if order.Status != "created" && order.Status != "pending" && order.Status != "failed" {
		copy := *order
		return &copy, false, nil
	}
	now := time.Now().UTC()
	order.Status = "closed"
	order.ClosedAt = &now
	order.CheckoutURL = ""
	order.LastError = reason
	copy := *order
	return &copy, true, nil
}

func (s *commercialPaymentTestStore) CloseExpiredCommercialOrders(limit int) (int64, error) {
	return 0, nil
}

func (s *commercialPaymentTestStore) FulfillCommercialOrder(orderNo string, confirmation *types.CommercialPaymentConfirmation) (*types.CommercialOrder, bool, error) {
	order := s.orders[orderNo]
	if order.Status == "fulfilled" {
		copy := *order
		return &copy, false, nil
	}
	if confirmation.AmountFen != order.AmountFen || confirmation.Currency != order.Currency {
		return nil, false, context.Canceled
	}
	now := time.Now().UTC()
	order.Status = "fulfilled"
	order.PaidAt = &now
	order.FulfilledAt = &now
	copy := *order
	return &copy, true, nil
}

func (s *commercialPaymentTestStore) BeginCommercialOrderRefund(orderNo, refundRequestNo string, _ time.Duration) (*types.CommercialOrder, bool, error) {
	order := s.orders[orderNo]
	if order == nil {
		return nil, false, nil
	}
	if order.Status != "fulfilled" {
		copy := *order
		return &copy, false, nil
	}
	order.Status = "refunding"
	order.RefundRequestNo = refundRequestNo
	copy := *order
	return &copy, true, nil
}

func (s *commercialPaymentTestStore) FailCommercialOrderRefund(orderNo, refundRequestNo, message string) error {
	order := s.orders[orderNo]
	if order != nil && order.Status == "refunding" && order.RefundRequestNo == refundRequestNo {
		order.Status = "fulfilled"
		order.LastError = message
	}
	return nil
}

func (s *commercialPaymentTestStore) CompleteCommercialOrderRefund(orderNo string, confirmation *types.CommercialRefundConfirmation) (*types.CommercialOrder, bool, error) {
	order := s.orders[orderNo]
	if order == nil || confirmation == nil || order.RefundRequestNo != confirmation.RefundRequestNo {
		return nil, false, context.Canceled
	}
	if order.Status == "refunded" {
		copy := *order
		return &copy, false, nil
	}
	order.Status = "refunded"
	refundedAt := confirmation.RefundedAt
	order.RefundedAt = &refundedAt
	copy := *order
	return &copy, true, nil
}

func (s *commercialPaymentTestStore) ClaimCommercialTrial(uid int64, planSlug string) (*types.CommercialSummary, error) {
	s.trialClaims[uid] = true
	return s.GetCommercialSummary(uid)
}

func (s *commercialPaymentTestStore) HasCommercialTrial(uid int64, planSlug string) (bool, error) {
	return s.trialClaims[uid], nil
}

func commercialPaymentRequest(method, path, body string, uid int64) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, uid))
	return req
}

func TestCommercialCatalogRespectsSaleRollout(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{
		{ID: 1, Slug: "hidden", Name: "Hidden", PriceFen: 100, Currency: "CNY", SaleState: "hidden", DurationDays: 30, State: 0, ModelBudgets: map[string]float64{"MiniMax-M3": 1}},
		{ID: 2, Slug: "test", Name: "Test", PriceFen: 200, Currency: "CNY", SaleState: "test", DurationDays: 30, State: 0, InternalQuotaTokens: 50_000_000, ModelBudgets: map[string]float64{"MiniMax-M3": 2}},
		{ID: 3, Slug: "public", Name: "Public", PriceFen: 300, Currency: "CNY", SaleState: "public", DurationDays: 30, State: 0, ModelBudgets: map[string]float64{"MiniMax-M3": 3}},
		{ID: 4, Slug: "empty", Name: "Empty", PriceFen: 400, Currency: "CNY", SaleState: "test", DurationDays: 30, State: 0},
		{ID: 5, Slug: "monthly-only", Name: "Monthly only", PriceFen: 500, Currency: "CNY", SaleState: "test", DurationDays: 30, State: 0, MonthlyBudget: 10},
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs:     map[int64]bool{38: true},
		TestPayments: map[int64]bool{38: true},
		Providers:    []CommercialPaymentProvider{NewTestCommercialPaymentProvider()},
	})
	recorder := httptest.NewRecorder()
	handler.HandleCatalog(recorder, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/catalog", "", 38))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Plans    []*types.CommercialPlan    `json:"plans"`
		Channels []commercialPaymentChannel `json:"channels"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Plans) != 2 || payload.Plans[0].Slug != "test" || payload.Plans[1].Slug != "public" {
		t.Fatalf("unexpected plans: %#v", payload.Plans)
	}
	if payload.Plans[0].InternalQuotaTokens != 0 || len(payload.Plans[0].ModelBudgets) != 0 || payload.Plans[0].MonthlyBudget != 0 {
		t.Fatalf("public catalog leaked internal quota data: %#v", payload.Plans[0])
	}
	if strings.Contains(recorder.Body.String(), "internal_quota_tokens") || strings.Contains(recorder.Body.String(), "model_budgets") || strings.Contains(recorder.Body.String(), "monthly_budget_cny") {
		t.Fatalf("public catalog leaked internal quota fields: %s", recorder.Body.String())
	}
	if len(payload.Channels) != 1 || payload.Channels[0].ID != commercialPaymentChannelTest {
		t.Fatalf("unexpected channels: %#v", payload.Channels)
	}
}

func TestCommercialRealPaymentChannelRequiresRelayEnforcement(t *testing.T) {
	provider := &queryCommercialPaymentProvider{}
	withoutSync := NewCommercialPaymentHandler(newCommercialPaymentTestStore(), CommercialPaymentHandlerOptions{
		Providers: []CommercialPaymentProvider{provider}, SaleChannels: map[string]bool{commercialPaymentChannelAlipayPage: true},
	})
	if channels := withoutSync.channelsFor(38); len(channels) != 0 {
		t.Fatalf("real payment channel was exposed without relay enforcement: %#v", channels)
	}
	withSync := NewCommercialPaymentHandler(newCommercialPaymentTestStore(), CommercialPaymentHandlerOptions{
		Providers:    []CommercialPaymentProvider{provider},
		SaleChannels: map[string]bool{commercialPaymentChannelAlipayPage: true},
		Syncer:       &CommercialRelaySyncer{enforceUIDs: map[int64]bool{38: true}},
	})
	if channels := withSync.channelsFor(38); len(channels) != 1 || channels[0].ID != commercialPaymentChannelAlipayPage {
		t.Fatalf("eligible real payment channel was not exposed: %#v", channels)
	}
	if channels := withSync.channelsFor(39); len(channels) != 0 {
		t.Fatalf("real payment channel leaked outside the relay enforce allowlist: %#v", channels)
	}
}

func TestCommercialTrialRequiresDedicatedFreeHiddenPlan(t *testing.T) {
	valid := &types.CommercialPlan{
		Slug: "trial", PriceFen: 0, SaleState: "hidden", DurationDays: 7, State: 0,
		ModelBudgets: map[string]float64{"MiniMax-M3": 5},
	}
	if !commercialTrialPlanAvailable(valid) {
		t.Fatal("expected dedicated trial plan to be available")
	}
	for name, mutate := range map[string]func(*types.CommercialPlan){
		"paid":     func(plan *types.CommercialPlan) { plan.PriceFen = 1 },
		"public":   func(plan *types.CommercialPlan) { plan.SaleState = "public" },
		"disabled": func(plan *types.CommercialPlan) { plan.State = 1 },
		"empty":    func(plan *types.CommercialPlan) { plan.ModelBudgets = map[string]float64{} },
		"monthly": func(plan *types.CommercialPlan) {
			plan.ModelBudgets = map[string]float64{}
			plan.MonthlyBudget = 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *valid
			candidate.ModelBudgets = map[string]float64{"MiniMax-M3": 5}
			mutate(&candidate)
			if commercialTrialPlanAvailable(&candidate) {
				t.Fatalf("unsafe trial plan %s was accepted: %#v", name, candidate)
			}
		})
	}
}

func TestCommercialTestPaymentOrderAndFulfillment(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{{
		ID: 7, Slug: "gray", Name: "Gray", PriceFen: 2990, Currency: "CNY", SaleState: "test", DurationDays: 30, State: 0,
		ModelBudgets: map[string]float64{"MiniMax-M3": 500},
	}}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs:     map[int64]bool{38: true},
		TestPayments: map[int64]bool{38: true},
		Providers:    []CommercialPaymentProvider{NewTestCommercialPaymentProvider()},
	})
	createRecorder := httptest.NewRecorder()
	handler.HandleOrders(createRecorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", `{"plan_id":7,"channel":"test","client_request_id":"order_request_123456"}`, 38))
	if createRecorder.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created struct {
		Order *types.CommercialOrder `json:"order"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Order == nil || created.Order.Status != "pending" || created.Order.AmountFen != 2990 {
		t.Fatalf("unexpected order: %#v", created.Order)
	}
	if len(created.Order.PlanModelBudgets) != 0 || created.Order.PlanMonthlyBudget != 0 {
		t.Fatalf("user order leaked internal plan budgets: %#v", created.Order)
	}
	if strings.Contains(createRecorder.Body.String(), "plan_model_budgets") || strings.Contains(createRecorder.Body.String(), "plan_monthly_budget_cny") {
		t.Fatalf("user order response leaked internal plan fields: %s", createRecorder.Body.String())
	}
	confirmRecorder := httptest.NewRecorder()
	handler.HandleTestConfirm(confirmRecorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders/test-confirm", `{"order_no":"`+created.Order.OrderNo+`"}`, 38))
	if confirmRecorder.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirmRecorder.Code, confirmRecorder.Body.String())
	}
	if store.orders[created.Order.OrderNo].Status != "fulfilled" {
		t.Fatalf("order was not fulfilled: %#v", store.orders[created.Order.OrderNo])
	}
}

func TestCommercialPaymentIntentCanOnlyBeClaimedOnce(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-ONCE"] = &types.CommercialOrder{OrderNo: "CC-ONCE", Status: "created"}
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	_, first, err := store.BeginCommercialOrderPayment("CC-ONCE", expiresAt)
	if err != nil || !first {
		t.Fatalf("first claim failed: claimed=%v err=%v", first, err)
	}
	order, second, err := store.BeginCommercialOrderPayment("CC-ONCE", expiresAt)
	if err != nil || second || order.Status != "pending" {
		t.Fatalf("duplicate claim was not suppressed: order=%#v claimed=%v err=%v", order, second, err)
	}
}

func TestCommercialPendingOrderUsesProviderQueryFallback(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-QUERY"] = &types.CommercialOrder{
		OrderNo: "CC-QUERY", UID: 38, PlanName: "查询兜底套餐", AmountFen: 2990, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending",
	}
	provider := &queryCommercialPaymentProvider{
		paid: true,
		confirmation: &types.CommercialPaymentConfirmation{
			Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-QUERY-1", ProviderTradeNo: "ALI-QUERY-1",
			AmountFen: 2990, Currency: "CNY", PaidAt: time.Now().UTC(),
		},
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs:  map[int64]bool{38: true},
		Providers: []CommercialPaymentProvider{provider},
	})
	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/orders?order_no=CC-QUERY", "", 38))
	if recorder.Code != http.StatusOK {
		t.Fatalf("query fallback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.calls != 1 || store.orders["CC-QUERY"].Status != "fulfilled" {
		t.Fatalf("query fallback did not fulfill order: calls=%d order=%#v", provider.calls, store.orders["CC-QUERY"])
	}
}

func TestCommercialPendingOrderQueriesAreThrottled(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-PENDING"] = &types.CommercialOrder{
		OrderNo: "CC-PENDING", UID: 38, AmountFen: 2990, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending",
	}
	provider := &queryCommercialPaymentProvider{}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs:  map[int64]bool{38: true},
		Providers: []CommercialPaymentProvider{provider},
	})
	for range 2 {
		recorder := httptest.NewRecorder()
		handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/orders?order_no=CC-PENDING", "", 38))
		if recorder.Code != http.StatusOK {
			t.Fatalf("pending query status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if provider.calls != 1 {
		t.Fatalf("expected one provider query inside throttle window, got %d", provider.calls)
	}
}

func TestCommercialPendingOrderCanBeCancelled(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-CANCEL"] = &types.CommercialOrder{
		OrderNo: "CC-CANCEL", UID: 38, AmountFen: 39900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending", CheckoutURL: "https://openapi.alipay.test/pay",
	}
	provider := &queryCommercialPaymentProvider{}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	recorder := httptest.NewRecorder()
	handler.HandleCancel(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders/cancel", `{"order_no":"CC-CANCEL"}`, 38))
	if recorder.Code != http.StatusOK || store.orders["CC-CANCEL"].Status != "closed" || provider.closeCalls != 1 {
		t.Fatalf("cancel status=%d body=%s order=%#v close_calls=%d", recorder.Code, recorder.Body.String(), store.orders["CC-CANCEL"], provider.closeCalls)
	}
	second := httptest.NewRecorder()
	handler.HandleCancel(second, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders/cancel", `{"order_no":"CC-CANCEL"}`, 38))
	if second.Code != http.StatusOK || provider.closeCalls != 1 {
		t.Fatalf("repeat cancel was not idempotent: status=%d body=%s close_calls=%d", second.Code, second.Body.String(), provider.closeCalls)
	}
}

func TestCommercialCancellationDoesNotOverrideCompletedPayment(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-CANCEL-PAID"] = &types.CommercialOrder{
		OrderNo: "CC-CANCEL-PAID", UID: 38, AmountFen: 39900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending",
	}
	provider := &queryCommercialPaymentProvider{
		paid: true,
		confirmation: &types.CommercialPaymentConfirmation{
			Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-CANCEL-PAID", ProviderTradeNo: "ALI-CANCEL-PAID",
			AmountFen: 39900, Currency: "CNY", PaidAt: time.Now().UTC(),
		},
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	recorder := httptest.NewRecorder()
	handler.HandleCancel(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders/cancel", `{"order_no":"CC-CANCEL-PAID"}`, 38))
	if recorder.Code != http.StatusConflict || store.orders["CC-CANCEL-PAID"].Status != "fulfilled" || provider.closeCalls != 0 {
		t.Fatalf("paid order was not protected: status=%d body=%s order=%#v close_calls=%d", recorder.Code, recorder.Body.String(), store.orders["CC-CANCEL-PAID"], provider.closeCalls)
	}
}

func TestCommercialCancellationKeepsOrderPendingWhenProviderCloseFails(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-CANCEL-FAIL"] = &types.CommercialOrder{
		OrderNo: "CC-CANCEL-FAIL", UID: 38, AmountFen: 39900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending",
	}
	provider := &queryCommercialPaymentProvider{closeErr: context.DeadlineExceeded}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	recorder := httptest.NewRecorder()
	handler.HandleCancel(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders/cancel", `{"order_no":"CC-CANCEL-FAIL"}`, 38))
	if recorder.Code != http.StatusBadGateway || store.orders["CC-CANCEL-FAIL"].Status != "pending" || provider.closeCalls != 1 {
		t.Fatalf("provider close failure was unsafe: status=%d body=%s order=%#v", recorder.Code, recorder.Body.String(), store.orders["CC-CANCEL-FAIL"])
	}
}

func TestCommercialCancellationIsOwnerScoped(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-OTHER-OWNER"] = &types.CommercialOrder{OrderNo: "CC-OTHER-OWNER", UID: 38, Status: "created"}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{})
	recorder := httptest.NewRecorder()
	handler.HandleCancel(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders/cancel", `{"order_no":"CC-OTHER-OWNER"}`, 39))
	if recorder.Code != http.StatusNotFound || store.orders["CC-OTHER-OWNER"].Status != "created" {
		t.Fatalf("cross-owner cancellation was not blocked: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCommercialClosedOrderUsesProviderQueryFallback(t *testing.T) {
	store := newCommercialPaymentTestStore()
	closedAt := time.Now().UTC().Add(-time.Hour)
	store.orders["CC-CLOSED-PAID"] = &types.CommercialOrder{
		OrderNo: "CC-CLOSED-PAID", UID: 38, AmountFen: 2990, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "closed", ClosedAt: &closedAt,
	}
	provider := &queryCommercialPaymentProvider{
		paid: true,
		confirmation: &types.CommercialPaymentConfirmation{
			Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-CLOSED-1", ProviderTradeNo: "ALI-CLOSED-1",
			AmountFen: 2990, Currency: "CNY", PaidAt: time.Now().UTC(),
		},
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs: map[int64]bool{38: true}, Providers: []CommercialPaymentProvider{provider}, SaleChannels: map[string]bool{},
	})
	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/orders?order_no=CC-CLOSED-PAID", "", 38))
	if recorder.Code != http.StatusOK || store.orders["CC-CLOSED-PAID"].Status != "fulfilled" {
		t.Fatalf("closed order was not recovered: status=%d body=%s order=%#v", recorder.Code, recorder.Body.String(), store.orders["CC-CLOSED-PAID"])
	}
}

func TestCommercialPaymentReconciliationFulfillsRecentPendingOrder(t *testing.T) {
	store := newCommercialPaymentTestStore()
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	store.orders["CC-RECONCILE"] = &types.CommercialOrder{
		OrderNo: "CC-RECONCILE", UID: 38, AmountFen: 39900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending", ExpiresAt: &expiresAt,
	}
	provider := &queryCommercialPaymentProvider{
		paid: true,
		confirmation: &types.CommercialPaymentConfirmation{
			Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-RECONCILE", ProviderTradeNo: "ALI-RECONCILE",
			AmountFen: 39900, Currency: "CNY", PaidAt: time.Now().UTC(),
		},
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		Providers: []CommercialPaymentProvider{provider},
	})
	if got := handler.ReconcileCommercialOrders(context.Background()); got != 1 {
		t.Fatalf("reconciled count=%d, want 1", got)
	}
	if store.orders["CC-RECONCILE"].Status != "fulfilled" || provider.calls != 1 {
		t.Fatalf("order=%#v provider_calls=%d", store.orders["CC-RECONCILE"], provider.calls)
	}
}

func TestCommercialPaymentReconciliationSkipsStaleClosedOrder(t *testing.T) {
	store := newCommercialPaymentTestStore()
	closedAt := time.Now().UTC().Add(-8 * 24 * time.Hour)
	store.orders["CC-RECONCILE-STALE"] = &types.CommercialOrder{
		OrderNo: "CC-RECONCILE-STALE", UID: 38, AmountFen: 39900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "closed", ClosedAt: &closedAt,
	}
	provider := &queryCommercialPaymentProvider{
		paid: true,
		confirmation: &types.CommercialPaymentConfirmation{
			Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-RECONCILE-STALE", ProviderTradeNo: "ALI-RECONCILE-STALE",
			AmountFen: 39900, Currency: "CNY", PaidAt: time.Now().UTC(),
		},
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		Providers: []CommercialPaymentProvider{provider},
	})
	if got := handler.ReconcileCommercialOrders(context.Background()); got != 0 {
		t.Fatalf("reconciled count=%d, want 0", got)
	}
	if store.orders["CC-RECONCILE-STALE"].Status != "closed" || provider.calls != 0 {
		t.Fatalf("stale order=%#v provider_calls=%d", store.orders["CC-RECONCILE-STALE"], provider.calls)
	}
}

func TestCommercialPaymentReconciliationSkipsStalePendingOrder(t *testing.T) {
	store := newCommercialPaymentTestStore()
	createdAt := time.Now().UTC().Add(-8 * 24 * time.Hour)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	store.orders["CC-RECONCILE-STALE-PENDING"] = &types.CommercialOrder{
		OrderNo: "CC-RECONCILE-STALE-PENDING", UID: 38, AmountFen: 39900, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending", CreatedAt: createdAt, ExpiresAt: &expiresAt,
	}
	provider := &queryCommercialPaymentProvider{paid: true, confirmation: &types.CommercialPaymentConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-RECONCILE-STALE-PENDING", ProviderTradeNo: "ALI-RECONCILE-STALE-PENDING",
		AmountFen: 39900, Currency: "CNY", PaidAt: time.Now().UTC(),
	}}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	if got := handler.ReconcileCommercialOrders(context.Background()); got != 0 {
		t.Fatalf("reconciled count=%d, want 0", got)
	}
	if store.orders["CC-RECONCILE-STALE-PENDING"].Status != "pending" || provider.calls != 0 {
		t.Fatalf("stale pending order=%#v provider_calls=%d", store.orders["CC-RECONCILE-STALE-PENDING"], provider.calls)
	}
}

func TestCommercialHistoricalOrderQuerySurvivesGrayDisable(t *testing.T) {
	store := newCommercialPaymentTestStore()
	closedAt := time.Now().UTC().Add(-time.Hour)
	store.orders["CC-CLOSED-DISABLED"] = &types.CommercialOrder{
		OrderNo: "CC-CLOSED-DISABLED", UID: 38, AmountFen: 2990, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "closed", ClosedAt: &closedAt,
	}
	provider := &queryCommercialPaymentProvider{
		paid: true,
		confirmation: &types.CommercialPaymentConfirmation{
			Channel: commercialPaymentChannelAlipayPage, EventID: "ALI-DISABLED-1", ProviderTradeNo: "ALI-DISABLED-1",
			AmountFen: 2990, Currency: "CNY", PaidAt: time.Now().UTC(),
		},
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		Providers: []CommercialPaymentProvider{provider}, SaleChannels: map[string]bool{},
	})
	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/orders?order_no=CC-CLOSED-DISABLED", "", 38))
	if recorder.Code != http.StatusOK || store.orders["CC-CLOSED-DISABLED"].Status != "fulfilled" {
		t.Fatalf("disabled gray user could not recover historical order: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	postRecorder := httptest.NewRecorder()
	handler.HandleOrders(postRecorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", `{}`, 38))
	if postRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled gray user unexpectedly created an order: status=%d body=%s", postRecorder.Code, postRecorder.Body.String())
	}
}

func TestCommercialPendingOrderRecoversMissingCheckoutIntent(t *testing.T) {
	store := newCommercialPaymentTestStore()
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	store.orders["CC-MISSING-CHECKOUT"] = &types.CommercialOrder{
		OrderNo: "CC-MISSING-CHECKOUT", UID: 38, AmountFen: 2990, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "pending", ExpiresAt: &expiresAt,
		UpdatedAt: time.Now().UTC().Add(-time.Minute),
	}
	provider := &queryCommercialPaymentProvider{intent: &CommercialPaymentIntent{
		CheckoutURL: "https://openapi.alipay.test/pay", ExpiresAt: time.Now().UTC().Add(20 * time.Minute),
	}}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs: map[int64]bool{38: true}, Providers: []CommercialPaymentProvider{provider},
	})
	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/orders?order_no=CC-MISSING-CHECKOUT", "", 38))
	if recorder.Code != http.StatusOK || provider.createCalls != 1 || store.orders["CC-MISSING-CHECKOUT"].CheckoutURL == "" {
		t.Fatalf("missing checkout intent was not recovered: status=%d body=%s calls=%d order=%#v", recorder.Code, recorder.Body.String(), provider.createCalls, store.orders["CC-MISSING-CHECKOUT"])
	}
}

func TestCommercialCreatedOrderRecoversCheckoutIntent(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-CREATED-CHECKOUT"] = &types.CommercialOrder{
		OrderNo: "CC-CREATED-CHECKOUT", UID: 38, AmountFen: 2990, Currency: "CNY",
		Channel: commercialPaymentChannelAlipayPage, Status: "created",
	}
	provider := &queryCommercialPaymentProvider{intent: &CommercialPaymentIntent{
		CheckoutURL: "https://openapi.alipay.test/pay", ExpiresAt: time.Now().UTC().Add(20 * time.Minute),
	}}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs: map[int64]bool{38: true}, Providers: []CommercialPaymentProvider{provider},
	})
	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/orders?order_no=CC-CREATED-CHECKOUT", "", 38))
	if recorder.Code != http.StatusOK || provider.createCalls != 1 || store.orders["CC-CREATED-CHECKOUT"].CheckoutURL == "" {
		t.Fatalf("created checkout intent was not recovered: status=%d body=%s calls=%d order=%#v", recorder.Code, recorder.Body.String(), provider.createCalls, store.orders["CC-CREATED-CHECKOUT"])
	}
}

func TestCommercialRelayValidationRejectsMissingModelMapping(t *testing.T) {
	relayUser := &commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
		Provider: "openai", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra"},
	}}}}
	if err := validateCommercialRelayModels(map[string]float64{"gpt-5.6-terra": 10}, relayUser); err != nil {
		t.Fatalf("configured model was rejected: %v", err)
	}
	if err := validateCommercialRelayModels(map[string]float64{"MiniMax-M3": 10}, relayUser); err == nil {
		t.Fatal("missing relay model mapping was accepted")
	}
}

func TestCommercialRelayRequiredPaymentModelRejectsMappingDrift(t *testing.T) {
	summary := &types.CommercialSummary{
		UID:           38,
		TotalsByModel: map[string]float64{"gpt-5.6-terra": 100, "retired-model": 50},
		Grants: []*types.CommercialQuotaGrant{
			{GrantType: "order", Model: "gpt-5.6-terra", AmountCNY: 100},
			{GrantType: "manual", Model: "retired-model", AmountCNY: 50},
		},
		Entitlements: []*types.CommercialEntitlement{{Source: "legacy", State: "active"}},
	}
	relayUser := &commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{}}}
	if err := validateCommercialRelayRequiredModels(summary, relayUser, nil); err == nil || !strings.Contains(err.Error(), "gpt-5.6-terra") {
		t.Fatalf("paid model mapping drift was accepted: %v", err)
	}
	relayUser.Limits.ModelLimits = []commercialRelayModelLimit{{
		Provider: "openai", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra"},
	}}
	managed := []*types.CommercialManagedRelayBudget{{
		UID: 38, Model: "retired-model", Provider: "retired", AllowedModels: []string{"retired-model"}, MaxLimit: 50,
	}}
	if err := validateCommercialRelayRequiredModels(summary, relayUser, managed); err != nil {
		t.Fatalf("historical manual model should not block paid model sync: %v", err)
	}
}

func TestCommercialRelayManagedModelRejectsMappingDrift(t *testing.T) {
	summary := &types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{"gpt-5.6-terra": 100}}
	managed := []*types.CommercialManagedRelayBudget{{
		UID: 38, Model: "gpt-5.6-terra", Provider: "openai", AllowedModels: []string{"gpt-5.6-terra"}, MaxLimit: 100,
	}}
	if err := validateCommercialRelayRequiredModels(summary, &commercialRelayUsageUser{Configured: true}, managed); err == nil {
		t.Fatal("managed model mapping drift was accepted")
	}
}

func TestCommercialRelayUpdateVerificationRejectsSilentNoop(t *testing.T) {
	updates := []commercialRelayProviderBudgetUpdate{{
		Provider: "openai", AllowedModels: []string{"gpt-5.6-terra"}, MaxLimit: 100, ResetDuration: "1M",
	}}
	relayUser := &commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
		Provider: "openai", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra"},
		Budget: commercialRelayBudget{MaxLimit: 50},
	}}}}
	if err := verifyCommercialRelayUpdates(updates, relayUser); err == nil {
		t.Fatal("silent relay update no-op was accepted")
	}
	relayUser.Limits.ModelLimits[0].Budget.MaxLimit = 100
	if err := verifyCommercialRelayUpdates(updates, relayUser); err != nil {
		t.Fatalf("verified relay update was rejected: %v", err)
	}
}

func TestCommercialRelayManagedPlanSkipsUnmappedHistoricalModels(t *testing.T) {
	summary := &types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{
		"gpt-5.6-terra": 100,
		"retired-model": 50,
	}}
	relayUser := &commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{{
		Provider: "openai", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra"},
		Budget: commercialRelayBudget{MaxLimit: 10, ResetDuration: "1M"},
	}}}}
	staleManaged := []*types.CommercialManagedRelayBudget{{
		UID: 38, Model: "retired-model", Provider: "retired-provider", AllowedModels: []string{"retired-model"}, MaxLimit: 50,
	}}
	updates, managed := commercialRelayManagedPlan(38, summary, relayUser, staleManaged)
	if len(updates) != 1 || updates[0].MaxLimit != 100 || len(managed) != 1 || managed[0].Model != "gpt-5.6-terra" {
		t.Fatalf("mapped model was blocked by historical data: updates=%#v managed=%#v", updates, managed)
	}
}

func TestCommercialRelayManagedPlanOnlyRemovesOwnedBudgets(t *testing.T) {
	relayUser := &commercialRelayUsageUser{Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
		{Provider: "deepseek", Model: "deepseek-v4-flash", AllowedModels: []string{"deepseek-v4-flash"}, Budget: commercialRelayBudget{MaxLimit: 100}},
		{Provider: "minimax", Model: "MiniMax-M3", AllowedModels: []string{"MiniMax-M3"}, Budget: commercialRelayBudget{MaxLimit: 500}},
	}}}
	managed := []*types.CommercialManagedRelayBudget{{
		UID: 38, Model: "deepseek-v4-flash", Provider: "deepseek", AllowedModels: []string{"deepseek-v4-flash"}, MaxLimit: 100,
	}}
	updates, next := commercialRelayManagedPlan(38, &types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{}}, relayUser, managed)
	if len(updates) != 0 || len(next) != 0 {
		t.Fatalf("scoped removal should drop the owned provider config: updates=%#v next=%#v", updates, next)
	}
	scopes := commercialRelayModelScopes(&types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{}}, relayUser, managed)
	if len(scopes) != 1 || len(scopes[0].AllowedModels) != 0 || scopes[0].ManagedModels[0] != "deepseek-v4-flash" {
		t.Fatalf("unexpected removal scope: %#v", scopes)
	}
}

func TestCommercialRelayScopeExcludesUnpurchasedSharedAlias(t *testing.T) {
	models := []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"}
	available := []commercialRelayModelLimit{}
	for _, model := range models {
		available = append(available, commercialRelayModelLimit{
			Provider: "gpt-upstream", Model: model, AllowedModels: models, SharedBudget: true,
		})
	}
	relayUser := &commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{
		ModelLimits:          available,
		AvailableModelLimits: available,
	}}
	summary := &types.CommercialSummary{
		UID: 38,
		TotalsByModel: map[string]float64{
			"gpt-5.6-terra": 100,
			"gpt-5.6-sol":   100,
		},
		Grants: []*types.CommercialQuotaGrant{
			{GrantType: "order", Model: "gpt-5.6-terra", AmountCNY: 100},
			{GrantType: "order", Model: "gpt-5.6-sol", AmountCNY: 100},
		},
	}

	scopes := commercialRelayModelScopes(summary, relayUser, nil)
	if len(scopes) != 1 || commercialRelayModelSetKey(scopes[0].ManagedModels) != commercialRelayModelSetKey(models) {
		t.Fatalf("unexpected managed model family: %#v", scopes)
	}
	if got := commercialRelayModelSetKey(scopes[0].AllowedModels); got != commercialRelayModelSetKey(models[:2]) {
		t.Fatalf("unpurchased Luna leaked into the scope: %#v", scopes)
	}
	updates, next := commercialRelayManagedPlan(38, summary, relayUser, nil)
	if len(updates) != 1 || updates[0].MaxLimit != 200 || commercialRelayModelSetKey(updates[0].AllowedModels) != commercialRelayModelSetKey(models[:2]) {
		t.Fatalf("shared provider was not narrowed: %#v", updates)
	}
	if len(next) != 2 {
		t.Fatalf("expected one managed association per purchased model: %#v", next)
	}
	for _, item := range next {
		if commercialRelayModelSetKey(item.AllowedModels) != commercialRelayModelSetKey(models[:2]) {
			t.Fatalf("managed state retained Luna: %#v", next)
		}
	}
}

func TestCommercialRelayModelScopesMatchRelayAdminOverlapMerge(t *testing.T) {
	triple := []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"}
	pair := []string{"gpt-5.6-terra", "gpt-5.6-sol"}
	expected := []commercialRelayModelScope{
		{ManagedModels: []string{"deepseek-v4-flash"}, AllowedModels: []string{"deepseek-v4-flash"}},
		{ManagedModels: triple, AllowedModels: triple},
		{ManagedModels: pair, AllowedModels: pair},
		{ManagedModels: []string{"MiniMax-M2.7"}, AllowedModels: []string{"MiniMax-M2.7"}},
		{ManagedModels: []string{"MiniMax-M3"}, AllowedModels: []string{"MiniMax-M3"}},
	}
	// Relay Admin merges the overlapping pair into the existing triple scope.
	actual := []commercialRelayModelScope{
		{ManagedModels: []string{"deepseek-v4-flash"}, AllowedModels: []string{"deepseek-v4-flash"}},
		{ManagedModels: triple, AllowedModels: triple},
		{ManagedModels: []string{"MiniMax-M2.7"}, AllowedModels: []string{"MiniMax-M2.7"}},
		{ManagedModels: []string{"MiniMax-M3"}, AllowedModels: []string{"MiniMax-M3"}},
	}
	if !commercialRelayModelScopesMatch(actual, expected) {
		t.Fatalf("valid Relay Admin overlap merge was rejected: actual=%#v expected=%#v", actual, expected)
	}

	actual[1].AllowedModels = pair
	if commercialRelayModelScopesMatch(actual, expected) {
		t.Fatalf("scope narrowing was not detected: actual=%#v expected=%#v", actual, expected)
	}
}

func TestCommercialRelayUsesAvailableCatalogAfterScopedConfigWasRemoved(t *testing.T) {
	models := []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"}
	relayUser := &commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{
		AvailableModelLimits: []commercialRelayModelLimit{
			{Provider: "gpt-upstream", Model: "gpt-5.6-terra", AllowedModels: models},
			{Provider: "gpt-upstream", Model: "gpt-5.6-sol", AllowedModels: models},
			{Provider: "gpt-upstream", Model: "gpt-5.6-luna", AllowedModels: models},
		},
		ModelScopes: []commercialRelayModelScope{{ManagedModels: models, AllowedModels: []string{}}},
	}}
	if err := validateCommercialRelayModels(map[string]float64{"gpt-5.6-terra": 100}, relayUser); err != nil {
		t.Fatalf("repurchase was blocked after scoped removal: %v", err)
	}
	summary := &types.CommercialSummary{
		UID: 38, TotalsByModel: map[string]float64{"gpt-5.6-terra": 100},
		Grants: []*types.CommercialQuotaGrant{{GrantType: "order", Model: "gpt-5.6-terra", AmountCNY: 100}},
	}
	updates, _ := commercialRelayManagedPlan(38, summary, relayUser, nil)
	if len(updates) != 1 || commercialRelayModelSetKey(updates[0].AllowedModels) != commercialRelayModelSetKey([]string{"gpt-5.6-terra"}) {
		t.Fatalf("removed provider config could not be rebuilt: %#v", updates)
	}
}

func TestCommercialRelaySyncWritesScopeAndNarrowedBudgetTogether(t *testing.T) {
	models := []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"}
	available := make([]commercialRelayModelLimit, 0, len(models))
	for _, model := range models {
		available = append(available, commercialRelayModelLimit{
			Provider: "gpt-upstream", Model: model, AllowedModels: models, SharedBudget: true,
			Budget: commercialRelayBudget{MaxLimit: 5, ResetDuration: "1M"},
		})
	}
	store := &commercialRelaySyncTestStore{summary: &types.CommercialSummary{
		UID: 38,
		TotalsByModel: map[string]float64{
			"gpt-5.6-terra": 100,
			"gpt-5.6-sol":   100,
		},
		Grants: []*types.CommercialQuotaGrant{
			{GrantType: "order", Model: "gpt-5.6-terra", AmountCNY: 100},
			{GrantType: "order", Model: "gpt-5.6-sol", AmountCNY: 100},
		},
	}}
	var posted struct {
		Budgets               []commercialRelayProviderBudgetUpdate `json:"provider_config_budgets"`
		Scopes                []commercialRelayModelScope           `json:"model_scopes"`
		MonthlyBudget         float64                               `json:"monthly_budget"`
		MonthlyBudgetDuration string                                `json:"monthly_budget_duration"`
		UsageWindowStart      *string                               `json:"usage_window_start"`
	}
	applied := false
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			active := available
			scopes := []commercialRelayModelScope{}
			if applied {
				active = []commercialRelayModelLimit{
					{Provider: "gpt-upstream", Model: "gpt-5.6-terra", AllowedModels: models[:2], SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 200, ResetDuration: "1M"}},
					{Provider: "gpt-upstream", Model: "gpt-5.6-sol", AllowedModels: models[:2], SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 200, ResetDuration: "1M"}},
				}
				scopes = posted.Scopes
			}
			_ = json.NewEncoder(w).Encode(commercialRelayUsageUser{
				Configured: true,
				Key:        &commercialRelayKeySummary{State: "active"},
				UsageWindowStart: func() string {
					if applied && posted.UsageWindowStart != nil {
						return *posted.UsageWindowStart
					}
					return ""
				}(),
				Limits: commercialRelayLimits{
					MonthlyBudget: commercialRelayBudget{MaxLimit: posted.MonthlyBudget, ResetDuration: posted.MonthlyBudgetDuration},
					ModelLimits:   active, AvailableModelLimits: available, ModelScopes: scopes,
				},
			})
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			applied = true
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer relay.Close()

	syncer := NewCommercialRelaySyncer(
		store,
		&RelayAdminClient{baseURL: relay.URL, token: "test-token", client: relay.Client()},
		CommercialRelaySyncerOptions{EnforceUIDs: map[int64]bool{38: true}},
	)
	updates, err := syncer.SyncUID(context.Background(), 38)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || len(posted.Budgets) != 1 || len(posted.Scopes) != 1 {
		t.Fatalf("incomplete commercial relay write: updates=%#v posted=%#v", updates, posted)
	}
	if commercialRelayModelSetKey(posted.Budgets[0].AllowedModels) != commercialRelayModelSetKey(models[:2]) ||
		commercialRelayModelSetKey(posted.Scopes[0].AllowedModels) != commercialRelayModelSetKey(models[:2]) {
		t.Fatalf("Luna leaked into the synchronized policy: %#v", posted)
	}
	if len(store.replaced) != 2 {
		t.Fatalf("managed relay state was not saved: %#v", store.replaced)
	}
}

func TestCommercialRelayManagedPlanSumsSharedProviderBudget(t *testing.T) {
	shared := []string{"model-a", "model-b"}
	relayUser := &commercialRelayUsageUser{Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
		{Provider: "shared-provider", Model: "model-a", AllowedModels: shared, SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 5}},
		{Provider: "shared-provider", Model: "model-b", AllowedModels: shared, SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 5}},
	}}}
	summary := &types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{"model-a": 10, "model-b": 20}}
	updates, next := commercialRelayManagedPlan(38, summary, relayUser, nil)
	if len(updates) != 1 || updates[0].MaxLimit != 30 {
		t.Fatalf("shared budget should be summed once: %#v", updates)
	}
	if len(next) != 2 || next[0].MaxLimit != 30 || next[1].MaxLimit != 30 {
		t.Fatalf("shared ownership records should retain the summed limit: %#v", next)
	}
}

func TestCommercialRelaySharedPlanUsesFullPoolForEveryAllowedProvider(t *testing.T) {
	relayUser := &commercialRelayUsageUser{Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
		{Provider: "minimax-m27", Model: "MiniMax-M2.7", AllowedModels: []string{"MiniMax-M2.7"}, Budget: commercialRelayBudget{MaxLimit: 1000}},
		{Provider: "minimax-m3", Model: "MiniMax-M3", AllowedModels: []string{"MiniMax-M3"}, Budget: commercialRelayBudget{MaxLimit: 500}},
		{Provider: "deepseek", Model: "deepseek-v4-flash", AllowedModels: []string{"deepseek-v4-flash"}, Budget: commercialRelayBudget{MaxLimit: 100}},
		{Provider: "gpt", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra", "gpt-5.6-sol"}, SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 31500}},
		{Provider: "gpt", Model: "gpt-5.6-sol", AllowedModels: []string{"gpt-5.6-terra", "gpt-5.6-sol"}, SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 31500}},
	}}}
	summary := &types.CommercialSummary{TotalCNY: 33600, TotalsByModel: map[string]float64{
		"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-v4-flash": 100,
		"gpt-5.6-terra": 15750, "gpt-5.6-sol": 15750, "glm-5.1": 500,
	}}

	updates, managed := commercialRelaySharedManagedPlan(38, summary, relayUser, nil)

	if len(updates) != 4 {
		t.Fatalf("updates=%#v", updates)
	}
	for _, update := range updates {
		if update.MaxLimit != 33600 {
			t.Fatalf("provider %s received %v, want full shared pool", update.Provider, update.MaxLimit)
		}
	}
	if len(managed) != 5 {
		t.Fatalf("managed associations=%d, want 5: %#v", len(managed), managed)
	}
}

func TestCommercialRelayBaselineClassifiesFreeAndLegacyProfiles(t *testing.T) {
	freeUser := &commercialRelayUsageUser{Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
		{Provider: "m27-a", Model: "MiniMax-M2.7", Budget: commercialRelayBudget{MaxLimit: 1000}},
		{Provider: "m27-b", Model: "MiniMax-M2.7", Budget: commercialRelayBudget{MaxLimit: 1000}},
		{Provider: "m3", Model: "MiniMax-M3", Budget: commercialRelayBudget{MaxLimit: 500}},
		{Provider: "deepseek", Model: "deepseek-v4-flash", Budget: commercialRelayBudget{MaxLimit: 100}},
	}}}
	profile, budgets := commercialRelayBaseline(freeUser)
	if profile != commercialRelayBaselineProfileFree || len(budgets) != 3 || budgets["MiniMax-M2.7"] != 1000 {
		t.Fatalf("default profile was not recognized: profile=%s budgets=%#v", profile, budgets)
	}

	freeUser.Limits.ModelLimits = append(freeUser.Limits.ModelLimits, commercialRelayModelLimit{
		Provider: "gpt", Model: "gpt-5.6-terra", Budget: commercialRelayBudget{MaxLimit: 5000},
	})
	profile, budgets = commercialRelayBaseline(freeUser)
	if profile != commercialRelayBaselineProfileLegacy || budgets["gpt-5.6-terra"] != 5000 {
		t.Fatalf("custom profile was not preserved: profile=%s budgets=%#v", profile, budgets)
	}
}

func TestCommercialRelayBaselineCoexistsWithPaidAndManualQuota(t *testing.T) {
	paid := &types.CommercialSummary{
		Entitlements: []*types.CommercialEntitlement{{Source: "order", State: "active"}},
		Grants:       []*types.CommercialQuotaGrant{{GrantType: "order", Model: "gpt-5.6-terra", AmountCNY: 100}},
	}
	profile, budgets, err := commercialRelayBaselineForSummary(paid, nil)
	if err != nil || profile != commercialRelayBaselineProfileFree || len(budgets) != len(commercialRelayFreeBudgets) {
		t.Fatalf("paid account did not receive a free baseline: profile=%q budgets=%#v err=%v", profile, budgets, err)
	}

	paid.Grants = append(paid.Grants, &types.CommercialQuotaGrant{GrantType: "manual", Model: "MiniMax-M3", AmountCNY: 500})
	profile, budgets, err = commercialRelayBaselineForSummary(paid, nil)
	if err != nil || profile != commercialRelayBaselineProfileLegacy || len(budgets) != 0 {
		t.Fatalf("manual quota was not preserved as a legacy baseline: profile=%q budgets=%#v err=%v", profile, budgets, err)
	}

	paid.Entitlements = append(paid.Entitlements, &types.CommercialEntitlement{Source: "legacy", State: "active"})
	if !commercialRelayHasBaselineEntitlement(paid) {
		t.Fatal("active legacy baseline was not recognized")
	}
}

func TestCommercialRelayBaselineRestoresFreeAfterRefund(t *testing.T) {
	summary := &types.CommercialSummary{
		Ledger: []*types.CommercialLedgerEntry{{SourceType: "refund", EntryType: "revoke"}},
	}
	relayUser := &commercialRelayUsageUser{Limits: commercialRelayLimits{
		MonthlyBudget: commercialRelayBudget{MaxLimit: 10500, ResetDuration: "1M"},
	}}
	profile, budgets, err := commercialRelayBaselineForSummary(summary, relayUser)
	if err != nil || profile != commercialRelayBaselineProfileFree || len(budgets) != len(commercialRelayFreeBudgets) {
		t.Fatalf("refund did not restore free baseline: profile=%q budgets=%#v err=%v", profile, budgets, err)
	}
}

func TestCommercialRelayBaselinePreservesResetAndCreatesSharedPolicy(t *testing.T) {
	reset := "2026-08-01 08:30:00.123456789+00:00"
	baseStore := &commercialRelaySyncTestStore{summary: &types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{}}}
	store := &commercialRelayBaselineTestStore{commercialRelaySyncTestStore: baseStore}
	state := commercialRelayUsageUser{Configured: true, Key: &commercialRelayKeySummary{State: "active"}, Limits: commercialRelayLimits{
		ModelLimits: []commercialRelayModelLimit{
			{Provider: "m27", Model: "MiniMax-M2.7", AllowedModels: []string{"MiniMax-M2.7"}, Budget: commercialRelayBudget{MaxLimit: 1000, ResetDuration: "1M", LastReset: reset}},
			{Provider: "m3", Model: "MiniMax-M3", AllowedModels: []string{"MiniMax-M3"}, Budget: commercialRelayBudget{MaxLimit: 500, ResetDuration: "1M", LastReset: reset}},
			{Provider: "deepseek", Model: "deepseek-v4-flash", AllowedModels: []string{"deepseek-v4-flash"}, Budget: commercialRelayBudget{MaxLimit: 100, ResetDuration: "1M", LastReset: reset}},
		},
	}}
	state.Limits.AvailableModelLimits = append([]commercialRelayModelLimit(nil), state.Limits.ModelLimits...)
	var posted map[string]interface{}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(state)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			state.Limits.MonthlyBudget = commercialRelayBudget{MaxLimit: posted["monthly_budget"].(float64), ResetDuration: "1M"}
			state.UsageWindowStart = posted["usage_window_start"].(string)
			for index := range state.Limits.ModelLimits {
				state.Limits.ModelLimits[index].Budget.MaxLimit = 1600
			}
			var scopes []commercialRelayModelScope
			raw, _ := json.Marshal(posted["model_scopes"])
			_ = json.Unmarshal(raw, &scopes)
			state.Limits.ModelScopes = scopes
			_ = json.NewEncoder(w).Encode(state)
		}
	}))
	defer relay.Close()

	syncer := NewCommercialRelaySyncer(store, &RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}, CommercialRelaySyncerOptions{EnforceUIDs: map[int64]bool{38: true}})
	if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
		t.Fatal(err)
	}
	if store.created != 1 || store.profile != commercialRelayBaselineProfileFree || len(store.budgets) != 3 {
		t.Fatalf("baseline was not created exactly once: %#v", store)
	}
	if got := store.startsAt.Format(time.RFC3339Nano); got != "2026-08-01T08:30:00.123456789Z" {
		t.Fatalf("reset anchor changed: %s", got)
	}
	if posted["monthly_budget"] != float64(1600) || posted["usage_window_start"] != "2026-08-01T08:30:00Z" {
		t.Fatalf("shared policy mismatch: %#v", posted)
	}
	if len(state.Limits.ModelScopes) != 3 {
		t.Fatalf("free models were not scoped: %#v", state.Limits.ModelScopes)
	}
}

func TestCommercialRelayBootstrapPagesEveryConfiguredKey(t *testing.T) {
	const total = 106
	requests := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit != 10 {
			t.Fatalf("bootstrap requested an unsafe page size: %d", limit)
		}
		end := offset + limit
		if end > total {
			end = total
		}
		users := make([]commercialRelayUsageUser, 0, end-offset)
		for uid := offset + 1; uid <= end; uid++ {
			users = append(users, commercialRelayUsageUser{
				UID:             int64(uid),
				Configured:      true,
				GovernanceError: strings.Repeat("x", 30*1024),
			})
		}
		_ = json.NewEncoder(w).Encode(commercialRelayUsageResponse{Users: users, TotalCount: total})
	}))
	defer relay.Close()
	store := &commercialRelaySyncTestStore{summary: &types.CommercialSummary{TotalsByModel: map[string]float64{}}}
	syncer := NewCommercialRelaySyncer(store, &RelayAdminClient{baseURL: relay.URL, token: "test", client: relay.Client()}, CommercialRelaySyncerOptions{EnforceEnabled: true})

	syncer.bootstrapConfiguredRelayUsers(context.Background())

	if len(syncer.queue) != total || len(syncer.pendingUIDs) != total {
		t.Fatalf("bootstrap missed relay keys: queued=%d pending=%d", len(syncer.queue), len(syncer.pendingUIDs))
	}
	if requests != 11 {
		t.Fatalf("bootstrap request count=%d, want 11", requests)
	}
}

func TestCommercialRelaySharedPolicyUsesEarliestActiveEntitlement(t *testing.T) {
	older := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 8, 14, 7, 32, 8, 0, time.UTC)
	summary := &types.CommercialSummary{
		TotalCNY: 33600,
		Entitlements: []*types.CommercialEntitlement{
			{State: "active", StartsAt: older},
			{State: "active", StartsAt: latest},
			{State: "expired", StartsAt: latest.Add(time.Hour)},
		},
	}
	if got := commercialRelaySharedLimit(summary); got != 33600 {
		t.Fatalf("shared limit=%v", got)
	}
	if got := commercialRelayUsageWindowStart(summary); got != "2026-08-01T00:00:00Z" {
		t.Fatalf("usage window=%q", got)
	}
	user := &commercialRelayUsageUser{Configured: true, UsageWindowStart: "2026-08-01T00:00:00Z", Limits: commercialRelayLimits{
		MonthlyBudget: commercialRelayBudget{MaxLimit: 33600, ResetDuration: "1M"},
	}}
	if err := verifyCommercialRelaySharedPolicy(33600, commercialRelayUsageWindowStart(summary), user); err != nil {
		t.Fatal(err)
	}
	cleared := &commercialRelayUsageUser{Configured: true, Limits: commercialRelayLimits{
		MonthlyBudget: commercialRelayBudget{MaxLimit: commercialRelayBlockedLimit, ResetDuration: "1M"},
	}}
	if err := verifyCommercialRelaySharedPolicy(commercialRelayBlockedLimit, "", cleared); err != nil {
		t.Fatalf("cleared commercial window should verify: %v", err)
	}
	cleared.UsageWindowStart = "2026-08-14T07:32:08Z"
	if err := verifyCommercialRelaySharedPolicy(commercialRelayBlockedLimit, "", cleared); err == nil {
		t.Fatal("stale commercial window was accepted after entitlement removal")
	}
}

func TestCommercialRelayPaidPackageCycleOverridesFreeBaseline(t *testing.T) {
	freeStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	paidStart := time.Date(2026, 8, 14, 7, 32, 8, 0, time.UTC)
	summary := &types.CommercialSummary{Entitlements: []*types.CommercialEntitlement{
		{Source: "free", State: "active", StartsAt: freeStart},
		{Source: "order", State: "active", StartsAt: paidStart},
	}}
	if got := commercialRelayUsageWindowStart(summary); got != "2026-08-14T07:32:08Z" {
		t.Fatalf("paid package did not own the quota cycle: %q", got)
	}
	summary.Entitlements[1].State = "revoked"
	if got := commercialRelayUsageWindowStart(summary); got != "2026-08-01T00:00:00Z" {
		t.Fatalf("free baseline did not resume after refund: %q", got)
	}
	paidUser := &commercialRelayUsageUser{UsageWindowStart: "2026-08-14T07:32:08Z"}
	if got := commercialRelayUsageWindowStartForSync(summary, paidUser); got != "2026-08-14T07:32:08Z" {
		t.Fatalf("refund moved the quota window backwards: %q", got)
	}
	if got := commercialRelayUsageWindowStartForSync(&types.CommercialSummary{}, paidUser); got != "" {
		t.Fatalf("an account without an active entitlement retained a quota window: %q", got)
	}
}

func TestCommercialRelaySyncWritesAndVerifiesSharedPoolPolicy(t *testing.T) {
	start := time.Date(2026, 8, 14, 7, 32, 8, 0, time.UTC)
	store := &commercialRelaySyncTestStore{summary: &types.CommercialSummary{
		UID: 38, TotalCNY: 30,
		TotalsByModel: map[string]float64{"MiniMax-M3": 10, "gpt-5.6-terra": 20},
		Entitlements:  []*types.CommercialEntitlement{{State: "active", StartsAt: start}},
	}}
	state := commercialRelayUsageUser{Configured: true, Key: &commercialRelayKeySummary{State: "active"}, Limits: commercialRelayLimits{
		MonthlyBudget: commercialRelayBudget{MaxLimit: 100, ResetDuration: "1M"},
		ModelLimits: []commercialRelayModelLimit{
			{Provider: "minimax", Model: "MiniMax-M3", AllowedModels: []string{"MiniMax-M3"}, Budget: commercialRelayBudget{MaxLimit: 10, ResetDuration: "1M"}},
			{Provider: "gpt", Model: "gpt-5.6-terra", AllowedModels: []string{"gpt-5.6-terra"}, Budget: commercialRelayBudget{MaxLimit: 20, ResetDuration: "1M"}},
		},
	}}
	var posted map[string]interface{}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(state)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state.Limits.MonthlyBudget.MaxLimit = posted["monthly_budget"].(float64)
			state.Limits.MonthlyBudget.ResetDuration = posted["monthly_budget_duration"].(string)
			state.UsageWindowStart = posted["usage_window_start"].(string)
			rawUpdates, _ := json.Marshal(posted["provider_config_budgets"])
			var updates []commercialRelayProviderBudgetUpdate
			_ = json.Unmarshal(rawUpdates, &updates)
			for i := range state.Limits.ModelLimits {
				for _, update := range updates {
					if commercialManagedBudgetKey(update.Provider, update.AllowedModels) == commercialManagedBudgetKey(state.Limits.ModelLimits[i].Provider, state.Limits.ModelLimits[i].AllowedModels) {
						state.Limits.ModelLimits[i].Budget.MaxLimit = update.MaxLimit
						state.Limits.ModelLimits[i].Budget.ResetDuration = update.ResetDuration
					}
				}
			}
			_ = json.NewEncoder(w).Encode(state)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer relay.Close()
	syncer := NewCommercialRelaySyncer(store, &RelayAdminClient{baseURL: relay.URL, token: "test-token", client: relay.Client()}, CommercialRelaySyncerOptions{
		EnforceUIDs: map[int64]bool{38: true},
	})

	updates, err := syncer.SyncUID(context.Background(), 38)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || posted["monthly_budget"] != float64(30) || posted["monthly_budget_duration"] != "1M" || posted["usage_window_start"] != "2026-08-14T07:32:08Z" {
		t.Fatalf("unexpected shared policy payload: updates=%#v payload=%#v", updates, posted)
	}
	for _, limit := range state.Limits.ModelLimits {
		if limit.Budget.MaxLimit != 30 {
			t.Fatalf("provider %s did not receive the full shared pool: %#v", limit.Provider, limit.Budget)
		}
	}
	if len(store.replaced) != 2 {
		t.Fatalf("managed relay associations were not persisted: %#v", store.replaced)
	}
}

func TestCommercialRelaySyncClearsExpiredPackageWindow(t *testing.T) {
	store := &commercialRelaySyncTestStore{summary: &types.CommercialSummary{
		UID: 38, TotalsByModel: map[string]float64{},
	}}
	state := commercialRelayUsageUser{
		Configured:       true,
		UsageWindowStart: "2026-08-14T07:32:08Z",
		Key:              &commercialRelayKeySummary{State: "active"},
		Limits: commercialRelayLimits{MonthlyBudget: commercialRelayBudget{
			MaxLimit: 30, ResetDuration: "1M",
		}},
	}
	var posted map[string]interface{}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(state)
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state.Limits.MonthlyBudget.MaxLimit = posted["monthly_budget"].(float64)
			state.Limits.MonthlyBudget.ResetDuration = posted["monthly_budget_duration"].(string)
			state.UsageWindowStart = ""
			_ = json.NewEncoder(w).Encode(state)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer relay.Close()
	syncer := NewCommercialRelaySyncer(store, &RelayAdminClient{baseURL: relay.URL, token: "test-token", client: relay.Client()}, CommercialRelaySyncerOptions{
		EnforceUIDs: map[int64]bool{38: true},
	})

	if _, err := syncer.SyncUID(context.Background(), 38); err != nil {
		t.Fatal(err)
	}
	window, found := posted["usage_window_start"]
	if !found || window != nil {
		t.Fatalf("expired package must explicitly clear the usage window: %#v", posted)
	}
	if posted["monthly_budget"] != commercialRelayBlockedLimit {
		t.Fatalf("expired package must block the shared pool: %#v", posted)
	}
}

func TestCommercialRelayDryRunRecognizesSharedProviderBudget(t *testing.T) {
	shared := []string{"model-a", "model-b"}
	relayUser := &commercialRelayUsageUser{Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
		{Provider: "shared-provider", Model: "model-a", AllowedModels: shared, SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 30}},
		{Provider: "shared-provider", Model: "model-b", AllowedModels: shared, SharedBudget: true, Budget: commercialRelayBudget{MaxLimit: 30}},
	}}}
	summary := &types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{"model-a": 10, "model-b": 20}}
	dryRun := compareCommercialRelayBudgets(38, summary, relayUser)
	if dryRun.CanApply || len(dryRun.ProposedUpdates) != 0 {
		t.Fatalf("shared budget should already match: %#v", dryRun.ProposedUpdates)
	}
	if len(dryRun.Comparisons) != 2 || dryRun.Comparisons[0].Status != "match" || dryRun.Comparisons[1].Status != "match" {
		t.Fatalf("unexpected comparisons: %#v", dryRun.Comparisons)
	}
}

func TestCommercialManagedBudgetKeyCanonicalizesDeepSeekVisionAlias(t *testing.T) {
	public := commercialManagedBudgetKey("deepseek-anthropic", []string{"deepseek-v4-flash"})
	withVision := commercialManagedBudgetKey("deepseek-anthropic", []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp"})
	if public != withVision {
		t.Fatalf("deepseek public/vision provider configs must share budget identity: public=%q withVision=%q", public, withVision)
	}
}

func TestTruncateUTF8DoesNotSplitRune(t *testing.T) {
	value := truncateUTF8Bytes("CatsCo 教师套餐", 10)
	if !strings.HasPrefix("CatsCo 教师套餐", value) || len(value) > 10 {
		t.Fatalf("invalid truncation %q", value)
	}
}

func TestAlipayPaymentStaysDisabledWithoutSecrets(t *testing.T) {
	for _, name := range []string{
		"CATS_ALIPAY_APP_ID",
		"CATS_ALIPAY_SELLER_ID",
		"CATS_ALIPAY_PRIVATE_KEY_FILE",
		"CATS_ALIPAY_PUBLIC_KEY_FILE",
		"CATS_ALIPAY_NOTIFY_URL",
		"CATS_ALIPAY_RETURN_URL",
	} {
		t.Setenv(name, "")
	}
	provider, missing, err := NewAlipayPagePaymentProviderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if provider != nil || len(missing) != 6 {
		t.Fatalf("provider=%#v missing=%#v", provider, missing)
	}
}

func TestAlipayPaymentRejectsUnsafeNotifyURL(t *testing.T) {
	t.Setenv("CATS_ALIPAY_APP_ID", "2026000000000001")
	t.Setenv("CATS_ALIPAY_SELLER_ID", "2088000000000001")
	t.Setenv("CATS_ALIPAY_PRIVATE_KEY_FILE", "unused-private-key.pem")
	t.Setenv("CATS_ALIPAY_PUBLIC_KEY_FILE", "unused-public-key.pem")
	t.Setenv("CATS_ALIPAY_NOTIFY_URL", "http://app.catsco.cc/api/payments/alipay/notify?source=test")
	t.Setenv("CATS_ALIPAY_RETURN_URL", "https://app.catsco.cc/")

	provider, missing, err := NewAlipayPagePaymentProviderFromEnv()
	if err == nil || !strings.Contains(err.Error(), "HTTPS URL without query or fragment") {
		t.Fatalf("expected unsafe notify URL to be rejected, got provider=%#v missing=%#v err=%v", provider, missing, err)
	}
}

func TestAlipayPaymentRejectsUnsafeReturnURL(t *testing.T) {
	t.Setenv("CATS_ALIPAY_APP_ID", "2026000000000001")
	t.Setenv("CATS_ALIPAY_SELLER_ID", "2088000000000001")
	t.Setenv("CATS_ALIPAY_PRIVATE_KEY_FILE", "unused-private-key.pem")
	t.Setenv("CATS_ALIPAY_PUBLIC_KEY_FILE", "unused-public-key.pem")
	t.Setenv("CATS_ALIPAY_NOTIFY_URL", "https://app.catsco.cc/api/payments/alipay/notify")
	t.Setenv("CATS_ALIPAY_RETURN_URL", "javascript:alert(1)")

	provider, missing, err := NewAlipayPagePaymentProviderFromEnv()
	if err == nil || !strings.Contains(err.Error(), "HTTPS URL without fragment") {
		t.Fatalf("expected unsafe return URL to be rejected, got provider=%#v missing=%#v err=%v", provider, missing, err)
	}
}

func TestAlipayPaymentLoadsFileBackedKeys(t *testing.T) {
	appPrivatePEM, _ := generateAlipayTestKeyPair(t)
	_, alipayPublicPEM := generateAlipayTestKeyPair(t)
	dir := t.TempDir()
	privateKeyPath := filepath.Join(dir, "app_private_key.pem")
	publicKeyPath := filepath.Join(dir, "alipay_public_key.pem")
	for path, content := range map[string][]byte{
		privateKeyPath: appPrivatePEM,
		publicKeyPath:  alipayPublicPEM,
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CATS_ALIPAY_APP_ID", "2026000000000001")
	t.Setenv("CATS_ALIPAY_SELLER_ID", "2088000000000001")
	t.Setenv("CATS_ALIPAY_PRIVATE_KEY_FILE", privateKeyPath)
	t.Setenv("CATS_ALIPAY_PUBLIC_KEY_FILE", publicKeyPath)
	t.Setenv("CATS_ALIPAY_NOTIFY_URL", "https://app.catsco.cc/api/payments/alipay/notify")
	t.Setenv("CATS_ALIPAY_RETURN_URL", "https://app.catsco.cc/")
	t.Setenv("CATS_ALIPAY_PRODUCTION", "0")
	provider, missing, err := NewAlipayPagePaymentProviderFromEnv()
	if err != nil || provider == nil || len(missing) != 0 {
		t.Fatalf("provider=%#v missing=%#v err=%v", provider, missing, err)
	}
	if provider.(*alipayPagePaymentProvider).production {
		t.Fatal("Alipay should default to sandbox unless production is explicitly enabled")
	}
}

func TestAlipayPaymentCreatesExactPageIntent(t *testing.T) {
	orderNo := "CC202607140000000000000000000001"
	paymentURL, err := url.Parse("https://openapi-sandbox.dl.alipaydev.com/gateway.do?method=alipay.trade.page.pay")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAlipayPaymentClient{pageURL: paymentURL}
	expiresAt := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Second)
	provider := &alipayPagePaymentProvider{
		appID: "2026000000000001", sellerID: "2088000000000001",
		notifyURL: "https://app.catsco.cc/api/payments/alipay/notify",
		returnURL: "https://app.catsco.cc/", client: fake,
	}
	intent, err := provider.CreatePayment(context.Background(), &types.CommercialOrder{
		OrderNo: orderNo, PlanName: "教师套餐", AmountFen: 2990, Currency: "CNY", ExpiresAt: &expiresAt,
	})
	if err != nil || intent == nil || intent.CheckoutURL != paymentURL.String() {
		t.Fatalf("create Alipay intent: intent=%#v err=%v", intent, err)
	}
	trade := fake.lastPagePay.Trade
	if trade.OutTradeNo != orderNo || trade.TotalAmount != "29.90" || trade.ProductCode != alipayProductCodePagePay ||
		trade.SellerId != provider.sellerID || trade.NotifyURL != provider.notifyURL ||
		trade.ReturnURL != provider.returnURL || trade.GoodsType != "0" ||
		fake.lastPagePay.IntegrationType != alipayIntegrationTypePCWeb {
		t.Fatalf("unexpected page pay request: %#v", fake.lastPagePay)
	}
	if trade.TimeExpire != expiresAt.In(alipayChinaLocation).Format(alipayTimeLayout) {
		t.Fatalf("unexpected expiry %q", trade.TimeExpire)
	}
}

func TestAlipayPaymentClosesUnpaidOrder(t *testing.T) {
	fake := &fakeAlipayPaymentClient{close: &alipay.TradeCloseRsp{
		Error:      alipay.Error{Code: alipay.CodeSuccess},
		OutTradeNo: "CC-CLOSE-ALIPAY",
	}}
	provider := &alipayPagePaymentProvider{client: fake}
	err := provider.ClosePayment(context.Background(), &types.CommercialOrder{OrderNo: "CC-CLOSE-ALIPAY"})
	if err != nil || fake.lastClose.OutTradeNo != "CC-CLOSE-ALIPAY" {
		t.Fatalf("close Alipay order: request=%#v err=%v", fake.lastClose, err)
	}
}

func TestAlipayPaymentTreatsMissingTradeAsClosed(t *testing.T) {
	fake := &fakeAlipayPaymentClient{close: &alipay.TradeCloseRsp{
		Error: alipay.Error{Code: alipay.Code("40004"), SubCode: "ACQ.TRADE_NOT_EXIST"},
	}}
	provider := &alipayPagePaymentProvider{client: fake}
	if err := provider.ClosePayment(context.Background(), &types.CommercialOrder{OrderNo: "CC-NOT-CREATED"}); err != nil {
		t.Fatalf("missing Alipay trade should be locally closable: %v", err)
	}
}

func TestAlipayPaymentRejectsFailedTradeClose(t *testing.T) {
	fake := &fakeAlipayPaymentClient{close: &alipay.TradeCloseRsp{
		Error: alipay.Error{Code: alipay.Code("40004"), SubCode: "ACQ.TRADE_STATUS_ERROR"},
	}}
	provider := &alipayPagePaymentProvider{client: fake}
	if err := provider.ClosePayment(context.Background(), &types.CommercialOrder{OrderNo: "CC-PAID"}); err == nil {
		t.Fatal("failed Alipay close response was accepted")
	}
}

func TestAlipayPaymentRefundsExactOrderAmount(t *testing.T) {
	fake := &fakeAlipayPaymentClient{refund: &alipay.TradeRefundRsp{
		Error:      alipay.Error{Code: alipay.CodeSuccess},
		OutTradeNo: "CC-REFUND-ALIPAY",
		TradeNo:    "2026081322000000000001",
		RefundFee:  "399.00",
		FundChange: "Y",
	}}
	provider := &alipayPagePaymentProvider{client: fake}
	confirmation, err := provider.RefundPayment(context.Background(), &types.CommercialOrder{
		OrderNo: "CC-REFUND-ALIPAY", ProviderTradeNo: fake.refund.TradeNo, AmountFen: 39900, Currency: "CNY",
	}, "CCRF-CC-REFUND-ALIPAY", "user requested refund")
	if err != nil || confirmation == nil {
		t.Fatalf("refund Alipay order: confirmation=%#v err=%v", confirmation, err)
	}
	if fake.lastRefund.OutTradeNo != "CC-REFUND-ALIPAY" || fake.lastRefund.OutRequestNo != "CCRF-CC-REFUND-ALIPAY" || fake.lastRefund.RefundAmount != "399.00" {
		t.Fatalf("unexpected Alipay refund request: %#v", fake.lastRefund)
	}
	if confirmation.AmountFen != 39900 || confirmation.ProviderTradeNo != fake.refund.TradeNo || confirmation.RefundRequestNo != fake.lastRefund.OutRequestNo {
		t.Fatalf("unexpected refund confirmation: %#v", confirmation)
	}
}

func TestAlipayPaymentRejectsRefundAmountMismatch(t *testing.T) {
	fake := &fakeAlipayPaymentClient{refund: &alipay.TradeRefundRsp{
		Error: alipay.Error{Code: alipay.CodeSuccess}, OutTradeNo: "CC-REFUND-MISMATCH", TradeNo: "trade-1", RefundFee: "0.01",
	}}
	provider := &alipayPagePaymentProvider{client: fake}
	if _, err := provider.RefundPayment(context.Background(), &types.CommercialOrder{
		OrderNo: "CC-REFUND-MISMATCH", ProviderTradeNo: "trade-1", AmountFen: 39900, Currency: "CNY",
	}, "CCRF-CC-REFUND-MISMATCH", "test"); err == nil || !strings.Contains(err.Error(), "amount mismatch") {
		t.Fatalf("expected refund amount mismatch, got %v", err)
	}
}

func TestCommercialRefundIsIdempotentAndReleasesFailedClaim(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-REFUND-FLOW"] = &types.CommercialOrder{
		OrderNo: "CC-REFUND-FLOW", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "fulfilled", ProviderTradeNo: "trade-refund-flow", AmountFen: 1, Currency: "CNY",
	}
	provider := &queryCommercialPaymentProvider{refund: &types.CommercialRefundConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "CCRF-CC-REFUND-FLOW",
		ProviderTradeNo: "trade-refund-flow", RefundRequestNo: "CCRF-CC-REFUND-FLOW",
		AmountFen: 1, Currency: "CNY", RefundedAt: time.Now().UTC(),
	}}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	refunded, changed, err := handler.RefundOrder(context.Background(), "CC-REFUND-FLOW", "test refund")
	if err != nil || !changed || refunded == nil || refunded.Status != "refunded" || provider.refundCalls != 1 {
		t.Fatalf("refund result=%#v changed=%v calls=%d err=%v", refunded, changed, provider.refundCalls, err)
	}
	refunded, changed, err = handler.RefundOrder(context.Background(), "CC-REFUND-FLOW", "repeat")
	if err != nil || changed || refunded == nil || refunded.Status != "refunded" || provider.refundCalls != 1 {
		t.Fatalf("repeated refund result=%#v changed=%v calls=%d err=%v", refunded, changed, provider.refundCalls, err)
	}

	store.orders["CC-REFUND-RETRY"] = &types.CommercialOrder{
		OrderNo: "CC-REFUND-RETRY", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "fulfilled", ProviderTradeNo: "trade-refund-retry", AmountFen: 1, Currency: "CNY",
	}
	provider.refund = nil
	provider.refundErr = context.DeadlineExceeded
	if _, _, err := handler.RefundOrder(context.Background(), "CC-REFUND-RETRY", "retry test"); err == nil {
		t.Fatal("provider refund failure was accepted")
	}
	if order := store.orders["CC-REFUND-RETRY"]; order.Status != "fulfilled" || order.LastError == "" {
		t.Fatalf("failed refund claim was not released: %#v", order)
	}
}

func TestCommercialAdminRefundRequiresExactOrderConfirmation(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-ADMIN-REFUND"] = &types.CommercialOrder{
		OrderNo: "CC-ADMIN-REFUND", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "fulfilled", ProviderTradeNo: "trade-admin-refund", AmountFen: 1, Currency: "CNY",
	}
	provider := &queryCommercialPaymentProvider{refund: &types.CommercialRefundConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "CCRF-CC-ADMIN-REFUND",
		ProviderTradeNo: "trade-admin-refund", RefundRequestNo: "CCRF-CC-ADMIN-REFUND",
		AmountFen: 1, Currency: "CNY", RefundedAt: time.Now().UTC(),
	}}
	paymentHandler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	admin := NewAccountAdminHandler(nil, nil, nil, store)
	admin.SetCommercialPaymentHandler(paymentHandler)

	request := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/order-refunds", strings.NewReader(`{"order_no":"CC-ADMIN-REFUND","confirm_order_no":"wrong"}`))
	request.RemoteAddr = "127.0.0.1:40200"
	recorder := httptest.NewRecorder()
	admin.HandleCommercialOrderRefund(recorder, request)
	if recorder.Code != http.StatusBadRequest || provider.refundCalls != 0 {
		t.Fatalf("mismatched confirmation status=%d calls=%d body=%s", recorder.Code, provider.refundCalls, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/order-refunds", strings.NewReader(`{"order_no":"CC-ADMIN-REFUND","confirm_order_no":"CC-ADMIN-REFUND","reason":"test"}`))
	request.RemoteAddr = "127.0.0.1:40200"
	recorder = httptest.NewRecorder()
	admin.HandleCommercialOrderRefund(recorder, request)
	if recorder.Code != http.StatusOK || provider.refundCalls != 1 || store.orders["CC-ADMIN-REFUND"].Status != "refunded" {
		t.Fatalf("confirmed refund status=%d calls=%d order=%#v body=%s", recorder.Code, provider.refundCalls, store.orders["CC-ADMIN-REFUND"], recorder.Body.String())
	}
}

func TestAlipayPaymentNormalizesVerifiedNotification(t *testing.T) {
	fake := &fakeAlipayPaymentClient{notification: &alipay.Notification{
		AppId: "2026000000000001", SellerId: "2088000000000001",
		NotifyType: alipay.NotifyTypeTradeStatusSync, TradeStatus: alipay.TradeStatusSuccess,
		OutTradeNo: "CC202607140000000000000000000001", TradeNo: "2026071422000000000001",
		TotalAmount: "29.90", GmtPayment: "2026-07-14 12:34:56",
	}}
	provider := &alipayPagePaymentProvider{
		appID: fake.notification.AppId, sellerID: fake.notification.SellerId,
		client: fake,
	}
	request := httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("notify_type=trade_status_sync"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	orderNo, confirmation, err := provider.ParseNotification(context.Background(), request)
	if err != nil || orderNo != fake.notification.OutTradeNo || confirmation.EventID != fake.notification.TradeNo ||
		confirmation.AmountFen != 2990 || confirmation.Currency != "CNY" || len(confirmation.PayloadHash) != 64 {
		t.Fatalf("normalize Alipay notification: order=%q confirmation=%#v err=%v", orderNo, confirmation, err)
	}
	fake.notification.SellerId = "unexpected"
	request = httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("notify_type=trade_status_sync"))
	if _, _, err := provider.ParseNotification(context.Background(), request); err == nil || !strings.Contains(err.Error(), "seller mismatch") {
		t.Fatalf("expected seller mismatch, got %v", err)
	}
}

func TestAlipayPaymentRejectsOversizedNotification(t *testing.T) {
	provider := &alipayPagePaymentProvider{client: &fakeAlipayPaymentClient{}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/payments/alipay/notify",
		strings.NewReader(strings.Repeat("x", maxAlipayNotificationBytes+1)),
	)

	if _, _, err := provider.ParseNotification(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "request body is too large") {
		t.Fatalf("expected oversized notification to be rejected, got %v", err)
	}
}

func TestAlipayPaymentVerifiesRealRSA2Notification(t *testing.T) {
	appPrivatePEM, _ := generateAlipayTestKeyPair(t)
	alipayPrivatePEM, alipayPublicPEM := generateAlipayTestKeyPair(t)
	receiver, err := alipay.New("2026000000000001", string(appPrivatePEM), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.LoadAliPayPublicKey(string(alipayPublicPEM)); err != nil {
		t.Fatal(err)
	}
	signer, err := alipay.New("signer", string(alipayPrivatePEM), false)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"app_id":       {"2026000000000001"},
		"seller_id":    {"2088000000000001"},
		"notify_type":  {alipay.NotifyTypeTradeStatusSync},
		"trade_status": {string(alipay.TradeStatusSuccess)},
		"out_trade_no": {"CC202607140000000000000000000001"},
		"trade_no":     {"2026071422000000000001"},
		"total_amount": {"29.90"},
		"gmt_payment":  {"2026-07-14 12:34:56"},
	}
	signature, err := signer.SignValues(values)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign_type", "RSA2")
	values.Set("sign", base64.StdEncoding.EncodeToString(signature))
	provider := &alipayPagePaymentProvider{
		appID: "2026000000000001", sellerID: "2088000000000001", client: receiver,
	}
	request := httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, confirmation, err := provider.ParseNotification(context.Background(), request); err != nil || confirmation.AmountFen != 2990 {
		t.Fatalf("verify signed Alipay notification: confirmation=%#v err=%v", confirmation, err)
	}
	values.Set("total_amount", "0.01")
	request = httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader(values.Encode()))
	if _, _, err := provider.ParseNotification(context.Background(), request); err == nil {
		t.Fatal("tampered Alipay notification should fail signature verification")
	}
}

func TestAlipayPaymentQueryFallbackNormalizesPaidOrder(t *testing.T) {
	fake := &fakeAlipayPaymentClient{query: &alipay.TradeQueryRsp{
		Error:   alipay.Error{Code: alipay.Code("10000")},
		TradeNo: "2026071422000000000001", OutTradeNo: "CC-QUERY-PAID",
		TradeStatus: alipay.TradeStatusSuccess, TotalAmount: "29.90", SendPayDate: "2026-07-14 12:34:56",
	}}
	provider := &alipayPagePaymentProvider{client: fake}
	confirmation, paid, err := provider.QueryPayment(context.Background(), &types.CommercialOrder{OrderNo: "CC-QUERY-PAID"})
	if err != nil || !paid || confirmation.AmountFen != 2990 || confirmation.ProviderTradeNo != fake.query.TradeNo {
		t.Fatalf("query paid order: paid=%v confirmation=%#v err=%v", paid, confirmation, err)
	}
	if fake.lastQuery.OutTradeNo != "CC-QUERY-PAID" {
		t.Fatalf("unexpected query request: %#v", fake.lastQuery)
	}
}

func TestAlipayNotifyHandlerAcknowledgesOnlyFulfilledOrders(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-ALI-NOTIFY"] = &types.CommercialOrder{
		OrderNo: "CC-ALI-NOTIFY", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "pending", AmountFen: 2990, Currency: "CNY",
	}
	fake := &fakeAlipayPaymentClient{notification: &alipay.Notification{
		AppId: "2026000000000001", SellerId: "2088000000000001",
		NotifyType: alipay.NotifyTypeTradeStatusSync, TradeStatus: alipay.TradeStatusSuccess,
		OutTradeNo: "CC-ALI-NOTIFY", TradeNo: "2026071422000000000001", TotalAmount: "29.90",
	}}
	provider := &alipayPagePaymentProvider{appID: fake.notification.AppId, sellerID: fake.notification.SellerId, client: fake}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	for range 2 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("notify_type=trade_status_sync"))
		handler.HandleAlipayNotify(recorder, request)
		if recorder.Code != http.StatusOK || recorder.Body.String() != "success" {
			t.Fatalf("notify response status=%d body=%q", recorder.Code, recorder.Body.String())
		}
	}
	if store.orders["CC-ALI-NOTIFY"].Status != "fulfilled" {
		t.Fatalf("order was not fulfilled: %#v", store.orders["CC-ALI-NOTIFY"])
	}
}

func TestAlipayNotifyHandlerRejectsUnavailableAndMismatchedPayments(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.orders["CC-ALI-MISMATCH"] = &types.CommercialOrder{
		OrderNo: "CC-ALI-MISMATCH", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "pending", AmountFen: 2990, Currency: "CNY",
	}

	disabledHandler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{})
	recorder := httptest.NewRecorder()
	disabledHandler.HandleAlipayNotify(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("")),
	)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != "failure" {
		t.Fatalf("disabled notify response status=%d body=%q", recorder.Code, recorder.Body.String())
	}

	fake := &fakeAlipayPaymentClient{notification: &alipay.Notification{
		AppId: "2026000000000001", SellerId: "2088000000000001",
		NotifyType: alipay.NotifyTypeTradeStatusSync, TradeStatus: alipay.TradeStatusSuccess,
		OutTradeNo: "CC-ALI-MISMATCH", TradeNo: "2026071422000000000002", TotalAmount: "0.01",
	}}
	provider := &alipayPagePaymentProvider{
		appID: fake.notification.AppId, sellerID: fake.notification.SellerId, client: fake,
	}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		Providers: []CommercialPaymentProvider{provider},
	})

	recorder = httptest.NewRecorder()
	handler.HandleAlipayNotify(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/payments/alipay/notify", nil),
	)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Body.String() != "failure" {
		t.Fatalf("method notify response status=%d body=%q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.HandleAlipayNotify(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("notify_type=trade_status_sync")),
	)
	if recorder.Code != http.StatusConflict || recorder.Body.String() != "failure" {
		t.Fatalf("mismatched notify response status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if store.orders["CC-ALI-MISMATCH"].Status != "pending" {
		t.Fatalf("mismatched payment must not fulfill order: %#v", store.orders["CC-ALI-MISMATCH"])
	}
}

func TestAlipayMoneyParsingIsExact(t *testing.T) {
	for input, expected := range map[string]int64{"0.01": 1, "1": 100, "29.9": 2990, "29.90": 2990} {
		actual, err := parseCNYFen(input)
		if err != nil || actual != expected {
			t.Fatalf("parse %q: actual=%d expected=%d err=%v", input, actual, expected, err)
		}
	}
	for _, input := range []string{"", "-1.00", "+1.00", "1.001", "1.", ".01", "abc", "92233720368547758.08"} {
		if _, err := parseCNYFen(input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func generateAlipayTestKeyPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
}

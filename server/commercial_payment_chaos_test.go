package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
	"github.com/smartwalle/alipay/v3"
)

type retryableCommercialPaymentProvider struct {
	intent      *CommercialPaymentIntent
	createErr   error
	createCalls int
}

func (p *retryableCommercialPaymentProvider) Channel() string { return commercialPaymentChannelTest }
func (p *retryableCommercialPaymentProvider) Label() string   { return "异常测试支付" }
func (p *retryableCommercialPaymentProvider) CreatePayment(context.Context, *types.CommercialOrder) (*CommercialPaymentIntent, error) {
	p.createCalls++
	return p.intent, p.createErr
}
func (p *retryableCommercialPaymentProvider) ParseNotification(context.Context, *http.Request) (string, *types.CommercialPaymentConfirmation, error) {
	return "", nil, context.Canceled
}

type failOnceRefundCompletionStore struct {
	*commercialPaymentTestStore
	failCompletion bool
}

type failOnceOrderLookupStore struct {
	*commercialPaymentTestStore
	failLookup bool
}

type staleRefundRestartStore struct {
	*commercialPaymentTestStore
}

func (s *staleRefundRestartStore) BeginCommercialOrderRefund(orderNo, refundRequestNo string, staleAfter time.Duration) (*types.CommercialOrder, bool, error) {
	order := s.orders[orderNo]
	if order != nil && order.Status == "refunding" {
		order.RefundRequestNo = refundRequestNo
		copy := *order
		return &copy, true, nil
	}
	return s.commercialPaymentTestStore.BeginCommercialOrderRefund(orderNo, refundRequestNo, staleAfter)
}

func (s *failOnceOrderLookupStore) GetCommercialOrder(uid int64, orderNo string) (*types.CommercialOrder, error) {
	if s.failLookup {
		s.failLookup = false
		return nil, context.DeadlineExceeded
	}
	return s.commercialPaymentTestStore.GetCommercialOrder(uid, orderNo)
}

func (s *failOnceRefundCompletionStore) CompleteCommercialOrderRefund(orderNo string, confirmation *types.CommercialRefundConfirmation) (*types.CommercialOrder, bool, error) {
	if s.failCompletion {
		s.failCompletion = false
		return nil, false, context.DeadlineExceeded
	}
	return s.commercialPaymentTestStore.CompleteCommercialOrderRefund(orderNo, confirmation)
}

func commercialChaosPlan() *types.CommercialPlan {
	return &types.CommercialPlan{
		ID: 71, Slug: "chaos-personal", Name: "异常测试套餐", PriceFen: 1, Currency: "CNY",
		SaleState: "test", DurationDays: 30, State: 0,
		ModelBudgets: map[string]float64{"gpt-5.6-terra": 1},
	}
}

func TestCommercialOrderRejectsAmbiguousOrOversizedJSON(t *testing.T) {
	valid := `{"plan_id":71,"channel":"test","client_request_id":"chaos_json_0001"}`
	cases := map[string]string{
		"empty":          "",
		"unknown field":  `{"plan_id":71,"channel":"test","client_request_id":"chaos_json_0001","amount_fen":1}`,
		"trailing value": valid + `{}`,
		"oversized":      valid + strings.Repeat(" ", 64<<10),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store := newCommercialPaymentTestStore()
			store.plans = []*types.CommercialPlan{commercialChaosPlan()}
			provider := &retryableCommercialPaymentProvider{}
			handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
				TestUIDs: map[int64]bool{38: true}, TestPayments: map[int64]bool{38: true},
				Providers: []CommercialPaymentProvider{provider},
			})
			recorder := httptest.NewRecorder()
			handler.HandleOrders(recorder, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
			if recorder.Code != http.StatusBadRequest || len(store.orders) != 0 || provider.createCalls != 0 {
				t.Fatalf("status=%d orders=%d create_calls=%d body=%s", recorder.Code, len(store.orders), provider.createCalls, recorder.Body.String())
			}
		})
	}
}

func TestCommercialPaymentIntentFailureRetriesWithoutDuplicateOrder(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{commercialChaosPlan()}
	provider := &retryableCommercialPaymentProvider{createErr: context.DeadlineExceeded}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
		TestUIDs: map[int64]bool{38: true}, TestPayments: map[int64]bool{38: true},
		Providers: []CommercialPaymentProvider{provider},
	})
	body := `{"plan_id":71,"channel":"test","client_request_id":"chaos_retry_0001"}`

	first := httptest.NewRecorder()
	handler.HandleOrders(first, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if first.Code != http.StatusBadGateway || len(store.orders) != 1 || provider.createCalls != 1 {
		t.Fatalf("first status=%d orders=%d calls=%d body=%s", first.Code, len(store.orders), provider.createCalls, first.Body.String())
	}
	var orderNo string
	for candidate, order := range store.orders {
		orderNo = candidate
		if order.Status != "failed" || order.LastError == "" {
			t.Fatalf("failed intent did not leave a retryable order: %#v", order)
		}
	}

	provider.createErr = nil
	provider.intent = &CommercialPaymentIntent{CheckoutURL: "https://openapi.alipay.test/pay", ExpiresAt: time.Now().UTC().Add(20 * time.Minute)}
	second := httptest.NewRecorder()
	handler.HandleOrders(second, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if second.Code != http.StatusOK || len(store.orders) != 1 || provider.createCalls != 2 || store.orders[orderNo].Status != "pending" {
		t.Fatalf("retry status=%d orders=%d calls=%d order=%#v body=%s", second.Code, len(store.orders), provider.createCalls, store.orders[orderNo], second.Body.String())
	}

	third := httptest.NewRecorder()
	handler.HandleOrders(third, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if third.Code != http.StatusOK || len(store.orders) != 1 || provider.createCalls != 2 {
		t.Fatalf("idempotent retry status=%d orders=%d calls=%d body=%s", third.Code, len(store.orders), provider.createCalls, third.Body.String())
	}
}

func TestCommercialOrderResponseLostAcrossWebDeployReturnsSameOrder(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{commercialChaosPlan()}
	provider := &retryableCommercialPaymentProvider{intent: &CommercialPaymentIntent{
		CheckoutURL: "https://openapi.alipay.test/pay",
		ExpiresAt:   time.Now().UTC().Add(20 * time.Minute),
	}}
	newHandler := func() *CommercialPaymentHandler {
		return NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
			TestUIDs: map[int64]bool{38: true}, TestPayments: map[int64]bool{38: true},
			Providers: []CommercialPaymentProvider{provider},
		})
	}
	body := `{"plan_id":71,"channel":"test","client_request_id":"deploy_lost_response_0001"}`

	lostResponse := httptest.NewRecorder()
	newHandler().HandleOrders(lostResponse, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if lostResponse.Code != http.StatusOK {
		t.Fatalf("pre-deploy status=%d body=%s", lostResponse.Code, lostResponse.Body.String())
	}

	recoveredResponse := httptest.NewRecorder()
	newHandler().HandleOrders(recoveredResponse, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if recoveredResponse.Code != http.StatusOK || len(store.orders) != 1 || provider.createCalls != 1 {
		t.Fatalf("post-deploy status=%d orders=%d provider_calls=%d body=%s", recoveredResponse.Code, len(store.orders), provider.createCalls, recoveredResponse.Body.String())
	}
	var first, second struct {
		Order *types.CommercialOrder `json:"order"`
	}
	if err := json.Unmarshal(lostResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(recoveredResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if first.Order == nil || second.Order == nil || first.Order.OrderNo != second.Order.OrderNo || second.Order.Status != "pending" {
		t.Fatalf("lost response created a second order: first=%#v second=%#v", first.Order, second.Order)
	}
}

func TestCommercialAlipayCallbackRetriesAcrossServiceRestart(t *testing.T) {
	base := newCommercialPaymentTestStore()
	base.orders["CC-DEPLOY-CALLBACK"] = &types.CommercialOrder{
		OrderNo: "CC-DEPLOY-CALLBACK", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "pending", AmountFen: 1, Currency: "CNY",
	}
	store := &failOnceOrderLookupStore{commercialPaymentTestStore: base, failLookup: true}
	fake := &fakeAlipayPaymentClient{notification: &alipay.Notification{
		AppId: "2026000000000001", SellerId: "2088000000000001",
		NotifyType: alipay.NotifyTypeTradeStatusSync, TradeStatus: alipay.TradeStatusSuccess,
		OutTradeNo: "CC-DEPLOY-CALLBACK", TradeNo: "2026081422000000000010", TotalAmount: "0.01",
	}}
	newHandler := func() *CommercialPaymentHandler {
		provider := &alipayPagePaymentProvider{appID: fake.notification.AppId, sellerID: fake.notification.SellerId, client: fake}
		return NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	}
	notify := func(handler *CommercialPaymentHandler) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("notify_type=trade_status_sync"))
		handler.HandleAlipayNotify(recorder, request)
		return recorder
	}

	first := notify(newHandler())
	if first.Code != http.StatusServiceUnavailable || first.Body.String() != "failure" {
		t.Fatalf("transient database outage status=%d body=%q", first.Code, first.Body.String())
	}
	second := notify(newHandler())
	third := notify(newHandler())
	if second.Code != http.StatusOK || second.Body.String() != "success" || third.Code != http.StatusOK || third.Body.String() != "success" {
		t.Fatalf("callback replay did not recover: second=%d/%q third=%d/%q", second.Code, second.Body.String(), third.Code, third.Body.String())
	}
	if order := base.orders["CC-DEPLOY-CALLBACK"]; order.Status != "fulfilled" || order.PaidAt == nil {
		t.Fatalf("callback replay did not fulfill the order: %#v", order)
	}
}

type commercialDeployRelayServer struct {
	server       *httptest.Server
	failures     atomic.Int32
	requestCount atomic.Int32
	mu           sync.Mutex
	applied      bool
	scopes       []commercialRelayModelScope
	blockFirst   atomic.Bool
	firstStarted chan struct{}
	firstRelease chan struct{}
}

func newCommercialDeployRelayServer(t *testing.T, failures int32) *commercialDeployRelayServer {
	t.Helper()
	relay := &commercialDeployRelayServer{}
	relay.failures.Store(failures)
	relay.server = httptest.NewServer(http.HandlerFunc(relay.handle))
	t.Cleanup(relay.server.Close)
	return relay
}

func (s *commercialDeployRelayServer) handle(w http.ResponseWriter, r *http.Request) {
	s.requestCount.Add(1)
	if r.Method == http.MethodGet {
		if s.blockFirst.CompareAndSwap(true, false) {
			close(s.firstStarted)
			<-s.firstRelease
		}
		for {
			remaining := s.failures.Load()
			if remaining <= 0 {
				break
			}
			if s.failures.CompareAndSwap(remaining, remaining-1) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"relay is deploying"}`))
				return
			}
		}
	}

	models := []string{"gpt-5.6-terra"}
	available := []commercialRelayModelLimit{{
		Provider: "gpt-upstream", Model: models[0], AllowedModels: models, SharedBudget: true,
		Budget: commercialRelayBudget{MaxLimit: 5, ResetDuration: "1M"},
	}}
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		applied := s.applied
		scopes := append([]commercialRelayModelScope(nil), s.scopes...)
		s.mu.Unlock()
		active := available
		if applied {
			active = []commercialRelayModelLimit{{
				Provider: "gpt-upstream", Model: models[0], AllowedModels: models, SharedBudget: true,
				Budget: commercialRelayBudget{MaxLimit: 100, ResetDuration: "1M"},
			}}
		}
		_ = json.NewEncoder(w).Encode(commercialRelayUsageUser{
			Configured: true,
			Key:        &commercialRelayKeySummary{State: "active"},
			Limits: commercialRelayLimits{
				ModelLimits: active, AvailableModelLimits: available, ModelScopes: scopes,
			},
		})
	case http.MethodPost:
		var payload struct {
			Scopes []commercialRelayModelScope `json:"model_scopes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.applied = true
		s.scopes = append([]commercialRelayModelScope(nil), payload.Scopes...)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newCommercialDeploySyncStore(reconcile bool) *commercialRelaySyncTestStore {
	store := &commercialRelaySyncTestStore{
		summary: &types.CommercialSummary{
			UID:           38,
			TotalsByModel: map[string]float64{"gpt-5.6-terra": 100},
			Grants: []*types.CommercialQuotaGrant{{
				GrantType: "order", Model: "gpt-5.6-terra", AmountCNY: 100,
			}},
		},
		replacedCh:    make(chan struct{}, 1),
		replacedUIDCh: make(chan int64, 128),
	}
	if reconcile {
		store.reconcileUIDs = []int64{38}
	}
	return store
}

func newCommercialDeploySyncer(store *commercialRelaySyncTestStore, relay *commercialDeployRelayServer) *CommercialRelaySyncer {
	return NewCommercialRelaySyncer(
		store,
		&RelayAdminClient{baseURL: relay.server.URL, token: "test-token", client: relay.server.Client()},
		CommercialRelaySyncerOptions{
			EnforceUIDs: map[int64]bool{38: true}, Interval: time.Hour,
			RetryDelays: []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
		},
	)
}

func TestCommercialPurchaseSurvivesRelayAndWebDeployWindow(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{commercialChaosPlan()}
	relay := newCommercialDeployRelayServer(t, 1)
	syncStore := newCommercialDeploySyncStore(false)
	provider := &queryCommercialPaymentProvider{intent: &CommercialPaymentIntent{
		CheckoutURL: "https://openapi.alipay.test/pay",
		ExpiresAt:   time.Now().UTC().Add(20 * time.Minute),
	}}
	newHandler := func() *CommercialPaymentHandler {
		return NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{
			TestUIDs:  map[int64]bool{38: true},
			Providers: []CommercialPaymentProvider{provider},
			SaleChannels: map[string]bool{
				commercialPaymentChannelAlipayPage: true,
			},
			Syncer: newCommercialDeploySyncer(syncStore, relay),
		})
	}
	body := `{"plan_id":71,"channel":"alipay_page","client_request_id":"deploy_relay_0001"}`

	whileRelayDeploys := httptest.NewRecorder()
	newHandler().HandleOrders(whileRelayDeploys, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if whileRelayDeploys.Code != http.StatusServiceUnavailable || len(store.orders) != 0 || provider.createCalls != 0 {
		t.Fatalf("relay deploy created an unsafe order: status=%d orders=%d calls=%d body=%s", whileRelayDeploys.Code, len(store.orders), provider.createCalls, whileRelayDeploys.Body.String())
	}

	afterDeploy := httptest.NewRecorder()
	newHandler().HandleOrders(afterDeploy, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if afterDeploy.Code != http.StatusOK || len(store.orders) != 1 || provider.createCalls != 1 {
		t.Fatalf("relay recovery status=%d orders=%d calls=%d body=%s", afterDeploy.Code, len(store.orders), provider.createCalls, afterDeploy.Body.String())
	}
	var pendingOrderNo string
	for orderNo := range store.orders {
		pendingOrderNo = orderNo
	}

	relay.failures.Store(1)
	retryDuringRelayDeploy := httptest.NewRecorder()
	newHandler().HandleOrders(retryDuringRelayDeploy, commercialPaymentRequest(http.MethodPost, "/api/relay/commercial/orders", body, 38))
	if retryDuringRelayDeploy.Code != http.StatusServiceUnavailable || len(store.orders) != 1 || provider.createCalls != 1 || relay.failures.Load() != 0 {
		t.Fatalf("relay deploy retry was unsafe: status=%d orders=%d calls=%d failures=%d body=%s", retryDuringRelayDeploy.Code, len(store.orders), provider.createCalls, relay.failures.Load(), retryDuringRelayDeploy.Body.String())
	}
	refreshedPage := httptest.NewRecorder()
	newHandler().HandleOrders(refreshedPage, commercialPaymentRequest(http.MethodGet, "/api/relay/commercial/orders", "", 38))
	if refreshedPage.Code != http.StatusOK || !strings.Contains(refreshedPage.Body.String(), pendingOrderNo) {
		t.Fatalf("web deploy could not restore the pending order: status=%d body=%s", refreshedPage.Code, refreshedPage.Body.String())
	}
}

func TestCommercialCallbackCommitsWhileRelayDeploysAndRetriesSync(t *testing.T) {
	paymentStore := newCommercialPaymentTestStore()
	paymentStore.orders["CC-RELAY-DEPLOY-CALLBACK"] = &types.CommercialOrder{
		OrderNo: "CC-RELAY-DEPLOY-CALLBACK", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "pending", AmountFen: 1, Currency: "CNY",
	}
	relay := newCommercialDeployRelayServer(t, 1)
	syncStore := newCommercialDeploySyncStore(false)
	syncer := newCommercialDeploySyncer(syncStore, relay)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncer.Start(ctx)
	fake := &fakeAlipayPaymentClient{notification: &alipay.Notification{
		AppId: "2026000000000001", SellerId: "2088000000000001",
		NotifyType: alipay.NotifyTypeTradeStatusSync, TradeStatus: alipay.TradeStatusSuccess,
		OutTradeNo: "CC-RELAY-DEPLOY-CALLBACK", TradeNo: "2026081422000000000011", TotalAmount: "0.01",
	}}
	provider := &alipayPagePaymentProvider{appID: fake.notification.AppId, sellerID: fake.notification.SellerId, client: fake}
	handler := NewCommercialPaymentHandler(paymentStore, CommercialPaymentHandlerOptions{
		Providers: []CommercialPaymentProvider{provider}, Syncer: syncer,
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("notify_type=trade_status_sync"))
	handler.HandleAlipayNotify(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "success" {
		t.Fatalf("relay deploy blocked payment confirmation: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if order := paymentStore.orders["CC-RELAY-DEPLOY-CALLBACK"]; order.Status != "fulfilled" {
		t.Fatalf("payment was not committed before relay sync: %#v", order)
	}
	select {
	case <-syncStore.replacedCh:
	case <-time.After(time.Second):
		t.Fatal("relay sync did not recover after the deploy window")
	}
	if relay.requestCount.Load() < 4 {
		t.Fatalf("relay retry/verification path was incomplete: requests=%d", relay.requestCount.Load())
	}
}

func TestCommercialRelayStartupReconcilesQueueLostDuringRestart(t *testing.T) {
	relay := newCommercialDeployRelayServer(t, 0)
	store := newCommercialDeploySyncStore(true)
	store.reconcileUIDs = make([]int64, 75)
	for index := range store.reconcileUIDs {
		store.reconcileUIDs[index] = int64(index + 1)
	}
	syncer := newCommercialDeploySyncer(store, relay)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncer.Start(ctx)

	reconciled := map[int64]bool{}
	deadline := time.After(3 * time.Second)
	for len(reconciled) < len(store.reconcileUIDs) {
		select {
		case uid := <-store.replacedUIDCh:
			reconciled[uid] = true
		case <-deadline:
			t.Fatalf("a restarted service reconciled only %d/%d users before the periodic interval", len(reconciled), len(store.reconcileUIDs))
		}
	}
}

func TestCommercialRelayRequeuesOverflowedUIDOutsideDatabaseCandidates(t *testing.T) {
	relay := newCommercialDeployRelayServer(t, 0)
	store := newCommercialDeploySyncStore(false)
	syncer := newCommercialDeploySyncer(store, relay)
	for index := 0; index < cap(syncer.queue); index++ {
		syncer.queue <- commercialRelaySyncRequest{uid: int64(10_000 + index)}
	}

	syncer.Enqueue(38)
	if pending := syncer.pendingUIDs[38]; pending == nil || pending.queued {
		t.Fatalf("overflowed UID was not retained as idle pending state: %#v", pending)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncer.Start(ctx)
	select {
	case uid := <-store.replacedUIDCh:
		if uid != 38 {
			t.Fatalf("unexpected reconciled UID: %d", uid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("overflowed UID outside database candidates was not requeued")
	}
}

func TestCommercialRelayDeployRetriesAreBounded(t *testing.T) {
	relay := newCommercialDeployRelayServer(t, 100)
	store := newCommercialDeploySyncStore(false)
	syncer := newCommercialDeploySyncer(store, relay)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncer.Start(ctx)
	for range 100 {
		syncer.Enqueue(38)
	}

	deadline := time.Now().Add(time.Second)
	for relay.requestCount.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if requests := relay.requestCount.Load(); requests != 3 {
		t.Fatalf("expected one attempt and two retries, got %d requests", requests)
	}
	time.Sleep(30 * time.Millisecond)
	if requests := relay.requestCount.Load(); requests != 3 {
		t.Fatalf("permanent relay outage caused unbounded retries: requests=%d", requests)
	}
}

func TestCommercialRelayDeduplicatesAndReplaysUpdateDuringActiveSync(t *testing.T) {
	relay := newCommercialDeployRelayServer(t, 0)
	relay.firstStarted = make(chan struct{})
	relay.firstRelease = make(chan struct{})
	relay.blockFirst.Store(true)
	store := newCommercialDeploySyncStore(false)
	syncer := newCommercialDeploySyncer(store, relay)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncer.Start(ctx)
	syncer.Enqueue(38)

	select {
	case <-relay.firstStarted:
	case <-time.After(time.Second):
		close(relay.firstRelease)
		t.Fatal("first relay sync did not start")
	}
	for range 100 {
		syncer.Enqueue(38)
	}
	close(relay.firstRelease)

	completed := 0
	deadline := time.After(2 * time.Second)
	for completed < 2 {
		select {
		case <-store.replacedUIDCh:
			completed++
		case <-deadline:
			t.Fatalf("an update arriving during sync was lost; completed=%d", completed)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if requests := relay.requestCount.Load(); requests != 4 {
		t.Fatalf("duplicate notifications were not coalesced into one follow-up sync: requests=%d", requests)
	}
}

func TestCommercialRelayReplaysNewGenerationAfterActiveSyncFails(t *testing.T) {
	relay := newCommercialDeployRelayServer(t, 100)
	relay.firstStarted = make(chan struct{})
	relay.firstRelease = make(chan struct{})
	relay.blockFirst.Store(true)
	store := newCommercialDeploySyncStore(false)
	syncer := newCommercialDeploySyncer(store, relay)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncer.Start(ctx)
	syncer.Enqueue(38)

	select {
	case <-relay.firstStarted:
	case <-time.After(time.Second):
		close(relay.firstRelease)
		t.Fatal("first relay sync did not start")
	}
	for range 100 {
		syncer.Enqueue(38)
	}
	close(relay.firstRelease)

	deadline := time.Now().Add(time.Second)
	for relay.requestCount.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if requests := relay.requestCount.Load(); requests != 4 {
		t.Fatalf("new generation was lost after the active sync failed: requests=%d", requests)
	}
	time.Sleep(30 * time.Millisecond)
	if requests := relay.requestCount.Load(); requests != 4 {
		t.Fatalf("new generation caused unbounded retries: requests=%d", requests)
	}
}

func TestCommercialDelayedAlipayCallbackFulfillsClosedOrder(t *testing.T) {
	store := newCommercialPaymentTestStore()
	closedAt := time.Now().UTC().Add(-time.Minute)
	store.orders["CC-DELAYED-CALLBACK"] = &types.CommercialOrder{
		OrderNo: "CC-DELAYED-CALLBACK", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "closed", AmountFen: 1, Currency: "CNY", ClosedAt: &closedAt,
	}
	fake := &fakeAlipayPaymentClient{notification: &alipay.Notification{
		AppId: "2026000000000001", SellerId: "2088000000000001",
		NotifyType: alipay.NotifyTypeTradeStatusSync, TradeStatus: alipay.TradeStatusSuccess,
		OutTradeNo: "CC-DELAYED-CALLBACK", TradeNo: "2026081422000000000001", TotalAmount: "0.01",
	}}
	provider := &alipayPagePaymentProvider{appID: fake.notification.AppId, sellerID: fake.notification.SellerId, client: fake}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})

	for attempt := 1; attempt <= 2; attempt++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/payments/alipay/notify", strings.NewReader("notify_type=trade_status_sync"))
		handler.HandleAlipayNotify(recorder, request)
		if recorder.Code != http.StatusOK || recorder.Body.String() != "success" {
			t.Fatalf("attempt=%d status=%d body=%q", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if order := store.orders["CC-DELAYED-CALLBACK"]; order.Status != "fulfilled" || order.PaidAt == nil {
		t.Fatalf("delayed callback did not recover the closed order: %#v", order)
	}
}

func TestCommercialRefundRecoversWhenProviderSucceededBeforeLocalCommit(t *testing.T) {
	base := newCommercialPaymentTestStore()
	base.orders["CC-REFUND-COMMIT-RETRY"] = &types.CommercialOrder{
		OrderNo: "CC-REFUND-COMMIT-RETRY", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "fulfilled", ProviderTradeNo: "trade-refund-commit-retry", AmountFen: 1, Currency: "CNY",
	}
	store := &failOnceRefundCompletionStore{commercialPaymentTestStore: base, failCompletion: true}
	provider := &queryCommercialPaymentProvider{refund: &types.CommercialRefundConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "CCRF-CC-REFUND-COMMIT-RETRY",
		ProviderTradeNo: "trade-refund-commit-retry", RefundRequestNo: "CCRF-CC-REFUND-COMMIT-RETRY",
		AmountFen: 1, Currency: "CNY", RefundedAt: time.Now().UTC(),
	}}
	handler := NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})

	if _, _, err := handler.RefundOrder(context.Background(), "CC-REFUND-COMMIT-RETRY", "commit ambiguity"); err == nil {
		t.Fatal("local completion failure was accepted")
	}
	if order := base.orders["CC-REFUND-COMMIT-RETRY"]; order.Status != "fulfilled" || order.LastError == "" {
		t.Fatalf("failed completion did not release the refund claim: %#v", order)
	}
	refunded, changed, err := handler.RefundOrder(context.Background(), "CC-REFUND-COMMIT-RETRY", "commit retry")
	if err != nil || !changed || refunded == nil || refunded.Status != "refunded" || provider.refundCalls != 2 {
		t.Fatalf("retry result=%#v changed=%v calls=%d err=%v", refunded, changed, provider.refundCalls, err)
	}
	_, changed, err = handler.RefundOrder(context.Background(), "CC-REFUND-COMMIT-RETRY", "duplicate retry")
	if err != nil || changed || provider.refundCalls != 2 {
		t.Fatalf("duplicate retry changed=%v calls=%d err=%v", changed, provider.refundCalls, err)
	}
}

func TestCommercialRefundResumesAfterProcessStopsBeforeLocalCommit(t *testing.T) {
	base := newCommercialPaymentTestStore()
	base.orders["CC-REFUND-RESTART"] = &types.CommercialOrder{
		OrderNo: "CC-REFUND-RESTART", UID: 38, Channel: commercialPaymentChannelAlipayPage,
		Status: "refunding", RefundRequestNo: "CCRF-CC-REFUND-RESTART",
		ProviderTradeNo: "trade-refund-restart", AmountFen: 1, Currency: "CNY",
	}
	store := &staleRefundRestartStore{commercialPaymentTestStore: base}
	provider := &queryCommercialPaymentProvider{refund: &types.CommercialRefundConfirmation{
		Channel: commercialPaymentChannelAlipayPage, EventID: "CCRF-CC-REFUND-RESTART",
		ProviderTradeNo: "trade-refund-restart", RefundRequestNo: "CCRF-CC-REFUND-RESTART",
		AmountFen: 1, Currency: "CNY", RefundedAt: time.Now().UTC(),
	}}
	newHandler := func() *CommercialPaymentHandler {
		return NewCommercialPaymentHandler(store, CommercialPaymentHandlerOptions{Providers: []CommercialPaymentProvider{provider}})
	}

	refunded, changed, err := newHandler().RefundOrder(context.Background(), "CC-REFUND-RESTART", "resume after deploy")
	if err != nil || !changed || refunded == nil || refunded.Status != "refunded" || provider.refundCalls != 1 {
		t.Fatalf("stale refund did not recover: order=%#v changed=%v calls=%d err=%v", refunded, changed, provider.refundCalls, err)
	}
	refunded, changed, err = newHandler().RefundOrder(context.Background(), "CC-REFUND-RESTART", "duplicate after deploy")
	if err != nil || changed || refunded == nil || refunded.Status != "refunded" || provider.refundCalls != 1 {
		t.Fatalf("recovered refund was not idempotent: order=%#v changed=%v calls=%d err=%v", refunded, changed, provider.refundCalls, err)
	}
}

func TestAlipayRefundRejectsAmbiguousProviderResponses(t *testing.T) {
	baseResponse := func() *alipay.TradeRefundRsp {
		return &alipay.TradeRefundRsp{
			Error: alipay.Error{Code: alipay.CodeSuccess}, OutTradeNo: "CC-REFUND-CHAOS",
			TradeNo: "2026081422000000000002", RefundFee: "399.00", FundChange: "Y",
		}
	}
	cases := []struct {
		name        string
		response    *alipay.TradeRefundRsp
		providerErr error
		want        string
	}{
		{name: "transport timeout", providerErr: context.DeadlineExceeded, want: "refund Alipay order"},
		{name: "empty response", want: "empty refund response"},
		{name: "provider rejection", response: &alipay.TradeRefundRsp{Error: alipay.Error{Code: alipay.Code("40004")}}, want: "refund failed"},
		{name: "order mismatch", response: func() *alipay.TradeRefundRsp { r := baseResponse(); r.OutTradeNo = "OTHER"; return r }(), want: "order mismatch"},
		{name: "missing trade", response: func() *alipay.TradeRefundRsp { r := baseResponse(); r.TradeNo = ""; return r }(), want: "trade mismatch"},
		{name: "trade mismatch", response: func() *alipay.TradeRefundRsp { r := baseResponse(); r.TradeNo = "OTHER"; return r }(), want: "trade mismatch"},
		{name: "malformed amount", response: func() *alipay.TradeRefundRsp { r := baseResponse(); r.RefundFee = "NaN"; return r }(), want: "amount mismatch"},
		{name: "partial amount", response: func() *alipay.TradeRefundRsp { r := baseResponse(); r.RefundFee = "0.01"; return r }(), want: "amount mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &alipayPagePaymentProvider{client: &fakeAlipayPaymentClient{refund: tc.response, refundErr: tc.providerErr}}
			_, err := provider.RefundPayment(context.Background(), &types.CommercialOrder{
				OrderNo: "CC-REFUND-CHAOS", ProviderTradeNo: "2026081422000000000002", AmountFen: 39900, Currency: "CNY",
			}, "CCRF-CC-REFUND-CHAOS", "chaos test")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAlipayQueryHandlesNonFinalAndInvalidResponses(t *testing.T) {
	baseResponse := func() *alipay.TradeQueryRsp {
		return &alipay.TradeQueryRsp{
			Error: alipay.Error{Code: alipay.CodeSuccess}, OutTradeNo: "CC-QUERY-CHAOS",
			TradeNo: "2026081422000000000003", TradeStatus: alipay.TradeStatusSuccess, TotalAmount: "399.00",
		}
	}
	cases := []struct {
		name        string
		response    *alipay.TradeQueryRsp
		providerErr error
		wantErr     string
		wantPaid    bool
	}{
		{name: "transport timeout", providerErr: context.DeadlineExceeded, wantErr: "query Alipay order"},
		{name: "empty response", wantErr: "empty query response"},
		{name: "trade not found", response: &alipay.TradeQueryRsp{Error: alipay.Error{Code: alipay.Code("40004"), SubCode: "ACQ.TRADE_NOT_EXIST"}}},
		{name: "provider rejection", response: &alipay.TradeQueryRsp{Error: alipay.Error{Code: alipay.Code("40004"), SubCode: "ACQ.SYSTEM_ERROR"}}, wantErr: "query failed"},
		{name: "not paid yet", response: func() *alipay.TradeQueryRsp { r := baseResponse(); r.TradeStatus = "WAIT_BUYER_PAY"; return r }()},
		{name: "order mismatch", response: func() *alipay.TradeQueryRsp { r := baseResponse(); r.OutTradeNo = "OTHER"; return r }(), wantErr: "order mismatch"},
		{name: "missing trade", response: func() *alipay.TradeQueryRsp { r := baseResponse(); r.TradeNo = ""; return r }(), wantErr: "missing trade_no"},
		{name: "malformed amount", response: func() *alipay.TradeQueryRsp { r := baseResponse(); r.TotalAmount = "399.001"; return r }(), wantErr: "queried amount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &alipayPagePaymentProvider{client: &fakeAlipayPaymentClient{query: tc.response, queryErr: tc.providerErr}}
			_, paid, err := provider.QueryPayment(context.Background(), &types.CommercialOrder{OrderNo: "CC-QUERY-CHAOS"})
			if tc.wantErr == "" {
				if err != nil || paid != tc.wantPaid {
					t.Fatalf("paid=%v err=%v", paid, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || paid {
				t.Fatalf("expected %q, paid=%v err=%v", tc.wantErr, paid, err)
			}
		})
	}
}

func TestAlipayNotificationRejectsInvalidBusinessFields(t *testing.T) {
	baseNotification := func() *alipay.Notification {
		return &alipay.Notification{
			AppId: "2026000000000001", SellerId: "2088000000000001",
			NotifyType: alipay.NotifyTypeTradeStatusSync, TradeStatus: alipay.TradeStatusSuccess,
			OutTradeNo: "CC-NOTIFY-CHAOS", TradeNo: "2026081422000000000004", TotalAmount: "399.00",
		}
	}
	provider := &alipayPagePaymentProvider{appID: "2026000000000001", sellerID: "2088000000000001"}
	cases := []struct {
		name   string
		mutate func(*alipay.Notification)
		want   string
	}{
		{name: "app mismatch", mutate: func(n *alipay.Notification) { n.AppId = "OTHER" }, want: "application mismatch"},
		{name: "seller mismatch", mutate: func(n *alipay.Notification) { n.SellerId = "OTHER" }, want: "seller mismatch"},
		{name: "wrong notify type", mutate: func(n *alipay.Notification) { n.NotifyType = "other" }, want: "type is invalid"},
		{name: "unfinished trade", mutate: func(n *alipay.Notification) { n.TradeStatus = "WAIT_BUYER_PAY" }, want: "not successful"},
		{name: "missing order", mutate: func(n *alipay.Notification) { n.OutTradeNo = "" }, want: "missing out_trade_no"},
		{name: "missing trade", mutate: func(n *alipay.Notification) { n.TradeNo = "" }, want: "missing trade_no"},
		{name: "zero amount", mutate: func(n *alipay.Notification) { n.TotalAmount = "0" }, want: "amount is invalid"},
		{name: "malformed amount", mutate: func(n *alipay.Notification) { n.TotalAmount = "399.001" }, want: "amount is invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notification := baseNotification()
			tc.mutate(notification)
			_, _, err := provider.confirmationFromNotification(notification, strings.Repeat("a", 64))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

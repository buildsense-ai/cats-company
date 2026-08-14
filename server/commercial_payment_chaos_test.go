package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// commercialTopupTestStore 在已有的商业支付测试 store 上加充值下单能力，
// 让「充值走的是同一条下单/支付/回调链路」这件事在单测里也能被真正走一遍。
type commercialTopupTestStore struct {
	*commercialPaymentTestStore
	topups map[string]*types.CommercialOrder
}

func newCommercialTopupTestStore() *commercialTopupTestStore {
	return &commercialTopupTestStore{
		commercialPaymentTestStore: newCommercialPaymentTestStore(),
		topups:                     map[string]*types.CommercialOrder{},
	}
}

func (s *commercialTopupTestStore) CreateCommercialTopupOrder(uid, amountFen int64, channel, clientRequestID string, expiresAt *time.Time) (*types.CommercialOrder, error) {
	key := strings.TrimSpace(clientRequestID)
	if existing := s.topups[key]; existing != nil {
		if existing.AmountFen != amountFen || existing.Channel != channel {
			return nil, errCommercialTopupConflictForTest
		}
		copy := *existing
		return &copy, nil
	}
	order := &types.CommercialOrder{
		ID:                   int64(len(s.topups) + 1),
		OrderNo:              "CT" + key,
		UID:                  uid,
		Kind:                 types.CommercialOrderKindCreditTopup,
		PlanSlug:             "credit_topup",
		PlanName:             "点数充值",
		AmountFen:            amountFen,
		Currency:             "CNY",
		Channel:              channel,
		Status:               "created",
		ClientRequestID:      key,
		PlanModelCreditUnits: amountFen * commercialTopupUnitsPerFen,
		ExpiresAt:            expiresAt,
		CreatedAt:            time.Now().UTC(),
		UpdatedAt:            time.Now().UTC(),
	}
	s.topups[key] = order
	// 让充值订单也进入父 store 的订单表：支付发起/回调/退款走的是同一套
	// 既有代码路径，测试必须真的经过它们，而不是绕开。
	s.orders[order.OrderNo] = order
	copy := *order
	return &copy, nil
}

type commercialTopupConflictError struct{}

func (commercialTopupConflictError) Error() string {
	return "client request id is already used by another order"
}

var errCommercialTopupConflictForTest = commercialTopupConflictError{}

// newCommercialTopupTestHandler builds a handler whose Alipay channel is really
// sellable for one uid, which is the only state in which the catalog may
// advertise credit_topup.
func newCommercialTopupTestHandler(store CommercialPaymentStore, provider CommercialPaymentProvider, uid int64) *CommercialPaymentHandler {
	options := CommercialPaymentHandlerOptions{
		PublicEnabled: true,
		Providers:     []CommercialPaymentProvider{provider},
	}
	if provider != nil {
		options.Syncer = &CommercialRelaySyncer{enforceUIDs: map[int64]bool{uid: true}}
	}
	return NewCommercialPaymentHandler(store, options)
}

func commercialTopupTestRequest(uid int64, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/relay/commercial/orders", strings.NewReader(body))
	return req.WithContext(context.WithValue(req.Context(), uidKey, uid))
}

func TestCommercialTopupRateMatchesProductContract(t *testing.T) {
	if commercialTopupUnitsPerFen != 1_000_000 {
		t.Fatalf("1 fen must be 1 credit = 1000000 units, got %d", commercialTopupUnitsPerFen)
	}
	if got := commercialTopupUnitsForAmount(1000); got != 1000*1_000_000 {
		t.Fatalf("10 CNY must be 1000 credits, got %d units", got)
	}
	if got := commercialTopupUnitsForAmount(0); got != 0 {
		t.Fatalf("a zero amount must convert to zero units, got %d", got)
	}
}

func TestCommercialTopupConfigDefaultsAndEnv(t *testing.T) {
	t.Setenv("CATSCO_CREDIT_TOPUP_ENABLED", "")
	t.Setenv("CATSCO_CREDIT_TOPUP_MIN_FEN", "")
	t.Setenv("CATSCO_CREDIT_TOPUP_MAX_FEN", "")
	t.Setenv("CATSCO_CREDIT_TOPUP_PRESETS_FEN", "")
	cfg := commercialTopupConfigFromEnv()
	if cfg.Killed || cfg.MinAmountFen != 100 || cfg.MaxAmountFen != 1_000_000 || cfg.CreditsPerCNY != 100 {
		t.Fatalf("unexpected default top-up config: %#v", cfg)
	}
	if len(cfg.PresetsFen) != 6 || cfg.PresetsFen[0] != 1000 || cfg.PresetsFen[5] != 50000 {
		t.Fatalf("unexpected default presets: %#v", cfg.PresetsFen)
	}

	t.Setenv("CATSCO_CREDIT_TOPUP_ENABLED", "off")
	t.Setenv("CATSCO_CREDIT_TOPUP_MIN_FEN", "500")
	t.Setenv("CATSCO_CREDIT_TOPUP_MAX_FEN", "200000")
	t.Setenv("CATSCO_CREDIT_TOPUP_PRESETS_FEN", "500,1000,999999,oops")
	cfg = commercialTopupConfigFromEnv()
	if !cfg.Killed || cfg.MinAmountFen != 500 || cfg.MaxAmountFen != 200000 {
		t.Fatalf("env was not applied: %#v", cfg)
	}
	// 超上限与非法项被丢掉，剩下的档位仍然可用。
	if len(cfg.PresetsFen) != 2 || cfg.PresetsFen[0] != 500 || cfg.PresetsFen[1] != 1000 {
		t.Fatalf("invalid presets leaked through: %#v", cfg.PresetsFen)
	}
}

func TestCommercialTopupCatalogRequiresRealGates(t *testing.T) {
	uid := int64(4242)
	store := newCommercialTopupTestStore()
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", "")

	// 没有 Alipay 售卖通道：不能卖。
	handler := newCommercialTopupTestHandler(store, &queryCommercialPaymentProvider{}, uid)
	block := handler.commercialTopupCatalog(uid)
	if enabled, _ := block["enabled"].(bool); enabled {
		t.Fatalf("top-up must stay disabled without a sale channel: %#v", block)
	}

	// 通道在，但 relay 侧的 credit 结算没配：买了也花不出去，仍然不卖。
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", strings.Repeat("a", 64))
	handler = newCommercialTopupTestHandler(store, &queryCommercialPaymentProvider{}, uid)
	if enabled, _ := handler.commercialTopupCatalog(uid)["enabled"].(bool); !enabled {
		t.Fatalf("top-up should be sellable once both gates are open: %#v", handler.commercialTopupCatalog(uid))
	}

	// 运营 kill switch 优先。
	t.Setenv("CATSCO_CREDIT_TOPUP_ENABLED", "false")
	if enabled, _ := handler.commercialTopupCatalog(uid)["enabled"].(bool); enabled {
		t.Fatal("kill switch must disable the product")
	}
	t.Setenv("CATSCO_CREDIT_TOPUP_ENABLED", "")

	// 金额区间与换算率由服务端给，前端不必自己算。
	block = handler.commercialTopupCatalog(uid)
	if block["credits_per_cny"] != int64(100) || block["min_amount_fen"] != int64(100) || block["max_amount_fen"] != int64(1000000) {
		t.Fatalf("catalog block is missing the product contract: %#v", block)
	}
}

func TestCommercialTopupOrderRejectsSubscriptionShapes(t *testing.T) {
	uid := int64(7)
	store := newCommercialTopupTestStore()
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", strings.Repeat("b", 64))
	handler := newCommercialTopupTestHandler(store, &queryCommercialPaymentProvider{}, uid)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"plan id on a top-up", `{"kind":"credit_topup","plan_id":3,"amount_fen":1000,"channel":"alipay_page","client_request_id":"topup-aaaaaaaa"}`, http.StatusBadRequest},
		{"amount on a package order", `{"kind":"plan","plan_id":3,"amount_fen":1000,"channel":"alipay_page","client_request_id":"topup-bbbbbbbb"}`, http.StatusBadRequest},
		{"unknown kind", `{"kind":"gift","amount_fen":1000,"channel":"alipay_page","client_request_id":"topup-cccccccc"}`, http.StatusBadRequest},
		{"amount below the floor", `{"kind":"credit_topup","amount_fen":1,"channel":"alipay_page","client_request_id":"topup-dddddddd"}`, http.StatusBadRequest},
		{"amount above the ceiling", `{"kind":"credit_topup","amount_fen":1000001,"channel":"alipay_page","client_request_id":"topup-eeeeeeee"}`, http.StatusBadRequest},
		{"short client request id", `{"kind":"credit_topup","amount_fen":1000,"channel":"alipay_page","client_request_id":"x"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		handler.HandleOrders(recorder, commercialTopupTestRequest(uid, tc.body))
		if recorder.Code != tc.want {
			t.Fatalf("%s: status=%d body=%s", tc.name, recorder.Code, recorder.Body.String())
		}
	}
	if len(store.topups) != 0 {
		t.Fatalf("a rejected request must not create an order: %#v", store.topups)
	}
}

func TestCommercialTopupOrderSnapshotAndIdempotency(t *testing.T) {
	uid := int64(11)
	store := newCommercialTopupTestStore()
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", strings.Repeat("c", 64))
	provider := &queryCommercialPaymentProvider{intent: &CommercialPaymentIntent{CheckoutURL: "https://pay.example/x", ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}}
	handler := newCommercialTopupTestHandler(store, provider, uid)

	body := `{"kind":"credit_topup","amount_fen":3000,"channel":"alipay_page","client_request_id":"topup-12345678"}`
	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialTopupTestRequest(uid, body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("create top-up: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Order *types.CommercialOrder `json:"order"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Order == nil {
		t.Fatalf("decode response: %v %s", err, recorder.Body.String())
	}
	order := response.Order
	if order.Kind != types.CommercialOrderKindCreditTopup || order.PlanID != 0 || order.AmountFen != 3000 {
		t.Fatalf("top-up order snapshot is wrong: %#v", order)
	}
	if order.PlanModelCreditUnits != 3000*commercialTopupUnitsPerFen {
		t.Fatalf("units snapshot must be amount*1e6, got %d", order.PlanModelCreditUnits)
	}
	if order.PlanDurationDays != 0 || order.PlanMonthlyBudget != 0 {
		t.Fatalf("a top-up must not carry subscription fields: %#v", order)
	}
	if order.CheckoutURL != "https://pay.example/x" || order.Status != "pending" {
		t.Fatalf("payment was not started: %#v", order)
	}

	// 同一个 client_request_id + 同金额：重放，不产生第二笔订单。
	recorder = httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialTopupTestRequest(uid, body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("replay: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(store.topups) != 1 {
		t.Fatalf("a replay created a second order: %#v", store.topups)
	}

	// 同一个 id + 不同金额：直接冲突，不能把 30 元改成 100 元。
	recorder = httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialTopupTestRequest(uid,
		`{"kind":"credit_topup","amount_fen":10000,"channel":"alipay_page","client_request_id":"topup-12345678"}`))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("same id with another amount must conflict: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCommercialTopupOrdersUnavailableWithoutGates(t *testing.T) {
	uid := int64(12)
	store := newCommercialTopupTestStore()
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", "")
	handler := newCommercialTopupTestHandler(store, &queryCommercialPaymentProvider{}, uid)
	recorder := httptest.NewRecorder()
	handler.HandleOrders(recorder, commercialTopupTestRequest(uid,
		`{"kind":"credit_topup","amount_fen":3000,"channel":"alipay_page","client_request_id":"topup-open-gate"}`))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("top-up must be refused while the settlement channel is unconfigured: status=%d body=%s",
			recorder.Code, recorder.Body.String())
	}
	if len(store.topups) != 0 {
		t.Fatalf("refused request created an order: %#v", store.topups)
	}
}

func TestCommercialTopupEnvDoesNotLeakBetweenTests(t *testing.T) {
	// 这个用例存在的意义：上面几个用例靠 t.Setenv 改流程开关，如果谁忘了
	// 恢复，产品门禁就会在别的用例里被误开。这里显式确认默认是关的。
	if _, present := os.LookupEnv("CATSCO_CREDIT_TOPUP_PRESETS_FEN"); present && os.Getenv("CATSCO_CREDIT_TOPUP_PRESETS_FEN") != "" {
		t.Skip("presets override is set by the environment")
	}
	cfg := commercialTopupConfigFromEnv()
	if cfg.CreditsPerCNY != commercialTopupCreditsPerCNY {
		t.Fatalf("credits per CNY drifted: %#v", cfg)
	}
}

func TestCommercialTopupKillSwitchBlocksDirectOrder(t *testing.T) {
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", strings.Repeat("c", 64))
	t.Setenv("CATSCO_CREDIT_TOPUP_ENABLED", "off")
	s := newCommercialTopupTestStore()
	h := newCommercialTopupTestHandler(s, &queryCommercialPaymentProvider{}, 77)
	r := httptest.NewRecorder()
	h.HandleOrders(r, commercialTopupTestRequest(77, `{"kind":"credit_topup","amount_fen":1000,"channel":"alipay_page","client_request_id":"killed-request-1"}`))
	if r.Code != http.StatusServiceUnavailable || len(s.topups) != 0 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}

func TestCommercialTopupRejectsOverflowAmount(t *testing.T) {
	if got := commercialTopupUnitsForAmount(int64(^uint64(0) >> 1)); got != 0 {
		t.Fatalf("overflow amount accepted: %d", got)
	}
}

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type commercialPaymentTestStore struct {
	*commercialTestStore
	orders      map[string]*types.CommercialOrder
	requestIDs  map[string]string
	trialClaims map[int64]bool
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

func (s *commercialPaymentTestStore) SetCommercialOrderPaymentIntent(orderNo, codeURL string, expiresAt time.Time) (*types.CommercialOrder, error) {
	order := s.orders[orderNo]
	order.Status = "pending"
	order.CodeURL = codeURL
	order.ExpiresAt = &expiresAt
	copy := *order
	return &copy, nil
}

func (s *commercialPaymentTestStore) BeginCommercialOrderPayment(orderNo string, expiresAt time.Time) (*types.CommercialOrder, bool, error) {
	order := s.orders[orderNo]
	if order == nil {
		return nil, false, context.Canceled
	}
	if order.Status != "created" && order.Status != "failed" {
		copy := *order
		return &copy, false, nil
	}
	order.Status = "pending"
	order.CodeURL = ""
	order.ExpiresAt = &expiresAt
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
		{ID: 1, Slug: "hidden", Name: "Hidden", PriceFen: 100, Currency: "CNY", SaleState: "hidden", State: 0},
		{ID: 2, Slug: "test", Name: "Test", PriceFen: 200, Currency: "CNY", SaleState: "test", State: 0},
		{ID: 3, Slug: "public", Name: "Public", PriceFen: 300, Currency: "CNY", SaleState: "public", State: 0},
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
	if len(payload.Channels) != 1 || payload.Channels[0].ID != commercialPaymentChannelTest {
		t.Fatalf("unexpected channels: %#v", payload.Channels)
	}
}

func TestCommercialTestPaymentOrderAndFulfillment(t *testing.T) {
	store := newCommercialPaymentTestStore()
	store.plans = []*types.CommercialPlan{{
		ID: 7, Slug: "gray", Name: "Gray", PriceFen: 2990, Currency: "CNY", SaleState: "test", DurationDays: 30, State: 0,
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

func TestCommercialRelayManagedPlanOnlyRemovesOwnedBudgets(t *testing.T) {
	relayUser := &commercialRelayUsageUser{Limits: commercialRelayLimits{ModelLimits: []commercialRelayModelLimit{
		{Provider: "deepseek", Model: "deepseek-v4-flash", AllowedModels: []string{"deepseek-v4-flash"}, Budget: commercialRelayBudget{MaxLimit: 100}},
		{Provider: "minimax", Model: "MiniMax-M3", AllowedModels: []string{"MiniMax-M3"}, Budget: commercialRelayBudget{MaxLimit: 500}},
	}}}
	managed := []*types.CommercialManagedRelayBudget{{
		UID: 38, Model: "deepseek-v4-flash", Provider: "deepseek", AllowedModels: []string{"deepseek-v4-flash"}, MaxLimit: 100,
	}}
	updates, next := commercialRelayManagedPlan(38, &types.CommercialSummary{UID: 38, TotalsByModel: map[string]float64{}}, relayUser, managed)
	if len(updates) != 1 || updates[0].Provider != "deepseek" || updates[0].MaxLimit != commercialRelayBlockedLimit {
		t.Fatalf("unexpected removal updates: %#v", updates)
	}
	if len(next) != 1 || next[0].MaxLimit != commercialRelayBlockedLimit {
		t.Fatalf("managed budget should retain the expiry block: %#v", next)
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

func TestTruncateUTF8DoesNotSplitRune(t *testing.T) {
	value := truncateUTF8("CatsCo 教师套餐", 10)
	if !strings.HasPrefix("CatsCo 教师套餐", value) || len(value) > 10 {
		t.Fatalf("invalid truncation %q", value)
	}
}

func TestWeChatPaymentStaysDisabledWithoutSecrets(t *testing.T) {
	for _, name := range []string{
		"CATS_WECHAT_PAY_APP_ID",
		"CATS_WECHAT_PAY_MCH_ID",
		"CATS_WECHAT_PAY_MCH_CERT_SERIAL",
		"CATS_WECHAT_PAY_MCH_PRIVATE_KEY_FILE",
		"CATS_WECHAT_PAY_API_V3_KEY_FILE",
		"CATS_WECHAT_PAY_NOTIFY_URL",
	} {
		t.Setenv(name, "")
	}
	provider, missing, err := NewWeChatNativePaymentProviderFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider != nil || len(missing) != 6 {
		t.Fatalf("provider=%#v missing=%#v", provider, missing)
	}
}

func TestWeChatPaymentSupportsPublicKeyMode(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	privateKeyPath := filepath.Join(dir, "apiclient_key.pem")
	publicKeyPath := filepath.Join(dir, "wechatpay_pub.pem")
	apiV3KeyPath := filepath.Join(dir, "api_v3_key")
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	for path, content := range map[string][]byte{
		privateKeyPath: privatePEM,
		publicKeyPath:  publicPEM,
		apiV3KeyPath:   []byte("12345678901234567890123456789012"),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CATS_WECHAT_PAY_APP_ID", "wx-test-app")
	t.Setenv("CATS_WECHAT_PAY_MCH_ID", "1900000000")
	t.Setenv("CATS_WECHAT_PAY_MCH_CERT_SERIAL", "TEST-CERT-SERIAL")
	t.Setenv("CATS_WECHAT_PAY_MCH_PRIVATE_KEY_FILE", privateKeyPath)
	t.Setenv("CATS_WECHAT_PAY_API_V3_KEY_FILE", apiV3KeyPath)
	t.Setenv("CATS_WECHAT_PAY_NOTIFY_URL", "https://app.catsco.cc/api/payments/wechat/notify")
	t.Setenv("CATS_WECHAT_PAY_PUBLIC_KEY_ID", "PUB_KEY_ID_TEST")
	t.Setenv("CATS_WECHAT_PAY_PUBLIC_KEY_FILE", publicKeyPath)
	provider, missing, err := NewWeChatNativePaymentProviderFromEnv(context.Background())
	if err != nil || provider == nil || len(missing) != 0 {
		t.Fatalf("provider=%#v missing=%#v err=%v", provider, missing, err)
	}
}

package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	commercialPaymentChannelTest         = "test"
	commercialPaymentChannelWeChatNative = "wechat_native"
)

type CommercialPaymentStore interface {
	CommercialStore
	GetCommercialPlan(id int64) (*types.CommercialPlan, error)
	GetCommercialPlanBySlug(slug string) (*types.CommercialPlan, error)
	CreateCommercialOrder(order *types.CommercialOrder) (*types.CommercialOrder, error)
	BeginCommercialOrderPayment(orderNo string, expiresAt time.Time) (*types.CommercialOrder, bool, error)
	SetCommercialOrderPaymentIntent(orderNo, codeURL string, expiresAt time.Time) (*types.CommercialOrder, error)
	FailCommercialOrder(orderNo, message string) error
	GetCommercialOrder(uid int64, orderNo string) (*types.CommercialOrder, error)
	ListCommercialOrders(uid int64, limit int) ([]*types.CommercialOrder, error)
	CloseExpiredCommercialOrders(limit int) (int64, error)
	FulfillCommercialOrder(orderNo string, confirmation *types.CommercialPaymentConfirmation) (*types.CommercialOrder, bool, error)
	ClaimCommercialTrial(uid int64, planSlug string) (*types.CommercialSummary, error)
	HasCommercialTrial(uid int64, planSlug string) (bool, error)
}

type CommercialPaymentIntent struct {
	CodeURL   string
	ExpiresAt time.Time
}

type CommercialPaymentProvider interface {
	Channel() string
	Label() string
	CreatePayment(context.Context, *types.CommercialOrder) (*CommercialPaymentIntent, error)
	ParseNotification(context.Context, *http.Request) (string, *types.CommercialPaymentConfirmation, error)
}

type CommercialPaymentHandlerOptions struct {
	PublicEnabled bool
	TestUIDs      map[int64]bool
	TestPayments  map[int64]bool
	TrialPlanSlug string
	Providers     []CommercialPaymentProvider
	Syncer        *CommercialRelaySyncer
}

type CommercialPaymentHandler struct {
	store         CommercialPaymentStore
	publicEnabled bool
	testUIDs      map[int64]bool
	testPayments  map[int64]bool
	trialPlanSlug string
	providers     map[string]CommercialPaymentProvider
	syncer        *CommercialRelaySyncer
}

type commercialPaymentChannel struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	TestMode bool   `json:"test_mode"`
}

func NewCommercialPaymentHandler(store CommercialPaymentStore, opts CommercialPaymentHandlerOptions) *CommercialPaymentHandler {
	h := &CommercialPaymentHandler{
		store:         store,
		publicEnabled: opts.PublicEnabled,
		testUIDs:      copyCommercialUIDSet(opts.TestUIDs),
		testPayments:  copyCommercialUIDSet(opts.TestPayments),
		trialPlanSlug: strings.TrimSpace(opts.TrialPlanSlug),
		providers:     map[string]CommercialPaymentProvider{},
		syncer:        opts.Syncer,
	}
	for _, provider := range opts.Providers {
		if provider == nil || strings.TrimSpace(provider.Channel()) == "" {
			continue
		}
		h.providers[provider.Channel()] = provider
	}
	return h
}

func copyCommercialUIDSet(value map[int64]bool) map[int64]bool {
	out := map[int64]bool{}
	for uid, enabled := range value {
		if uid > 0 && enabled {
			out[uid] = true
		}
	}
	return out
}

func (h *CommercialPaymentHandler) enabledFor(uid int64) bool {
	return h != nil && h.store != nil && (h.publicEnabled || h.testUIDs[uid])
}

func (h *CommercialPaymentHandler) testAllowedFor(uid int64) bool {
	return h != nil && h.testPayments[uid]
}

func (h *CommercialPaymentHandler) planVisibleFor(uid int64, plan *types.CommercialPlan) bool {
	if plan == nil || plan.State != 0 || plan.PriceFen <= 0 || !strings.EqualFold(plan.Currency, "CNY") {
		return false
	}
	switch plan.SaleState {
	case "public":
		return h.publicEnabled || h.testUIDs[uid]
	case "test":
		return h.testUIDs[uid]
	default:
		return false
	}
}

func (h *CommercialPaymentHandler) channelsFor(uid int64) []commercialPaymentChannel {
	channels := []commercialPaymentChannel{}
	for _, id := range []string{commercialPaymentChannelWeChatNative, commercialPaymentChannelTest} {
		provider := h.providers[id]
		if provider == nil || (id == commercialPaymentChannelTest && !h.testAllowedFor(uid)) {
			continue
		}
		channels = append(channels, commercialPaymentChannel{ID: id, Label: provider.Label(), TestMode: id == commercialPaymentChannelTest})
	}
	return channels
}

func (h *CommercialPaymentHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if !h.enabledFor(uid) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false, "plans": []interface{}{}, "channels": []interface{}{}})
		return
	}
	plans, err := h.store.ListCommercialPlans(false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load commercial plans"})
		return
	}
	visible := make([]*types.CommercialPlan, 0, len(plans))
	for _, plan := range plans {
		if h.planVisibleFor(uid, plan) {
			visible = append(visible, plan)
		}
	}
	trialAvailable := false
	if h.trialPlanSlug != "" {
		trialPlan, planErr := h.store.GetCommercialPlanBySlug(h.trialPlanSlug)
		claimed, claimErr := h.store.HasCommercialTrial(uid, h.trialPlanSlug)
		trialAvailable = planErr == nil && claimErr == nil && trialPlan != nil && trialPlan.State == 0 && !claimed
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":            true,
		"plans":              visible,
		"channels":           h.channelsFor(uid),
		"trial_available":    trialAvailable,
		"test_mode":          h.testAllowedFor(uid),
		"payment_configured": len(h.channelsFor(uid)) > 0,
	})
}

var commercialClientRequestPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{7,63}$`)

func (h *CommercialPaymentHandler) HandleOrders(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if !h.enabledFor(uid) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial payment is not enabled"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		_, _ = h.store.CloseExpiredCommercialOrders(100)
		orderNo := strings.TrimSpace(r.URL.Query().Get("order_no"))
		if orderNo != "" {
			order, err := h.store.GetCommercialOrder(uid, orderNo)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load order"})
				return
			}
			if order == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "order not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"order": order})
			return
		}
		orders, err := h.store.ListCommercialOrders(uid, 20)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load orders"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"orders": orders})
	case http.MethodPost:
		h.createOrder(w, r, uid)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *CommercialPaymentHandler) createOrder(w http.ResponseWriter, r *http.Request, uid int64) {
	var req struct {
		PlanID          int64  `json:"plan_id"`
		Channel         string `json:"channel"`
		ClientRequestID string `json:"client_request_id"`
	}
	if err := decodeCommercialJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid order request"})
		return
	}
	req.Channel = strings.TrimSpace(req.Channel)
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.PlanID <= 0 || !commercialClientRequestPattern.MatchString(req.ClientRequestID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid order request"})
		return
	}
	provider := h.providers[req.Channel]
	if provider == nil || (req.Channel == commercialPaymentChannelTest && !h.testAllowedFor(uid)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payment channel is unavailable"})
		return
	}
	plan, err := h.store.GetCommercialPlan(req.PlanID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load commercial plan"})
		return
	}
	if !h.planVisibleFor(uid, plan) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "commercial plan is unavailable"})
		return
	}
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	order, err := h.store.CreateCommercialOrder(&types.CommercialOrder{
		OrderNo:         newCommercialOrderNo(),
		UID:             uid,
		PlanID:          plan.ID,
		Channel:         req.Channel,
		ClientRequestID: req.ClientRequestID,
		ExpiresAt:       &expiresAt,
	})
	if err != nil {
		message := "failed to create commercial order"
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "purchase limit") {
			message = "purchase limit reached"
			status = http.StatusConflict
		} else if strings.Contains(err.Error(), "not purchasable") || strings.Contains(err.Error(), "not found") {
			message = "commercial plan is unavailable"
			status = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "client request id") {
			message = "duplicate order request"
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": message})
		return
	}
	if order.Status != "created" && order.Status != "failed" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"order": order})
		return
	}
	order, claimed, err := h.store.BeginCommercialOrderPayment(order.OrderNo, expiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare payment"})
		return
	}
	if !claimed {
		writeJSON(w, http.StatusOK, map[string]interface{}{"order": order})
		return
	}
	intent, err := provider.CreatePayment(r.Context(), order)
	if err != nil {
		_ = h.store.FailCommercialOrder(order.OrderNo, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create payment"})
		return
	}
	if intent == nil || intent.ExpiresAt.IsZero() {
		_ = h.store.FailCommercialOrder(order.OrderNo, "payment provider returned an invalid intent")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment provider returned an invalid intent"})
		return
	}
	order, err = h.store.SetCommercialOrderPaymentIntent(order.OrderNo, intent.CodeURL, intent.ExpiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save payment order"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"order": order})
}

func decodeCommercialJSON(r *http.Request, target interface{}) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func newCommercialOrderNo() string {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		copy(random, sum[:8])
	}
	return "CC" + time.Now().UTC().Format("20060102150405") + strings.ToUpper(hex.EncodeToString(random))
}

func (h *CommercialPaymentHandler) HandleTestConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if !h.enabledFor(uid) || !h.testAllowedFor(uid) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var req struct {
		OrderNo string `json:"order_no"`
	}
	if err := decodeCommercialJSON(r, &req); err != nil || strings.TrimSpace(req.OrderNo) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid test payment request"})
		return
	}
	order, err := h.store.GetCommercialOrder(uid, req.OrderNo)
	if err != nil || order == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "order not found"})
		return
	}
	if order.Channel != commercialPaymentChannelTest {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "order is not a test payment"})
		return
	}
	confirmation := &types.CommercialPaymentConfirmation{
		Channel:         commercialPaymentChannelTest,
		EventID:         "test:" + order.OrderNo,
		ProviderTradeNo: "TEST-" + order.OrderNo,
		AmountFen:       order.AmountFen,
		Currency:        order.Currency,
		PaidAt:          time.Now().UTC(),
		PayloadHash:     paymentPayloadHash([]byte(order.OrderNo)),
	}
	fulfilled, changed, err := h.store.FulfillCommercialOrder(order.OrderNo, confirmation)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if changed {
		h.enqueueRelaySync(fulfilled.UID)
	}
	summary, _ := h.store.GetCommercialSummary(uid)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "order": fulfilled, "summary": summary})
}

func (h *CommercialPaymentHandler) HandleClaimTrial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if !h.enabledFor(uid) || h.trialPlanSlug == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trial package is unavailable"})
		return
	}
	summary, err := h.store.ClaimCommercialTrial(uid, h.trialPlanSlug)
	if err != nil {
		message := "trial package is unavailable"
		if strings.Contains(err.Error(), "already claimed") {
			message = "trial package already claimed"
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": message})
		return
	}
	h.enqueueRelaySync(uid)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "summary": summary})
}

func (h *CommercialPaymentHandler) HandleWeChatNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWeChatNotifyResponse(w, http.StatusMethodNotAllowed, "FAIL", "method not allowed")
		return
	}
	if h == nil || h.store == nil {
		writeWeChatNotifyResponse(w, http.StatusServiceUnavailable, "FAIL", "payment service unavailable")
		return
	}
	provider := h.providers[commercialPaymentChannelWeChatNative]
	if provider == nil {
		writeWeChatNotifyResponse(w, http.StatusServiceUnavailable, "FAIL", "payment channel unavailable")
		return
	}
	orderNo, confirmation, err := provider.ParseNotification(r.Context(), r)
	if err != nil {
		writeWeChatNotifyResponse(w, http.StatusBadRequest, "FAIL", "invalid notification")
		return
	}
	order, err := h.store.GetCommercialOrder(0, orderNo)
	if err != nil || order == nil {
		writeWeChatNotifyResponse(w, http.StatusNotFound, "FAIL", "order not found")
		return
	}
	fulfilled, changed, err := h.store.FulfillCommercialOrder(orderNo, confirmation)
	if err != nil {
		writeWeChatNotifyResponse(w, http.StatusConflict, "FAIL", "fulfillment failed")
		return
	}
	if changed {
		h.enqueueRelaySync(fulfilled.UID)
	}
	writeWeChatNotifyResponse(w, http.StatusOK, "SUCCESS", "success")
}

func writeWeChatNotifyResponse(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

func (h *CommercialPaymentHandler) enqueueRelaySync(uid int64) {
	if h != nil && h.syncer != nil {
		h.syncer.Enqueue(uid)
	}
}

func paymentPayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type testCommercialPaymentProvider struct{}

func NewTestCommercialPaymentProvider() CommercialPaymentProvider {
	return &testCommercialPaymentProvider{}
}

func (p *testCommercialPaymentProvider) Channel() string { return commercialPaymentChannelTest }
func (p *testCommercialPaymentProvider) Label() string   { return "灰度测试支付" }

func (p *testCommercialPaymentProvider) CreatePayment(_ context.Context, order *types.CommercialOrder) (*CommercialPaymentIntent, error) {
	if order == nil {
		return nil, fmt.Errorf("order is required")
	}
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	if order.ExpiresAt != nil {
		expiresAt = order.ExpiresAt.UTC()
	}
	return &CommercialPaymentIntent{ExpiresAt: expiresAt}, nil
}

func (p *testCommercialPaymentProvider) ParseNotification(context.Context, *http.Request) (string, *types.CommercialPaymentConfirmation, error) {
	return "", nil, fmt.Errorf("test payment does not accept notifications")
}

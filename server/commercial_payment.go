package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	commercialPaymentChannelTest       = "test"
	commercialPaymentChannelAlipayPage = "alipay_page"
)

var (
	errCommercialRefundInvalid     = errors.New("invalid commercial refund request")
	errCommercialRefundNotFound    = errors.New("commercial order not found")
	errCommercialRefundConflict    = errors.New("commercial refund conflict")
	errCommercialRefundUnavailable = errors.New("commercial refund unavailable")
)

type CommercialPaymentStore interface {
	CommercialStore
	GetCommercialPlan(id int64) (*types.CommercialPlan, error)
	GetCommercialPlanBySlug(slug string) (*types.CommercialPlan, error)
	CreateCommercialOrder(order *types.CommercialOrder) (*types.CommercialOrder, error)
	BeginCommercialOrderPayment(orderNo string, expiresAt time.Time) (*types.CommercialOrder, bool, error)
	SetCommercialOrderPaymentIntent(orderNo, checkoutURL string, expiresAt time.Time) (*types.CommercialOrder, error)
	FailCommercialOrder(orderNo, message string) error
	GetCommercialOrder(uid int64, orderNo string) (*types.CommercialOrder, error)
	ListCommercialOrders(uid int64, limit int) ([]*types.CommercialOrder, error)
	CancelCommercialOrder(uid int64, orderNo, reason string) (*types.CommercialOrder, bool, error)
	CloseExpiredCommercialOrders(limit int) (int64, error)
	FulfillCommercialOrder(orderNo string, confirmation *types.CommercialPaymentConfirmation) (*types.CommercialOrder, bool, error)
	ClaimCommercialTrial(uid int64, planSlug string) (*types.CommercialSummary, error)
	HasCommercialTrial(uid int64, planSlug string) (bool, error)
}

type CommercialRefundStore interface {
	CommercialPaymentStore
	BeginCommercialOrderRefund(orderNo, refundRequestNo string, staleAfter time.Duration) (*types.CommercialOrder, bool, error)
	FailCommercialOrderRefund(orderNo, refundRequestNo, message string) error
	CompleteCommercialOrderRefund(orderNo string, confirmation *types.CommercialRefundConfirmation) (*types.CommercialOrder, bool, error)
}

type CommercialPaymentIntent struct {
	CheckoutURL string
	ExpiresAt   time.Time
}

type CommercialPaymentProvider interface {
	Channel() string
	Label() string
	CreatePayment(context.Context, *types.CommercialOrder) (*CommercialPaymentIntent, error)
	ParseNotification(context.Context, *http.Request) (string, *types.CommercialPaymentConfirmation, error)
}

type CommercialPaymentQuerier interface {
	QueryPayment(context.Context, *types.CommercialOrder) (*types.CommercialPaymentConfirmation, bool, error)
}

type CommercialPaymentCloser interface {
	ClosePayment(context.Context, *types.CommercialOrder) error
}

type CommercialPaymentRefunder interface {
	RefundPayment(context.Context, *types.CommercialOrder, string, string) (*types.CommercialRefundConfirmation, error)
}

type CommercialPaymentHandlerOptions struct {
	PublicEnabled bool
	TestUIDs      map[int64]bool
	TestPayments  map[int64]bool
	TrialPlanSlug string
	Providers     []CommercialPaymentProvider
	SaleChannels  map[string]bool
	Syncer        *CommercialRelaySyncer
}

type CommercialPaymentHandler struct {
	store         CommercialPaymentStore
	publicEnabled bool
	testUIDs      map[int64]bool
	testPayments  map[int64]bool
	trialPlanSlug string
	providers     map[string]CommercialPaymentProvider
	saleChannels  map[string]bool
	syncer        *CommercialRelaySyncer
	queryMu       sync.Mutex
	nextQueries   map[string]time.Time
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
		saleChannels:  map[string]bool{},
		syncer:        opts.Syncer,
		nextQueries:   map[string]time.Time{},
	}
	for _, provider := range opts.Providers {
		if provider == nil || strings.TrimSpace(provider.Channel()) == "" {
			continue
		}
		h.providers[provider.Channel()] = provider
		if opts.SaleChannels == nil || opts.SaleChannels[provider.Channel()] {
			h.saleChannels[provider.Channel()] = true
		}
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

func (h *CommercialPaymentHandler) saleChannelAvailable(uid int64, channel string) bool {
	if h == nil || h.providers[channel] == nil || !h.saleChannels[channel] {
		return false
	}
	if channel == commercialPaymentChannelTest {
		return h.testAllowedFor(uid)
	}
	return h.syncer != nil && h.syncer.EnforcedFor(uid)
}

func (h *CommercialPaymentHandler) planVisibleFor(uid int64, plan *types.CommercialPlan) bool {
	if plan == nil || plan.State != 0 || plan.PriceFen <= 0 || plan.DurationDays <= 0 ||
		!strings.EqualFold(plan.Currency, "CNY") || plan.MonthlyBudget > 0 || !commercialModelBudgetsConfigured(plan.ModelBudgets) {
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

func commercialPlanHasBenefits(plan *types.CommercialPlan) bool {
	if plan == nil {
		return false
	}
	if plan.MonthlyBudget > 0 {
		return true
	}
	for _, amount := range plan.ModelBudgets {
		if amount > 0 {
			return true
		}
	}
	return false
}

func commercialTrialPlanAvailable(plan *types.CommercialPlan) bool {
	if plan == nil || plan.State != 0 || plan.PriceFen != 0 || plan.SaleState != "hidden" || plan.DurationDays <= 0 || plan.MonthlyBudget > 0 {
		return false
	}
	return commercialModelBudgetsConfigured(plan.ModelBudgets)
}

func commercialOrderForUser(order *types.CommercialOrder) *types.CommercialOrder {
	if order == nil {
		return nil
	}
	copy := *order
	copy.PlanMonthlyBudget = 0
	copy.PlanModelBudgets = nil
	copy.RefundRequestNo = ""
	return &copy
}

func commercialOrdersForUser(orders []*types.CommercialOrder) []*types.CommercialOrder {
	out := make([]*types.CommercialOrder, 0, len(orders))
	for _, order := range orders {
		if sanitized := commercialOrderForUser(order); sanitized != nil {
			out = append(out, sanitized)
		}
	}
	return out
}

func (h *CommercialPaymentHandler) channelsFor(uid int64) []commercialPaymentChannel {
	channels := []commercialPaymentChannel{}
	for _, id := range []string{commercialPaymentChannelAlipayPage, commercialPaymentChannelTest} {
		provider := h.providers[id]
		if provider == nil || !h.saleChannelAvailable(uid, id) {
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
			visible = append(visible, commercialPlanForUser(plan))
		}
	}
	trialAvailable := false
	if h.trialPlanSlug != "" {
		trialPlan, planErr := h.store.GetCommercialPlanBySlug(h.trialPlanSlug)
		claimed, claimErr := h.store.HasCommercialTrial(uid, h.trialPlanSlug)
		trialAvailable = planErr == nil && claimErr == nil && commercialTrialPlanAvailable(trialPlan) && !claimed
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
	switch r.Method {
	case http.MethodGet:
		if h == nil || h.store == nil || uid <= 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial payment is not available"})
			return
		}
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
			order = h.refreshPendingCommercialOrder(r.Context(), order)
			if order.Status == "pending" && order.ExpiresAt != nil && !order.ExpiresAt.After(time.Now().UTC()) {
				_, _ = h.store.CloseExpiredCommercialOrders(100)
				if closed, loadErr := h.store.GetCommercialOrder(uid, orderNo); loadErr == nil && closed != nil {
					order = closed
				}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"order": commercialOrderForUser(order)})
			return
		}
		_, _ = h.store.CloseExpiredCommercialOrders(100)
		orders, err := h.store.ListCommercialOrders(uid, 20)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load orders"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"orders": commercialOrdersForUser(orders)})
	case http.MethodPost:
		if !h.enabledFor(uid) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial payment is not enabled"})
			return
		}
		h.createOrder(w, r, uid)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *CommercialPaymentHandler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if h == nil || h.store == nil || uid <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial payment is not available"})
		return
	}
	var req struct {
		OrderNo string `json:"order_no"`
	}
	if err := decodeCommercialJSON(r, &req); err != nil || strings.TrimSpace(req.OrderNo) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid cancel request"})
		return
	}
	req.OrderNo = strings.TrimSpace(req.OrderNo)
	order, err := h.store.GetCommercialOrder(uid, req.OrderNo)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load order"})
		return
	}
	if order == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "order not found"})
		return
	}
	if order.Status == "closed" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "order": commercialOrderForUser(order)})
		return
	}
	if order.Status != "created" && order.Status != "pending" && order.Status != "failed" {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "order can no longer be cancelled",
			"order": commercialOrderForUser(order),
		})
		return
	}

	provider := h.providers[order.Channel]
	if order.Status == "pending" {
		if fulfilled := h.fulfillCommercialOrderIfPaid(r.Context(), order); fulfilled != nil {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error": "payment has already completed",
				"order": commercialOrderForUser(fulfilled),
			})
			return
		}
		closer, ok := provider.(CommercialPaymentCloser)
		if !ok {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment channel cannot close this order"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err = closer.ClosePayment(ctx, order)
		cancel()
		if err != nil {
			if fulfilled := h.fulfillCommercialOrderIfPaid(r.Context(), order); fulfilled != nil {
				writeJSON(w, http.StatusConflict, map[string]interface{}{
					"error": "payment has already completed",
					"order": commercialOrderForUser(fulfilled),
				})
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to close payment order"})
			return
		}
	}

	closed, changed, err := h.store.CancelCommercialOrder(uid, order.OrderNo, "cancelled by user")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to cancel order"})
		return
	}
	if closed == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "order not found"})
		return
	}
	if !changed && closed.Status != "closed" {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "order status changed while cancelling",
			"order": commercialOrderForUser(closed),
		})
		return
	}
	h.clearCommercialPaymentQuery(order.OrderNo)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "order": commercialOrderForUser(closed)})
}

func (h *CommercialPaymentHandler) fulfillCommercialOrderIfPaid(ctx context.Context, order *types.CommercialOrder) *types.CommercialOrder {
	if h == nil || order == nil {
		return nil
	}
	querier, ok := h.providers[order.Channel].(CommercialPaymentQuerier)
	if !ok {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	confirmation, paid, err := querier.QueryPayment(queryCtx, order)
	cancel()
	if err != nil || !paid || confirmation == nil {
		return nil
	}
	fulfilled, changed, err := h.store.FulfillCommercialOrder(order.OrderNo, confirmation)
	if err != nil || fulfilled == nil {
		return nil
	}
	h.clearCommercialPaymentQuery(order.OrderNo)
	if changed {
		h.enqueueRelaySync(fulfilled.UID)
	}
	return fulfilled
}

// RefundOrder performs a provider-side full refund and then atomically revokes
// the order entitlement and its quota grants. A deterministic provider request
// number makes a retry safe if the provider succeeds before the local commit.
func (h *CommercialPaymentHandler) RefundOrder(ctx context.Context, orderNo, reason string) (*types.CommercialOrder, bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	reason = strings.TrimSpace(reason)
	if h == nil || orderNo == "" {
		return nil, false, errCommercialRefundInvalid
	}
	store, ok := h.store.(CommercialRefundStore)
	if !ok {
		return nil, false, errCommercialRefundUnavailable
	}
	order, err := store.GetCommercialOrder(0, orderNo)
	if err != nil {
		return nil, false, fmt.Errorf("load commercial refund order: %w", err)
	}
	if order == nil {
		return nil, false, errCommercialRefundNotFound
	}
	if order.Status == "refunded" {
		return order, false, nil
	}
	if order.Status != "fulfilled" && order.Status != "refunding" {
		return order, false, fmt.Errorf("%w: order status is %s", errCommercialRefundConflict, order.Status)
	}
	provider := h.providers[order.Channel]
	refunder, ok := provider.(CommercialPaymentRefunder)
	if !ok {
		return order, false, fmt.Errorf("%w: payment channel does not support refunds", errCommercialRefundUnavailable)
	}
	refundRequestNo := commercialRefundRequestNo(order.OrderNo)
	claimedOrder, claimed, err := store.BeginCommercialOrderRefund(order.OrderNo, refundRequestNo, 2*time.Minute)
	if err != nil {
		return order, false, fmt.Errorf("begin commercial refund: %w", err)
	}
	if claimedOrder != nil {
		order = claimedOrder
	}
	if !claimed {
		if order.Status == "refunded" {
			return order, false, nil
		}
		return order, false, fmt.Errorf("%w: refund is already in progress", errCommercialRefundConflict)
	}
	confirmation, err := refunder.RefundPayment(ctx, order, refundRequestNo, reason)
	if err != nil {
		_ = store.FailCommercialOrderRefund(order.OrderNo, refundRequestNo, err.Error())
		return order, false, fmt.Errorf("refund payment: %w", err)
	}
	refunded, changed, err := store.CompleteCommercialOrderRefund(order.OrderNo, confirmation)
	if err != nil {
		_ = store.FailCommercialOrderRefund(order.OrderNo, refundRequestNo, err.Error())
		return order, false, fmt.Errorf("complete commercial refund: %w", err)
	}
	if changed && refunded != nil {
		h.enqueueRelaySync(refunded.UID)
	}
	return refunded, changed, nil
}

func commercialRefundRequestNo(orderNo string) string {
	return "CCRF-" + strings.TrimSpace(orderNo)
}

func (h *CommercialPaymentHandler) refreshPendingCommercialOrder(ctx context.Context, order *types.CommercialOrder) *types.CommercialOrder {
	if h == nil || order == nil || (order.Status != "created" && order.Status != "pending" && order.Status != "closed") {
		return order
	}
	if order.Status == "closed" {
		closedAt := order.ClosedAt
		if closedAt == nil || closedAt.Before(time.Now().UTC().Add(-7*24*time.Hour)) {
			return order
		}
	}
	provider := h.providers[order.Channel]
	querier, ok := provider.(CommercialPaymentQuerier)
	if order.Status != "created" && ok && h.claimCommercialPaymentQuery(order.OrderNo, time.Now().UTC()) {
		confirmation, paid, err := querier.QueryPayment(ctx, order)
		if err == nil && paid && confirmation != nil {
			fulfilled, changed, fulfillErr := h.store.FulfillCommercialOrder(order.OrderNo, confirmation)
			if fulfillErr == nil && fulfilled != nil {
				h.clearCommercialPaymentQuery(order.OrderNo)
				if changed {
					h.enqueueRelaySync(fulfilled.UID)
				}
				return fulfilled
			}
		}
	}
	if (order.Status != "created" && order.Status != "pending") || strings.TrimSpace(order.CheckoutURL) != "" || provider == nil {
		return order
	}
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	claimedOrder, claimed, err := h.store.BeginCommercialOrderPayment(order.OrderNo, expiresAt)
	if err != nil || !claimed {
		return order
	}
	intent, err := provider.CreatePayment(ctx, claimedOrder)
	if err != nil || intent == nil || intent.ExpiresAt.IsZero() {
		if err == nil {
			err = fmt.Errorf("payment provider returned an invalid intent")
		}
		_ = h.store.FailCommercialOrder(order.OrderNo, err.Error())
		return order
	}
	recovered, err := h.store.SetCommercialOrderPaymentIntent(order.OrderNo, intent.CheckoutURL, intent.ExpiresAt)
	if err != nil || recovered == nil {
		if err != nil {
			_ = h.store.FailCommercialOrder(order.OrderNo, err.Error())
		}
		return order
	}
	return recovered
}

func (h *CommercialPaymentHandler) claimCommercialPaymentQuery(orderNo string, now time.Time) bool {
	orderNo = strings.TrimSpace(orderNo)
	if h == nil || orderNo == "" {
		return false
	}
	h.queryMu.Lock()
	defer h.queryMu.Unlock()
	if next := h.nextQueries[orderNo]; next.After(now) {
		return false
	}
	if len(h.nextQueries) >= 1024 {
		staleBefore := now.Add(-30 * time.Minute)
		for candidate, next := range h.nextQueries {
			if next.Before(staleBefore) {
				delete(h.nextQueries, candidate)
			}
		}
	}
	h.nextQueries[orderNo] = now.Add(10 * time.Second)
	return true
}

func (h *CommercialPaymentHandler) clearCommercialPaymentQuery(orderNo string) {
	if h == nil {
		return
	}
	h.queryMu.Lock()
	delete(h.nextQueries, strings.TrimSpace(orderNo))
	h.queryMu.Unlock()
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
	if provider == nil || !h.saleChannelAvailable(uid, req.Channel) {
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
	if req.Channel != commercialPaymentChannelTest {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err = h.syncer.ValidatePurchase(ctx, uid, plan)
		cancel()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "套餐额度暂时无法同步，请稍后重试"})
			return
		}
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
	if order.Status != "created" && order.Status != "failed" && !(order.Status == "pending" && strings.TrimSpace(order.CheckoutURL) == "") {
		writeJSON(w, http.StatusOK, map[string]interface{}{"order": commercialOrderForUser(order)})
		return
	}
	order, claimed, err := h.store.BeginCommercialOrderPayment(order.OrderNo, expiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare payment"})
		return
	}
	if !claimed {
		writeJSON(w, http.StatusOK, map[string]interface{}{"order": commercialOrderForUser(order)})
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
	order, err = h.store.SetCommercialOrderPaymentIntent(order.OrderNo, intent.CheckoutURL, intent.ExpiresAt)
	if err != nil {
		_ = h.store.FailCommercialOrder(order.OrderNo, err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save payment order"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"order": commercialOrderForUser(order)})
}

func decodeCommercialJSON(r *http.Request, target interface{}) error {
	payload, err := readLimitedBody(r.Body, 64<<10)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body contains multiple JSON values")
		}
		return err
	}
	return nil
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "order": commercialOrderForUser(fulfilled), "summary": commercialUsageSummaryForUser(summary)})
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
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "summary": commercialUsageSummaryForUser(summary)})
}

func (h *CommercialPaymentHandler) HandleAlipayNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAlipayNotifyResponse(w, http.StatusMethodNotAllowed, false)
		return
	}
	if h == nil || h.store == nil {
		writeAlipayNotifyResponse(w, http.StatusServiceUnavailable, false)
		return
	}
	provider := h.providers[commercialPaymentChannelAlipayPage]
	if provider == nil {
		writeAlipayNotifyResponse(w, http.StatusServiceUnavailable, false)
		return
	}
	orderNo, confirmation, err := provider.ParseNotification(r.Context(), r)
	if err != nil {
		writeAlipayNotifyResponse(w, http.StatusBadRequest, false)
		return
	}
	order, err := h.store.GetCommercialOrder(0, orderNo)
	if err != nil {
		writeAlipayNotifyResponse(w, http.StatusServiceUnavailable, false)
		return
	}
	if order == nil {
		writeAlipayNotifyResponse(w, http.StatusNotFound, false)
		return
	}
	fulfilled, changed, err := h.store.FulfillCommercialOrder(orderNo, confirmation)
	if err != nil {
		writeAlipayNotifyResponse(w, http.StatusConflict, false)
		return
	}
	if changed {
		h.enqueueRelaySync(fulfilled.UID)
	}
	writeAlipayNotifyResponse(w, http.StatusOK, true)
}

func writeAlipayNotifyResponse(w http.ResponseWriter, status int, success bool) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if success {
		_, _ = w.Write([]byte("success"))
		return
	}
	_, _ = w.Write([]byte("failure"))
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

func (p *testCommercialPaymentProvider) ClosePayment(context.Context, *types.CommercialOrder) error {
	return nil
}

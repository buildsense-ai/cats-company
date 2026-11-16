package server

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// 独立点数充值（credit top-up）。
//
// 产品合同（写进 API，前端照抄，不允许前端自己推算）：
//   - 1 分 = 1 点 = 1_000_000 micro units；1 元 = 100 点。
//   - 充值不带赠送、不带机器、不带订阅，也不改变用户已有的套餐与租期。
//   - 充值订单走 POST /api/relay/commercial/orders，kind="credit_topup"，
//     金额由 amount_fen 决定；不存在「按金额建商品」的做法。
//
// enabled 由服务端算，前端不许硬开：真实的 alipay_page 售卖通道必须已注册
// （provider 存在且对该用户可售），并且 relay 侧 credit 结算通道已配置。
// 少任何一个，充值会在「付了钱却花不出去」的状态下卖掉，所以宁可先不卖。

const (
	// commercialTopupCreditsPerCNY 是产品价格锚：1 元 = 100 点。
	commercialTopupCreditsPerCNY int64 = 100
	// commercialTopupUnitsPerFen 是账本换算率：1 分 = 1 点 = 1000000 micro units。
	commercialTopupUnitsPerFen int64 = 1_000_000
	// commercialTopupDefaultMinFen / MaxFen 是默认金额区间（1 元 ~ 10000 元）。
	commercialTopupDefaultMinFen int64 = 100
	commercialTopupDefaultMaxFen int64 = 1_000_000
)

// commercialTopupDefaultPresetsFen 是默认档位（10/30/50/100/300/500 元）。
var commercialTopupDefaultPresetsFen = []int64{1000, 3000, 5000, 10000, 30000, 50000}

// CommercialTopupStore is the order-side capability. It stays optional so a
// focused test store keeps compiling; the catalog then reports enabled=false
// instead of advertising a product the deployment cannot sell.
type CommercialTopupStore interface {
	CreateCommercialTopupOrder(uid, amountFen int64, channel, clientRequestID string, expiresAt *time.Time) (*types.CommercialOrder, error)
}

type commercialTopupConfig struct {
	// Killed is the explicit ops kill switch. It defaults to off (product on):
	// the two real gates below — a live Alipay page channel for this buyer and a
	// configured credit settlement channel — are what actually decide.
	Killed        bool
	MinAmountFen  int64
	MaxAmountFen  int64
	CreditsPerCNY int64
	PresetsFen    []int64
}

func commercialTopupConfigFromEnv() commercialTopupConfig {
	cfg := commercialTopupConfig{
		MinAmountFen:  commercialTopupDefaultMinFen,
		MaxAmountFen:  commercialTopupDefaultMaxFen,
		CreditsPerCNY: commercialTopupCreditsPerCNY,
		PresetsFen:    append([]int64(nil), commercialTopupDefaultPresetsFen...),
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_CREDIT_TOPUP_ENABLED"))) {
	case "0", "false", "no", "off":
		cfg.Killed = true
	}
	if value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("CATSCO_CREDIT_TOPUP_MIN_FEN")), 10, 64); err == nil && value > 0 {
		cfg.MinAmountFen = value
	}
	if value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("CATSCO_CREDIT_TOPUP_MAX_FEN")), 10, 64); err == nil && value > 0 {
		cfg.MaxAmountFen = value
	}
	if cfg.MaxAmountFen < cfg.MinAmountFen {
		cfg.MaxAmountFen = cfg.MinAmountFen
	}
	if raw := strings.TrimSpace(os.Getenv("CATSCO_CREDIT_TOPUP_PRESETS_FEN")); raw != "" {
		presets := make([]int64, 0, 8)
		for _, item := range strings.Split(raw, ",") {
			value, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
			if err != nil || value < cfg.MinAmountFen || value > cfg.MaxAmountFen {
				continue
			}
			presets = append(presets, value)
		}
		if len(presets) > 0 {
			cfg.PresetsFen = presets
		}
	}
	return cfg
}

// commercialTopupSaleGatesOpen reports whether the two hard dependencies of a
// sale are actually present: a live Alipay page channel for this buyer, and the
// relay-side credit settlement channel that makes the bought units spendable.
func (h *CommercialPaymentHandler) commercialTopupSaleGatesOpen(uid int64) bool {
	if h == nil || h.store == nil {
		return false
	}
	if _, ok := h.store.(CommercialTopupStore); !ok {
		return false
	}
	if h.providers[commercialPaymentChannelAlipayPage] == nil {
		return false
	}
	if !h.saleChannelAvailable(uid, commercialPaymentChannelAlipayPage) {
		return false
	}
	return commercialCreditSettlementConfigured()
}

// commercialTopupCatalog builds the credit_topup block of the catalog.
//
// It is always present (so a client can render "not available yet" with the
// reason) but enabled only when the deployment can really sell and honour it.
func (h *CommercialPaymentHandler) commercialTopupCatalog(uid int64) map[string]interface{} {
	cfg := commercialTopupConfigFromEnv()
	block := map[string]interface{}{
		"enabled":         false,
		"min_amount_fen":  cfg.MinAmountFen,
		"max_amount_fen":  cfg.MaxAmountFen,
		"credits_per_cny": cfg.CreditsPerCNY,
		"presets_fen":     append([]int64(nil), cfg.PresetsFen...),
	}
	switch {
	case cfg.Killed:
		block["reason"] = "点数充值暂未开放"
	case !h.enabledFor(uid):
		block["reason"] = "商业支付未开启"
	case !h.commercialTopupSaleGatesOpen(uid):
		block["reason"] = "点数充值通道未配置"
	default:
		block["enabled"] = true
	}
	return block
}

// createTopupOrder handles kind="credit_topup" on the ordinary orders route.
//
// The order is created by the store (which owns the amount -> units snapshot and
// the client_request_id idempotency), then handed to the same payment start path
// a package uses, so signing, row locking and replay protection stay in one
// place.
func (h *CommercialPaymentHandler) createTopupOrder(w http.ResponseWriter, r *http.Request, uid int64, channel, clientRequestID string, amountFen int64) {
	if amountFen <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid credit top-up amount"})
		return
	}
	if commercialTopupConfigFromEnv().Killed {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credit top-up is not enabled", "code": "credit_topup_unavailable"})
		return
	}
	if channel != commercialPaymentChannelAlipayPage {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payment channel is unavailable"})
		return
	}
	if !h.commercialTopupSaleGatesOpen(uid) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "credit top-up is not enabled",
			"code":  "credit_topup_unavailable",
		})
		return
	}
	cfg := commercialTopupConfigFromEnv()
	if amountFen < cfg.MinAmountFen || amountFen > cfg.MaxAmountFen {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":          "credit top-up amount is out of range",
			"code":           "credit_topup_amount_out_of_range",
			"min_amount_fen": cfg.MinAmountFen,
			"max_amount_fen": cfg.MaxAmountFen,
		})
		return
	}
	if provider := h.providers[channel]; provider == nil || !h.saleChannelAvailable(uid, channel) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payment channel is unavailable"})
		return
	}
	topupStore, ok := h.store.(CommercialTopupStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "credit top-up is not enabled",
			"code":  "credit_topup_unavailable",
		})
		return
	}
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	order, err := topupStore.CreateCommercialTopupOrder(uid, amountFen, channel, clientRequestID, &expiresAt)
	if err != nil {
		message := "failed to create credit top-up order"
		status := http.StatusInternalServerError
		switch {
		case strings.Contains(err.Error(), "already used by another order"):
			// Same idempotency key, different amount: the retry is not the same
			// purchase, and honouring it would let a 10 CNY order become 100.
			message = "duplicate order request"
			status = http.StatusConflict
		case strings.Contains(err.Error(), "invalid"):
			message = "invalid credit top-up amount"
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": message})
		return
	}
	h.startCommercialOrderPayment(w, r, order, expiresAt)
}

// startCommercialOrderPayment claims the order for payment and asks the provider
// for a checkout intent. It is shared by packages and top-ups so both go through
// exactly the same claim/replay rules.
func (h *CommercialPaymentHandler) startCommercialOrderPayment(w http.ResponseWriter, r *http.Request, order *types.CommercialOrder, expiresAt time.Time) {
	if order == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create commercial order"})
		return
	}
	if order.Status != "created" && order.Status != "failed" && !(order.Status == "pending" && strings.TrimSpace(order.CheckoutURL) == "") {
		writeJSON(w, http.StatusOK, map[string]interface{}{"order": commercialOrderForUser(order)})
		return
	}
	provider := h.providers[order.Channel]
	if provider == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payment channel is unavailable"})
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
	intent, err := provider.CreatePayment(withCommercialPaymentCallbackOrigin(r.Context(), r), order)
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

// commercialTopupUnitsForAmount mirrors the store's fixed rate for callers that
// only need the number (tests and internal credit statements).
func commercialTopupUnitsForAmount(amountFen int64) int64 {
	if amountFen <= 0 || amountFen > int64(^uint64(0)>>1)/commercialTopupUnitsPerFen {
		return 0
	}
	return amountFen * commercialTopupUnitsPerFen
}

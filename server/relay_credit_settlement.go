package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// 平台侧代扣（设计 §4.5 结算闭环）：外部执行后端（http / mcp / worker）自报用量，
// 平台按发布者定价算出金额后，调 relay 的 /internal/users/{uid}/charge 把钱记到
// 调用方的当月用量上。
//
// 为什么账本先记 pending_credit 再单独结算：写账本和扣费跨进程、跨库，不可能放一个
// 事务里。只能靠「账本行是唯一事实来源 + request_id(=run_id) 幂等」把重试做安全：
//   - 扣成功但状态没写上 → 下一轮重复扣，relay 幂等返回 duplicate，结果不变
//   - 扣失败 → 状态仍是 pending_credit，下一轮接着重试
const (
	// relay 侧 note 截断长度，与 handle_internal_charge 的 body 处理一致。
	serviceChargeNoteLimit = 200
	// 单轮结算批量，避免一次扫太久把 sweep 拖住。
	serviceSettlementBatch = 100
	// 结算回看窗口（天）：超过就留给人工对账，不让一条坏账被无限重试。
	serviceSettlementWindowDays = 7
)

type relayChargeRequest struct {
	RequestID string  `json:"request_id"`
	AmountCNY float64 `json:"amount_cny"`
	Note      string  `json:"note,omitempty"`
}

type relayChargeResponse struct {
	OK          bool    `json:"ok"`
	UID         int64   `json:"uid"`
	RequestID   string  `json:"request_id"`
	AmountCNY   float64 `json:"amount_cny"`
	Duplicate   bool    `json:"duplicate"`
	ManualTotal float64 `json:"manual_charge_total_cny"`
}

// ChargeUser 把 amountCNY 记到 uid 的当月用量（幂等）。
// duplicate=true 表示 request_id 之前已经扣过，本次没有新增用量。
func (c *RelayAdminClient) ChargeUser(
	ctx context.Context,
	uid int64,
	requestID string,
	amountCNY float64,
	note string,
) (duplicate bool, err error) {
	if uid <= 0 {
		return false, fmt.Errorf("relay charge: invalid uid %d", uid)
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false, errors.New("relay charge: request_id is required")
	}
	if amountCNY <= 0 {
		return false, fmt.Errorf("relay charge: amount must be positive, got %v", amountCNY)
	}
	if len(note) > serviceChargeNoteLimit {
		note = note[:serviceChargeNoteLimit]
	}
	var out relayChargeResponse
	path := fmt.Sprintf("/internal/users/%d/charge", uid)
	body := relayChargeRequest{RequestID: requestID, AmountCNY: amountCNY, Note: note}
	if err := c.Do(ctx, http.MethodPost, path, body, &out); err != nil {
		return false, err
	}
	// relay 明确回报没记上（既非新增也非重复）→ 当成失败，留给下一轮。
	if !out.OK && !out.Duplicate {
		return false, fmt.Errorf("relay charge: unexpected response for run %s", requestID)
	}
	return out.Duplicate, nil
}

// ServiceCreditCharger 是代扣通道的最小面（便于测试替身）。
type ServiceCreditCharger interface {
	ChargeUser(ctx context.Context, uid int64, requestID string, amountCNY float64, note string) (bool, error)
}

// ServiceLedgerSettlementStore 是账本结算所需的存储面。
type ServiceLedgerSettlementStore interface {
	ListPendingCreditLedger(ctx context.Context, limit int) ([]store.ServiceRunLedgerCharge, error)
	MarkServiceRunLedgerState(ctx context.Context, runID string, state string) (bool, error)
}

// relayAdminPermanentFailure 判断 relay 的拒绝是否「重试也没用」。
// 4xx（除 429 限流）说明请求本身有问题（uid 没建 key、金额非法），
// 再刷也只是刷日志，标记 charge_failed 交给人工对账更实在。
func relayAdminPermanentFailure(err error) bool {
	var adminErr relayAdminError
	if !errors.As(err, &adminErr) {
		return false
	}
	if adminErr.status == http.StatusTooManyRequests {
		return false
	}
	return adminErr.status >= 400 && adminErr.status < 500
}

// SetCharger 注入代扣通道；未注入时账本只记待扣，不做结算。
func (w *ServiceRunLedgerWriter) SetCharger(charger ServiceCreditCharger) {
	if w == nil {
		return
	}
	w.charger = charger
}

// SettlePendingCredit 把账本里 pending_credit 的行逐个扣掉（幂等、可重入）。
// 返回本轮真正结算成功的条数。
func (w *ServiceRunLedgerWriter) SettlePendingCredit(ctx context.Context) (int, error) {
	if w == nil || w.charger == nil {
		return 0, nil
	}
	settler, ok := w.store.(ServiceLedgerSettlementStore)
	if !ok {
		return 0, nil
	}
	entries, err := settler.ListPendingCreditLedger(ctx, serviceSettlementBatch)
	if err != nil {
		return 0, err
	}
	charged := 0
	for _, entry := range entries {
		settled, err := w.settleOne(ctx, settler, entry)
		if err != nil {
			// 留在 pending_credit：下一轮重试；日志里带上金额方便对账。
			log.Printf("service credit settlement: run=%s uid=%d amount_micros=%d state=%s: %v",
				entry.RunID, entry.PayerUID, entry.GrossMicros, settled, err)
		}
		if settled == store.ServiceBillingCharged {
			charged++
		}
	}
	return charged, nil
}

// settleOne 结算单条：调用方付的是 gross（fee 是平台从 gross 里抽给发布方的），
// 所以代扣金额 = gross_micros / 1e6。
func (w *ServiceRunLedgerWriter) settleOne(
	ctx context.Context,
	settler ServiceLedgerSettlementStore,
	entry store.ServiceRunLedgerCharge,
) (string, error) {
	amountCNY := float64(entry.GrossMicros) / 1_000_000.0
	note := "catsco service run " + entry.RunID
	if _, err := w.charger.ChargeUser(ctx, entry.PayerUID, entry.RunID, amountCNY, note); err != nil {
		if !relayAdminPermanentFailure(err) {
			return store.ServiceBillingPendingCredit, err
		}
		// relay 明确拒绝：标成失败态，别再无限重试。
		if _, markErr := settler.MarkServiceRunLedgerState(ctx, entry.RunID, store.ServiceBillingChargeFailed); markErr != nil {
			return store.ServiceBillingPendingCredit, fmt.Errorf("%w (mark failed: %v)", err, markErr)
		}
		return store.ServiceBillingChargeFailed, err
	}
	// relay 侧钱已经扣了（或之前扣过）：即使这里状态没写上，下一轮也只是重复扣 + duplicate，
	// 不会多收钱，所以这一步失败不致命。
	if _, err := settler.MarkServiceRunLedgerState(ctx, entry.RunID, store.ServiceBillingCharged); err != nil {
		return store.ServiceBillingPendingCredit, fmt.Errorf("charged but mark failed: %w", err)
	}
	return store.ServiceBillingCharged, nil
}

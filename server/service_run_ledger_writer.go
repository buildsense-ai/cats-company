package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
)

// run 级账本写入器（设计 §4.5）：**谁调用谁付费、按量给**。
//
// 为什么不埋点：run 有 4 条终态路径（worker complete / awaiting_input / fail / 外部 runner），
// 逐条埋点容易漏。这里像回执投递器一样做**补账扫描**：扫已终态且无账本行的 run，
// 按会话钉定的 definition（含 pricing 快照）算钱后落行；写入按 run_id 幂等。
//
// 扣费闭环：账本行落成 pending_credit 后，同一轮扫描继续调 relay 代扣接口
// （request_id=run_id，幂等）把钱记进调用方当月用量，成功后置 charged。
// 失败（relay 暂不可用）留给下一轮重试；4xx 明确拒绝则置 charge_failed 不再重试。
const (
	serviceLedgerSweepInterval = 30 * time.Second
	serviceLedgerSweepBatch    = 200
	serviceLedgerChargeBatch   = 200
	serviceLedgerSweepTimeout  = 20 * time.Second
)

// ServiceRunLedgerStore 是记账所需的最小存储面（便于测试替身）。
type ServiceRunLedgerStore interface {
	ListBillableServiceRuns(ctx context.Context, limit int) ([]store.ServiceRunBillable, error)
	RecordServiceRunLedger(ctx context.Context, entry store.ServiceRunLedgerEntry) (bool, error)
}

// ServiceRunLedgerChargeStore 是补扣所需的最小存储面（PG/MySQL adapter 实现）。
type ServiceRunLedgerChargeStore interface {
	ListPendingServiceRunLedgerCharges(ctx context.Context, limit int) ([]store.ServiceRunLedgerCharge, error)
	UpdateServiceRunLedgerBillingState(ctx context.Context, runID, fromState, toState string) (bool, error)
}

type ServiceRunLedgerWriter struct {
	store    ServiceRunLedgerStore
	charges  ServiceRunLedgerChargeStore
	charger  ServiceUserCharger
	feeBps   int
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewServiceRunLedgerWriterFromEnv(st ServiceRunLedgerStore) *ServiceRunLedgerWriter {
	w := &ServiceRunLedgerWriter{
		store:  st,
		feeBps: PlatformFeeBps(),
		stopCh: make(chan struct{}),
	}
	if charges, ok := st.(ServiceRunLedgerChargeStore); ok {
		w.charges = charges
	}
	return w
}

// EnableCharges 注入 relay 代扣通道。未注入时只记账不动钱（本地/测试）。
func (w *ServiceRunLedgerWriter) EnableCharges(charger ServiceUserCharger) {
	if w == nil || charger == nil {
		return
	}
	w.charger = charger
}

// SetFeeBps 覆盖平台抽成（测试注入）。
func (w *ServiceRunLedgerWriter) SetFeeBps(bps int) {
	if bps < 0 {
		bps = 0
	}
	if bps > 10000 {
		bps = 10000
	}
	w.feeBps = bps
}

// Start 周期补账（阻塞；挂 goroutine）。
func (w *ServiceRunLedgerWriter) Start(interval time.Duration) {
	if interval <= 0 {
		interval = serviceLedgerSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), serviceLedgerSweepTimeout)
			if n, err := w.SweepOnce(ctx); err != nil {
				log.Printf("service run ledger: %v", err)
			} else if n > 0 {
				log.Printf("service run ledger: recorded %d run(s)", n)
			}
			cancel()
		}
	}
}

func (w *ServiceRunLedgerWriter) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// SweepOnce 补一轮账（测试直接调用）。返回新写入行数。
// 记账之后紧接着补扣本轮（含历史遗留的 pending_credit 行）。
func (w *ServiceRunLedgerWriter) SweepOnce(ctx context.Context) (int, error) {
	runs, err := w.store.ListBillableServiceRuns(ctx, serviceLedgerSweepBatch)
	if err != nil {
		return 0, err
	}
	recorded := 0
	for _, run := range runs {
		entry := w.buildEntry(run)
		inserted, err := w.store.RecordServiceRunLedger(ctx, entry)
		if err != nil {
			log.Printf("service run ledger: record run=%s: %v", run.RunID, err)
			continue
		}
		if inserted {
			recorded++
		}
	}
	if charged, err := w.ChargeSweepOnce(ctx); err != nil {
		log.Printf("service run ledger: charge sweep: %v", err)
	} else if charged > 0 {
		log.Printf("service run ledger: charged %d run(s)", charged)
	}
	return recorded, nil
}

// ChargeSweepOnce 补扣一轮：把 pending_credit 行通过 relay 代扣接口结掉。
// 单行失败只记日志（留在 pending 等下一轮；4xx 明确拒绝转 charge_failed），
// 只有列表查询失败才返回错误。返回本轮成功扣费行数。
func (w *ServiceRunLedgerWriter) ChargeSweepOnce(ctx context.Context) (int, error) {
	if w.charges == nil || w.charger == nil {
		return 0, nil
	}
	rows, err := w.charges.ListPendingServiceRunLedgerCharges(ctx, serviceLedgerChargeBatch)
	if err != nil {
		return 0, err
	}
	charged := 0
	for _, row := range rows {
		// 金额换算成元调 relay（微元整数 → CNY）。
		amountCNY := float64(row.GrossMicros) / 1_000_000.0
		note := fmt.Sprintf("service run %s (payee %d)", row.RunID, row.PayeeUID)
		duplicate, err := w.charger.ChargeUser(ctx, row.PayerUID, row.RunID, amountCNY, note)
		if err != nil {
			if isPermanentChargeError(err) {
				// relay 明确拒绝（4xx）：重试也不会变好，停手等人工处理。
				if _, uerr := w.charges.UpdateServiceRunLedgerBillingState(ctx,
					row.RunID, store.ServiceBillingPendingCredit, store.ServiceBillingChargeFailed); uerr != nil {
					log.Printf("service run ledger: mark charge_failed run=%s: %v", row.RunID, uerr)
				}
				log.Printf("service run ledger: charge rejected run=%s uid=%d amount=%.6f: %v",
					row.RunID, row.PayerUID, amountCNY, err)
				continue
			}
			log.Printf("service run ledger: charge run=%s uid=%d amount=%.6f: %v (will retry)",
				row.RunID, row.PayerUID, amountCNY, err)
			continue
		}
		// 扣费成功（或 relay 已有同 request_id 记录 = 之前扣过）→ 置 charged。
		// 若这一步失败，下一轮会因 request_id 幂等拿到 duplicate，不会重复扣钱。
		if _, err := w.charges.UpdateServiceRunLedgerBillingState(ctx,
			row.RunID, store.ServiceBillingPendingCredit, store.ServiceBillingCharged); err != nil {
			log.Printf("service run ledger: mark charged run=%s: %v", row.RunID, err)
			continue
		}
		charged++
		if duplicate {
			log.Printf("service run ledger: charge run=%s uid=%d already recorded by relay", row.RunID, row.PayerUID)
		}
	}
	return charged, nil
}

// isPermanentChargeError 判断 relay 是否明确拒绝（非重试类 4xx）。
func isPermanentChargeError(err error) bool {
	var relayErr relayAdminError
	if !errors.As(err, &relayErr) {
		return false
	}
	if relayErr.status < 400 || relayErr.status >= 500 {
		return false
	}
	return relayErr.status != http.StatusRequestTimeout && relayErr.status != http.StatusTooManyRequests
}

// buildEntry 把一条终态 run 换算成账本行（金额按当时 pricing 快照）。
func (w *ServiceRunLedgerWriter) buildEntry(run store.ServiceRunBillable) store.ServiceRunLedgerEntry {
	pricing := ParseServiceAgentPricing(run.DefinitionJSON)
	charge := ComputeServiceRunCharge(pricing, run.PromptTokens, run.CompletionTokens, run.DurationMs, w.feeBps)
	return store.ServiceRunLedgerEntry{
		ServiceRunBillable: run,
		PricingJSON:        serviceAgentPricingSnapshot(run.DefinitionJSON),
		Currency:           charge.Currency,
		GrossMicros:        charge.Gross,
		FeeMicros:          charge.Fee,
		NetMicros:          charge.Net,
		// 谁调用谁给钱；收款方是 agent owner（internal agent → 0 = 平台自营）。
		PayerUID:     run.CallerUID,
		PayeeUID:     run.OwnerUID,
		BillingState: resolveBillingState(run, charge.Gross),
	}
}

// resolveBillingState 判定这次 run 的结算归属（设计 §4.5：按会话请求实时结算）。
//   - 免费（未定价）→ free
//   - 自己调自己（caller == owner）→ self_call（钱左手到右手，不扣费）
//   - 平台自带执行（codex）→ 调用方额度已在 relay 模型调用时实时扣除
//   - 外部执行后端（http/mcp/worker）→ 对端自报用量，金额计入调用方「待扣」，
//     由补扣扫描通过 relay 代扣接口结清（成功后 charged）
func resolveBillingState(run store.ServiceRunBillable, grossMicros int64) string {
	if grossMicros <= 0 {
		return store.ServiceBillingFree
	}
	if run.OwnerKind != store.ServiceAgentOwnerInternal && run.CallerUID > 0 && run.CallerUID == run.OwnerUID {
		return store.ServiceBillingSelfCall
	}
	switch run.RunnerType {
	case "http", "mcp":
		// 外部执行后端自报用量，平台按定价算钱 → 平台侧补扣。
		return store.ServiceBillingPendingCredit
	case "worker":
		// 用户自己的 worker（cmd/service-run-worker）认领执行：平台只做编排，
		// 调用方额度是否被扣取决于该 worker 有没有走 relay，平台侧无法确认 → 同样记「待扣」。
		return store.ServiceBillingPendingCredit
	default:
		return store.ServiceBillingSettledPlatform
	}
}

// serviceAgentPricingSnapshot 取 definition.pricing 原文（账本里留计价快照）。
func serviceAgentPricingSnapshot(definition json.RawMessage) json.RawMessage {
	if len(definition) == 0 {
		return nil
	}
	var parsed struct {
		Pricing json.RawMessage `json:"pricing"`
	}
	if err := json.Unmarshal(definition, &parsed); err != nil {
		return nil
	}
	if len(parsed.Pricing) == 0 || string(parsed.Pricing) == "null" {
		return nil
	}
	return parsed.Pricing
}

package server

import (
	"context"
	"encoding/json"
	"log"
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
// 只记账不动钱：真正的扣费/入账读这张表另做，先把「谁用了多少、按什么价」记准。
const (
	serviceLedgerSweepInterval = 30 * time.Second
	serviceLedgerSweepBatch    = 200
	serviceLedgerSweepTimeout  = 20 * time.Second
)

// ServiceRunLedgerStore 是记账所需的最小存储面（便于测试替身）。
type ServiceRunLedgerStore interface {
	ListBillableServiceRuns(ctx context.Context, limit int) ([]store.ServiceRunBillable, error)
	RecordServiceRunLedger(ctx context.Context, entry store.ServiceRunLedgerEntry) (bool, error)
}

type ServiceRunLedgerWriter struct {
	store    ServiceRunLedgerStore
	feeBps   int
	charger  ServiceCreditCharger
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewServiceRunLedgerWriterFromEnv(st ServiceRunLedgerStore) *ServiceRunLedgerWriter {
	return &ServiceRunLedgerWriter{
		store:  st,
		feeBps: PlatformFeeBps(),
		stopCh: make(chan struct{}),
	}
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
	// 新写入的 pending_credit 行会在同一轮里被后面扫到并结算，缩短「跑了但没扣」的窗口。
	if settled, err := w.SettlePendingCredit(ctx); err != nil {
		log.Printf("service credit settlement: %v", err)
	} else if settled > 0 {
		log.Printf("service credit settlement: charged %d run(s)", settled)
	}
	return recorded, nil
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
//   - 平台自带执行（codex）→ 调用方额度已在 relay 模型调用时实时扣除（这是真实模型成本，
//     自己不自己调都得付，所以不参与下面的 self_call 豁免）
//   - 外部执行后端（http/mcp/worker）和 bot-as-tool（bot）→ 金额计入调用方「待扣」，
//     由结算器调 relay 代扣；bot 型必须算：它的模型开销走 bot 自己的额度，调用方这边平台收不到；
//     若调用方就是 owner（自己调自己）→ self_call：同账号左右口袋，不代扣
func resolveBillingState(run store.ServiceRunBillable, grossMicros int64) string {
	if grossMicros <= 0 {
		return store.ServiceBillingFree
	}
	switch run.RunnerType {
	case "http", "mcp", "worker", "bot":
		// 自己调自己：payer 与 payee 是同一个账号，代扣只是把钱从自己的额度里扣走，
		// 而 owner 出款通道还没实现 → 净损失一次，所以只记账不扣费。
		// internal agent 不算（payee=平台，钱真的要从调用方收到平台）。
		if run.OwnerKind != store.ServiceAgentOwnerInternal && run.OwnerUID > 0 && run.CallerUID == run.OwnerUID {
			return store.ServiceBillingSelfCall
		}
		// 外部执行后端自报用量 / 用户自己的 worker 认领执行 / bot-as-tool 的 bot 回合：
		// 平台侧原本扣不到（bot 的模型开销记在它自己的 owner 名下），一律记「待扣」，
		// 由结算器调 relay 代扣。
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

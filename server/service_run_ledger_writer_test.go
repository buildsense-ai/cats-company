package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// ---- 计费（设计 §4.5：谁调用谁付费、按量给）----

func TestParseServiceAgentPricing(t *testing.T) {
	cases := []struct {
		name       string
		definition string
		wantNil    bool
		wantUnit   string
	}{
		{name: "no pricing", definition: `{"async_threshold_seconds":60}`, wantNil: true},
		{name: "invalid json", definition: `{`, wantNil: true},
		{name: "empty unit", definition: `{"pricing":{"per_run":0.1}}`, wantNil: true},
		{name: "token pricing", definition: `{"pricing":{"unit":"TOKEN","per_1k_tokens":0.02}}`, wantUnit: "token"},
		{name: "run pricing", definition: `{"pricing":{"unit":"run","per_run":0.05}}`, wantUnit: "run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pricing := ParseServiceAgentPricing(json.RawMessage(tc.definition))
			if tc.wantNil {
				if pricing != nil {
					t.Fatalf("pricing = %+v, want nil", pricing)
				}
				return
			}
			if pricing == nil || pricing.Unit != tc.wantUnit {
				t.Fatalf("pricing = %+v, want unit=%s", pricing, tc.wantUnit)
			}
			if pricing.Currency != "CNY" {
				t.Fatalf("currency default = %q, want CNY", pricing.Currency)
			}
		})
	}
}

// 金额全部按微元整数：0.02 元/1K token × 1500 token = 0.03 元 = 30000 微元。
func TestComputeServiceRunCharge(t *testing.T) {
	t.Run("token 计价", func(t *testing.T) {
		pricing := &ServiceAgentPricing{Unit: ServicePricingUnitToken, Per1KTokens: 0.02}
		charge := ComputeServiceRunCharge(pricing, 1000, 500, 0, 0)
		if charge.Gross != 30000 || charge.Fee != 0 || charge.Net != 30000 {
			t.Fatalf("charge = %+v, want gross/net 30000", charge)
		}
	})

	t.Run("token 计价 + 平台抽成 20%", func(t *testing.T) {
		pricing := &ServiceAgentPricing{Unit: ServicePricingUnitToken, Per1KTokens: 0.02}
		charge := ComputeServiceRunCharge(pricing, 1000, 500, 0, 2000)
		if charge.Gross != 30000 || charge.Fee != 6000 || charge.Net != 24000 {
			t.Fatalf("charge = %+v, want 30000/6000/24000", charge)
		}
	})

	t.Run("按次计价", func(t *testing.T) {
		pricing := &ServiceAgentPricing{Unit: ServicePricingUnitRun, PerRun: 0.05}
		charge := ComputeServiceRunCharge(pricing, 0, 0, 0, 0)
		if charge.Gross != 50000 {
			t.Fatalf("gross = %d, want 50000", charge.Gross)
		}
	})

	t.Run("按秒计价", func(t *testing.T) {
		pricing := &ServiceAgentPricing{Unit: ServicePricingUnitSecond, PerSecond: 0.001}
		charge := ComputeServiceRunCharge(pricing, 0, 0, 2500, 0)
		if charge.Gross != 2500 { // 2.5s * 0.001 元 = 0.0025 元
			t.Fatalf("gross = %d, want 2500", charge.Gross)
		}
	})

	t.Run("未定价 = 0", func(t *testing.T) {
		charge := ComputeServiceRunCharge(nil, 5000, 5000, 1000, 2000)
		if charge.Gross != 0 || charge.Fee != 0 || charge.Net != 0 {
			t.Fatalf("charge = %+v, want zeros", charge)
		}
	})

	t.Run("用量为 0 = 0", func(t *testing.T) {
		pricing := &ServiceAgentPricing{Unit: ServicePricingUnitToken, Per1KTokens: 0.02}
		charge := ComputeServiceRunCharge(pricing, 0, 0, 0, 0)
		if charge.Gross != 0 {
			t.Fatalf("gross = %d, want 0", charge.Gross)
		}
	})
}

func TestPlatformFeeBps(t *testing.T) {
	t.Setenv(servicePlatformFeeBpsEnv, "")
	if got := PlatformFeeBps(); got != 0 {
		t.Fatalf("default fee = %d, want 0", got)
	}
	t.Setenv(servicePlatformFeeBpsEnv, "2000")
	if got := PlatformFeeBps(); got != 2000 {
		t.Fatalf("fee = %d, want 2000", got)
	}
	t.Setenv(servicePlatformFeeBpsEnv, "99999")
	if got := PlatformFeeBps(); got != 10000 {
		t.Fatalf("fee clamp = %d, want 10000", got)
	}
	t.Setenv(servicePlatformFeeBpsEnv, "abc")
	if got := PlatformFeeBps(); got != 0 {
		t.Fatalf("invalid fee = %d, want 0", got)
	}
}

// ---- 账本写入器 ----

type fakeLedgerStore struct {
	mu      sync.Mutex
	runs    []store.ServiceRunBillable
	entries map[string]store.ServiceRunLedgerEntry
}

func newFakeLedgerStore(runs ...store.ServiceRunBillable) *fakeLedgerStore {
	return &fakeLedgerStore{runs: runs, entries: map[string]store.ServiceRunLedgerEntry{}}
}

func (f *fakeLedgerStore) ListBillableServiceRuns(context.Context, int) ([]store.ServiceRunBillable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.ServiceRunBillable, 0, len(f.runs))
	for _, run := range f.runs {
		if _, done := f.entries[run.RunID]; done {
			continue // 已记账的不再返回（与 SQL 的 LEFT JOIN ... IS NULL 同语义）
		}
		out = append(out, run)
	}
	return out, nil
}

func (f *fakeLedgerStore) RecordServiceRunLedger(_ context.Context, entry store.ServiceRunLedgerEntry) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.entries[entry.RunID]; exists {
		return false, nil
	}
	f.entries[entry.RunID] = entry
	return true, nil
}

func (f *fakeLedgerStore) SumPendingCallerChargeMicros(_ context.Context, payerUID int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total int64
	for _, entry := range f.entries {
		if entry.PayerUID == payerUID && entry.BillingState == store.ServiceBillingPendingCredit {
			total += entry.GrossMicros
		}
	}
	return total, nil
}

// ListPendingCreditLedger 模拟 SQL：只返回 window 内、payer/gross 有效的待扣行。
func (f *fakeLedgerStore) ListPendingCreditLedger(_ context.Context, limit int) ([]store.ServiceRunLedgerCharge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.ServiceRunLedgerCharge, 0, len(f.entries))
	for _, entry := range f.entries {
		if entry.BillingState != store.ServiceBillingPendingCredit || entry.PayerUID <= 0 || entry.GrossMicros <= 0 {
			continue
		}
		out = append(out, store.ServiceRunLedgerCharge{
			RunID:       entry.RunID,
			PayerUID:    entry.PayerUID,
			GrossMicros: entry.GrossMicros,
			Currency:    entry.Currency,
			FinishedAt:  entry.FinishedAt,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeLedgerStore) MarkServiceRunLedgerState(_ context.Context, runID string, state string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.entries[runID]
	if !ok || entry.BillingState != store.ServiceBillingPendingCredit {
		return false, nil
	}
	entry.BillingState = state
	f.entries[runID] = entry
	return true, nil
}

func ledgerTestRun(runID string, definition string) store.ServiceRunBillable {
	return store.ServiceRunBillable{
		RunID:            runID,
		SessionID:        "sess_" + runID,
		AgentID:          7,
		AgentSlug:        "my-helper",
		OwnerKind:        store.ServiceAgentOwnerUser,
		OwnerUID:         441,
		CallerUID:        442,
		CallerBotUID:     1619,
		RunnerType:       "http",
		Status:           "completed",
		PromptTokens:     1000,
		CompletionTokens: 500,
		DurationMs:       2500,
		DefinitionJSON:   json.RawMessage(definition),
		FinishedAt:       time.Now().Add(-time.Minute),
	}
}

// 谁调用谁给钱：payer=caller，payee=owner；金额按 pricing 快照算；重复扫描幂等。
func TestServiceRunLedgerWriter(t *testing.T) {
	st := newFakeLedgerStore(
		ledgerTestRun("run_1", `{"pricing":{"unit":"token","per_1k_tokens":0.02}}`),
		ledgerTestRun("run_2", `{"async_threshold_seconds":60}`),
	)
	writer := NewServiceRunLedgerWriterFromEnv(st)
	writer.SetFeeBps(2000)
	ctx := context.Background()

	recorded, err := writer.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if recorded != 2 {
		t.Fatalf("recorded = %d, want 2", recorded)
	}

	paid := st.entries["run_1"]
	if paid.PayerUID != 442 || paid.PayeeUID != 441 {
		t.Fatalf("payer/payee = %d/%d, want caller/owner 442/441", paid.PayerUID, paid.PayeeUID)
	}
	// 外部执行后端：金额计入调用方「待扣」（relay 扣不到对端的模型用量）。
	if paid.BillingState != store.ServiceBillingPendingCredit {
		t.Fatalf("billing_state = %q, want pending_credit (runner=http)", paid.BillingState)
	}
	if paid.GrossMicros != 30000 || paid.FeeMicros != 6000 || paid.NetMicros != 24000 {
		t.Fatalf("amounts = gross %d fee %d net %d, want 30000/6000/24000",
			paid.GrossMicros, paid.FeeMicros, paid.NetMicros)
	}
	if !strings.Contains(string(paid.PricingJSON), "per_1k_tokens") {
		t.Fatalf("pricing snapshot missing: %s", paid.PricingJSON)
	}
	if paid.Currency != "CNY" || paid.RunnerType != "http" || paid.PromptTokens != 1000 {
		t.Fatalf("ledger metadata = %+v", paid)
	}

	// 未定价的 agent 也记账（金额 0），便于后续定价回溯。
	free := st.entries["run_2"]
	if free.GrossMicros != 0 || free.PayerUID != 442 {
		t.Fatalf("free run = %+v", free)
	}
	if free.BillingState != store.ServiceBillingFree {
		t.Fatalf("free billing_state = %q, want free", free.BillingState)
	}

	// 重复扫描：已记账不再返回 → 0
	again, err := writer.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 {
		t.Fatalf("second sweep recorded = %d, want 0 (idempotent)", again)
	}
}

func TestServiceRunLedgerWriterInternalOwner(t *testing.T) {
	run := ledgerTestRun("run_int", `{"pricing":{"unit":"run","per_run":0.05}}`)
	run.OwnerKind = store.ServiceAgentOwnerInternal
	run.OwnerUID = 0
	st := newFakeLedgerStore(run)
	writer := NewServiceRunLedgerWriterFromEnv(st)

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	entry := st.entries["run_int"]
	if entry.PayeeUID != 0 {
		t.Fatalf("internal agent payee = %d, want 0 (平台自营)", entry.PayeeUID)
	}
	if entry.GrossMicros != 50000 || entry.NetMicros != 50000 {
		t.Fatalf("amounts = %+v", entry)
	}
}

// 平台自带执行（codex）的金额已由 relay 在模型调用时实时扣除 → settled_platform。
func TestServiceRunLedgerWriterSettledForPlatformRunner(t *testing.T) {
	run := ledgerTestRun("run_codex", `{"pricing":{"unit":"token","per_1k_tokens":0.02}}`)
	run.RunnerType = "codex"
	st := newFakeLedgerStore(run)
	writer := NewServiceRunLedgerWriterFromEnv(st)

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := st.entries["run_codex"].BillingState; got != store.ServiceBillingSettledPlatform {
		t.Fatalf("billing_state = %q, want settled_platform", got)
	}
}

// 预检并入「待扣」：外部 agent 的金额把调用方额度提前打到上限 → 拒单。
func TestServiceBalancePrecheckIncludesPendingCharge(t *testing.T) {
	ledger := newFakeLedgerStore(ledgerTestRun("run_pending", `{"pricing":{"unit":"run","per_run":0.6}}`))
	writer := NewServiceRunLedgerWriterFromEnv(ledger)
	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// 待扣 = 0.6 元 = 600000 微元
	pending, err := ledger.SumPendingCallerChargeMicros(context.Background(), 442)
	if err != nil || pending != 600000 {
		t.Fatalf("pending = %d err=%v, want 600000", pending, err)
	}

	checker := &ServiceBalanceChecker{pending: ledger}
	user := &commercialRelayUsageUser{UID: 442, Configured: true}
	user.Limits.MonthlyBudget = commercialRelayBudget{MaxLimit: 1.0, CurrentUsage: 0.5}
	// relay 侧剩余 0.5，但加上待扣 0.6 → 已超额
	if !balanceExhaustedWithPending(checker, context.Background(), 442, user) {
		t.Fatal("pending charge should push usage over max_limit")
	}
	// 预算充足时不误判
	user.Limits.MonthlyBudget = commercialRelayBudget{MaxLimit: 5.0, CurrentUsage: 0.5}
	if balanceExhaustedWithPending(checker, context.Background(), 442, user) {
		t.Fatal("ample budget should not be exhausted")
	}
}

// balanceExhaustedWithPending 是预检里「额度是否耗尽」的直出断言入口（与 Check 同口径）。
func balanceExhaustedWithPending(c *ServiceBalanceChecker, ctx context.Context, uid int64, user *commercialRelayUsageUser) bool {
	pendingCNY := 0.0
	if c != nil && c.pending != nil {
		if micros, err := c.pending.SumPendingCallerChargeMicros(ctx, uid); err == nil {
			pendingCNY = float64(micros) / 1_000_000.0
		}
	}
	exhausted := func(budget commercialRelayBudget) bool {
		return budget.MaxLimit > 0 && budget.CurrentUsage+pendingCNY >= budget.MaxLimit
	}
	return exhausted(user.Limits.MonthlyBudget)
}

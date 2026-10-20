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

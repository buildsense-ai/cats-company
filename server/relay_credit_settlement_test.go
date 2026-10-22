package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// ---- 代扣结算（设计 §4.5 闭环：pending_credit → relay 当月用量）----

type creditChargeCall struct {
	UID       int64
	RequestID string
	AmountCNY float64
	Note      string
}

type fakeCreditCharger struct {
	mu    sync.Mutex
	calls []creditChargeCall
	dup   bool
	err   error
}

func (f *fakeCreditCharger) ChargeUser(_ context.Context, uid int64, requestID string, amountCNY float64, note string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, creditChargeCall{UID: uid, RequestID: requestID, AmountCNY: amountCNY, Note: note})
	if f.err != nil {
		return false, f.err
	}
	return f.dup, nil
}

func (f *fakeCreditCharger) snapshot() []creditChargeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]creditChargeCall(nil), f.calls...)
}

func ledgerEntryState(t *testing.T, st *fakeLedgerStore, runID string) string {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	entry, ok := st.entries[runID]
	if !ok {
		t.Fatalf("ledger entry %q missing", runID)
	}
	return entry.BillingState
}

// 外部 runner 的账本行写完后应在同一轮被结算成 charged，且扣的是 gross。
func TestSettlementChargesPendingCredit(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_charge_ok", `{"pricing":{"unit":"token","per_1k_tokens":0.02}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)
	writer.SetFeeBps(2000) // 20% 抽成：gross 30000 / fee 6000 / net 24000
	charger := &fakeCreditCharger{}
	writer.SetCharger(charger)

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if state := ledgerEntryState(t, st, "run_charge_ok"); state != store.ServiceBillingCharged {
		t.Fatalf("billing_state = %q, want charged", state)
	}
	calls := charger.snapshot()
	if len(calls) != 1 {
		t.Fatalf("charge calls = %d, want 1", len(calls))
	}
	if calls[0].UID != 442 {
		t.Fatalf("charged uid = %d, want caller 442", calls[0].UID)
	}
	if calls[0].RequestID != "run_charge_ok" {
		t.Fatalf("request_id = %q, want run id (relay 幂等键)", calls[0].RequestID)
	}
	// 调用方付 gross，不是 net（fee 是平台抽给发布方的部分）。
	if diff := calls[0].AmountCNY - 0.03; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("amount = %v, want 0.03 (gross 口径)", calls[0].AmountCNY)
	}
}

// 已结算的行不会再被扫到：第二轮不该重复扣。
func TestSettlementDoesNotRecharge(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_charge_once", `{"pricing":{"unit":"run","per_run":0.05}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)
	charger := &fakeCreditCharger{}
	writer.SetCharger(charger)
	ctx := context.Background()

	if _, err := writer.SweepOnce(ctx); err != nil {
		t.Fatalf("sweep 1: %v", err)
	}
	if _, err := writer.SweepOnce(ctx); err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	if calls := charger.snapshot(); len(calls) != 1 {
		t.Fatalf("charge calls = %d, want 1 (幂等：charged 后不再重扣)", len(calls))
	}
}

// relay 临时不可用 → 保持待扣，下一轮重试，不能标记成失败。
func TestSettlementKeepsPendingOnTransientError(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_charge_retry", `{"pricing":{"unit":"run","per_run":0.05}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)
	charger := &fakeCreditCharger{err: relayAdminError{status: 503, message: "relay down"}}
	writer.SetCharger(charger)

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if state := ledgerEntryState(t, st, "run_charge_retry"); state != store.ServiceBillingPendingCredit {
		t.Fatalf("billing_state = %q, want pending_credit（留给下一轮重试）", state)
	}
}

// 网络错误（非 relayAdminError）也按可重试处理。
func TestSettlementKeepsPendingOnNetworkError(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_charge_net", `{"pricing":{"unit":"run","per_run":0.05}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)
	writer.SetCharger(&fakeCreditCharger{err: errors.New("connection refused")})

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if state := ledgerEntryState(t, st, "run_charge_net"); state != store.ServiceBillingPendingCredit {
		t.Fatalf("billing_state = %q, want pending_credit", state)
	}
}

// relay 明确拒绝（4xx）→ 标记 charge_failed，别无限重试刷日志。
func TestSettlementMarksFailedOnRejection(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_charge_404", `{"pricing":{"unit":"run","per_run":0.05}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)
	writer.SetCharger(&fakeCreditCharger{err: relayAdminError{status: 404, message: "relay key not found"}})

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if state := ledgerEntryState(t, st, "run_charge_404"); state != store.ServiceBillingChargeFailed {
		t.Fatalf("billing_state = %q, want charge_failed", state)
	}
}

// 429 是限流，属于可重试，不能当成永久失败。
func TestSettlementRetriesOnRateLimit(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_charge_429", `{"pricing":{"unit":"run","per_run":0.05}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)
	writer.SetCharger(&fakeCreditCharger{err: relayAdminError{status: 429, message: "slow down"}})

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if state := ledgerEntryState(t, st, "run_charge_429"); state != store.ServiceBillingPendingCredit {
		t.Fatalf("billing_state = %q, want pending_credit", state)
	}
}

// relay 回报 duplicate（这个 run 之前扣过）也算结算成功。
func TestSettlementTreatsDuplicateAsSettled(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_charge_dup", `{"pricing":{"unit":"run","per_run":0.05}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)
	writer.SetCharger(&fakeCreditCharger{dup: true})

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if state := ledgerEntryState(t, st, "run_charge_dup"); state != store.ServiceBillingCharged {
		t.Fatalf("billing_state = %q, want charged", state)
	}
}

// 平台自带的 codex 执行已经由 relay 实时扣过，不该再走代扣。
func TestSettlementSkipsPlatformRunner(t *testing.T) {
	run := ledgerTestRun("run_platform", `{"pricing":{"unit":"run","per_run":0.05}}`)
	run.RunnerType = "codex"
	st := newFakeLedgerStore(run)
	writer := NewServiceRunLedgerWriterFromEnv(st)
	charger := &fakeCreditCharger{}
	writer.SetCharger(charger)

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if calls := charger.snapshot(); len(calls) != 0 {
		t.Fatalf("charge calls = %d, want 0 (codex 已实时扣)", len(calls))
	}
	if state := ledgerEntryState(t, st, "run_platform"); state != store.ServiceBillingSettledPlatform {
		t.Fatalf("billing_state = %q, want settled_platform", state)
	}
}

// 没配 relay 时只记账不动钱（本地/测试环境）。
func TestSettlementWithoutChargerKeepsPending(t *testing.T) {
	st := newFakeLedgerStore(ledgerTestRun("run_no_charger", `{"pricing":{"unit":"run","per_run":0.05}}`))
	writer := NewServiceRunLedgerWriterFromEnv(st)

	if _, err := writer.SweepOnce(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if state := ledgerEntryState(t, st, "run_no_charger"); state != store.ServiceBillingPendingCredit {
		t.Fatalf("billing_state = %q, want pending_credit", state)
	}
}

// ChargeUser 的 HTTP 契约：路径、鉴权、body 字段名要跟 relay 对齐。
func TestRelayAdminClientChargeUserRequest(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"uid":38,"request_id":"run_x","amount_cny":0.03,"duplicate":false,"manual_charge_total_cny":0.03}`))
	}))
	defer srv.Close()

	client := &RelayAdminClient{baseURL: srv.URL, token: "relay-secret", client: srv.Client()}
	dup, err := client.ChargeUser(context.Background(), 38, "run_x", 0.03, "catsco service run run_x")
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if dup {
		t.Fatalf("duplicate = true, want false")
	}
	if gotPath != "/internal/users/38/charge" {
		t.Fatalf("path = %q, want /internal/users/38/charge", gotPath)
	}
	if gotAuth != "Bearer relay-secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotBody["request_id"] != "run_x" {
		t.Fatalf("body request_id = %v", gotBody["request_id"])
	}
	if amount, ok := gotBody["amount_cny"].(float64); !ok || amount != 0.03 {
		t.Fatalf("body amount_cny = %v", gotBody["amount_cny"])
	}
}

func TestRelayAdminClientChargeUserValidatesInput(t *testing.T) {
	client := &RelayAdminClient{baseURL: "http://127.0.0.1:1", token: "t", client: http.DefaultClient}
	if _, err := client.ChargeUser(context.Background(), 0, "run", 1, ""); err == nil {
		t.Fatalf("uid=0 should fail")
	}
	if _, err := client.ChargeUser(context.Background(), 1, "  ", 1, ""); err == nil {
		t.Fatalf("empty request_id should fail")
	}
	if _, err := client.ChargeUser(context.Background(), 1, "run", 0, ""); err == nil {
		t.Fatalf("zero amount should fail")
	}
}

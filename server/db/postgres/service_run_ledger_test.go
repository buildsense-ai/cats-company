package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// run 级账本（000046 / 设计 §4.5）。

func TestServiceRunLedgerSchemaConstants(t *testing.T) {
	for _, want := range []string{
		"service_run_ledger", "payer_uid", "payee_uid", "gross_micros", "fee_micros",
		"net_micros", "pricing_json", "runner_type", "caller_bot_uid",
	} {
		if !strings.Contains(createServiceRunLedgerTable, want) {
			t.Fatalf("ledger table missing %q", want)
		}
	}
	for _, want := range []string{"idx_service_run_ledger_payer", "idx_service_run_ledger_payee", "idx_service_run_ledger_agent"} {
		if !strings.Contains(createServiceRunLedgerIndexes, want) {
			t.Fatalf("ledger indexes missing %q", want)
		}
	}
}

func TestServiceRunLedgerMigrationFilesReversible(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000046_service_run_ledger.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS service_run_ledger", "payer_uid", "payee_uid"} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("up migration missing %q", want)
		}
	}
	down, err := os.ReadFile("../migrations/postgres/000046_service_run_ledger.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS service_run_ledger") {
		t.Fatalf("down migration not reversible: %s", down)
	}
}

// 真实链路：终态 run → 出现在待记账列表 → 写入 → 不再出现（幂等）。
func TestServiceRunLedgerListAndRecord(t *testing.T) {
	db := pgTestDB(t)
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// 启动建表不再自带 hello 种子。
	seedHelloAgent(t, db)
	ctx := context.Background()

	var agentID int64
	if err := db.db.QueryRowContext(ctx, `SELECT id FROM service_agents WHERE slug = 'hello'`).Scan(&agentID); err != nil {
		t.Fatalf("get hello agent: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `
		INSERT INTO service_sessions (session_id, agent_id, definition_rev, caller_uid, caller_bot_uid)
		VALUES ('sess_ledger_1', $1, 1, 442, 1619)`, agentID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `
		INSERT INTO service_runs (run_id, session_id, state, usage_json, started_at, finished_at)
		VALUES ('run_ledger_1', 'sess_ledger_1', 'completed',
		        '{"prompt_tokens": 1000, "completion_tokens": 500}'::jsonb,
		        now() - interval '3 seconds', now())`); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	billable, err := db.ListBillableServiceRuns(ctx, 10)
	if err != nil {
		t.Fatalf("list billable: %v", err)
	}
	if len(billable) != 1 {
		t.Fatalf("billable = %+v, want 1", billable)
	}
	item := billable[0]
	if item.RunID != "run_ledger_1" || item.AgentSlug != "hello" ||
		item.CallerUID != 442 || item.CallerBotUID != 1619 ||
		item.PromptTokens != 1000 || item.CompletionTokens != 500 {
		t.Fatalf("billable item = %+v", item)
	}
	if item.DurationMs < 2500 || item.DurationMs > 6000 {
		t.Fatalf("duration = %dms, want ~3000", item.DurationMs)
	}
	if item.RunnerType != "codex" {
		t.Fatalf("runner_type = %q, want codex (no runner in definition)", item.RunnerType)
	}

	entry := store.ServiceRunLedgerEntry{
		ServiceRunBillable: item,
		PricingJSON:        json.RawMessage(`{"unit":"token","per_1k_tokens":0.02}`),
		Currency:           "CNY",
		GrossMicros:        30000,
		FeeMicros:          6000,
		NetMicros:          24000,
		PayerUID:           item.CallerUID,
		PayeeUID:           item.OwnerUID,
	}
	inserted, err := db.RecordServiceRunLedger(ctx, entry)
	if err != nil || !inserted {
		t.Fatalf("record: inserted=%v err=%v", inserted, err)
	}
	// 幂等重放
	again, err := db.RecordServiceRunLedger(ctx, entry)
	if err != nil || again {
		t.Fatalf("replay: inserted=%v err=%v, want false/nil", again, err)
	}
	// 已记账 → 不再进待记账列表
	billable, err = db.ListBillableServiceRuns(ctx, 10)
	if err != nil {
		t.Fatalf("list after record: %v", err)
	}
	if len(billable) != 0 {
		t.Fatalf("billable after record = %+v, want empty", billable)
	}

	var gross, fee, net int64
	var payer, payee int64
	if err := db.db.QueryRowContext(ctx, `
		SELECT gross_micros, fee_micros, net_micros, payer_uid, payee_uid
		FROM service_run_ledger WHERE run_id = 'run_ledger_1'`).Scan(&gross, &fee, &net, &payer, &payee); err != nil {
		t.Fatalf("read ledger row: %v", err)
	}
	if gross != 30000 || fee != 6000 || net != 24000 || payer != 442 || payee != item.OwnerUID {
		t.Fatalf("ledger row = gross %d fee %d net %d payer %d payee %d", gross, fee, net, payer, payee)
	}
	_ = time.Now
}

// 补扣闭环的 DB 语义：pending 行可列、状态迁移幂等、欠费口径 = gross（含 charge_failed）。
func TestServiceRunLedgerPendingCharges(t *testing.T) {
	db := pgTestDB(t)
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	ctx := context.Background()

	mkEntry := func(runID string, gross int64) store.ServiceRunLedgerEntry {
		return store.ServiceRunLedgerEntry{
			ServiceRunBillable: store.ServiceRunBillable{
				RunID: runID, SessionID: "sess_charge", AgentID: 1, AgentSlug: "hello",
				OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441,
				CallerUID: 442, RunnerType: "mcp", Status: "completed",
				FinishedAt: time.Now(),
			},
			Currency: "CNY", GrossMicros: gross, NetMicros: gross,
			PayerUID: 442, PayeeUID: 441, BillingState: store.ServiceBillingPendingCredit,
		}
	}
	for _, entry := range []store.ServiceRunLedgerEntry{mkEntry("run_charge_1", 30000), mkEntry("run_charge_2", 20000)} {
		if inserted, err := db.RecordServiceRunLedger(ctx, entry); err != nil || !inserted {
			t.Fatalf("record %s: inserted=%v err=%v", entry.RunID, inserted, err)
		}
	}

	rows, err := db.ListPendingServiceRunLedgerCharges(ctx, 10)
	if err != nil {
		t.Fatalf("list pending charges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("pending charges = %+v, want 2", rows)
	}
	for _, row := range rows {
		if row.PayerUID != 442 || row.PayeeUID != 441 || row.Currency != "CNY" || row.GrossMicros <= 0 {
			t.Fatalf("pending charge row = %+v", row)
		}
	}
	if sum, err := db.SumPendingCallerChargeMicros(ctx, 442); err != nil || sum != 50000 {
		t.Fatalf("pending sum = %d err=%v, want 50000", sum, err)
	}

	// pending → charged：只在 from_state 匹配时生效（幂等）。
	if ok, err := db.UpdateServiceRunLedgerBillingState(ctx, "run_charge_1", store.ServiceBillingPendingCredit, store.ServiceBillingCharged); err != nil || !ok {
		t.Fatalf("mark charged: ok=%v err=%v", ok, err)
	}
	if ok, err := db.UpdateServiceRunLedgerBillingState(ctx, "run_charge_1", store.ServiceBillingPendingCredit, store.ServiceBillingCharged); err != nil || ok {
		t.Fatalf("mark charged replay: ok=%v err=%v, want false/nil", ok, err)
	}
	if rows, err = db.ListPendingServiceRunLedgerCharges(ctx, 10); err != nil || len(rows) != 1 || rows[0].RunID != "run_charge_2" {
		t.Fatalf("pending after charged = %+v err=%v, want run_charge_2 only", rows, err)
	}
	if sum, err := db.SumPendingCallerChargeMicros(ctx, 442); err != nil || sum != 20000 {
		t.Fatalf("pending sum after charged = %d err=%v, want 20000", sum, err)
	}

	// pending → charge_failed：不再列出补扣，但仍算欠费。
	if ok, err := db.UpdateServiceRunLedgerBillingState(ctx, "run_charge_2", store.ServiceBillingPendingCredit, store.ServiceBillingChargeFailed); err != nil || !ok {
		t.Fatalf("mark charge_failed: ok=%v err=%v", ok, err)
	}
	if rows, err = db.ListPendingServiceRunLedgerCharges(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatalf("pending after failed = %+v err=%v, want empty", rows, err)
	}
	if sum, err := db.SumPendingCallerChargeMicros(ctx, 442); err != nil || sum != 20000 {
		t.Fatalf("pending sum after failed = %d err=%v, want 20000 (still owed)", sum, err)
	}
}

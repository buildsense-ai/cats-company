package mysql

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// run 级账本（000046；PG 同语义镜像）。
func TestMySQLServiceRunLedgerSchemaConstants(t *testing.T) {
	for _, want := range []string{
		"service_run_ledger", "payer_uid", "payee_uid", "gross_micros", "pricing_json",
		"idx_service_run_ledger_payer",
	} {
		if !strings.Contains(createServiceRunLedgerTable, want) {
			t.Fatalf("ledger table missing %q", want)
		}
	}
}

func TestMySQLServiceRunLedgerMigrationFilesReversible(t *testing.T) {
	up, err := os.ReadFile("../migrations/mysql/000046_service_run_ledger.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), "CREATE TABLE IF NOT EXISTS service_run_ledger") {
		t.Fatalf("up migration missing table: %s", up)
	}
	down, err := os.ReadFile("../migrations/mysql/000046_service_run_ledger.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS service_run_ledger") {
		t.Fatalf("down migration not reversible: %s", down)
	}
}

// 终态 run → 待记账 → 写入 → 幂等 → 不再出现。
func TestMySQLServiceRunLedgerListAndRecord(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()

	aID := seedMySQLServiceAgent(t, db)
	if _, err := db.db.ExecContext(ctx, `
		INSERT INTO service_sessions (session_id, agent_id, definition_rev, caller_uid, caller_bot_uid)
		VALUES ('sess_ledger_m1', ?, 1, 442, 1619)`, aID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `
		INSERT INTO service_runs (run_id, session_id, state, input_json, error_text, usage_json, started_at, finished_at)
		VALUES ('run_ledger_m1', 'sess_ledger_m1', 'completed',
		        JSON_OBJECT(), '',
		        JSON_OBJECT('prompt_tokens', 1000, 'completion_tokens', 500),
		        DATE_SUB(NOW(6), INTERVAL 3 SECOND), NOW(6))`); err != nil {
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
	if item.RunID != "run_ledger_m1" || item.CallerUID != 442 || item.PromptTokens != 1000 {
		t.Fatalf("billable item = %+v", item)
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
	again, err := db.RecordServiceRunLedger(ctx, entry)
	if err != nil || again {
		t.Fatalf("replay: inserted=%v err=%v, want false/nil", again, err)
	}
	billable, err = db.ListBillableServiceRuns(ctx, 10)
	if err != nil {
		t.Fatalf("list after record: %v", err)
	}
	if len(billable) != 0 {
		t.Fatalf("billable after record = %+v, want empty", billable)
	}
}

// 预检口径：pending_credit（待重试）+ charge_failed（人工追缴）都算欠款；
// charged / self_call 不能重复计入。
func TestMySQLServiceRunLedgerPendingChargeSum(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()
	const payer int64 = 9077
	t.Cleanup(func() {
		_, _ = db.db.ExecContext(context.Background(), `DELETE FROM service_run_ledger WHERE payer_uid = ?`, payer)
	})

	cases := []struct {
		runID string
		state string
		gross int64
	}{
		{"run_sum_my_pending", store.ServiceBillingPendingCredit, 30000},
		{"run_sum_my_failed", store.ServiceBillingChargeFailed, 20000},
		{"run_sum_my_charged", store.ServiceBillingCharged, 50000},
		{"run_sum_my_self", store.ServiceBillingSelfCall, 70000},
	}
	for _, c := range cases {
		entry := store.ServiceRunLedgerEntry{
			ServiceRunBillable: store.ServiceRunBillable{
				RunID: c.runID, SessionID: "sess_" + c.runID, AgentID: 1, AgentSlug: "ledger-sum",
				OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: 441, CallerUID: payer,
				RunnerType: "worker", Status: "completed", FinishedAt: time.Now(),
			},
			Currency: "CNY", GrossMicros: c.gross,
			PayerUID: payer, PayeeUID: 441, BillingState: store.ServiceBillingPendingCredit,
		}
		if _, err := db.RecordServiceRunLedger(ctx, entry); err != nil {
			t.Fatalf("record %s: %v", c.runID, err)
		}
		if _, err := db.db.ExecContext(ctx, `UPDATE service_run_ledger SET billing_state = ? WHERE run_id = ?`, c.state, c.runID); err != nil {
			t.Fatalf("set state %s: %v", c.runID, err)
		}
	}

	total, err := db.SumPendingCallerChargeMicros(ctx, payer)
	if err != nil {
		t.Fatalf("sum pending: %v", err)
	}
	if total != 50000 {
		t.Fatalf("pending sum = %d, want 50000 (pending 30000 + charge_failed 20000)", total)
	}
}

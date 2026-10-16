package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// 生命周期回注收件箱（000033）：schema 常量 / 编号迁移 / 终态事务写回执 / 待投递列 / 送达标记。

func TestServiceRunReceiptsSchemaConstants(t *testing.T) {
	if !strings.Contains(createServiceRunReceiptsTable, "receipt_id VARCHAR(64) PRIMARY KEY") {
		t.Fatal("service_run_receipts table missing primary key")
	}
	if !strings.Contains(createServiceRunReceiptsTable, "delivered_at TIMESTAMPTZ") {
		t.Fatal("service_run_receipts table missing delivered_at")
	}
	if !strings.Contains(createServiceRunReceiptsTable, "REFERENCES service_runs(run_id) ON DELETE CASCADE") {
		t.Fatal("service_run_receipts must reference service_runs with cascade")
	}
	if !strings.Contains(createServiceRunReceiptsPendingIndex, "idx_service_run_receipts_pending") {
		t.Fatal("missing pending receipts index")
	}
}

func TestServiceRunReceiptsMigrationMatchesRuntimeSchema(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000033_service_run_receipts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS service_run_receipts",
		"idx_service_run_receipts_pending",
		"REFERENCES service_runs(run_id) ON DELETE CASCADE",
	} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("migration missing %q: %s", want, up)
		}
	}
	down, err := os.ReadFile("../migrations/postgres/000033_service_run_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS service_run_receipts") {
		t.Fatalf("down migration must be reversible: %s", down)
	}
}

// 000033 在 000027 之上幂等执行，且可回滚再上。
func TestServiceRunReceiptsMigrationFilesExecute(t *testing.T) {
	db := pgTestDB(t)
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql")
	execRetireV4MigrationFile(t, db, "000033_service_run_receipts.up.sql")
	execRetireV4MigrationFile(t, db, "000033_service_run_receipts.up.sql") // 幂等重放
	execRetireV4MigrationFile(t, db, "000033_service_run_receipts.down.sql")
	execRetireV4MigrationFile(t, db, "000033_service_run_receipts.up.sql")
}

// 终态事务与回执原子：completed / awaiting_input / failed(不可重试) 各写一份；
// orphaned（可重试中间态）不写；幂等重放不重复写。
func TestServiceRunReceiptsWrittenOnTerminalTransitions(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")

	newRunningRun := func(callerUID int64, token string) string {
		t.Helper()
		sessID, err := db.CreateServiceSession(ctx, aID, 1, callerUID, 0, "")
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, token, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		claimed, err := db.ClaimServiceRuns(ctx, "inst_rcp", 10)
		if err != nil || len(claimed) != 1 || claimed[0].RunID != run.RunID {
			t.Fatalf("claim run %s: %+v %v", run.RunID, claimed, err)
		}
		return run.RunID
	}

	// ① completed → 回执 + 幂等重放不重复
	run1 := newRunningRun(441, "ct_rcp_1")
	if completed, err := db.CompleteServiceRun(ctx, run1, "res_1", json.RawMessage(`{"text":"ok"}`), json.RawMessage(`{}`)); err != nil || !completed {
		t.Fatalf("complete run1: %v %v", completed, err)
	}
	if _, err := db.CompleteServiceRun(ctx, run1, "res_1", json.RawMessage(`{"text":"ok"}`), nil); err != nil {
		t.Fatalf("idempotent complete replay: %v", err)
	}
	receipts, err := db.ListUndeliveredServiceRunReceipts(ctx, 10)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("after complete receipts = %+v err=%v, want 1", receipts, err)
	}
	wantID := "rcp_" + strings.TrimPrefix(run1, "run_")
	if receipts[0].ReceiptID != wantID || receipts[0].RunID != run1 || receipts[0].CallerUID != 441 || receipts[0].Status != "completed" {
		t.Fatalf("completed receipt = %+v", receipts[0])
	}
	var payload map[string]any
	if err := json.Unmarshal(receipts[0].Payload, &payload); err != nil || payload["contract_version"] != "catsco.service-run-receipt.v1" {
		t.Fatalf("receipt payload contract = %s (err=%v)", receipts[0].Payload, err)
	}

	// ② awaiting_input → 回执含 question；错态/重放语义
	run2 := newRunningRun(442, "ct_rcp_2")
	envelope := json.RawMessage(`{"contract_version":"catsco.service-run-result.v1","status":"awaiting_input","output":{"question":"请提供周报的日期范围"}}`)
	if awaited, err := db.AwaitServiceRunInput(ctx, run2, "awt_1", envelope); err != nil || !awaited {
		t.Fatalf("await run2: %v %v", awaited, err)
	}
	if awaited, err := db.AwaitServiceRunInput(ctx, run2, "awt_1", envelope); err != nil || awaited {
		t.Fatalf("await replay must be idempotent: %v %v", awaited, err)
	}
	if _, err := db.AwaitServiceRunInput(ctx, run2, "awt_2", envelope); !errors.Is(err, store.ErrServiceRunConflict) {
		t.Fatalf("await wrong result_id = %v, want ErrServiceRunConflict", err)
	}
	if _, err := db.CompleteServiceRun(ctx, run2, "res_2", nil, nil); !errors.Is(err, store.ErrServiceRunConflict) {
		t.Fatalf("complete after awaiting = %v, want ErrServiceRunConflict", err)
	}
	receipts, _ = db.ListUndeliveredServiceRunReceipts(ctx, 10)
	if len(receipts) != 2 {
		t.Fatalf("after await receipts = %+v, want 2", receipts)
	}
	var awaitReceipt *store.ServiceRunReceipt
	for i := range receipts {
		if receipts[i].ReceiptID == "rcp_"+strings.TrimPrefix(run2, "run_") {
			awaitReceipt = &receipts[i]
		}
	}
	if awaitReceipt == nil || awaitReceipt.Status != "awaiting_input" || awaitReceipt.CallerUID != 442 {
		t.Fatalf("awaiting receipt = %+v", awaitReceipt)
	}
	if !strings.Contains(string(awaitReceipt.Payload), "请提供周报的日期范围") {
		t.Fatalf("awaiting receipt payload question = %s", awaitReceipt.Payload)
	}

	// ③ failed（不可重试）→ 回执含 error；orphaned（可重试）不写回执
	run3 := newRunningRun(443, "ct_rcp_3")
	if err := db.FailServiceRun(ctx, run3, "按错误重试 3 次仍失败", false); err != nil {
		t.Fatalf("fail run3: %v", err)
	}
	receipts, _ = db.ListUndeliveredServiceRunReceipts(ctx, 10)
	if len(receipts) != 3 {
		t.Fatalf("after failed receipts = %+v, want 3", receipts)
	}
	for _, r := range receipts {
		if r.RunID == run3 {
			if r.Status != "failed" || !strings.Contains(string(r.Payload), "按错误重试 3 次仍失败") {
				t.Fatalf("failed receipt = %+v payload=%s", r, r.Payload)
			}
		}
	}
	run4 := newRunningRun(444, "ct_rcp_4")
	if err := db.FailServiceRun(ctx, run4, "network blip", true); err != nil {
		t.Fatalf("retryable fail run4: %v", err)
	}
	if receiptsAfter, _ := db.ListUndeliveredServiceRunReceipts(ctx, 10); len(receiptsAfter) != 3 {
		t.Fatalf("orphaned transition must not write receipts: %+v", receiptsAfter)
	}

	// ④ 送达标记 / 投递尝试计数
	if err := db.MarkServiceRunReceiptDelivered(ctx, receipts[0].ReceiptID); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	if err := db.TouchServiceRunReceiptDelivery(ctx, receipts[1].ReceiptID); err != nil {
		t.Fatalf("touch delivery: %v", err)
	}
	pending, err := db.ListUndeliveredServiceRunReceipts(ctx, 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending after deliver = %+v err=%v, want 2", pending, err)
	}
	touched := false
	for _, r := range pending {
		if r.ReceiptID == receipts[1].ReceiptID {
			touched = r.DeliveryAttempts == 1
		}
	}
	if !touched {
		t.Fatalf("delivery attempts not bumped: %+v", pending)
	}
}

// 接管次数耗尽（attempts>=3）→ reaper 终态失败 → 回执入箱；orphaned 中间态不入箱。
func TestServiceRunReceiptsOnReaperFinalFailure(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")

	sessID, err := db.CreateServiceSession(ctx, aID, 1, 445, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_rcp_reap", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// attempts 1 → orphaned（无回执）；attempts 2 → orphaned（无回执）；attempts 3 → failed（有回执）
	for i := 1; i <= 3; i++ {
		claimed, err := db.ClaimServiceRuns(ctx, "inst_reap", 10)
		if err != nil || len(claimed) != 1 || claimed[0].RunID != run.RunID || claimed[0].Attempts != i {
			t.Fatalf("claim #%d: %+v %v", i, claimed, err)
		}
		forceLeaseExpired(t, db, run.RunID)
		if n, err := db.ReapExpiredServiceRuns(ctx); err != nil || n != 1 {
			t.Fatalf("reap #%d: %d %v", i, n, err)
		}
		pending, _ := db.ListUndeliveredServiceRunReceipts(ctx, 10)
		if i < 3 && len(pending) != 0 {
			t.Fatalf("orphaned reap #%d must not write receipts: %+v", i, pending)
		}
		if i == 3 {
			if len(pending) != 1 || pending[0].RunID != run.RunID || pending[0].Status != "failed" {
				t.Fatalf("final reap receipt = %+v", pending)
			}
			if !strings.Contains(string(pending[0].Payload), "lease_expired_attempts") {
				t.Fatalf("final reap receipt payload = %s", pending[0].Payload)
			}
		}
	}
	if got, err := db.GetServiceRun(ctx, run.RunID); err != nil || got.State != "failed" {
		t.Fatalf("run after attempts exhausted = %+v err=%v", got, err)
	}
}

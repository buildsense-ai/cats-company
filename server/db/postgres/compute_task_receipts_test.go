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

// Cats 算力任务终态回执的契约：
//   - 000085 与启动常量是同一份脚本（新建库与已迁移库结构一致）；
//   - 回执在 FinishComputeTask 同一事务入箱，payload 字段完整；
//   - finish 重放不重复扣费、不重复回执；终态 + 已有回执的重放不报错。

func computeTaskReceiptMigrationSQL(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000085_compute_task_receipts." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestComputeTaskReceiptMigrationMatchesStartupSchema(t *testing.T) {
	up := stripSQLComments(computeTaskReceiptMigrationSQL(t, "up"))
	want := stripSQLComments(createComputeTaskReceiptsTable)
	if up != want {
		t.Fatalf("000085_compute_task_receipts.up.sql differs from createComputeTaskReceiptsTable:\n got: %s\nwant: %s", up, want)
	}
	for _, fragment := range []string{
		"receipt_id VARCHAR(80) PRIMARY KEY",
		"quote_id VARCHAR(64) NOT NULL UNIQUE REFERENCES compute_tasks(quote_id) ON DELETE CASCADE",
		"caller_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE",
		"payload_json JSONB NOT NULL",
		"delivery_attempts INT NOT NULL DEFAULT 0",
		"delivered_at TIMESTAMPTZ DEFAULT NULL",
		"idx_compute_task_receipts_pending",
	} {
		if !strings.Contains(up, fragment) {
			t.Fatalf("000085 up migration is missing %q", fragment)
		}
	}
	down := computeTaskReceiptMigrationSQL(t, "down")
	if !strings.Contains(down, "DROP TABLE IF EXISTS compute_task_receipts;") {
		t.Fatalf("down migration must be reversible: %s", down)
	}
	source, err := os.ReadFile("schema.go")
	if err != nil {
		t.Fatal(err)
	}
	statements := string(source)
	start := strings.Index(statements, "statements := []string{")
	if start < 0 {
		t.Fatal("schema.go has no CreateSchema statement list")
	}
	list := statements[start:]
	end := strings.Index(list, "\n\t}\n")
	if end < 0 || !strings.Contains(list[:end], "\t\tcreateComputeTaskReceiptsTable,") {
		t.Fatal("CreateSchema does not run createComputeTaskReceiptsTable")
	}
	// 外键指向 compute_tasks：回执表必须排在 083 之后。
	tables := strings.Index(list[:end], "\t\tcreateComputeTaskTables,")
	receipts := strings.Index(list[:end], "\t\tcreateComputeTaskReceiptsTable,")
	if tables < 0 || receipts < 0 || tables > receipts {
		t.Fatal("CreateSchema must create compute_tasks before compute_task_receipts")
	}
}

// 回执 id 由 quote_id 确定性派生，payload 字段就是 WS 消息体（不依赖数据库）。
func TestComputeTaskReceiptPayloadContract(t *testing.T) {
	quoteID := "hpc_0123456789abcdef0123456789abcdef"
	receiptID := store.ComputeTaskReceiptIDForQuote(quoteID)
	if receiptID != "ctr_0123456789abcdef0123456789abcdef" {
		t.Fatalf("receipt id = %q", receiptID)
	}
	if again := store.ComputeTaskReceiptIDForQuote(quoteID); again != receiptID {
		t.Fatalf("receipt id is not deterministic: %q then %q", receiptID, again)
	}
	now := time.Date(2026, 3, 1, 2, 3, 4, 0, time.UTC)
	payload := store.BuildComputeTaskReceiptPayload(receiptID, quoteID, store.ComputeTaskCompleted, store.ComputeTaskCompleted,
		120_000_000, store.ComputeTaskProgress{Phase: "released", SlurmJobID: "job-7"}, now)
	var decoded store.ComputeTaskReceiptPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode receipt payload: %v (%s)", err, payload)
	}
	if decoded.ContractVersion != "catsco.compute-task-receipt.v1" || decoded.ReceiptID != receiptID ||
		decoded.QuoteID != quoteID || decoded.Status != store.ComputeTaskCompleted || decoded.State != store.ComputeTaskCompleted ||
		decoded.ChargedUnits != 120_000_000 || decoded.Progress.SlurmJobID != "job-7" || decoded.CreatedAt != "2026-03-01T02:03:04Z" {
		t.Fatalf("receipt payload = %+v", decoded)
	}
	if !store.ComputeTaskReceiptTerminal(store.ComputeTaskCompleted) || !store.ComputeTaskReceiptTerminal(store.ComputeTaskFailed) ||
		store.ComputeTaskReceiptTerminal(store.ComputeTaskActive) || store.ComputeTaskReceiptTerminal(store.ComputeTaskQueued) {
		t.Fatal("terminal state predicate is wrong")
	}
}

// 终态回执与结算同一事务：成功/失败各一份，重放不重复扣费也不重复回执。
func TestPostgresComputeTaskReceiptWrittenOnFinish(t *testing.T) {
	db := newComputeTaskTestAdapter(t)
	ctx := context.Background()
	unit := store.ModelCreditUnitsPerCredit
	uid := createCreditTopupTestUser(t, db, "compute-task-receipt-buyer")
	grantComputeTaskCredits(t, db, uid, store.ModelCreditBucketPaid, 1000, "topup:compute-task-receipt")

	now := time.Now().UTC().Truncate(time.Microsecond)
	quote := testComputeTaskQuote("hpc_0123456789abcdef0123456789abcdee", 300*unit)
	if err := db.CreateComputeTaskQuote(ctx, uid, quote); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	if _, err := db.ConfirmComputeTask(ctx, uid, quote.ID, quote.MaxUnits, now); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	lease := "hpc_44444444444444444444444444444444"
	if _, err := db.ClaimComputeTask(ctx, lease, now, now.Add(store.ComputeTaskLeaseTTL)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if pending, err := db.ListUndeliveredComputeTaskReceipts(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("receipts before finish = %+v err=%v, want none", pending, err)
	}

	final := store.ComputeTaskProgress{Phase: "released", SlurmJobID: "job-7", SlurmExitCode: "0:0", ReleasedAt: &now}
	if err := db.SaveComputeTaskProgress(ctx, quote.ID, lease, final); err != nil {
		t.Fatalf("save final progress: %v", err)
	}
	if err := db.FinishComputeTask(ctx, quote.ID, lease, final, 120*unit, true, now); err != nil {
		t.Fatalf("finish: %v", err)
	}
	receipts, err := db.ListUndeliveredComputeTaskReceipts(ctx, 10)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("after finish receipts = %+v err=%v, want 1", receipts, err)
	}
	receipt := receipts[0]
	if receipt.ReceiptID != store.ComputeTaskReceiptIDForQuote(quote.ID) || receipt.QuoteID != quote.ID ||
		receipt.CallerUID != uid || receipt.Status != store.ComputeTaskCompleted || receipt.DeliveryAttempts != 0 {
		t.Fatalf("completed receipt = %+v", receipt)
	}
	var payload store.ComputeTaskReceiptPayload
	if err := json.Unmarshal(receipt.Payload, &payload); err != nil {
		t.Fatalf("decode receipt payload: %v (%s)", err, receipt.Payload)
	}
	if payload.ContractVersion != "catsco.compute-task-receipt.v1" || payload.ReceiptID != receipt.ReceiptID ||
		payload.QuoteID != quote.ID || payload.Status != store.ComputeTaskCompleted ||
		payload.State != store.ComputeTaskCompleted || payload.ChargedUnits != 120*unit ||
		payload.Progress.Phase != "released" || payload.Progress.SlurmJobID != "job-7" || payload.CreatedAt == "" {
		t.Fatalf("receipt payload = %+v", payload)
	}

	// 逐字相同的终态重放：不报错、不二次扣费、不产生第二份回执。
	before := computeTaskAccount(t, db, uid)
	if err := db.FinishComputeTask(ctx, quote.ID, lease, final, 120*unit, true, now.Add(time.Minute)); err != nil {
		t.Fatalf("idempotent finish replay: %v", err)
	}
	if after := computeTaskAccount(t, db, uid); *after != *before {
		t.Fatalf("finish replay moved money: before=%+v after=%+v", before, after)
	}
	if events := countComputeTaskLedgerEvents(t, db, uid, quote.ID, store.ModelCreditEventSettle, store.ModelCreditBucketPaid); events != 1 {
		t.Fatalf("settle events after replay = %d, want 1", events)
	}
	if again, err := db.ListUndeliveredComputeTaskReceipts(ctx, 10); err != nil || len(again) != 1 {
		t.Fatalf("replay receipts = %+v err=%v, want 1", again, err)
	}

	// 送达标记 / 投递尝试计数：已送达的回执不再出现在待投递列。
	if err := db.TouchComputeTaskReceiptDelivery(ctx, receipt.ReceiptID); err != nil {
		t.Fatalf("touch delivery: %v", err)
	}
	if pending, _ := db.ListUndeliveredComputeTaskReceipts(ctx, 10); len(pending) != 1 || pending[0].DeliveryAttempts != 1 {
		t.Fatalf("after touch receipts = %+v, want one receipt with 1 attempt", pending)
	}
	if err := db.MarkComputeTaskReceiptDelivered(ctx, receipt.ReceiptID); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	if err := db.MarkComputeTaskReceiptDelivered(ctx, receipt.ReceiptID); err != nil {
		t.Fatalf("mark delivered twice: %v", err)
	}
	if pending, _ := db.ListUndeliveredComputeTaskReceipts(ctx, 10); len(pending) != 0 {
		t.Fatalf("pending after delivery = %+v, want none", pending)
	}

	// 失败终态也写回执（status=state=failed，扣 0 单位）。
	failedQuote := testComputeTaskQuote("hpc_ffffffffffffffffffffffffffffffff", 60*unit)
	if err := db.CreateComputeTaskQuote(ctx, uid, failedQuote); err != nil {
		t.Fatalf("create failed quote: %v", err)
	}
	if _, err := db.ConfirmComputeTask(ctx, uid, failedQuote.ID, failedQuote.MaxUnits, now); err != nil {
		t.Fatalf("confirm failed quote: %v", err)
	}
	failedLease := "hpc_55555555555555555555555555555555"
	if _, err := db.ClaimComputeTask(ctx, failedLease, now, now.Add(store.ComputeTaskLeaseTTL)); err != nil {
		t.Fatalf("claim failed quote: %v", err)
	}
	if err := db.FinishComputeTask(ctx, failedQuote.ID, failedLease, store.ComputeTaskProgress{}, 0, false, now); err != nil {
		t.Fatalf("fail quote: %v", err)
	}
	pending, err := db.ListUndeliveredComputeTaskReceipts(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("after failed finish receipts = %+v err=%v, want 1", pending, err)
	}
	if pending[0].QuoteID != failedQuote.ID || pending[0].Status != store.ComputeTaskFailed {
		t.Fatalf("failed receipt = %+v", pending[0])
	}
	var failedPayload store.ComputeTaskReceiptPayload
	if err := json.Unmarshal(pending[0].Payload, &failedPayload); err != nil {
		t.Fatalf("decode failed receipt payload: %v", err)
	}
	if failedPayload.Status != store.ComputeTaskFailed || failedPayload.State != store.ComputeTaskFailed || failedPayload.ChargedUnits != 0 {
		t.Fatalf("failed receipt payload = %+v", failedPayload)
	}

	// 级联：删掉终态任务行会带走它的回执，不留孤儿回执（外键 ON DELETE CASCADE）。
	if _, err := db.db.Exec(`DELETE FROM compute_tasks WHERE quote_id = $1`, failedQuote.ID); err != nil {
		t.Fatalf("delete compute task: %v", err)
	}
	var remaining int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM compute_task_receipts WHERE quote_id = $1`, failedQuote.ID).Scan(&remaining); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("receipts left after task deletion = %d, want 0", remaining)
	}
}

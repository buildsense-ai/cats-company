package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 生命周期回注收件箱（PG 000033 同语义镜像）：终态事务写回执 / 待投递列 / 送达标记。
func TestMySQLServiceRunReceipts(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()
	aID := seedMySQLServiceAgent(t, db)

	newRunningRun := func(callerUID int64) string {
		t.Helper()
		sessID, err := db.CreateServiceSession(ctx, aID, 1, callerUID, 0, "")
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, fmt.Sprintf("ct_rcp_%d", time.Now().UnixNano()), json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		claimed, err := db.ClaimServiceRuns(ctx, "inst_rcp_mysql", 10)
		if err != nil || len(claimed) != 1 || claimed[0].RunID != run.RunID {
			t.Fatalf("claim run %s: %+v %v", run.RunID, claimed, err)
		}
		return run.RunID
	}

	// completed → 回执入箱（幂等重放不重复）
	run1 := newRunningRun(441)
	if completed, err := db.CompleteServiceRun(ctx, run1, "res_1", json.RawMessage(`{"text":"ok"}`), nil); err != nil || !completed {
		t.Fatalf("complete run1: %v %v", completed, err)
	}
	if _, err := db.CompleteServiceRun(ctx, run1, "res_1", json.RawMessage(`{"text":"ok"}`), nil); err != nil {
		t.Fatalf("idempotent complete replay: %v", err)
	}
	receipts, err := db.ListUndeliveredServiceRunReceipts(ctx, 10)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("after complete receipts = %+v err=%v, want 1", receipts, err)
	}
	if receipts[0].ReceiptID != "rcp_"+strings.TrimPrefix(run1, "run_") || receipts[0].Status != "completed" || receipts[0].CallerUID != 441 {
		t.Fatalf("completed receipt = %+v", receipts[0])
	}
	var payload map[string]any
	if err := json.Unmarshal(receipts[0].Payload, &payload); err != nil || payload["contract_version"] != "catsco.service-run-receipt.v1" {
		t.Fatalf("receipt payload = %s (err=%v)", receipts[0].Payload, err)
	}

	// awaiting_input → 回执含 question；重放幂等；complete 撞错态
	run2 := newRunningRun(442)
	envelope := json.RawMessage(`{"contract_version":"catsco.service-run-result.v1","status":"awaiting_input","output":{"question":"请补充必要素材"}}`)
	if awaited, err := db.AwaitServiceRunInput(ctx, run2, "awt_1", envelope); err != nil || !awaited {
		t.Fatalf("await run2: %v %v", awaited, err)
	}
	if awaited, err := db.AwaitServiceRunInput(ctx, run2, "awt_1", envelope); err != nil || awaited {
		t.Fatalf("await replay must be idempotent: %v %v", awaited, err)
	}
	if _, err := db.CompleteServiceRun(ctx, run2, "res_2", nil, nil); !errors.Is(err, store.ErrServiceRunConflict) {
		t.Fatalf("complete after awaiting = %v, want ErrServiceRunConflict", err)
	}
	receipts, _ = db.ListUndeliveredServiceRunReceipts(ctx, 10)
	if len(receipts) != 2 {
		t.Fatalf("after await receipts = %+v, want 2", receipts)
	}
	foundAwait := false
	for _, r := range receipts {
		if r.RunID == run2 {
			foundAwait = r.Status == "awaiting_input" && strings.Contains(string(r.Payload), "请补充必要素材")
		}
	}
	if !foundAwait {
		t.Fatalf("awaiting receipt missing/incorrect: %+v", receipts)
	}

	// failed（不可重试）→ 回执；orphaned（可重试）不写
	run3 := newRunningRun(443)
	if err := db.FailServiceRun(ctx, run3, "boom", false); err != nil {
		t.Fatalf("fail run3: %v", err)
	}
	if receiptsAfter, _ := db.ListUndeliveredServiceRunReceipts(ctx, 10); len(receiptsAfter) != 3 {
		t.Fatalf("after failed receipts = %+v, want 3", receiptsAfter)
	}
	run4 := newRunningRun(444)
	if err := db.FailServiceRun(ctx, run4, "blip", true); err != nil {
		t.Fatalf("retryable fail run4: %v", err)
	}
	if receiptsAfter, _ := db.ListUndeliveredServiceRunReceipts(ctx, 10); len(receiptsAfter) != 3 {
		t.Fatalf("orphaned must not write receipts: %+v", receiptsAfter)
	}

	// 送达标记 + 尝试计数
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
	bumped := false
	for _, r := range pending {
		if r.ReceiptID == receipts[1].ReceiptID && r.DeliveryAttempts == 1 {
			bumped = true
		}
	}
	if !bumped {
		t.Fatalf("delivery attempts not bumped: %+v", pending)
	}
}

package postgres

import (
	"context"
	"encoding/json"
	"testing"
)

// 准入闸（bot_busy / caller_busy）拒掉的单发生在派发之前：它没拿过租约，attempts
// 永远停在 0，也就没有别的执行方会「接管」它。以前这种单落到 orphaned，于是永久
// 非终态——调用方等不到回执，看板把它算成「卡住」，告警每两小时重提醒一次。
func TestServiceRunFailOnAdmissionRejectionIsTerminal(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")
	sessID, err := db.CreateServiceSession(ctx, aID, 1, 451, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_never_dispatched", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// retryable=true：调用方可以重试，但这张单本身已经没有可继续的执行方。
	if err := db.FailServiceRun(ctx, run.RunID, "caller_busy: 并发上限", true); err != nil {
		t.Fatalf("fail service run: %v", err)
	}

	got, err := db.GetServiceRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if got.State != "failed" {
		t.Fatalf("state = %q, want failed: a run that was never dispatched cannot be taken over", got.State)
	}
	var receipts int
	if err := db.db.QueryRow(`SELECT count(*) FROM service_run_receipts WHERE run_id = $1`, run.RunID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("receipts = %d, want 1: callers must be told the run ended", receipts)
	}
	if claimed, err := db.ClaimServiceRuns(ctx, "inst_after_reject", 10); err != nil || len(claimed) != 0 {
		t.Fatalf("a rejected run must not be claimable: %+v %v", claimed, err)
	}
}

// 历史残留：修复前被准入闸拒掉的单停在 orphaned 且 attempts=0，reaper 的两条
// 通道（running 租约过期、submitted 没人领）都扫不到它。这里验证 reaper 会把这档
// 收成终态，同时不碰刚写进来、还可能被接管的记录。
func TestServiceReapClosesNeverDispatchedOrphan(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")
	sessID, err := db.CreateServiceSession(ctx, aID, 1, 452, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	stale, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_orphan_stale", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("create stale run: %v", err)
	}
	// 一个会话同时只允许一张在跑的单，第二条换会话建。
	freshSessID, err := db.CreateServiceSession(ctx, aID, 1, 453, 0, "")
	if err != nil {
		t.Fatalf("create fresh session: %v", err)
	}
	fresh, _, err := db.CreateServiceRunIdempotent(ctx, freshSessID, "ct_orphan_fresh", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("create fresh run: %v", err)
	}
	for _, runID := range []string{stale.RunID, fresh.RunID} {
		if _, err := db.db.Exec(`UPDATE service_runs SET state='orphaned', attempts=0, error_text='caller_busy: 并发上限' WHERE run_id=$1`, runID); err != nil {
			t.Fatalf("seed orphan %s: %v", runID, err)
		}
	}
	if _, err := db.db.Exec(`UPDATE service_runs SET created_at = now() - interval '20 minutes' WHERE run_id=$1`, stale.RunID); err != nil {
		t.Fatalf("age stale orphan: %v", err)
	}

	if _, err := db.ReapExpiredServiceRuns(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}

	gotStale, err := db.GetServiceRun(ctx, stale.RunID)
	if err != nil {
		t.Fatalf("read stale run: %v", err)
	}
	if gotStale.State != "failed" {
		t.Fatalf("stale orphan state = %q, want failed", gotStale.State)
	}
	var receipts int
	if err := db.db.QueryRow(`SELECT count(*) FROM service_run_receipts WHERE run_id = $1`, stale.RunID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("stale orphan receipts = %d, want 1", receipts)
	}
	gotFresh, err := db.GetServiceRun(ctx, fresh.RunID)
	if err != nil {
		t.Fatalf("read fresh run: %v", err)
	}
	if gotFresh.State != "orphaned" {
		t.Fatalf("fresh orphan state = %q, want orphaned (the grace window must hold)", gotFresh.State)
	}
}

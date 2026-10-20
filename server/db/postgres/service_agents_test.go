package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func seedHelloAgent(t *testing.T, db *Adapter) {
	t.Helper()
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql")
	// 终态事务含回执写入（000033）——与生产迁移顺序一致。
	execRetireV4MigrationFile(t, db, "000033_service_run_receipts.up.sql")
	// 目录订阅清单（000034）——与生产迁移顺序一致。
	execRetireV4MigrationFile(t, db, "000034_service_agent_subscriptions.up.sql")
	// 归属与可见性（000045）。
	execRetireV4MigrationFile(t, db, "000045_service_agents_ownership.up.sql")
}

func agentID(t *testing.T, st *Adapter, slug string) int64 {
	t.Helper()
	id, _, _, _, err := st.GetServiceAgentBySlug(context.Background(), slug)
	if err != nil {
		t.Fatalf("get service agent %s: %v", slug, err)
	}
	return id
}

func forceLeaseExpired(t *testing.T, db *Adapter, runID string) {
	t.Helper()
	if _, err := db.db.Exec(`UPDATE service_runs SET lease_expires_at = now() - interval '1 minute' WHERE run_id = $1`, runID); err != nil {
		t.Fatalf("force lease expired: %v", err)
	}
}

func TestServiceRunsClaimLeaseAndIdempotency(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")

	sessID, err := db.CreateServiceSession(ctx, aID, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	r1, existed, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_1", json.RawMessage(`{"hi":1}`))
	if err != nil || existed {
		t.Fatalf("fresh run must not be idempotent hit: %+v existed=%v err=%v", r1, existed, err)
	}
	r2, existed, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_1", json.RawMessage(`{"hi":1}`))
	if err != nil || !existed || r2.RunID != r1.RunID {
		t.Fatalf("idempotency broken: %+v %+v existed=%v err=%v", r1, r2, existed, err)
	}
	// 同会话另一 client_token → 活跃 run 存在 → busy（保串行）
	if _, _, errBusy := db.CreateServiceRunIdempotent(ctx, sessID, "ct_2", json.RawMessage(`{}`)); !errors.Is(errBusy, store.ErrServiceSessionBusy) {
		t.Fatalf("want ErrServiceSessionBusy, got %v", errBusy)
	}

	claimed, err := db.ClaimServiceRuns(ctx, "inst_A", 10)
	if err != nil || len(claimed) != 1 || claimed[0].RunID != r1.RunID {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	again, err := db.ClaimServiceRuns(ctx, "inst_B", 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("double claim must be empty: %+v %v", again, err)
	}
	// renew 仅限租约持有者
	if ok, err := db.RenewServiceLease(ctx, r1.RunID, "inst_B"); err != nil || ok {
		t.Fatalf("renew by wrong instance: ok=%v err=%v", ok, err)
	}
	if ok, err := db.RenewServiceLease(ctx, r1.RunID, "inst_A"); err != nil || !ok {
		t.Fatalf("renew by owner: ok=%v err=%v", ok, err)
	}

	// 租约过期 → reaper → 可被重新领取（接管续跑）
	forceLeaseExpired(t, db, r1.RunID)
	n, err := db.ReapExpiredServiceRuns(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reap: %d %v", n, err)
	}
	reclaimed, err := db.ClaimServiceRuns(ctx, "inst_B", 10)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("orphaned run must be reclaimable: %+v %v", reclaimed, err)
	}
}

func TestServiceRunEventsCheckpointAndCompletion(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")
	sessID, err := db.CreateServiceSession(ctx, aID, 1, 441, 0, "assetsH")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_ev", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if claimed, err := db.ClaimServiceRuns(ctx, "inst_A", 10); err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}

	seq1, err := db.AppendServiceRunEvent(ctx, run.RunID, "progress", json.RawMessage(`{"p":1}`))
	if err != nil || seq1 != 1 {
		t.Fatalf("append1: %d %v", seq1, err)
	}
	seq2, err := db.AppendServiceRunEvent(ctx, run.RunID, "result", json.RawMessage(`{"answer":"ok"}`))
	if err != nil || seq2 != 2 {
		t.Fatalf("append2: %d %v", seq2, err)
	}
	events, err := db.ListServiceRunEvents(ctx, run.RunID, 0, 10)
	if err != nil || len(events) != 2 || events[1].Type != "result" {
		t.Fatalf("events: %+v %v", events, err)
	}
	tail, err := db.ListServiceRunEvents(ctx, run.RunID, seq1, 10)
	if err != nil || len(tail) != 1 || tail[0].Seq != seq2 {
		t.Fatalf("cursor tail: %+v %v", tail, err)
	}

	if err := db.SaveServiceCheckpoint(ctx, sessID, 1, json.RawMessage(`{"step":1}`)); err != nil {
		t.Fatalf("save ckpt 1: %v", err)
	}
	if err := db.SaveServiceCheckpoint(ctx, sessID, 2, json.RawMessage(`{"step":2}`)); err != nil {
		t.Fatalf("save ckpt 2: %v", err)
	}
	rev, snap, err := db.LatestServiceCheckpoint(ctx, sessID)
	var ckpt struct {
		Step int `json:"step"`
	}
	if err != nil || rev != 2 || json.Unmarshal(snap, &ckpt) != nil || ckpt.Step != 2 {
		t.Fatalf("latest ckpt: rev=%d snap=%s err=%v", rev, snap, err)
	}

	applied, err := db.CompleteServiceRun(ctx, run.RunID, "res_1", json.RawMessage(`{"ok":true}`), json.RawMessage(`{"prompt_tokens":1}`))
	if err != nil || !applied {
		t.Fatalf("complete: applied=%v err=%v", applied, err)
	}
	again, err := db.CompleteServiceRun(ctx, run.RunID, "res_1", json.RawMessage(`{"ok":true}`), nil)
	if err != nil || again {
		t.Fatalf("complete idempotent replay: applied=%v err=%v", again, err)
	}
	got, err := db.GetServiceRun(ctx, run.RunID)
	if err != nil || got.State != "completed" {
		t.Fatalf("get run after complete: %+v %v", got, err)
	}
}

func TestServiceRunFailAndAttemptsExhaustion(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	aID := agentID(t, db, "hello")

	// 三次租约过期 → attempts>=3 → failed
	sessID, err := db.CreateServiceSession(ctx, aID, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	for i := 1; i <= 3; i++ {
		claimed, err := db.ClaimServiceRuns(ctx, "inst_A", 10)
		if err != nil || len(claimed) != 1 || claimed[0].Attempts != i {
			t.Fatalf("claim %d: %+v %v", i, claimed, err)
		}
		forceLeaseExpired(t, db, run.RunID)
		if _, err := db.ReapExpiredServiceRuns(ctx); err != nil {
			t.Fatalf("reap %d: %v", i, err)
		}
	}
	got, err := db.GetServiceRun(ctx, run.RunID)
	if err != nil || got.State != "failed" {
		t.Fatalf("after 3 attempts expect failed: %+v %v", got, err)
	}

	// fail(retryable) → orphaned → 可重领；再 fail(fatal) → failed
	sess2, err := db.CreateServiceSession(ctx, aID, 1, 442, 0, "")
	if err != nil {
		t.Fatalf("create session2: %v", err)
	}
	run2, _, err := db.CreateServiceRunIdempotent(ctx, sess2, "ct_fail2", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("create run2: %v", err)
	}
	if claimed, err := db.ClaimServiceRuns(ctx, "inst_A", 10); err != nil || len(claimed) != 1 {
		t.Fatalf("claim run2: %+v %v", claimed, err)
	}
	if err := db.FailServiceRun(ctx, run2.RunID, "transient boom", true); err != nil {
		t.Fatalf("fail retryable: %v", err)
	}
	if got2, err := db.GetServiceRun(ctx, run2.RunID); err != nil || got2.State != "orphaned" {
		t.Fatalf("retryable fail expect orphaned: %+v %v", got2, err)
	}
	if claimed, err := db.ClaimServiceRuns(ctx, "inst_B", 10); err != nil || len(claimed) != 1 {
		t.Fatalf("reclaim run2: %+v %v", claimed, err)
	}
	if err := db.FailServiceRun(ctx, run2.RunID, "fatal", false); err != nil {
		t.Fatalf("fail fatal: %v", err)
	}
	if got2, err := db.GetServiceRun(ctx, run2.RunID); err != nil || got2.State != "failed" {
		t.Fatalf("fatal fail expect failed: %+v %v", got2, err)
	}
}

// 幂等键可选：不带 client_token 的调用每次入库新 run（NULL 语义；此前空串参与
// UNIQUE(session_id, client_token) → 同会话第二次无 token 调用永久撞唯一约束）。
func TestServiceRunsWithoutClientToken(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	// 000027 是历史 DDL（client_token NOT NULL）；新语义需 000031 迁移（结构变更 + 回填）。
	execRetireV4MigrationFile(t, db, "000031_service_runs_client_token_nullable.up.sql")
	ctx := context.Background()
	aID := agentID(t, db, "hello")

	sessID, err := db.CreateServiceSession(ctx, aID, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	first, existed, err := db.CreateServiceRunIdempotent(ctx, sessID, "", json.RawMessage(`{"n":1}`))
	if err != nil || existed {
		t.Fatalf("first tokenless run: %+v existed=%v err=%v", first, existed, err)
	}
	if claimed, err := db.ClaimServiceRuns(ctx, "inst_nt", 10); err != nil || len(claimed) != 1 {
		t.Fatalf("claim first: %+v err=%v", claimed, err)
	}
	if _, err := db.CompleteServiceRun(ctx, first.RunID, "res_nt_1", json.RawMessage(`{"ok":true}`), nil); err != nil {
		t.Fatalf("complete first: %v", err)
	}
	second, existed, err := db.CreateServiceRunIdempotent(ctx, sessID, "", json.RawMessage(`{"n":2}`))
	if err != nil || existed {
		t.Fatalf("second tokenless run must succeed: %+v existed=%v err=%v", second, existed, err)
	}
	if second.RunID == first.RunID {
		t.Fatalf("tokenless runs must be distinct: %s", second.RunID)
	}
	if second.ClientToken != "" {
		t.Fatalf("NULL token must scan back as empty string, got %q", second.ClientToken)
	}
}

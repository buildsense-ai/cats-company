package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func mysqlTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	dsn := os.Getenv("CATS_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN to run MySQL integration tests")
	}
	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create MySQL schema: %v", err)
	}
	return db
}

func seedMySQLServiceAgent(t *testing.T, db *Adapter) int64 {
	t.Helper()
	slug := fmt.Sprintf("svc-test-%d", time.Now().UnixNano())
	res, err := db.db.Exec(`INSERT INTO service_agents (slug, name) VALUES (?, ?)`, slug, "svc test agent")
	if err != nil {
		t.Fatalf("insert service agent: %v", err)
	}
	agentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("agent insert id: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO service_agent_versions (agent_id, version, definition_json, assets_hash) VALUES (?, 1, '{"async_threshold_seconds": 60, "models": ["gpt-6.1-sol"]}', '')`, agentID); err != nil {
		t.Fatalf("insert service agent version: %v", err)
	}
	t.Cleanup(func() {
		db.db.Exec(`DELETE FROM service_sessions WHERE agent_id = ?`, agentID)
		db.db.Exec(`DELETE FROM service_agent_versions WHERE agent_id = ?`, agentID)
		db.db.Exec(`DELETE FROM service_agents WHERE id = ?`, agentID)
	})
	return agentID
}

func forceMySQLLeaseExpired(t *testing.T, db *Adapter, runID string) {
	t.Helper()
	if _, err := db.db.Exec(`UPDATE service_runs SET lease_expires_at = DATE_SUB(NOW(6), INTERVAL 1 MINUTE) WHERE run_id = ?`, runID); err != nil {
		t.Fatalf("force lease expired: %v", err)
	}
}

func TestMySQLServiceRunClaimLeaseAndIdempotency(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()
	aID := seedMySQLServiceAgent(t, db)

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
	if ok, err := db.RenewServiceLease(ctx, r1.RunID, "inst_B"); err != nil || ok {
		t.Fatalf("renew by wrong instance: ok=%v err=%v", ok, err)
	}
	if ok, err := db.RenewServiceLease(ctx, r1.RunID, "inst_A"); err != nil || !ok {
		t.Fatalf("renew by owner: ok=%v err=%v", ok, err)
	}

	forceMySQLLeaseExpired(t, db, r1.RunID)
	n, err := db.ReapExpiredServiceRuns(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reap: %d %v", n, err)
	}
	reclaimed, err := db.ClaimServiceRuns(ctx, "inst_B", 10)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("orphaned run must be reclaimable: %+v %v", reclaimed, err)
	}
}

func TestMySQLServiceRunEventsCheckpointAndCompletion(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()
	aID := seedMySQLServiceAgent(t, db)

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

func TestMySQLServiceRunFailAndAttemptsExhaustion(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()
	aID := seedMySQLServiceAgent(t, db)

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
		forceMySQLLeaseExpired(t, db, run.RunID)
		if _, err := db.ReapExpiredServiceRuns(ctx); err != nil {
			t.Fatalf("reap %d: %v", i, err)
		}
	}
	got, err := db.GetServiceRun(ctx, run.RunID)
	if err != nil || got.State != "failed" {
		t.Fatalf("after 3 attempts expect failed: %+v %v", got, err)
	}

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

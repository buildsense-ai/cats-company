package mysql

import (
	"context"
	"encoding/json"
	"testing"
)

// TestMySQLServiceReapFailsAfterThreeAttempts 验证续跑上限：租约过期→reaper→接管，
// attempts 递增，第 3 次后判 failed 且不再可领（计划① 验收清单）。
func TestMySQLServiceReapFailsAfterThreeAttempts(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()
	aID := seedMySQLServiceAgent(t, db)
	sessID, err := db.CreateServiceSession(ctx, aID, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_reap3", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// 第 1、2 轮：领 → 租约过期 → reaper → orphaned（可再领，attempts 递增）
	for round := 1; round <= 2; round++ {
		claimed, err := db.ClaimServiceRuns(ctx, "inst_A", 10)
		if err != nil || len(claimed) != 1 || claimed[0].RunID != run.RunID || claimed[0].Attempts != round {
			t.Fatalf("round %d claim: %+v %v", round, claimed, err)
		}
		forceMySQLLeaseExpired(t, db, run.RunID)
		if n, err := db.ReapExpiredServiceRuns(ctx); err != nil || n != 1 {
			t.Fatalf("round %d reap: %d %v", round, n, err)
		}
		if got, err := db.GetServiceRun(ctx, run.RunID); err != nil || got.State != "orphaned" {
			t.Fatalf("round %d state = %+v err=%v, want orphaned", round, got, err)
		}
	}

	// 第 3 轮：领（attempts=3）→ 过期 → reaper → failed，且不可再领
	claimed, err := db.ClaimServiceRuns(ctx, "inst_C", 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 3 {
		t.Fatalf("third claim: %+v %v", claimed, err)
	}
	forceMySQLLeaseExpired(t, db, run.RunID)
	if n, err := db.ReapExpiredServiceRuns(ctx); err != nil || n != 1 {
		t.Fatalf("third reap: %d %v", n, err)
	}
	final, err := db.GetServiceRun(ctx, run.RunID)
	if err != nil || final.State != "failed" {
		t.Fatalf("final state = %+v err=%v, want failed", final, err)
	}
	var errText string
	if err := db.db.QueryRow(`SELECT error_text FROM service_runs WHERE run_id = ?`, run.RunID).Scan(&errText); err != nil || errText != "lease_expired_attempts" {
		t.Fatalf("error_text = %q err=%v, want lease_expired_attempts", errText, err)
	}
	if empty, err := db.ClaimServiceRuns(ctx, "inst_D", 10); err != nil || len(empty) != 0 {
		t.Fatalf("failed run must not be claimable: %+v %v", empty, err)
	}
}

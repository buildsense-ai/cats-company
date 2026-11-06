package postgres

import (
	"context"
	"encoding/json"
	"testing"
)

// 一条龙（真库）：上架 → 跑一次 run → 下架 → 彻底删除。
//
// 断言三件事：
//  1. 还在架时不能删（必须先收回）；
//  2. 删完 agent / 版本 / 会话 / run 全没了；
//  3. **账本不清**：service_run_ledger 是账务凭证，run 删了它也要留着。
func TestPurgeServiceAgentRemovesRuntimeDataKeepsLedger(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	// 发布通道要写 package_json（000029 的加法迁移；seedHelloAgent 没带这一条）。
	execRetireV4MigrationFile(t, db, "000029_service_agent_versions_package.up.sql")
	// 账本两张迁移：purge 必须留着账本，所以这个测试得先把表建出来。
	execRetireV4MigrationFile(t, db, "000046_service_run_ledger.up.sql")
	execRetireV4MigrationFile(t, db, "000047_service_run_ledger_billing_state.up.sql")
	ctx := context.Background()
	const slug = "purge-me"
	const owner = int64(441)

	if err := db.ClaimServiceAgentOwnership(ctx, slug, "Purge Me", "user", owner, "private"); err != nil {
		t.Fatalf("claim ownership: %v", err)
	}
	if _, err := db.PublishServiceAgentPackage(ctx, slug, "Purge Me",
		json.RawMessage(`{"runner":{"type":"codex"}}`), "hash-purge-me", json.RawMessage(`{}`), false); err != nil {
		t.Fatalf("publish: %v", err)
	}
	agent := agentID(t, db, slug)

	sessID, err := db.CreateServiceSession(ctx, agent, 1, owner, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_purge", json.RawMessage(`{"text":"x"}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := db.StartServiceRun(ctx, run.RunID, "inst-purge", 60); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := db.CompleteServiceRun(ctx, run.RunID, "res-purge", json.RawMessage(`{"output":{"text":"ok"}}`), nil); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	// 手工落一条账本行：purge 之后它必须还在。
	if _, err := db.db.Exec(`
		INSERT INTO service_run_ledger
			(run_id, session_id, agent_id, agent_slug, owner_kind, owner_uid, caller_uid,
			 runner_type, status, pricing_json, currency, gross_micros, fee_micros, net_micros,
			 payer_uid, payee_uid, finished_at, billing_state)
		VALUES ($1, $2, $3, $4, 'user', $5, $5, 'codex', 'completed',
		        '{"unit":"run","per_run":0.05}'::jsonb, 'CNY', 50000, 0, 50000, $5, $5, now(), 'free')`,
		run.RunID, sessID, agent, slug, owner); err != nil {
		t.Fatalf("insert ledger: %v", err)
	}

	// 还在架：拒绝删除。
	if _, err := db.PurgeServiceAgent(ctx, slug, owner); err == nil {
		t.Fatalf("purge while active should fail")
	}

	if err := db.SetServiceAgentState(ctx, slug, owner, false); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	result, err := db.PurgeServiceAgent(ctx, slug, owner)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if result.Versions == 0 || result.Sessions != 1 || result.Runs != 1 {
		t.Fatalf("purge result = %+v, want versions>0 sessions=1 runs=1", result)
	}

	if _, _, _, _, err := db.GetServiceAgentBySlug(ctx, slug); err == nil {
		t.Fatalf("agent still present after purge")
	}
	var sessions, runs, ledger int64
	if err := db.db.QueryRow(`SELECT count(*) FROM service_sessions WHERE agent_id = $1`, agent).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM service_runs WHERE run_id = $1`, run.RunID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM service_run_ledger WHERE run_id = $1`, run.RunID).Scan(&ledger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if sessions != 0 || runs != 0 {
		t.Fatalf("runtime data left: sessions=%d runs=%d, want 0/0", sessions, runs)
	}
	if ledger != 1 {
		t.Fatalf("ledger rows = %d, want 1 (账本不能被删)", ledger)
	}
}

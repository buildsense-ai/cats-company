package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// seedBotRunnerAgent 造一个 runner.type=bot 的应用（与 hello 的 codex 默认形态对照）。
func seedBotRunnerAgent(t *testing.T, db *Adapter, slug string, botUID int64) {
	t.Helper()
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql")
	execRetireV4MigrationFile(t, db, "000033_service_run_receipts.up.sql")
	execRetireV4MigrationFile(t, db, "000045_service_agents_ownership.up.sql")

	if _, err := db.db.Exec(`
		INSERT INTO service_agents (slug, name, state, owner_kind, owner_uid, visibility)
		VALUES ($1, $1, 'active', 'internal', 0, 'public')`, slug); err != nil {
		t.Fatalf("insert bot runner agent: %v", err)
	}
	definition := fmt.Sprintf(`{"runner":{"type":"bot","bot_uid":%d},"async_threshold_seconds":30}`, botUID)
	if _, err := db.db.Exec(`
		INSERT INTO service_agent_versions (agent_id, version, definition_json)
		SELECT id, 1, $2::jsonb FROM service_agents WHERE slug = $1`, slug, definition); err != nil {
		t.Fatalf("insert bot runner agent version: %v", err)
	}
}

// bot 型 run 没有人能接管（claim 只认 codex/worker），attempts 永远不会增长。
// 租约过期后如果像其它 runner 一样留在 orphaned，就是永久悬挂——调用方轮询一辈子
// 也等不到结论。这里断言它直接判终态失败，并且回执入箱。
func TestServiceReapFailsBotRunnerRunInsteadOfOrphaning(t *testing.T) {
	db := pgTestDB(t)
	seedBotRunnerAgent(t, db, "bot-lost", 4242)
	ctx := context.Background()

	agent := agentID(t, db, "bot-lost")
	sessID, err := db.CreateServiceSession(ctx, agent, 1, 441, 0, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, _, err := db.CreateServiceRunIdempotent(ctx, sessID, "ct_bot_lost", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	// 模拟 bot runner 已经抢占（StartServiceRun）之后平台进程崩掉，租约到期。
	if _, err := db.StartServiceRun(ctx, run.RunID, "bot:4242", 60); err != nil {
		t.Fatalf("start run: %v", err)
	}
	forceLeaseExpired(t, db, run.RunID)

	if n, err := db.ReapExpiredServiceRuns(ctx); err != nil || n < 1 {
		t.Fatalf("reap: n=%d err=%v, want >=1", n, err)
	}

	got, err := db.GetServiceRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.State != "failed" {
		t.Fatalf("state = %q, want failed（bot 型不该留在 orphaned）", got.State)
	}
	var errText string
	if err := db.db.QueryRow(`SELECT COALESCE(error_text, '') FROM service_runs WHERE run_id = $1`, run.RunID).Scan(&errText); err != nil {
		t.Fatalf("read error_text: %v", err)
	}
	if errText != "bot_runner_lost" {
		t.Fatalf("error_text = %q, want bot_runner_lost", errText)
	}
	var status string
	if err := db.db.QueryRow(`SELECT status FROM service_run_receipts WHERE run_id = $1`, run.RunID).Scan(&status); err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if status != "failed" {
		t.Fatalf("receipt status = %q, want failed", status)
	}
}

package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestServiceAgentsSchemaConstants(t *testing.T) {
	checks := map[string]string{
		createServiceSessionsTable:  "CREATE INDEX IF NOT EXISTS idx_service_sessions_caller",
		createServiceRunsTable:      "UNIQUE (session_id, client_token)",
		createServiceRunEventsTable: "PRIMARY KEY (run_id, seq)",
	}
	for c, sub := range checks {
		if !strings.Contains(c, sub) {
			t.Fatalf("schema missing %q", sub)
		}
	}
	// Fencing（设计 §4.8-1；计划 Task 2 补录：lease_epoch 列必须在 DDL）
	if !strings.Contains(createServiceRunsTable, "lease_epoch") {
		t.Fatal("service_runs missing lease_epoch (fencing)")
	}
}

func TestServiceAgentsMigrationMatchesConstant(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000027_service_agents.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(up), "CREATE TABLE IF NOT EXISTS service_sessions") {
		t.Fatalf("migration file missing sessions table: %s", up)
	}
	if !strings.Contains(string(up), "lease_epoch") {
		t.Fatal("migration file missing lease_epoch (fencing)")
	}
}

// pgTestDB 打开一个 DSN gated 的独立 schema 连接（照 artifact_runtime 集成测试模式）。
func pgTestDB(t *testing.T) *Adapter {
	t.Helper()
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_service_agents_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	t.Cleanup(func() { base.Close() })
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create service agents schema: %v", err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`) })
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open service agents database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// 000027 必须可在空库/已有库上幂等执行（DDL 全部 IF NOT EXISTS、种子 DO NOTHING）。
func TestServiceAgentsMigrationFilesExecute(t *testing.T) {
	db := pgTestDB(t)
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql")
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql") // 幂等重放
	execRetireV4MigrationFile(t, db, "000027_service_agents.down.sql")
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql") // 再上，供后续用例使用
}

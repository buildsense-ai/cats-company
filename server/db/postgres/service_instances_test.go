package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// 实例心跳注册表（000032）：schema 常量 / 编号迁移 / 运行时 CreateSchema / UPSERT 往返。

func TestServiceInstanceRegistrySchemaConstants(t *testing.T) {
	if !strings.Contains(createServiceInstancesTable, "instance_id VARCHAR(64) PRIMARY KEY") {
		t.Fatal("service_instances table missing primary key")
	}
	if !strings.Contains(createServiceInstancesTable, "last_seen_at TIMESTAMPTZ") {
		t.Fatal("service_instances table missing last_seen_at")
	}
	if !strings.Contains(createServiceRunsCreatedAtIndex, "idx_service_runs_created_at") {
		t.Fatal("missing service_runs created_at index")
	}
}

func TestServiceInstanceRegistryMigrationMatchesRuntimeSchema(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000032_service_instance_heartbeat_registry.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS service_instances", "idx_service_runs_created_at"} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("migration missing %q: %s", want, up)
		}
	}
	down, err := os.ReadFile("../migrations/postgres/000032_service_instance_heartbeat_registry.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP TABLE IF EXISTS service_instances") {
		t.Fatalf("down migration must be reversible: %s", down)
	}
}

// 000032 在 000027 之上幂等执行，且可回滚再上。
func TestServiceInstanceRegistryMigrationFilesExecute(t *testing.T) {
	db := pgTestDB(t)
	execRetireV4MigrationFile(t, db, "000027_service_agents.up.sql")
	execRetireV4MigrationFile(t, db, "000032_service_instance_heartbeat_registry.up.sql")
	execRetireV4MigrationFile(t, db, "000032_service_instance_heartbeat_registry.up.sql") // 幂等重放
	execRetireV4MigrationFile(t, db, "000032_service_instance_heartbeat_registry.down.sql")
	execRetireV4MigrationFile(t, db, "000032_service_instance_heartbeat_registry.up.sql")
}

// CreateSchema（运行时权威）后 UPSERT 往返：新实例登记 → 心跳刷新 → last_seen 前进、指标更新。
func TestServiceInstanceHeartbeatRegistry(t *testing.T) {
	db := pgTestDB(t)
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	ctx := context.Background()

	if err := db.RecordServiceInstanceHeartbeat(ctx, "catsco-agents-1", 2, json.RawMessage(`{"tokens_total":10}`)); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	var firstSeen, lastSeen time.Time
	var activeRuns int
	var metrics []byte
	if err := db.db.QueryRowContext(ctx, `
		SELECT active_runs, metrics_json, first_seen_at, last_seen_at
		FROM service_instances WHERE instance_id = $1`, "catsco-agents-1").
		Scan(&activeRuns, &metrics, &firstSeen, &lastSeen); err != nil {
		t.Fatalf("select after first heartbeat: %v", err)
	}
	if activeRuns != 2 || !strings.Contains(string(metrics), "tokens_total") {
		t.Fatalf("first heartbeat row = active=%d metrics=%s", activeRuns, metrics)
	}

	time.Sleep(10 * time.Millisecond)
	if err := db.RecordServiceInstanceHeartbeat(ctx, "catsco-agents-1", 0, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	var secondLastSeen time.Time
	if err := db.db.QueryRowContext(ctx, `
		SELECT active_runs, last_seen_at FROM service_instances WHERE instance_id = $1`, "catsco-agents-1").
		Scan(&activeRuns, &secondLastSeen); err != nil {
		t.Fatalf("select after second heartbeat: %v", err)
	}
	if activeRuns != 0 {
		t.Fatalf("upsert must refresh active_runs, got %d", activeRuns)
	}
	if !secondLastSeen.After(lastSeen) {
		t.Fatalf("last_seen must advance: %v -> %v", lastSeen, secondLastSeen)
	}
	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_instances`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("upsert must keep one row per instance, got %d", count)
	}

	// 负值防御 + 空 metrics 兜底
	if err := db.RecordServiceInstanceHeartbeat(ctx, "catsco-agents-2", -5, nil); err != nil {
		t.Fatalf("negative heartbeat: %v", err)
	}
	if err := db.db.QueryRowContext(ctx, `
		SELECT active_runs FROM service_instances WHERE instance_id = $1`, "catsco-agents-2").Scan(&activeRuns); err != nil {
		t.Fatalf("select negative clamp: %v", err)
	}
	if activeRuns != 0 {
		t.Fatalf("negative active_runs must clamp to 0, got %d", activeRuns)
	}
}

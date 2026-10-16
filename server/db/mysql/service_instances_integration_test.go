package mysql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 实例心跳注册表（PG 000032 同语义镜像）：CreateSchema 后 UPSERT 往返。
func TestMySQLServiceInstanceHeartbeatRegistry(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()

	if err := db.RecordServiceInstanceHeartbeat(ctx, "catsco-agents-1", 3, json.RawMessage(`{"tokens_total":7}`)); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	var activeRuns int
	var metrics []byte
	var firstSeen, lastSeen time.Time
	if err := db.db.QueryRowContext(ctx, `
		SELECT active_runs, metrics_json, first_seen_at, last_seen_at
		FROM service_instances WHERE instance_id = ?`, "catsco-agents-1").
		Scan(&activeRuns, &metrics, &firstSeen, &lastSeen); err != nil {
		t.Fatalf("select after first heartbeat: %v", err)
	}
	if activeRuns != 3 || !strings.Contains(string(metrics), "tokens_total") {
		t.Fatalf("first heartbeat row = active=%d metrics=%s", activeRuns, metrics)
	}

	time.Sleep(10 * time.Millisecond)
	if err := db.RecordServiceInstanceHeartbeat(ctx, "catsco-agents-1", 1, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	var secondLastSeen time.Time
	if err := db.db.QueryRowContext(ctx, `
		SELECT active_runs, last_seen_at FROM service_instances WHERE instance_id = ?`, "catsco-agents-1").
		Scan(&activeRuns, &secondLastSeen); err != nil {
		t.Fatalf("select after second heartbeat: %v", err)
	}
	if activeRuns != 1 {
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

	t.Cleanup(func() {
		db.db.Exec(`DELETE FROM service_instances WHERE instance_id IN ('catsco-agents-1','catsco-agents-2')`)
	})
}

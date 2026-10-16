package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecordServiceInstanceHeartbeat 落库实例心跳（监控数据源；设计 §7.1"实例在线数"）。
// UPSERT：每实例一行最新值（active_runs + metrics 快照 + last_seen_at 刷新）。
func (a *Adapter) RecordServiceInstanceHeartbeat(ctx context.Context, instanceID string, activeRuns int, metrics json.RawMessage) error {
	if activeRuns < 0 {
		activeRuns = 0
	}
	if len(metrics) == 0 {
		metrics = json.RawMessage(`{}`)
	}
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO service_instances (instance_id, active_runs, metrics_json)
		VALUES ($1, $2, $3::jsonb)
		ON CONFLICT (instance_id) DO UPDATE SET
			active_runs = EXCLUDED.active_runs,
			metrics_json = EXCLUDED.metrics_json,
			last_seen_at = clock_timestamp()`,
		instanceID, activeRuns, string(metrics)); err != nil {
		return fmt.Errorf("record service instance heartbeat: %w", err)
	}
	return nil
}

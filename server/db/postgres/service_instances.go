package postgres

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecordServiceInstanceHeartbeat 落库实例心跳（监控数据源；设计 §7.1"实例在线数"）。
// UPSERT：每实例一行最新值（active_runs + metrics 快照 + last_seen_at 刷新）。
// ownerUID 由鉴权层解析（0=平台内部 worker），不来自 payload。
func (a *Adapter) RecordServiceInstanceHeartbeat(ctx context.Context, instanceID string, ownerUID int64, activeRuns int, metrics json.RawMessage) error {
	if activeRuns < 0 {
		activeRuns = 0
	}
	if ownerUID < 0 {
		ownerUID = 0
	}
	if len(metrics) == 0 {
		metrics = json.RawMessage(`{}`)
	}
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO service_instances (instance_id, owner_uid, active_runs, metrics_json)
		VALUES ($1, $2, $3, $4::jsonb)
		ON CONFLICT (instance_id) DO UPDATE SET
			owner_uid = EXCLUDED.owner_uid,
			active_runs = EXCLUDED.active_runs,
			metrics_json = EXCLUDED.metrics_json,
			last_seen_at = clock_timestamp()`,
		instanceID, ownerUID, activeRuns, string(metrics)); err != nil {
		return fmt.Errorf("record service instance heartbeat: %w", err)
	}
	return nil
}

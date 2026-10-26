package mysql

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecordServiceInstanceHeartbeat 落库实例心跳（PG 同语义镜像）。
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
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			owner_uid = VALUES(owner_uid),
			active_runs = VALUES(active_runs),
			metrics_json = VALUES(metrics_json),
			last_seen_at = CURRENT_TIMESTAMP(6)`,
		instanceID, ownerUID, activeRuns, string(metrics)); err != nil {
		return fmt.Errorf("record service instance heartbeat: %w", err)
	}
	return nil
}

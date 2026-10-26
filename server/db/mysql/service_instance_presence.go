package mysql

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

// ListOnlineServiceInstances 列出窗口内有过心跳的实例（PG 同语义镜像）。
func (a *Adapter) ListOnlineServiceInstances(ctx context.Context, window time.Duration) ([]store.ServiceInstancePresence, error) {
	if window <= 0 {
		window = 90 * time.Second
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT instance_id, owner_uid, metrics_json
		FROM service_instances
		WHERE last_seen_at >= DATE_SUB(NOW(6), INTERVAL ? SECOND)`, int(window.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("list online service instances: %w", err)
	}
	defer rows.Close()

	var out []store.ServiceInstancePresence
	for rows.Next() {
		var item store.ServiceInstancePresence
		var metrics []byte
		if err := rows.Scan(&item.InstanceID, &item.OwnerUID, &metrics); err != nil {
			return nil, fmt.Errorf("scan online service instance: %w", err)
		}
		item.Metrics = json.RawMessage(metrics)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate online service instances: %w", err)
	}
	return out, nil
}

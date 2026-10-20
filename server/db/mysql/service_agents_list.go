package mysql

import (
	"context"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// ListServiceAgents 列出可调用的 service agents（每个 agent 取最新版本；MCP tools/list 来源）。
func (a *Adapter) ListServiceAgents(ctx context.Context) ([]store.ServiceAgentDescriptor, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT ranked.id, ranked.slug, ranked.name, ranked.version, ranked.threshold_seconds,
		       ranked.owner_kind, ranked.owner_uid, ranked.visibility
		FROM (
			SELECT a.id, a.slug, a.name, v.version,
			       COALESCE(CAST(v.definition_json->>'$.async_threshold_seconds' AS UNSIGNED), 60) AS threshold_seconds,
			       a.owner_kind, a.owner_uid, a.visibility,
			       ROW_NUMBER() OVER (PARTITION BY a.id ORDER BY v.version DESC) AS rn
			FROM service_agents a
			JOIN service_agent_versions v ON v.agent_id = a.id
			WHERE a.state = 'active'
		) ranked
		WHERE ranked.rn = 1
		ORDER BY ranked.id`)
	if err != nil {
		return nil, fmt.Errorf("list service agents: %w", err)
	}
	defer rows.Close()
	var agents []store.ServiceAgentDescriptor
	for rows.Next() {
		var d store.ServiceAgentDescriptor
		if err := rows.Scan(&d.ID, &d.Slug, &d.Name, &d.Version, &d.AsyncThresholdSeconds,
			&d.OwnerKind, &d.OwnerUID, &d.Visibility); err != nil {
			return nil, fmt.Errorf("scan service agent: %w", err)
		}
		agents = append(agents, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service agents: %w", err)
	}
	return agents, nil
}

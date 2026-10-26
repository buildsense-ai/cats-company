package postgres

import (
	"context"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// ListServiceAgents 列出可调用的 service agents（每个 agent 取最新版本；MCP tools/list 来源）。
// 归属/可见性一并返回，过滤在调用面做（public 全可见；private 仅 owner 本人）。
func (a *Adapter) ListServiceAgents(ctx context.Context) ([]store.ServiceAgentDescriptor, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT DISTINCT ON (a.id)
		       a.id, a.slug, a.name, v.version,
		       COALESCE((v.definition_json->>'async_threshold_seconds')::int, 60),
		       a.owner_kind, a.owner_uid, a.visibility,
		       COALESCE(v.definition_json->>'description', ''),
		       COALESCE(v.definition_json->>'input_schema', ''),
		       COALESCE(v.definition_json::text, '{}'),
		       COALESCE(a.self_intro_description, ''),
		       COALESCE(a.self_intro_input_schema, '')
		FROM service_agents a
		JOIN service_agent_versions v ON v.agent_id = a.id
		WHERE a.state = 'active'
		ORDER BY a.id, v.version DESC`)
	if err != nil {
		return nil, fmt.Errorf("list service agents: %w", err)
	}
	defer rows.Close()
	var agents []store.ServiceAgentDescriptor
	for rows.Next() {
		var d store.ServiceAgentDescriptor
		if err := rows.Scan(&d.ID, &d.Slug, &d.Name, &d.Version, &d.AsyncThresholdSeconds,
			&d.OwnerKind, &d.OwnerUID, &d.Visibility, &d.Description, &d.InputSchema,
			&d.DefinitionJSON, &d.SelfIntroDescription, &d.SelfIntroInputSchema); err != nil {
			return nil, fmt.Errorf("scan service agent: %w", err)
		}
		agents = append(agents, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service agents: %w", err)
	}
	return agents, nil
}

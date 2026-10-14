package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// GetServiceAgentDefinition 取 agent 当前版本的阈值与定义 JSON（claim 签发服务 token 用；计划① Task 8）。
func (a *Adapter) GetServiceAgentDefinition(ctx context.Context, agentID int64) (string, int, json.RawMessage, error) {
	var (
		slug       string
		threshold  int
		definition []byte
	)
	err := a.db.QueryRowContext(ctx, `
		SELECT a.slug, COALESCE((v.definition_json->>'async_threshold_seconds')::int, 60), v.definition_json
		FROM service_agent_versions v
		JOIN service_agents a ON a.id = v.agent_id
		WHERE v.agent_id = $1
		ORDER BY v.version DESC
		LIMIT 1`, agentID).Scan(&slug, &threshold, &definition)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil, store.ErrServiceAgentNotFound
	}
	if err != nil {
		return "", 0, nil, fmt.Errorf("get service agent definition: %w", err)
	}
	return slug, threshold, json.RawMessage(definition), nil
}

package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// GetServiceAgentDefinition 取指定版本的定义（claim 签发服务 token 用；计划① Task 8）。
// definitionRev<=0 = 最新版本；指定版本不存在时回退最新（会话钉定语义的兑底，不阻服务）。
func (a *Adapter) GetServiceAgentDefinition(ctx context.Context, agentID int64, definitionRev int64) (string, int, json.RawMessage, error) {
	var (
		slug       string
		threshold  int
		definition []byte
	)
	err := a.db.QueryRowContext(ctx, `
		SELECT a.slug, COALESCE(CAST(v.definition_json->>'$.async_threshold_seconds' AS UNSIGNED), 60), v.definition_json
		FROM service_agent_versions v
		JOIN service_agents a ON a.id = v.agent_id
		WHERE v.agent_id = ?
		ORDER BY (v.version = ?) DESC, v.version DESC
		LIMIT 1`, agentID, definitionRev).Scan(&slug, &threshold, &definition)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil, store.ErrServiceAgentNotFound
	}
	if err != nil {
		return "", 0, nil, fmt.Errorf("get service agent definition: %w", err)
	}
	return slug, threshold, json.RawMessage(definition), nil
}

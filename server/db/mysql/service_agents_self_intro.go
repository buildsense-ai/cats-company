package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// 自述建议（self_intro）：应用跑完后「建议」的自我描述（PG 同语义镜像）。
//
// 写这段的是模型，所以它只能**建议**：落库到 service_agents.self_intro_*，
// 等 owner 在面板上确认后才写进 definition。中间态不进 tools/list。
//
// 只存 description / input_schema：执行方式、价格、可见性一律不经此路修改。

// SetServiceAgentSelfIntro 记录自述建议（覆盖旧建议）。
func (a *Adapter) SetServiceAgentSelfIntro(ctx context.Context, slug, description, inputSchema string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("set service agent self intro: empty slug")
	}
	res, err := a.db.ExecContext(ctx, `
		UPDATE service_agents
		SET self_intro_description = ?,
		    self_intro_input_schema = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE slug = ? AND state = 'active'`, description, inputSchema, slug)
	if err != nil {
		return fmt.Errorf("set service agent self intro: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	if err := a.db.QueryRowContext(ctx,
		`SELECT 1 FROM service_agents WHERE slug = ?`, slug).Scan(new(int)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrServiceAgentNotFound
		}
		return fmt.Errorf("set service agent self intro: %w", err)
	}
	return nil
}

// ClearServiceAgentSelfIntro 清除待确认的自述建议（采纳或忽略后调用）。幂等。
func (a *Adapter) ClearServiceAgentSelfIntro(ctx context.Context, slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("clear service agent self intro: empty slug")
	}
	if _, err := a.db.ExecContext(ctx, `
		UPDATE service_agents
		SET self_intro_description = '',
		    self_intro_input_schema = '',
		    updated_at = CURRENT_TIMESTAMP
		WHERE slug = ?`, slug); err != nil {
		return fmt.Errorf("clear service agent self intro: %w", err)
	}
	return nil
}

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// ServiceRunOwnerUID 返回 run 所属应用的 owner_uid（owner_kind != user 时为 0）。
// per-user worker 凭据用它做 run 级鉴权。
func (a *Adapter) ServiceRunOwnerUID(ctx context.Context, runID string) (int64, error) {
	var (
		ownerKind string
		ownerUID  int64
	)
	err := a.db.QueryRowContext(ctx, `
		SELECT a.owner_kind, a.owner_uid
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		JOIN service_agents a ON a.id = s.agent_id
		WHERE r.run_id = ?`, runID).Scan(&ownerKind, &ownerUID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrServiceAgentNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("service run owner: %w", err)
	}
	if ownerKind != store.ServiceAgentOwnerUser {
		return 0, nil
	}
	return ownerUID, nil
}

// ServiceRunAgentSlug 返回 run 所属应用的 slug（PG 同语义镜像）。
// 已下架（inactive）的应用也会返回 slug：run 已经跑完了，建议先存着，
// 是否采纳交给 owner 判断。
func (a *Adapter) ServiceRunAgentSlug(ctx context.Context, runID string) (string, error) {
	var slug string
	err := a.db.QueryRowContext(ctx, `
		SELECT a.slug
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		JOIN service_agents a ON a.id = s.agent_id
		WHERE r.run_id = ?`, runID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", store.ErrServiceAgentNotFound
	}
	if err != nil {
		return "", fmt.Errorf("service run agent slug: %w", err)
	}
	return slug, nil
}

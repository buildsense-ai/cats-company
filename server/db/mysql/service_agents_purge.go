package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// ListServiceAgentsByOwner 列出某个 owner 名下的全部应用（含已下架），给「我的应用」管理面用。
// 与 PostgreSQL 侧同语义：按 owner 过滤、不过滤 state。
func (a *Adapter) ListServiceAgentsByOwner(ctx context.Context, ownerUID int64) ([]store.ServiceAgentDescriptor, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(v.version, 0),
		       COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(v.definition_json, '$.async_threshold_seconds')) AS SIGNED), 60),
		       a.owner_kind, a.owner_uid, a.visibility, a.state,
		       COALESCE(JSON_UNQUOTE(JSON_EXTRACT(v.definition_json, '$.description')), ''),
		       COALESCE(JSON_UNQUOTE(JSON_EXTRACT(v.definition_json, '$.input_schema')), ''),
		       COALESCE(CAST(v.definition_json AS CHAR), '{}'),
		       COALESCE(a.self_intro_description, ''),
		       COALESCE(a.self_intro_input_schema, '')
		FROM service_agents a
		LEFT JOIN service_agent_versions v
		       ON v.agent_id = a.id
		      AND v.version = (SELECT MAX(version) FROM service_agent_versions WHERE agent_id = a.id)
		WHERE a.owner_kind = 'user' AND a.owner_uid = ?
		ORDER BY a.id`, ownerUID)
	if err != nil {
		return nil, fmt.Errorf("list service agents by owner: %w", err)
	}
	defer rows.Close()
	agents := make([]store.ServiceAgentDescriptor, 0, 8)
	for rows.Next() {
		var d store.ServiceAgentDescriptor
		if err := rows.Scan(&d.ID, &d.Slug, &d.Name, &d.Version, &d.AsyncThresholdSeconds,
			&d.OwnerKind, &d.OwnerUID, &d.Visibility, &d.State, &d.Description, &d.InputSchema,
			&d.DefinitionJSON, &d.SelfIntroDescription, &d.SelfIntroInputSchema); err != nil {
			return nil, fmt.Errorf("scan service agent by owner: %w", err)
		}
		agents = append(agents, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service agents by owner: %w", err)
	}
	return agents, nil
}

// PurgeServiceAgent 彻底删除一个已下架的应用及其运行数据（owner 本人才能操作）。
// 账本 service_run_ledger 不删：它是账务凭证。
func (a *Adapter) PurgeServiceAgent(ctx context.Context, slug string, ownerUID int64) (store.ServiceAgentPurgeResult, error) {
	out := store.ServiceAgentPurgeResult{Slug: slug}
	if ownerUID <= 0 {
		return out, store.ErrServiceAgentOwnedByOther
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("begin purge service agent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		agentID   int64
		ownerKind string
		owner     int64
		state     string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, owner_kind, owner_uid, state FROM service_agents WHERE slug = ? FOR UPDATE`, slug).
		Scan(&agentID, &ownerKind, &owner, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return out, store.ErrServiceAgentNotFound
	}
	if err != nil {
		return out, fmt.Errorf("load service agent for purge: %w", err)
	}
	if ownerKind == store.ServiceAgentOwnerInternal || owner != ownerUID {
		return out, store.ErrServiceAgentOwnedByOther
	}
	if state == "active" {
		return out, store.ErrServiceAgentNotInactive
	}

	var pending int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		WHERE s.agent_id = ? AND r.state IN ('running','submitted')`, agentID).Scan(&pending); err != nil {
		return out, fmt.Errorf("count pending service runs: %w", err)
	}
	if pending > 0 {
		return out, store.ErrServiceAgentRunsPending
	}

	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM service_agent_versions WHERE agent_id = ?`, agentID).Scan(&out.Versions); err != nil {
		return out, fmt.Errorf("count service agent versions: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM service_sessions WHERE agent_id = ?`, agentID).Scan(&out.Sessions); err != nil {
		return out, fmt.Errorf("count service sessions: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		WHERE s.agent_id = ?`, agentID).Scan(&out.Runs); err != nil {
		return out, fmt.Errorf("count service runs: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM service_runs
		WHERE session_id IN (SELECT session_id FROM service_sessions WHERE agent_id = ?)`, agentID); err != nil {
		return out, fmt.Errorf("delete service runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM service_sessions WHERE agent_id = ?`, agentID); err != nil {
		return out, fmt.Errorf("delete service sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM service_agents WHERE id = ?`, agentID); err != nil {
		return out, fmt.Errorf("delete service agent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("commit purge service agent: %w", err)
	}
	return out, nil
}

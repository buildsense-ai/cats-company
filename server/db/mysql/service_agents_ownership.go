package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// ClaimServiceAgentOwnership 为用户自助上架占位 slug（设计 P2）。
// 语义与 PG 实现一致：不存在则建条目；owner 相同则刷新 name/visibility；
// owner 不同则区分「平台内部保留」与「他人占用」。
func (a *Adapter) ClaimServiceAgentOwnership(ctx context.Context, slug, name, ownerKind string, ownerUID int64, visibility string) error {
	if strings.TrimSpace(slug) == "" {
		return fmt.Errorf("claim service agent ownership: empty slug")
	}
	if strings.TrimSpace(name) == "" {
		name = slug
	}
	if ownerKind == "" {
		ownerKind = store.ServiceAgentOwnerUser
	}
	if visibility == "" {
		visibility = store.ServiceAgentVisibilityPublic
	}

	// INSERT IGNORE：并发抢占同一 slug 时只有一个成功，其余走后续校验。
	if _, err := a.db.ExecContext(ctx, `
		INSERT IGNORE INTO service_agents (slug, name, owner_kind, owner_uid, visibility)
		VALUES (?, ?, ?, ?, ?)`, slug, name, ownerKind, ownerUID, visibility); err != nil {
		return fmt.Errorf("claim service agent ownership: %w", err)
	}

	var currentKind, currentVisibility string
	var currentOwner int64
	err := a.db.QueryRowContext(ctx, `
		SELECT owner_kind, owner_uid, visibility FROM service_agents WHERE slug = ?`, slug).
		Scan(&currentKind, &currentOwner, &currentVisibility)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrServiceAgentNotFound
	}
	if err != nil {
		return fmt.Errorf("claim service agent ownership: %w", err)
	}

	if currentOwner != ownerUID || (ownerKind == store.ServiceAgentOwnerUser && currentKind != store.ServiceAgentOwnerUser) {
		if currentKind == store.ServiceAgentOwnerInternal || currentOwner == 0 {
			return store.ErrServiceAgentInternalReserved
		}
		return store.ErrServiceAgentOwnedByOther
	}

	if _, err := a.db.ExecContext(ctx, `
		UPDATE service_agents SET name = ?, visibility = ?, state = 'active' WHERE slug = ?`,
		name, visibility, slug); err != nil {
		return fmt.Errorf("claim service agent ownership: %w", err)
	}
	return nil
}

// SetServiceAgentState 上架/下架自己的应用（语义与 PG 实现一致：只有 user owner 本人可操作）。
func (a *Adapter) SetServiceAgentState(ctx context.Context, slug string, ownerUID int64, active bool) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("set service agent state: empty slug")
	}
	state := "inactive"
	if active {
		state = "active"
	}
	res, err := a.db.ExecContext(ctx, `
		UPDATE service_agents SET state = ?
		WHERE slug = ? AND owner_uid = ? AND owner_kind = 'user'`, state, slug, ownerUID)
	if err != nil {
		return fmt.Errorf("set service agent state: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	var currentOwner int64
	var currentKind string
	err = a.db.QueryRowContext(ctx, `
		SELECT owner_uid, owner_kind FROM service_agents WHERE slug = ?`, slug).
		Scan(&currentOwner, &currentKind)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrServiceAgentNotFound
	}
	if err != nil {
		return fmt.Errorf("set service agent state: %w", err)
	}
	if currentKind == store.ServiceAgentOwnerUser && currentOwner == ownerUID {
		return nil // 已是目标状态（幂等）
	}
	return store.ErrServiceAgentOwnedByOther
}

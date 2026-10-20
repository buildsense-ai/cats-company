package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// ClaimServiceAgentOwnership 为用户自助上架占位 slug（设计 P2）。
//   - slug 不存在：按给定 owner / visibility 建立条目（state=active）；
//   - 已存在且 owner 匹配：刷新 name / visibility（重复发布不改归属）；
//   - 已存在且 owner 不同：平台内部条目 → ErrServiceAgentInternalReserved；
//     其他用户 → ErrServiceAgentOwnedByOther。
//
// 并发安全：靠 slug 唯一约束 + 冲突后重查实现；无需显式锁（写仅改单行）。
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

	// 先尝试插入新条目（owner 即发布者）。
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO service_agents (slug, name, owner_kind, owner_uid, visibility)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (slug) DO NOTHING`, slug, name, ownerKind, ownerUID, visibility)
	if err != nil {
		return fmt.Errorf("claim service agent ownership: %w", err)
	}

	var currentKind, currentVisibility string
	var currentOwner int64
	err = a.db.QueryRowContext(ctx, `
		SELECT owner_kind, owner_uid, visibility FROM service_agents WHERE slug = $1`, slug).
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

	// 同一 owner 重复发布：刷新展示名与可见性（不改归属，不允许把自己改成 internal）。
	if _, err := a.db.ExecContext(ctx, `
		UPDATE service_agents
		SET name = $2, visibility = $3, updated_at = CURRENT_TIMESTAMP
		WHERE slug = $1`, slug, name, visibility); err != nil {
		return fmt.Errorf("claim service agent ownership: %w", err)
	}
	return nil
}

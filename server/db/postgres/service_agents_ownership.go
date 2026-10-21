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
	// 顺带把已下架的条目重新置为 active —— 重新发布即重新上架。
	if _, err := a.db.ExecContext(ctx, `
		UPDATE service_agents
		SET name = $2, visibility = $3, state = 'active', updated_at = CURRENT_TIMESTAMP
		WHERE slug = $1`, slug, name, visibility); err != nil {
		return fmt.Errorf("claim service agent ownership: %w", err)
	}
	return nil
}

// SetServiceAgentState 上架/下架自己的应用（设计 P2：分享可收回）。
// 只允许 user owner 本人操作；平台 internal 条目与别人的条目一律 ErrServiceAgentOwnedByOther。
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
		UPDATE service_agents
		SET state = $3, updated_at = CURRENT_TIMESTAMP
		WHERE slug = $1 AND owner_uid = $2 AND owner_kind = 'user'`, slug, ownerUID, state)
	if err != nil {
		return fmt.Errorf("set service agent state: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	// 没改到行：区分「不存在」与「不是本人」。
	var ownerUID0 int64
	var ownerKind string
	err = a.db.QueryRowContext(ctx, `
		SELECT owner_uid, owner_kind FROM service_agents WHERE slug = $1`, slug).
		Scan(&ownerUID0, &ownerKind)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrServiceAgentNotFound
	}
	if err != nil {
		return fmt.Errorf("set service agent state: %w", err)
	}
	if ownerKind == store.ServiceAgentOwnerUser && ownerUID0 == ownerUID {
		// 状态本来就是目标值（幂等）。
		return nil
	}
	return store.ErrServiceAgentOwnedByOther
}

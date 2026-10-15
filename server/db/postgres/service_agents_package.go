package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// PublishServiceAgentPackage 发布一个 agent 新版本（云端包分发；设计 A §2）。
// packageHash 为内容寻址：同 hash 已存在（任意版本）→ 幂等返回既有版本号，不重复入库。
// definition 由平台从包 manifest 派生（消除"包内模型 vs 平台定义"双写裂缝）。
func (a *Adapter) PublishServiceAgentPackage(ctx context.Context, slug string, definition json.RawMessage, packageHash string, bundle json.RawMessage) (int64, error) {
	if packageHash == "" {
		return 0, fmt.Errorf("publish service agent package: empty package hash")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("publish service agent package: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var agentID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM service_agents WHERE slug = $1 AND state = 'active'`, slug).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrServiceAgentNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("publish service agent package: %w", err)
	}

	// 锁 agent 行，避免并发 publish 抢同一 version。
	if _, err := tx.ExecContext(ctx, `SELECT id FROM service_agents WHERE id = $1 FOR UPDATE`, agentID); err != nil {
		return 0, fmt.Errorf("publish service agent package: %w", err)
	}

	var existing int64
	err = tx.QueryRowContext(ctx, `
		SELECT version FROM service_agent_versions
		WHERE agent_id = $1 AND assets_hash = $2 AND package_json IS NOT NULL
		ORDER BY version DESC LIMIT 1`, agentID, packageHash).Scan(&existing)
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("publish service agent package: %w", err)
	}

	var version int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO service_agent_versions (agent_id, version, definition_json, assets_hash, package_json)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2::jsonb, $3, $4::jsonb
		FROM service_agent_versions WHERE agent_id = $1
		RETURNING version`, agentID, string(definition), packageHash, string(bundle)).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("publish service agent package: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("publish service agent package: %w", err)
	}
	return version, nil
}

// GetServiceAgentPackage 按内容 hash 取包原文（实例拉取；hash 缺失 = 包不存在）。
func (a *Adapter) GetServiceAgentPackage(ctx context.Context, packageHash string) (json.RawMessage, error) {
	var bundle []byte
	err := a.db.QueryRowContext(ctx, `
		SELECT package_json FROM service_agent_versions
		WHERE assets_hash = $1 AND package_json IS NOT NULL
		ORDER BY created_at DESC LIMIT 1`, packageHash).Scan(&bundle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrServicePackageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get service agent package: %w", err)
	}
	return json.RawMessage(bundle), nil
}

// GetServiceAgentPackageHash 取 agent 最新版本的内容 hash（claim 下发 package_hash；空 = 无包）。
func (a *Adapter) GetServiceAgentPackageHash(ctx context.Context, agentID int64) (string, error) {
	var hash sql.NullString
	err := a.db.QueryRowContext(ctx, `
		SELECT assets_hash FROM service_agent_versions
		WHERE agent_id = $1 AND package_json IS NOT NULL
		ORDER BY version DESC LIMIT 1`, agentID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get service agent package hash: %w", err)
	}
	return hash.String, nil
}

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 用户自己的 worker 凭据（per-user）。见 store.ServiceWorkerTokenStore。

func (a *Adapter) CreateServiceWorkerToken(ctx context.Context, uid int64, tokenHash, prefix, label string) (store.ServiceWorkerToken, error) {
	res, err := a.db.ExecContext(ctx, `
		INSERT INTO service_worker_tokens (uid, token_hash, token_prefix, label)
		VALUES (?, ?, ?, ?)`, uid, tokenHash, prefix, label)
	if err != nil {
		return store.ServiceWorkerToken{}, fmt.Errorf("create service worker token: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return store.ServiceWorkerToken{}, fmt.Errorf("create service worker token: %w", err)
	}
	var createdAt time.Time
	if err := a.db.QueryRowContext(ctx, `
		SELECT created_at FROM service_worker_tokens WHERE id = ?`, id).Scan(&createdAt); err != nil {
		return store.ServiceWorkerToken{}, fmt.Errorf("create service worker token: %w", err)
	}
	return store.ServiceWorkerToken{ID: id, Prefix: prefix, Label: label, CreatedAt: createdAt}, nil
}

func (a *Adapter) ListServiceWorkerTokens(ctx context.Context, uid int64) ([]store.ServiceWorkerToken, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, token_prefix, label, created_at, last_used_at
		FROM service_worker_tokens
		WHERE uid = ? AND revoked_at IS NULL
		ORDER BY created_at DESC`, uid)
	if err != nil {
		return nil, fmt.Errorf("list service worker tokens: %w", err)
	}
	defer rows.Close()
	var out []store.ServiceWorkerToken
	for rows.Next() {
		var (
			token store.ServiceWorkerToken
			used  sql.NullTime
		)
		if err := rows.Scan(&token.ID, &token.Prefix, &token.Label, &token.CreatedAt, &used); err != nil {
			return nil, fmt.Errorf("scan service worker token: %w", err)
		}
		if used.Valid {
			last := used.Time
			token.LastUsedAt = &last
		}
		out = append(out, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service worker tokens: %w", err)
	}
	return out, nil
}

// RevokeServiceWorkerToken 撤销自己的凭据；已撤销视为幂等成功。
func (a *Adapter) RevokeServiceWorkerToken(ctx context.Context, uid, id int64) error {
	res, err := a.db.ExecContext(ctx, `
		UPDATE service_worker_tokens
		SET revoked_at = CURRENT_TIMESTAMP(6)
		WHERE id = ? AND uid = ? AND revoked_at IS NULL`, id, uid)
	if err != nil {
		return fmt.Errorf("revoke service worker token: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	var owner int64
	err = a.db.QueryRowContext(ctx, `
		SELECT uid FROM service_worker_tokens WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrServiceWorkerTokenNotFound
	}
	if err != nil {
		return fmt.Errorf("revoke service worker token: %w", err)
	}
	if owner != uid {
		return store.ErrServiceWorkerTokenNotFound
	}
	return nil
}

// ResolveServiceWorkerToken 用 sha256(明文) 换 uid 并刷新 last_used_at。
func (a *Adapter) ResolveServiceWorkerToken(ctx context.Context, tokenHash string) (int64, error) {
	var uid int64
	err := a.db.QueryRowContext(ctx, `
		SELECT uid FROM service_worker_tokens
		WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrServiceWorkerTokenNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve service worker token: %w", err)
	}
	if _, err := a.db.ExecContext(ctx, `
		UPDATE service_worker_tokens SET last_used_at = CURRENT_TIMESTAMP(6) WHERE token_hash = ?`, tokenHash); err != nil {
		// last_used_at 只是诊断信息，刷新失败不影响鉴权。
		_ = err
	}
	return uid, nil
}

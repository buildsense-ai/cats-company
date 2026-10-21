package postgres

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
	var (
		id        int64
		createdAt time.Time
	)
	err := a.db.QueryRowContext(ctx, `
		INSERT INTO service_worker_tokens (uid, token_hash, token_prefix, label)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`, uid, tokenHash, prefix, label).Scan(&id, &createdAt)
	if err != nil {
		return store.ServiceWorkerToken{}, fmt.Errorf("create service worker token: %w", err)
	}
	return store.ServiceWorkerToken{ID: id, Prefix: prefix, Label: label, CreatedAt: createdAt}, nil
}

func (a *Adapter) ListServiceWorkerTokens(ctx context.Context, uid int64) ([]store.ServiceWorkerToken, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, token_prefix, label, created_at, last_used_at
		FROM service_worker_tokens
		WHERE uid = $1 AND revoked_at IS NULL
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
		SET revoked_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND uid = $2 AND revoked_at IS NULL`, id, uid)
	if err != nil {
		return fmt.Errorf("revoke service worker token: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	// 没改到行：已撤销（幂等）还是不存在/不是本人。
	var existing int64
	err = a.db.QueryRowContext(ctx, `
		SELECT uid FROM service_worker_tokens WHERE id = $1 AND revoked_at IS NULL`, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		// 可能已经撤销过（该行存在但 revoked_at 非空）——再确认一次归属。
		var owner int64
		if err := a.db.QueryRowContext(ctx, `
			SELECT uid FROM service_worker_tokens WHERE id = $1`, id).Scan(&owner); err == nil && owner == uid {
			return nil
		}
		return store.ErrServiceWorkerTokenNotFound
	}
	if err != nil {
		return fmt.Errorf("revoke service worker token: %w", err)
	}
	if existing != uid {
		return store.ErrServiceWorkerTokenNotFound
	}
	return nil
}

// ResolveServiceWorkerToken 用 sha256(明文) 换 uid 并刷新 last_used_at。
func (a *Adapter) ResolveServiceWorkerToken(ctx context.Context, tokenHash string) (int64, error) {
	var uid int64
	err := a.db.QueryRowContext(ctx, `
		UPDATE service_worker_tokens
		SET last_used_at = CURRENT_TIMESTAMP
		WHERE token_hash = $1 AND revoked_at IS NULL
		RETURNING uid`, tokenHash).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrServiceWorkerTokenNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve service worker token: %w", err)
	}
	return uid, nil
}

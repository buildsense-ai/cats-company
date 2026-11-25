package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.MCPServerConfigStore = (*Adapter)(nil)

func (a *Adapter) CreateMCPServerConfig(ctx context.Context, cfg *store.MCPServerConfig) (int64, error) {
	var id int64
	err := a.db.QueryRowContext(ctx, `
INSERT INTO mcp_server_configs (owner_uid, name, endpoint, transport, enabled, auth_type, client_id, authorize_url, token_url, scopes, client_secret_enc, config_json)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		cfg.OwnerUID, cfg.Name, cfg.Endpoint, cfg.Transport, cfg.Enabled, cfg.AuthType,
		cfg.ClientID, cfg.AuthorizeURL, cfg.TokenURL, cfg.Scopes, cfg.ClientSecretEnc, jsonOrEmpty(cfg.ConfigJSON),
	).Scan(&id)
	return id, err
}

func jsonOrEmpty(raw []byte) any {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

func scanMCPServerConfig(rows interface{ Scan(...any) error }) (*store.MCPServerConfig, error) {
	var cfg store.MCPServerConfig
	err := rows.Scan(&cfg.ID, &cfg.OwnerUID, &cfg.Name, &cfg.Endpoint, &cfg.Transport, &cfg.Enabled,
		&cfg.AuthType, &cfg.ClientID, &cfg.AuthorizeURL, &cfg.TokenURL, &cfg.Scopes, &cfg.ClientSecretEnc,
		&cfg.ConfigJSON, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

const mcpServerConfigColumns = `id, owner_uid, name, endpoint, transport, enabled, auth_type, client_id, authorize_url, token_url, scopes, client_secret_enc, config_json, created_at, updated_at`

func (a *Adapter) GetMCPServerConfig(ctx context.Context, ownerUID, id int64) (*store.MCPServerConfig, error) {
	row := a.db.QueryRowContext(ctx, `SELECT `+mcpServerConfigColumns+` FROM mcp_server_configs WHERE id = $1 AND owner_uid = $2`, id, ownerUID)
	cfg, err := scanMCPServerConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMCPServerNotFound
	}
	return cfg, err
}

func (a *Adapter) ListMCPServerConfigs(ctx context.Context, ownerUID int64) ([]store.MCPServerConfig, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT `+mcpServerConfigColumns+` FROM mcp_server_configs WHERE owner_uid = $1 ORDER BY id`, ownerUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]store.MCPServerConfig, 0)
	for rows.Next() {
		cfg, err := scanMCPServerConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *cfg)
	}
	return out, rows.Err()
}

func (a *Adapter) UpdateMCPServerConfig(ctx context.Context, cfg *store.MCPServerConfig) error {
	res, err := a.db.ExecContext(ctx, `
UPDATE mcp_server_configs SET name=$1, endpoint=$2, transport=$3, enabled=$4, auth_type=$5, client_id=$6,
authorize_url=$7, token_url=$8, scopes=$9, client_secret_enc=$10, config_json=$11, updated_at=CURRENT_TIMESTAMP
WHERE id=$12 AND owner_uid=$13`,
		cfg.Name, cfg.Endpoint, cfg.Transport, cfg.Enabled, cfg.AuthType, cfg.ClientID,
		cfg.AuthorizeURL, cfg.TokenURL, cfg.Scopes, cfg.ClientSecretEnc, jsonOrEmpty(cfg.ConfigJSON),
		cfg.ID, cfg.OwnerUID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return store.ErrMCPServerNotFound
	}
	return err
}

func (a *Adapter) DeleteMCPServerConfig(ctx context.Context, ownerUID, id int64) error {
	res, err := a.db.ExecContext(ctx, `DELETE FROM mcp_server_configs WHERE id = $1 AND owner_uid = $2`, id, ownerUID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return store.ErrMCPServerNotFound
	}
	return err
}

func (a *Adapter) CreateMCPOAuthState(ctx context.Context, st *store.MCPOAuthState) error {
	_, err := a.db.ExecContext(ctx, `
INSERT INTO mcp_oauth_states (state, server_id, owner_uid, code_verifier, redirect_uri, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (state) DO NOTHING`,
		st.State, st.ServerID, st.OwnerUID, st.CodeVerifier, st.RedirectURI, st.ExpiresAt)
	return err
}

// TakeMCPOAuthState 一次性消费 state：读出即删（同一事务内 DELETE ... RETURNING）。
// 重放的回调拿不到第二份 state —— 授权码重放攻击在第一跳就被废掉。
func (a *Adapter) TakeMCPOAuthState(ctx context.Context, state string) (*store.MCPOAuthState, error) {
	// 回调顺带扫过期 state：没有独立 reaper，这条路径就是最好的清扫时机。
	sweepExpiredMCPOAuthStates(ctx, a.db, time.Now())
	var st store.MCPOAuthState
	err := a.db.QueryRowContext(ctx, `
DELETE FROM mcp_oauth_states WHERE state = $1
RETURNING state, server_id, owner_uid, code_verifier, redirect_uri, expires_at, created_at`, state).
		Scan(&st.State, &st.ServerID, &st.OwnerUID, &st.CodeVerifier, &st.RedirectURI, &st.ExpiresAt, &st.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMCPOAuthStateNotFound
	}
	return &st, err
}

func (a *Adapter) UpsertMCPOAuthCredential(ctx context.Context, cred *store.MCPOAuthCredential) error {
	var expiresAt any
	if !cred.ExpiresAt.IsZero() {
		expiresAt = cred.ExpiresAt
	}
	_, err := a.db.ExecContext(ctx, `
INSERT INTO mcp_oauth_credentials (server_id, owner_uid, access_token_enc, refresh_token_enc, token_type, scope, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (server_id, owner_uid) DO UPDATE SET
access_token_enc = EXCLUDED.access_token_enc,
refresh_token_enc = EXCLUDED.refresh_token_enc,
token_type = EXCLUDED.token_type,
scope = EXCLUDED.scope,
expires_at = EXCLUDED.expires_at,
updated_at = CURRENT_TIMESTAMP`,
		cred.ServerID, cred.OwnerUID, cred.AccessTokenEnc, cred.RefreshTokenEnc, cred.TokenType, cred.Scope, expiresAt)
	return err
}

func (a *Adapter) GetMCPOAuthCredential(ctx context.Context, ownerUID, serverID int64) (*store.MCPOAuthCredential, error) {
	var cred store.MCPOAuthCredential
	var expiresAt sql.NullTime
	err := a.db.QueryRowContext(ctx, `
SELECT server_id, owner_uid, access_token_enc, refresh_token_enc, token_type, scope, expires_at, updated_at
FROM mcp_oauth_credentials WHERE server_id = $1 AND owner_uid = $2`, serverID, ownerUID).
		Scan(&cred.ServerID, &cred.OwnerUID, &cred.AccessTokenEnc, &cred.RefreshTokenEnc, &cred.TokenType, &cred.Scope, &expiresAt, &cred.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMCPCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		cred.ExpiresAt = expiresAt.Time
	}
	return &cred, nil
}

func (a *Adapter) DeleteMCPOAuthCredential(ctx context.Context, ownerUID, serverID int64) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM mcp_oauth_credentials WHERE server_id = $1 AND owner_uid = $2`, serverID, ownerUID)
	return err
}

// sweepExpiredMCPOAuthStates 顺手清理过期 state（回调路径顺带做，无需独立 reaper）。
func sweepExpiredMCPOAuthStates(ctx context.Context, db *sql.DB, before time.Time) {
	_, _ = db.ExecContext(ctx, `DELETE FROM mcp_oauth_states WHERE expires_at < $1`, before)
}

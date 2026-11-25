package mysql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openchat/openchat/server/store"
)

var _ store.MCPServerConfigStore = (*Adapter)(nil)

func jsonOrEmpty(raw []byte) any {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

const mcpServerConfigColumns = `id, owner_uid, name, endpoint, transport, enabled, auth_type, client_id, authorize_url, token_url, scopes, client_secret_enc, config_json, created_at, updated_at`

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

func (a *Adapter) CreateMCPServerConfig(ctx context.Context, cfg *store.MCPServerConfig) (int64, error) {
	res, err := a.db.ExecContext(ctx, `
INSERT INTO mcp_server_configs (owner_uid, name, endpoint, transport, enabled, auth_type, client_id, authorize_url, token_url, scopes, client_secret_enc, config_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cfg.OwnerUID, cfg.Name, cfg.Endpoint, cfg.Transport, cfg.Enabled, cfg.AuthType,
		cfg.ClientID, cfg.AuthorizeURL, cfg.TokenURL, cfg.Scopes, cfg.ClientSecretEnc, jsonOrEmpty(cfg.ConfigJSON))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (a *Adapter) GetMCPServerConfig(ctx context.Context, ownerUID, id int64) (*store.MCPServerConfig, error) {
	row := a.db.QueryRowContext(ctx, `SELECT `+mcpServerConfigColumns+` FROM mcp_server_configs WHERE id = ? AND owner_uid = ?`, id, ownerUID)
	cfg, err := scanMCPServerConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMCPServerNotFound
	}
	return cfg, err
}

func (a *Adapter) ListMCPServerConfigs(ctx context.Context, ownerUID int64) ([]store.MCPServerConfig, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT `+mcpServerConfigColumns+` FROM mcp_server_configs WHERE owner_uid = ? ORDER BY id`, ownerUID)
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
UPDATE mcp_server_configs SET name=?, endpoint=?, transport=?, enabled=?, auth_type=?, client_id=?,
authorize_url=?, token_url=?, scopes=?, client_secret_enc=?, config_json=?
WHERE id=? AND owner_uid=?`,
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
	res, err := a.db.ExecContext(ctx, `DELETE FROM mcp_server_configs WHERE id = ? AND owner_uid = ?`, id, ownerUID)
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
INSERT IGNORE INTO mcp_oauth_states (state, server_id, owner_uid, code_verifier, redirect_uri, expires_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		st.State, st.ServerID, st.OwnerUID, st.CodeVerifier, st.RedirectURI, st.ExpiresAt)
	return err
}

// TakeMCPOAuthState 一次性消费 state：同一事务内 SELECT ... FOR UPDATE 后 DELETE，
// 重放的回调拿不到第二份 state。
func (a *Adapter) TakeMCPOAuthState(ctx context.Context, state string) (*store.MCPOAuthState, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var st store.MCPOAuthState
	err = tx.QueryRowContext(ctx, `
SELECT state, server_id, owner_uid, code_verifier, redirect_uri, expires_at, created_at
FROM mcp_oauth_states WHERE state = ? FOR UPDATE`, state).
		Scan(&st.State, &st.ServerID, &st.OwnerUID, &st.CodeVerifier, &st.RedirectURI, &st.ExpiresAt, &st.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMCPOAuthStateNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_oauth_states WHERE state = ?`, state); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &st, nil
}

func (a *Adapter) UpsertMCPOAuthCredential(ctx context.Context, cred *store.MCPOAuthCredential) error {
	var expiresAt any
	if !cred.ExpiresAt.IsZero() {
		expiresAt = cred.ExpiresAt
	}
	_, err := a.db.ExecContext(ctx, `
INSERT INTO mcp_oauth_credentials (server_id, owner_uid, access_token_enc, refresh_token_enc, token_type, scope, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
access_token_enc = VALUES(access_token_enc),
refresh_token_enc = VALUES(refresh_token_enc),
token_type = VALUES(token_type),
scope = VALUES(scope),
expires_at = VALUES(expires_at)`,
		cred.ServerID, cred.OwnerUID, cred.AccessTokenEnc, cred.RefreshTokenEnc, cred.TokenType, cred.Scope, expiresAt)
	return err
}

func (a *Adapter) GetMCPOAuthCredential(ctx context.Context, ownerUID, serverID int64) (*store.MCPOAuthCredential, error) {
	var cred store.MCPOAuthCredential
	var expiresAt sql.NullTime
	err := a.db.QueryRowContext(ctx, `
SELECT server_id, owner_uid, access_token_enc, refresh_token_enc, token_type, scope, expires_at, updated_at
FROM mcp_oauth_credentials WHERE server_id = ? AND owner_uid = ?`, serverID, ownerUID).
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
	_, err := a.db.ExecContext(ctx, `DELETE FROM mcp_oauth_credentials WHERE server_id = ? AND owner_uid = ?`, serverID, ownerUID)
	return err
}

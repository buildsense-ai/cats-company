CREATE TABLE IF NOT EXISTS mcp_server_configs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_uid BIGINT NOT NULL,
    name VARCHAR(128) NOT NULL,
    endpoint TEXT NOT NULL,
    transport VARCHAR(24) NOT NULL DEFAULT 'streamable_http',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    auth_type VARCHAR(16) NOT NULL DEFAULT 'none',
    client_id VARCHAR(256) NOT NULL DEFAULT '',
    authorize_url TEXT NOT NULL DEFAULT '',
    token_url TEXT NOT NULL DEFAULT '',
    scopes TEXT NOT NULL DEFAULT '',
    client_secret_enc TEXT NOT NULL DEFAULT '',
    config_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_mcp_server_configs_owner ON mcp_server_configs (owner_uid, enabled);

CREATE TABLE IF NOT EXISTS mcp_oauth_states (
    state VARCHAR(128) PRIMARY KEY,
    server_id BIGINT NOT NULL REFERENCES mcp_server_configs(id) ON DELETE CASCADE,
    owner_uid BIGINT NOT NULL,
    code_verifier VARCHAR(128) NOT NULL,
    redirect_uri TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_mcp_oauth_states_expires ON mcp_oauth_states (expires_at);

CREATE TABLE IF NOT EXISTS mcp_oauth_credentials (
    server_id BIGINT NOT NULL REFERENCES mcp_server_configs(id) ON DELETE CASCADE,
    owner_uid BIGINT NOT NULL,
    access_token_enc TEXT NOT NULL DEFAULT '',
    refresh_token_enc TEXT NOT NULL DEFAULT '',
    token_type VARCHAR(32) NOT NULL DEFAULT '',
    scope TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (server_id, owner_uid)
);

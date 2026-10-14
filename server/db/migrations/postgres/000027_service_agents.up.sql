CREATE TABLE IF NOT EXISTS service_agents (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(128) NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS service_agent_versions (
    agent_id BIGINT NOT NULL REFERENCES service_agents(id) ON DELETE CASCADE,
    version BIGINT NOT NULL,
    definition_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    assets_hash VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent_id, version)
);

CREATE TABLE IF NOT EXISTS service_sessions (
    session_id VARCHAR(64) PRIMARY KEY,
    agent_id BIGINT NOT NULL REFERENCES service_agents(id),
    definition_rev BIGINT NOT NULL,
    assets_hash VARCHAR(64) NOT NULL DEFAULT '',
    caller_uid BIGINT NOT NULL,
    caller_bot_uid BIGINT NOT NULL DEFAULT 0,
    state VARCHAR(16) NOT NULL DEFAULT 'active',
    last_event_seq BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_service_sessions_caller
    ON service_sessions (agent_id, caller_uid, caller_bot_uid);

CREATE TABLE IF NOT EXISTS service_runs (
    run_id VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL REFERENCES service_sessions(session_id) ON DELETE CASCADE,
    state VARCHAR(16) NOT NULL DEFAULT 'submitted',
    client_token VARCHAR(128) NOT NULL DEFAULT '',
    input_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    instance_id VARCHAR(64) NOT NULL DEFAULT '',
    lease_epoch BIGINT NOT NULL DEFAULT 0,
    lease_expires_at TIMESTAMPTZ,
    attempts INT NOT NULL DEFAULT 0,
    result_id VARCHAR(64) NOT NULL DEFAULT '',
    usage_json JSONB,
    result_json JSONB,
    checkpoint_ref VARCHAR(128) NOT NULL DEFAULT '',
    error_text TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (session_id, client_token)
);

CREATE TABLE IF NOT EXISTS service_run_events (
    run_id VARCHAR(64) NOT NULL REFERENCES service_runs(run_id) ON DELETE CASCADE,
    seq BIGINT NOT NULL,
    type VARCHAR(48) NOT NULL,
    payload_json JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (run_id, seq)
);

CREATE TABLE IF NOT EXISTS service_session_checkpoints (
    session_id VARCHAR(64) NOT NULL REFERENCES service_sessions(session_id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    snapshot_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, revision)
);

INSERT INTO service_agents (slug, name) VALUES ('hello', 'Hello 试点应用')
ON CONFLICT (slug) DO NOTHING;
INSERT INTO service_agent_versions (agent_id, version, definition_json, assets_hash)
SELECT id, 1, '{"async_threshold_seconds": 60, "models": ["gpt-6.1-sol"]}', '' FROM service_agents WHERE slug = 'hello'
ON CONFLICT DO NOTHING;

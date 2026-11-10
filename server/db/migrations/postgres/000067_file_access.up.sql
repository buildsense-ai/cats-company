CREATE TABLE IF NOT EXISTS file_uploads (
    object_key VARCHAR(1024) PRIMARY KEY CHECK (octet_length(object_key) BETWEEN 1 AND 1024),
    uploader_uid BIGINT NOT NULL,
    bound BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS file_message_bindings (
    resource_hash CHAR(64) NOT NULL,
    resource_key TEXT NOT NULL CHECK (octet_length(resource_key) BETWEEN 1 AND 16384),
    message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    topic_id VARCHAR(64) NOT NULL,
    PRIMARY KEY (resource_hash, message_id)
);
CREATE INDEX IF NOT EXISTS idx_file_message_bindings_message ON file_message_bindings (message_id);

CREATE TABLE IF NOT EXISTS file_media_sessions (
    token_hash CHAR(64) PRIMARY KEY,
    actor_uid BIGINT NOT NULL,
    parent_hash VARCHAR(64) NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_file_media_sessions_parent ON file_media_sessions (parent_hash);
CREATE INDEX IF NOT EXISTS idx_file_media_sessions_expiry ON file_media_sessions (expires_at);

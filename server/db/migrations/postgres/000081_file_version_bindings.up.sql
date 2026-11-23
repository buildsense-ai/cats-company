CREATE TABLE IF NOT EXISTS file_version_bindings (
    id BIGSERIAL PRIMARY KEY,
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    source_message_id BIGINT NOT NULL,
    source_attachment_index INTEGER NOT NULL,
    source_file_key VARCHAR(255) NOT NULL,
    latest_file_key VARCHAR(255) NOT NULL,
    latest_url VARCHAR(1024) NOT NULL,
    latest_name VARCHAR(255) NOT NULL,
    latest_size BIGINT NOT NULL DEFAULT 0,
    latest_mime_type VARCHAR(255) NOT NULL,
    updated_by_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_file_version_bindings_source UNIQUE (topic_id, source_message_id, source_attachment_index)
);
CREATE INDEX IF NOT EXISTS idx_file_version_bindings_topic_updated
    ON file_version_bindings (topic_id, updated_at);

package mysql

const createFileUploadsTable = `
CREATE TABLE IF NOT EXISTS file_uploads (
    object_key VARBINARY(1024) PRIMARY KEY,
    uploader_uid BIGINT NOT NULL,
    bound BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

const createFileMessageBindingsTable = `
CREATE TABLE IF NOT EXISTS file_message_bindings (
    resource_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    resource_key TEXT NOT NULL,
    message_id BIGINT NOT NULL,
    topic_id VARCHAR(64) NOT NULL,
    PRIMARY KEY (resource_hash, message_id),
    INDEX idx_file_message_bindings_message (message_id),
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

const createFileMediaSessionsTable = `
CREATE TABLE IF NOT EXISTS file_media_sessions (
    token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    actor_uid BIGINT NOT NULL,
    parent_hash VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    expires_at TIMESTAMP(6) NOT NULL,
    INDEX idx_file_media_sessions_parent (parent_hash),
    INDEX idx_file_media_sessions_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

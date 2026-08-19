CREATE TABLE IF NOT EXISTS image_upscale_tasks (
    process_id VARCHAR(128) PRIMARY KEY,
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_image_upscale_tasks_expires_at
    ON image_upscale_tasks (expires_at);

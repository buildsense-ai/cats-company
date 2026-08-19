CREATE TABLE IF NOT EXISTS commercial_operator_events (
    id BIGSERIAL PRIMARY KEY,
    service VARCHAR(128) NOT NULL,
    action VARCHAR(128) NOT NULL,
    target_type VARCHAR(64) NOT NULL DEFAULT '',
    target_ref VARCHAR(160) NOT NULL DEFAULT '',
    status_code INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_commercial_operator_events_created
    ON commercial_operator_events (created_at DESC);

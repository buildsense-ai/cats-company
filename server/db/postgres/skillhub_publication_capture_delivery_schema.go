package postgres

// Mirror migration 000044. No backfill from probes or upload grants.
const createSkillHubPublicationCaptureDeliveriesTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_capture_deliveries (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_entries(entry_id),
    state TEXT NOT NULL CHECK (state IN ('leased','pending','captured','needs_attention')),
    attempts INTEGER NOT NULL CHECK (attempts BETWEEN 1 AND 8),
    lease_token VARCHAR(32) NOT NULL CHECK (lease_token ~ '^[a-f0-9]{32}$'),
    lease_node VARCHAR(128) NOT NULL CHECK (lease_node ~ '^[A-Za-z0-9._:-]{1,128}$'),
    lease_connection VARCHAR(128) NOT NULL CHECK (lease_connection ~ '^[A-Za-z0-9._:-]{1,128}$'),
    lease_generation BIGINT NOT NULL CHECK (lease_generation > 0),
    lease_until TIMESTAMPTZ NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_code TEXT NOT NULL DEFAULT '' CHECK (last_code IN ('','retry','rejected','exhausted')),
    CHECK ((state IN ('leased','captured') AND last_code='') OR (state='pending' AND last_code='retry') OR (state='needs_attention' AND last_code IN ('rejected','exhausted')))
);
CREATE INDEX IF NOT EXISTS idx_skillhub_publication_capture_delivery_due ON skillhub_publication_capture_deliveries(state,next_attempt_at,entry_id);
`

package postgres

const createCommercialOpsActions = `
CREATE TABLE IF NOT EXISTS commercial_ops_actions (
 uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 operation_id VARCHAR(96) NOT NULL,
 fingerprint TEXT NOT NULL,
 request JSONB NOT NULL,
 preview JSONB NOT NULL,
 result JSONB,
 intent TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed')),
 package_applied BOOLEAN NOT NULL DEFAULT FALSE,
 relay_synced BOOLEAN NOT NULL DEFAULT FALSE,
 worker_status TEXT NOT NULL DEFAULT 'none' CHECK (worker_status IN ('none','pending','running','kept','created','queued','failed')),
 worker_uid BIGINT NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 lease TEXT NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,
 retry_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY (uid, operation_id)
);
CREATE INDEX IF NOT EXISTS idx_commercial_ops_actions_retry ON commercial_ops_actions(retry_at) WHERE status <> 'completed';
-- A consumed credit alone does not prove that provisioning completed. This
-- marker is written only on the successful HandleCreate path.
ALTER TABLE cloud_worker_credits ADD COLUMN IF NOT EXISTS operation_completed BOOLEAN NOT NULL DEFAULT FALSE;
`

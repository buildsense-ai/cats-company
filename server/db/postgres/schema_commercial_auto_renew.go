package postgres

// Internal auto-renew: per-user opt-in configuration plus the detailed run
// log. The scheduled runner extends an expiring personal/pro window for
// enabled users and records every attempt; relay-admin surfaces both. Mirrors
// 000025_commercial_auto_renew in the numbered migration set.
const migrateCommercialAutoRenew = `
-- Internal auto-renew: a per-user opt-in switch plus a detailed run log so the
-- relay-admin console can show exactly what the scheduled renewer did for each
-- account. Internal accounts opt in first; the same row shape is meant to
-- carry customer subscriptions once self-serve renewal opens.
CREATE TABLE IF NOT EXISTS commercial_auto_renew_configs (
    uid BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS commercial_auto_renew_runs (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL,
    action VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL,
    previous_expiry TIMESTAMPTZ,
    new_expiry TIMESTAMPTZ,
    message TEXT NOT NULL DEFAULT '',
    operation_id VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_commercial_auto_renew_runs_uid
    ON commercial_auto_renew_runs (uid, id DESC);

CREATE OR REPLACE TRIGGER trg_commercial_auto_renew_configs_updated_at
BEFORE UPDATE ON commercial_auto_renew_configs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
`

// rollbackCommercialAutoRenew drops the auto-renew tables. Renewal ledger
// rows and grants created by the runner stay as they are.
const rollbackCommercialAutoRenew = `
-- Roll back internal auto-renew: drop the run log first, then the opt-in
-- switch. Renewal ledger rows and grants created by the runner stay as they
-- are; uninstalling the scheduler never rewrites commercial history.
DROP INDEX IF EXISTS idx_commercial_auto_renew_runs_uid;
DROP TABLE IF EXISTS commercial_auto_renew_runs;
DROP TABLE IF EXISTS commercial_auto_renew_configs;
`

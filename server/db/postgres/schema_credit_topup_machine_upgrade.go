package postgres

// migrateCreditTopupAndMachineUpgrade is migration 000074 applied to a database
// created from scratch: CreateSchema builds the final shape, so the statements
// here are the same idempotent ALTER/CREATE pair the migration file carries.
//
// Two products share one rule: credit bought with money is not a subscription.
//
//   - A credit top-up is an ordinary commercial order with kind='credit_topup'
//     and no plan_id. It books exactly amount_fen * unitsPerFen units into the
//     paid bucket, with no gift, no trial, no machine and no change to any
//     package the buyer already holds.
//   - A machine upgrade sells the prorated difference between two shared-fleet
//     tiers for the remaining term of a worker the buyer already owns. It never
//     creates an entitlement, never extends the term, and keeps its plan in its
//     own table so a package renewal cannot pull the container back down.
const migrateCreditTopupAndMachineUpgrade = `
ALTER TABLE commercial_orders ADD COLUMN IF NOT EXISTS kind VARCHAR(24) NOT NULL DEFAULT 'plan';
ALTER TABLE commercial_orders ALTER COLUMN plan_id DROP NOT NULL;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_orders_kind' AND conrelid = 'commercial_orders'::regclass) THEN
        ALTER TABLE commercial_orders ADD CONSTRAINT chk_commercial_orders_kind
            CHECK (kind IN ('plan','credit_topup'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_orders_kind_plan' AND conrelid = 'commercial_orders'::regclass) THEN
        ALTER TABLE commercial_orders ADD CONSTRAINT chk_commercial_orders_kind_plan
            CHECK ((kind = 'credit_topup') = (plan_id IS NULL));
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_commercial_orders_uid_kind
    ON commercial_orders (uid, kind, created_at DESC);

CREATE TABLE IF NOT EXISTS cloud_worker_machine_upgrade_quotes (
    quote_id VARCHAR(64) PRIMARY KEY CHECK (quote_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$'),
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_name VARCHAR(80) NOT NULL,
    lifecycle_id BIGINT NOT NULL,
    from_plan VARCHAR(16) NOT NULL,
    to_plan VARCHAR(16) NOT NULL,
    cost_units BIGINT NOT NULL CHECK (cost_units > 0),
    worker_expires_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (from_plan IN ('light','standard','pro','heavy')),
    CHECK (to_plan IN ('light','standard','pro','heavy')),
    CHECK (from_plan <> to_plan)
);
CREATE INDEX IF NOT EXISTS idx_machine_upgrade_quotes_uid
    ON cloud_worker_machine_upgrade_quotes (uid, expires_at);

CREATE TABLE IF NOT EXISTS cloud_worker_machine_upgrades (
    operation_id VARCHAR(64) PRIMARY KEY CHECK (operation_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$'),
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_name VARCHAR(80) NOT NULL,
    lifecycle_id BIGINT NOT NULL,
    from_plan VARCHAR(16) NOT NULL,
    to_plan VARCHAR(16) NOT NULL,
    cost_units BIGINT NOT NULL CHECK (cost_units >= 0),
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','running','completed','failed','reconciling')),
    held BOOLEAN NOT NULL DEFAULT FALSE,
    settled BOOLEAN NOT NULL DEFAULT FALSE,
    client_request_id VARCHAR(64) NOT NULL,
    quote_id VARCHAR(64) NOT NULL DEFAULT '',
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    machine_expires_at TIMESTAMPTZ NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ DEFAULT NULL,
    finished_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (from_plan IN ('light','standard','pro','heavy')),
    CHECK (to_plan IN ('light','standard','pro','heavy')),
    CONSTRAINT uk_machine_upgrades_request UNIQUE (uid, client_request_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_machine_upgrades_open_tenant
    ON cloud_worker_machine_upgrades (tenant_name)
    WHERE status IN ('pending','running','reconciling');
CREATE INDEX IF NOT EXISTS idx_machine_upgrades_reconcile
    ON cloud_worker_machine_upgrades (status, updated_at)
    WHERE status IN ('pending','running','reconciling');

CREATE TABLE IF NOT EXISTS cloud_worker_machine_plan_terms (
    tenant_name VARCHAR(80) PRIMARY KEY,
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    lifecycle_id BIGINT NOT NULL DEFAULT 0,
    plan VARCHAR(16) NOT NULL CHECK (plan IN ('light','standard','pro','heavy')),
    base_plan VARCHAR(16) NOT NULL DEFAULT ''
        CHECK (base_plan IN ('','light','standard','pro','heavy')),
    operation_id VARCHAR(64) NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
ALTER TABLE cloud_worker_machine_plan_terms ADD COLUMN IF NOT EXISTS lifecycle_id BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_machine_plan_terms_lifecycle
    ON cloud_worker_machine_plan_terms (lifecycle_id);
CREATE INDEX IF NOT EXISTS idx_machine_plan_terms_owner
    ON cloud_worker_machine_plan_terms (owner_uid, expires_at);
`

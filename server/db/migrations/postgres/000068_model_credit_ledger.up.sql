-- Credit ledger foundation: integer micro-credit buckets, an idempotent ledger
-- event log and per-request holds. No product, payment, relay or route uses
-- these tables yet, and the legacy commercial quota/token tables are untouched.

CREATE TABLE IF NOT EXISTS model_credit_accounts (
    uid BIGINT PRIMARY KEY CHECK (uid > 0),
    monthly_units BIGINT NOT NULL DEFAULT 0 CHECK (monthly_units >= 0),
    gift_units BIGINT NOT NULL DEFAULT 0 CHECK (gift_units >= 0),
    paid_units BIGINT NOT NULL DEFAULT 0 CHECK (paid_units >= 0),
    debt_units BIGINT NOT NULL DEFAULT 0 CHECK (debt_units >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS model_credit_ledger_events (
    event_key VARCHAR(192) PRIMARY KEY CHECK (event_key ~ '^[a-z_]+:[A-Za-z0-9_.:-]{1,182}$'),
    uid BIGINT NOT NULL REFERENCES model_credit_accounts(uid) ON DELETE RESTRICT,
    bucket VARCHAR(16) NOT NULL CHECK (bucket IN ('monthly','gift','paid','debt')),
    event_type VARCHAR(16) NOT NULL CHECK (event_type IN ('grant','reserve','settle','release','debt_accrued','debt_repaid')),
    units BIGINT NOT NULL CHECK (units > 0),
    request_id VARCHAR(128) CHECK (request_id ~ '^[A-Za-z0-9_.:-]{1,128}$'),
    monthly_balance BIGINT NOT NULL CHECK (monthly_balance >= 0),
    gift_balance BIGINT NOT NULL CHECK (gift_balance >= 0),
    paid_balance BIGINT NOT NULL CHECK (paid_balance >= 0),
    debt_balance BIGINT NOT NULL CHECK (debt_balance >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (request_id IS NOT NULL OR event_type IN ('grant','debt_accrued','debt_repaid')),
    CHECK ((bucket = 'debt') = (event_type IN ('debt_accrued','debt_repaid')))
);
CREATE INDEX IF NOT EXISTS idx_model_credit_ledger_events_uid_created ON model_credit_ledger_events (uid, created_at);
CREATE INDEX IF NOT EXISTS idx_model_credit_ledger_events_request ON model_credit_ledger_events (request_id) WHERE request_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS model_credit_reservations (
    uid BIGINT NOT NULL REFERENCES model_credit_accounts(uid) ON DELETE RESTRICT,
    request_id VARCHAR(128) NOT NULL CHECK (request_id ~ '^[A-Za-z0-9_.:-]{1,128}$'),
    reserved_units BIGINT NOT NULL CHECK (reserved_units > 0),
    monthly_units BIGINT NOT NULL DEFAULT 0 CHECK (monthly_units >= 0),
    gift_units BIGINT NOT NULL DEFAULT 0 CHECK (gift_units >= 0),
    paid_units BIGINT NOT NULL DEFAULT 0 CHECK (paid_units >= 0),
    settled_units BIGINT CHECK (settled_units >= 0),
    state VARCHAR(16) NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','settled','released')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    no_usage_confirmed_at TIMESTAMPTZ,
    settled_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    PRIMARY KEY (uid, request_id),
    CHECK (monthly_units + gift_units + paid_units = reserved_units),
    CHECK ((state = 'reserved' AND settled_units IS NULL AND settled_at IS NULL AND released_at IS NULL) OR
           (state = 'settled' AND settled_units IS NOT NULL AND settled_at IS NOT NULL AND released_at IS NULL) OR
           (state = 'released' AND settled_units IS NULL AND settled_at IS NULL AND released_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_model_credit_reservations_expired
    ON model_credit_reservations (lease_expires_at, uid, request_id)
    WHERE state = 'reserved' AND no_usage_confirmed_at IS NOT NULL;
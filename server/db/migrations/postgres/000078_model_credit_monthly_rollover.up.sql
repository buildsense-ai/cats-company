ALTER TABLE model_credit_accounts
    ADD COLUMN IF NOT EXISTS frozen_monthly_units BIGINT NOT NULL DEFAULT 0 CHECK (frozen_monthly_units >= 0);

-- Subledger for the remaining units of each subscription grant. Keep revoked
-- rows so that an outstanding hold can never return refunded units.
CREATE TABLE IF NOT EXISTS model_credit_monthly_lots (
    grant_key TEXT PRIMARY KEY,
    uid BIGINT NOT NULL REFERENCES model_credit_accounts(uid) ON DELETE CASCADE,
    entitlement_id BIGINT,
    period_start TIMESTAMPTZ NOT NULL,
    units BIGINT NOT NULL CHECK (units >= 0),
    revoked BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_model_credit_monthly_lots_uid
    ON model_credit_monthly_lots (uid, period_start, grant_key);

-- An empty grant_key accounts for manual monthly units outside a subscription.
CREATE TABLE IF NOT EXISTS model_credit_monthly_hold_lots (
    uid BIGINT NOT NULL REFERENCES model_credit_accounts(uid) ON DELETE CASCADE,
    request_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    grant_key TEXT NOT NULL,
    units BIGINT NOT NULL CHECK (units > 0),
    PRIMARY KEY (uid, request_id, ordinal)
);

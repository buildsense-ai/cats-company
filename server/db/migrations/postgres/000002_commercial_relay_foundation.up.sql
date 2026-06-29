CREATE TABLE IF NOT EXISTS commercial_plans (
    id BIGSERIAL PRIMARY KEY,
    slug VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    monthly_budget_cny NUMERIC(14,6) NOT NULL DEFAULT 0,
    model_budgets JSONB NOT NULL DEFAULT '{}'::jsonb,
    duration_days INT NOT NULL DEFAULT 30,
    state SMALLINT NOT NULL DEFAULT 0,
    sort_order INT NOT NULL DEFAULT 100,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_plans_state CHECK (state IN (0, 1)),
    CONSTRAINT chk_commercial_plans_duration CHECK (duration_days > 0),
    CONSTRAINT chk_commercial_plans_budget CHECK (monthly_budget_cny >= 0)
);

CREATE TABLE IF NOT EXISTS commercial_invite_codes (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(64) NOT NULL UNIQUE,
    plan_id BIGINT NOT NULL REFERENCES commercial_plans(id) ON DELETE RESTRICT,
    max_redemptions INT NOT NULL DEFAULT 1,
    redeemed_count INT NOT NULL DEFAULT 0,
    state SMALLINT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_by_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_invites_state CHECK (state IN (0, 1)),
    CONSTRAINT chk_commercial_invites_redemptions CHECK (max_redemptions > 0 AND redeemed_count >= 0 AND redeemed_count <= max_redemptions)
);

CREATE TABLE IF NOT EXISTS commercial_entitlements (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id BIGINT NOT NULL REFERENCES commercial_plans(id) ON DELETE RESTRICT,
    source VARCHAR(32) NOT NULL DEFAULT 'manual',
    source_ref VARCHAR(128) NOT NULL DEFAULT '',
    state VARCHAR(16) NOT NULL DEFAULT 'active',
    starts_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_entitlements_state CHECK (state IN ('active','expired','revoked'))
);

CREATE TABLE IF NOT EXISTS commercial_quota_grants (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id BIGINT DEFAULT NULL REFERENCES commercial_plans(id) ON DELETE SET NULL,
    invite_code_id BIGINT DEFAULT NULL REFERENCES commercial_invite_codes(id) ON DELETE SET NULL,
    grant_type VARCHAR(32) NOT NULL DEFAULT 'manual',
    model VARCHAR(128) NOT NULL DEFAULT '*',
    amount_cny NUMERIC(14,6) NOT NULL,
    reset_duration VARCHAR(16) NOT NULL DEFAULT '1M',
    effective_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    note TEXT NOT NULL DEFAULT '',
    operator_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_quota_grants_amount CHECK (amount_cny > 0)
);

CREATE TABLE IF NOT EXISTS commercial_quota_ledger (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    model VARCHAR(128) NOT NULL DEFAULT '*',
    amount_cny NUMERIC(14,6) NOT NULL,
    entry_type VARCHAR(32) NOT NULL,
    source_type VARCHAR(32) NOT NULL,
    source_id BIGINT DEFAULT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_commercial_plans_state_sort ON commercial_plans (state, sort_order, id);
CREATE INDEX IF NOT EXISTS idx_commercial_invite_codes_plan ON commercial_invite_codes (plan_id, state);
CREATE INDEX IF NOT EXISTS idx_commercial_invite_codes_code_lower ON commercial_invite_codes (lower(code));
CREATE INDEX IF NOT EXISTS idx_commercial_entitlements_uid_state ON commercial_entitlements (uid, state, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_entitlements_invite_once
    ON commercial_entitlements (uid, source, source_ref)
    WHERE source = 'invite';
CREATE INDEX IF NOT EXISTS idx_commercial_quota_grants_uid_model ON commercial_quota_grants (uid, model, effective_at);
CREATE INDEX IF NOT EXISTS idx_commercial_quota_grants_expires ON commercial_quota_grants (expires_at);
CREATE INDEX IF NOT EXISTS idx_commercial_quota_ledger_uid_created ON commercial_quota_ledger (uid, created_at);

CREATE OR REPLACE TRIGGER trg_commercial_plans_updated_at BEFORE UPDATE ON commercial_plans
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_invite_codes_updated_at BEFORE UPDATE ON commercial_invite_codes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_entitlements_updated_at BEFORE UPDATE ON commercial_entitlements
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

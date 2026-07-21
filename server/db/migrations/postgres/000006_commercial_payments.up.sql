ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS price_fen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS currency VARCHAR(8) NOT NULL DEFAULT 'CNY';
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS sale_state VARCHAR(16) NOT NULL DEFAULT 'hidden';
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS purchase_limit INT NOT NULL DEFAULT 0;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_price') THEN
        ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_price CHECK (price_fen >= 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_sale_state') THEN
        ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_sale_state CHECK (sale_state IN ('hidden','test','public'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_purchase_limit') THEN
        ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_purchase_limit CHECK (purchase_limit >= 0);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS commercial_orders (
    id BIGSERIAL PRIMARY KEY,
    order_no VARCHAR(40) NOT NULL UNIQUE,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    plan_id BIGINT NOT NULL REFERENCES commercial_plans(id) ON DELETE RESTRICT,
    plan_slug VARCHAR(64) NOT NULL,
    plan_name VARCHAR(128) NOT NULL,
    plan_description TEXT NOT NULL DEFAULT '',
    plan_duration_days INT NOT NULL,
    plan_monthly_budget_cny NUMERIC(14,6) NOT NULL DEFAULT 0,
    plan_model_budgets JSONB NOT NULL DEFAULT '{}'::jsonb,
    amount_fen BIGINT NOT NULL,
    currency VARCHAR(8) NOT NULL DEFAULT 'CNY',
    channel VARCHAR(32) NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'created',
    provider_trade_no VARCHAR(128) NOT NULL DEFAULT '',
    code_url TEXT NOT NULL DEFAULT '',
    client_request_id VARCHAR(64) NOT NULL,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    paid_at TIMESTAMPTZ DEFAULT NULL,
    fulfilled_at TIMESTAMPTZ DEFAULT NULL,
    closed_at TIMESTAMPTZ DEFAULT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_orders_amount CHECK (amount_fen > 0),
    CONSTRAINT chk_commercial_orders_status CHECK (status IN ('created','pending','paid','fulfilled','closed','failed','refunding','refunded'))
);

CREATE TABLE IF NOT EXISTS commercial_payment_events (
    id BIGSERIAL PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    event_id VARCHAR(160) NOT NULL,
    order_no VARCHAR(40) NOT NULL REFERENCES commercial_orders(order_no) ON DELETE RESTRICT,
    provider_trade_no VARCHAR(128) NOT NULL DEFAULT '',
    event_type VARCHAR(32) NOT NULL DEFAULT 'payment_success',
    payload_hash VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'processed',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_payment_events_status CHECK (status IN ('processed','rejected','ignored')),
    UNIQUE(channel, event_id)
);

CREATE TABLE IF NOT EXISTS commercial_managed_relay_budgets (
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    model VARCHAR(128) NOT NULL,
    provider VARCHAR(128) NOT NULL,
    allowed_models JSONB NOT NULL DEFAULT '[]'::jsonb,
    max_limit NUMERIC(14,6) NOT NULL,
    reset_duration VARCHAR(16) NOT NULL DEFAULT '1M',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY(uid, model, provider, allowed_models)
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_entitlements_order_once
    ON commercial_entitlements (uid, source, source_ref)
    WHERE source = 'order';
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_entitlements_trial_once
    ON commercial_entitlements (uid, source)
    WHERE source = 'trial';
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_orders_uid_request ON commercial_orders (uid, client_request_id);
CREATE INDEX IF NOT EXISTS idx_commercial_orders_uid_created ON commercial_orders (uid, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_commercial_orders_status_expires ON commercial_orders (status, expires_at);
CREATE INDEX IF NOT EXISTS idx_commercial_payment_events_order ON commercial_payment_events (order_no, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_commercial_managed_relay_uid ON commercial_managed_relay_budgets (uid);

CREATE OR REPLACE TRIGGER trg_commercial_orders_updated_at BEFORE UPDATE ON commercial_orders
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_managed_relay_budgets_updated_at BEFORE UPDATE ON commercial_managed_relay_budgets
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

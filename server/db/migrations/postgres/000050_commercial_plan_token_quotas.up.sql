CREATE TABLE IF NOT EXISTS commercial_plan_token_quotas (
    plan_id BIGINT PRIMARY KEY REFERENCES commercial_plans(id) ON DELETE CASCADE,
    token_quota BIGINT NOT NULL DEFAULT 0,
    reset_duration VARCHAR(16) NOT NULL DEFAULT '1M',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_plan_token_quotas_quota CHECK (token_quota >= 0),
    CONSTRAINT chk_commercial_plan_token_quotas_duration CHECK (reset_duration IN ('1w', '1M'))
);

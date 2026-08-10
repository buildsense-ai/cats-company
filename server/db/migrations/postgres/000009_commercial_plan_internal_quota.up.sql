ALTER TABLE commercial_plans
    ADD COLUMN IF NOT EXISTS internal_quota_tokens BIGINT NOT NULL DEFAULT 0;

ALTER TABLE commercial_plans
    DROP CONSTRAINT IF EXISTS chk_commercial_plans_internal_quota_tokens;

ALTER TABLE commercial_plans
    ADD CONSTRAINT chk_commercial_plans_internal_quota_tokens
    CHECK (internal_quota_tokens >= 0);

ALTER TABLE commercial_plans
    DROP CONSTRAINT IF EXISTS chk_commercial_plans_internal_quota_tokens;

ALTER TABLE commercial_plans
    DROP COLUMN IF EXISTS internal_quota_tokens;

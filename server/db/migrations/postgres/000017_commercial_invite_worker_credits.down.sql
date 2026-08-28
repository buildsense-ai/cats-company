DROP INDEX IF EXISTS idx_cloud_worker_credits_entitlement;
ALTER TABLE cloud_worker_credits DROP COLUMN IF EXISTS entitlement_id;
ALTER TABLE commercial_invite_codes DROP CONSTRAINT IF EXISTS chk_commercial_invites_worker_credits;
ALTER TABLE commercial_invite_codes DROP COLUMN IF EXISTS cloud_worker_credits;

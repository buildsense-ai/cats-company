ALTER TABLE commercial_invite_codes
    ADD COLUMN IF NOT EXISTS cloud_worker_credits INT NOT NULL DEFAULT 0;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_commercial_invites_worker_credits'
          AND conrelid = 'commercial_invite_codes'::regclass
    ) THEN
        ALTER TABLE commercial_invite_codes
            ADD CONSTRAINT chk_commercial_invites_worker_credits
            CHECK (cloud_worker_credits >= 0);
    END IF;
END $$;

ALTER TABLE cloud_worker_credits
    ADD COLUMN IF NOT EXISTS entitlement_id BIGINT
    REFERENCES commercial_entitlements(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_cloud_worker_credits_entitlement
    ON cloud_worker_credits(entitlement_id)
    WHERE entitlement_id IS NOT NULL;

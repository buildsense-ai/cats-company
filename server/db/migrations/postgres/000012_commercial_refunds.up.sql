ALTER TABLE commercial_orders
    ADD COLUMN IF NOT EXISTS refund_request_no VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refunded_at TIMESTAMPTZ DEFAULT NULL;

ALTER TABLE commercial_quota_grants
    ADD COLUMN IF NOT EXISTS source_ref VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ DEFAULT NULL;

UPDATE commercial_quota_grants
SET source_ref = substring(note FROM 7)
WHERE grant_type = 'order' AND source_ref = '' AND note LIKE 'order %';

UPDATE commercial_quota_grants
SET source_ref = substring(note FROM 8)
WHERE grant_type = 'invite' AND source_ref = '' AND note LIKE 'invite %';

CREATE INDEX IF NOT EXISTS idx_commercial_quota_grants_source
    ON commercial_quota_grants (grant_type, source_ref);

CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_refund_ledger_grant
    ON commercial_quota_ledger (source_type, source_id, entry_type)
    WHERE source_type = 'refund' AND entry_type = 'revoke';

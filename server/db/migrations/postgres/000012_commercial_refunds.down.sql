DROP INDEX IF EXISTS uk_commercial_refund_ledger_grant;
DROP INDEX IF EXISTS idx_commercial_quota_grants_source;

ALTER TABLE commercial_quota_grants
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS source_ref;

ALTER TABLE commercial_orders
    DROP COLUMN IF EXISTS refunded_at,
    DROP COLUMN IF EXISTS refund_request_no;

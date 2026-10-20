-- 000047_service_run_ledger_billing_state (down)
DROP INDEX IF EXISTS idx_service_run_ledger_pending;
ALTER TABLE service_run_ledger DROP COLUMN IF EXISTS billing_state;

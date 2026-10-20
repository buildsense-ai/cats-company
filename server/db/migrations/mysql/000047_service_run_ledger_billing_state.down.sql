-- 000047_service_run_ledger_billing_state (down, MySQL)
DROP INDEX idx_service_run_ledger_pending ON service_run_ledger;
ALTER TABLE service_run_ledger DROP COLUMN billing_state;

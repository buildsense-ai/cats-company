-- 000046_service_run_ledger (down)
DROP INDEX IF EXISTS idx_service_run_ledger_agent;
DROP INDEX IF EXISTS idx_service_run_ledger_payee;
DROP INDEX IF EXISTS idx_service_run_ledger_payer;
DROP TABLE IF EXISTS service_run_ledger;

-- 000047_service_run_ledger_billing_state (up, MySQL)
-- 结算状态（设计 §4.5）：按会话请求实时结算。
ALTER TABLE service_run_ledger ADD COLUMN billing_state VARCHAR(24) NOT NULL DEFAULT 'free';
CREATE INDEX idx_service_run_ledger_pending ON service_run_ledger (payer_uid);

DROP INDEX IF EXISTS idx_model_credit_reservations_root_task;
ALTER TABLE model_credit_reservations DROP CONSTRAINT IF EXISTS chk_model_credit_reservations_bucket_total;
ALTER TABLE model_credit_reservations DROP COLUMN IF EXISTS root_task_id;
ALTER TABLE model_credit_reservations DROP COLUMN IF EXISTS unfunded_units;
-- 恢复 000068 的 hold 合计约束。NOT VALID 让回滚在历史透支预占还活着时也能成功：
-- 已经存在的行不再校验，新行仍然受限。
ALTER TABLE model_credit_reservations DROP CONSTRAINT IF EXISTS model_credit_reservations_check;
ALTER TABLE model_credit_reservations ADD CONSTRAINT model_credit_reservations_check
    CHECK (monthly_units + gift_units + paid_units = reserved_units) NOT VALID;

DROP INDEX IF EXISTS idx_model_credit_tasks_uid_state;
DROP TABLE IF EXISTS model_credit_tasks;

ALTER TABLE model_credit_accounts DROP COLUMN IF EXISTS retained_task_id;

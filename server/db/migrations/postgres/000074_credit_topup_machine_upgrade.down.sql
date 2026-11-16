-- 回滚 000074 仅允许未产生交易的环境。生产回退应用版本时保留兼容表结构。
-- 禁止为了恢复 NOT NULL 而删除充值订单、支付回执、预占或已购买权益。
BEGIN;
LOCK TABLE commercial_orders, cloud_worker_machine_upgrades, cloud_worker_machine_plan_terms IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM commercial_orders WHERE kind = 'credit_topup')
       OR EXISTS (SELECT 1 FROM cloud_worker_machine_upgrades)
       OR EXISTS (SELECT 1 FROM cloud_worker_machine_plan_terms) THEN
        RAISE EXCEPTION 'cannot roll back credit top-up/machine upgrade schema with transaction history; preserve records and roll back application only';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_commercial_orders_uid_kind;
ALTER TABLE commercial_orders DROP CONSTRAINT IF EXISTS chk_commercial_orders_kind_plan;
ALTER TABLE commercial_orders DROP CONSTRAINT IF EXISTS chk_commercial_orders_kind;
ALTER TABLE commercial_orders DROP COLUMN IF EXISTS kind;
ALTER TABLE commercial_orders ALTER COLUMN plan_id SET NOT NULL;

DROP INDEX IF EXISTS idx_machine_plan_terms_lifecycle;
DROP INDEX IF EXISTS idx_machine_plan_terms_owner;
DROP TABLE IF EXISTS cloud_worker_machine_plan_terms;
DROP INDEX IF EXISTS idx_machine_upgrades_reconcile;
DROP INDEX IF EXISTS uk_machine_upgrades_open_tenant;
DROP TABLE IF EXISTS cloud_worker_machine_upgrades;
DROP INDEX IF EXISTS idx_machine_upgrade_quotes_uid;
DROP TABLE IF EXISTS cloud_worker_machine_upgrade_quotes;
COMMIT;

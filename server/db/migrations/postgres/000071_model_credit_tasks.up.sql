CREATE TABLE IF NOT EXISTS model_credit_tasks (
    root_task_id VARCHAR(128) PRIMARY KEY CHECK (root_task_id ~ '^[A-Za-z0-9_.:-]{1,128}$'),
    uid BIGINT NOT NULL REFERENCES model_credit_accounts(uid) ON DELETE RESTRICT,
    state VARCHAR(16) NOT NULL CHECK (state IN ('running','paused','completed')),
    reason VARCHAR(64) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    started_seq BIGINT NOT NULL CHECK (started_seq > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (uid, started_seq)
);
CREATE INDEX IF NOT EXISTS idx_model_credit_tasks_uid_state
    ON model_credit_tasks (uid, state, started_seq DESC);
-- 套餐档位快照：合法 start 那一刻从服务端有效套餐取 plan_tier(1..3) 存下来，之后
-- start 重放 / status / resume / finish 都回同一份；任务运行中套餐到期也要按到期前
-- 费率跑到结束（docs/credit-plan-rules-20261109.md）。0 = 没有可信快照（本列上线前
-- 写下的旧行），读取方必须失败关闭，绝不替旧任务猜档位。
-- 用 ADD COLUMN IF NOT EXISTS：这段 DDL 每次启动都会跑，重复执行必须安全。
ALTER TABLE model_credit_tasks ADD COLUMN IF NOT EXISTS plan_tier SMALLINT NOT NULL DEFAULT 0
    CHECK (plan_tier BETWEEN 0 AND 3);

ALTER TABLE model_credit_accounts ADD COLUMN IF NOT EXISTS retained_task_id VARCHAR(128)
    CHECK (retained_task_id IS NULL OR retained_task_id ~ '^[A-Za-z0-9_.:-]{1,128}$');

ALTER TABLE model_credit_reservations ADD COLUMN IF NOT EXISTS root_task_id VARCHAR(128)
    CHECK (root_task_id IS NULL OR root_task_id ~ '^[A-Za-z0-9_.:-]{1,128}$');
ALTER TABLE model_credit_reservations ADD COLUMN IF NOT EXISTS unfunded_units BIGINT NOT NULL DEFAULT 0
    CHECK (unfunded_units >= 0);
-- 000068 的合计约束只允许「三个消费桶之和 = 预占」，透支预占还要算上 unfunded_units。
ALTER TABLE model_credit_reservations DROP CONSTRAINT IF EXISTS model_credit_reservations_check;
DO $$
BEGIN
    IF to_regclass('model_credit_reservations') IS NULL THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'chk_model_credit_reservations_bucket_total' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE model_credit_reservations ADD CONSTRAINT chk_model_credit_reservations_bucket_total
            CHECK (monthly_units + gift_units + paid_units + unfunded_units = reserved_units);
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_model_credit_reservations_root_task
    ON model_credit_reservations (uid, root_task_id)
    WHERE root_task_id IS NOT NULL;

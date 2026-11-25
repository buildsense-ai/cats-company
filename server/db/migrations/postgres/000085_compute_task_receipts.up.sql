-- Cats 算力任务的终态回执收件箱。
--
-- 算力任务是独立的 /api/compute/tasks/... 链路，不走 service_runs，所以不能复用
-- service_run_receipts：那张表的外键指向 run。这里以 quote_id 为唯一事实锚点，
-- 一个任务至多一份回执（UNIQUE + ON CONFLICT DO NOTHING 保证 finish 重放不生成第二份）。
--
-- 回执在 FinishComputeTask 的同一事务里写入（与结算、终态同一提交），因此不存在
-- 「钱扣了但没有回执」的窗口。delivered_at 为空 = 还没送到调用方任何一个在线连接，
-- 投递器按 created_at 顺序补投；delivery_attempts 只是可观测的尝试计数。
CREATE TABLE IF NOT EXISTS compute_task_receipts (
    receipt_id VARCHAR(80) PRIMARY KEY CHECK (receipt_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{3,79}$'),
    quote_id VARCHAR(64) NOT NULL UNIQUE REFERENCES compute_tasks(quote_id) ON DELETE CASCADE,
    caller_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL CHECK (status IN ('completed','failed')),
    payload_json JSONB NOT NULL CHECK (jsonb_typeof(payload_json) = 'object'),
    delivery_attempts INT NOT NULL DEFAULT 0 CHECK (delivery_attempts >= 0),
    delivered_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_compute_task_receipts_pending
    ON compute_task_receipts (created_at) WHERE delivered_at IS NULL;

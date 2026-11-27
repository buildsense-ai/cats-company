-- 算力任务容量准入（migration 000086）。
--
-- 旧库通过 ALTER 获得预留字段，新库先由 000083 建表，再由本迁移补列；
-- 事务级 advisory lock 保证多个 worker 的 claim 使用同一容量快照。
ALTER TABLE compute_tasks
    ADD COLUMN IF NOT EXISTS admitted_nodes INT NOT NULL DEFAULT 0;
UPDATE compute_tasks t
SET admitted_nodes = (q.spec->>'nodes')::int
FROM compute_task_quotes q
WHERE q.quote_id = t.quote_id
  AND t.state = 'active'
  AND t.admitted_nodes = 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'compute_tasks'::regclass
          AND conname = 'compute_tasks_admitted_nodes_nonnegative'
    ) THEN
        ALTER TABLE compute_tasks
            ADD CONSTRAINT compute_tasks_admitted_nodes_nonnegative CHECK (admitted_nodes >= 0);
    END IF;
END
$$;

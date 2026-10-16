-- 回滚：删除实例注册表与 runs 时间索引。

DROP INDEX IF EXISTS idx_service_runs_created_at;

DROP TABLE IF EXISTS service_instances;

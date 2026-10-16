-- 实例心跳注册（监控数据源；设计 §7.1"实例在线数"）。
-- 背景：实例每 10s 向平台发心跳（携 active_runs + metrics 快照），此前只回执不落库，
-- 平台完全不知道有几台实例活着、各自多忙。本表 = 实例注册表（每实例一行，UPSERT 最新值）。
-- 同时为监控板查询补 service_runs(created_at) 索引（按时间窗聚合）。

CREATE TABLE IF NOT EXISTS service_instances (
    instance_id VARCHAR(64) PRIMARY KEY,
    active_runs INT NOT NULL DEFAULT 0,
    metrics_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS idx_service_runs_created_at
    ON service_runs (created_at DESC);

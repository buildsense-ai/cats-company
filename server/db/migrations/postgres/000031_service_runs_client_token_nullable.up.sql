-- 幂等键可选：client_token 未提供 = NULL（而非空串）。
-- 背景：空串参与 UNIQUE (session_id, client_token) → 同会话第二次不带 token 的调用
-- 永久撞唯一约束（生产 2026-10-15 实测 create_run_failed duplicate key）。
-- 多 NULL 在 PG 唯一约束下互不冲突（NULLS DISTINCT 默认）。

ALTER TABLE service_runs
    ALTER COLUMN client_token DROP NOT NULL;

-- 历史行回填（数据操作仅编号迁移；CreateSchema 只做结构）。
UPDATE service_runs SET client_token = NULL WHERE client_token = '';

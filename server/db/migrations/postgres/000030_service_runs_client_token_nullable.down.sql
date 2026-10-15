-- 回滚：恢复 NOT NULL 语义。
-- 注意：若同一会话存在多个 NULL token 的 run，回填空串会违反
-- UNIQUE (session_id, client_token)，down 将失败——属预期保护（不允许制造歧义数据）。
UPDATE service_runs SET client_token = '' WHERE client_token IS NULL;

ALTER TABLE service_runs
    ALTER COLUMN client_token SET NOT NULL;

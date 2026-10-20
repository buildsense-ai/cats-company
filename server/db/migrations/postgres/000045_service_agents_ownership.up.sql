-- 000045_service_agents_ownership (up)
-- 用户自助上架：agent 归属与可见性（设计 §4.3 P2）。
-- 幂等：IF NOT EXISTS，可在已有 data 上重复执行。
ALTER TABLE service_agents
    ADD COLUMN IF NOT EXISTS owner_kind VARCHAR(16) NOT NULL DEFAULT 'internal',
    ADD COLUMN IF NOT EXISTS owner_uid BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS visibility VARCHAR(16) NOT NULL DEFAULT 'public';

CREATE INDEX IF NOT EXISTS idx_service_agents_owner ON service_agents (owner_uid);

-- 000045_service_agents_ownership (up, MySQL)
-- 用户自助上架：agent 归属与可见性（设计 §4.3 P2）。
ALTER TABLE service_agents ADD COLUMN owner_kind VARCHAR(16) NOT NULL DEFAULT 'internal';
ALTER TABLE service_agents ADD COLUMN owner_uid BIGINT NOT NULL DEFAULT 0;
ALTER TABLE service_agents ADD COLUMN visibility VARCHAR(16) NOT NULL DEFAULT 'public';
CREATE INDEX idx_service_agents_owner ON service_agents (owner_uid);

-- 000045_service_agents_ownership (down, MySQL)
DROP INDEX idx_service_agents_owner ON service_agents;
ALTER TABLE service_agents DROP COLUMN visibility;
ALTER TABLE service_agents DROP COLUMN owner_uid;
ALTER TABLE service_agents DROP COLUMN owner_kind;

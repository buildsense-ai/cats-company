-- 000045_service_agents_ownership (down)
DROP INDEX IF EXISTS idx_service_agents_owner;

ALTER TABLE service_agents
    DROP COLUMN IF EXISTS visibility,
    DROP COLUMN IF EXISTS owner_uid,
    DROP COLUMN IF EXISTS owner_kind;

DROP INDEX IF EXISTS idx_service_agent_versions_assets_hash;

ALTER TABLE service_agent_versions
    DROP COLUMN IF EXISTS package_json;

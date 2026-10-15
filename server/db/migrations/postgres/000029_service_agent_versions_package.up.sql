-- 云端包分发：service_agent_versions 增加 package_json（自包含包原文，内容寻址 assets_hash）。
-- 与 server/db/postgres/schema.go 的 CreateSchema 保持等价（运行时权威 = CreateSchema）。
ALTER TABLE service_agent_versions
    ADD COLUMN IF NOT EXISTS package_json JSONB DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_service_agent_versions_assets_hash
    ON service_agent_versions (assets_hash);

-- 000062_service_agents_self_intro (up)
-- 自述建议（self_intro）：应用跑完后「建议」的自我描述，等 owner 确认才写进 definition（PG 镜像）。
-- MySQL 一条 ALTER 只能加一列；重复列错误由迁移执行器的 isIgnorableMigrationError 吞掉。
-- TEXT 不能带字面量 DEFAULT，NOT NULL 又会让省略这两列的 INSERT 在 strict 模式报 1364，
-- 所以用可空列；读取侧 COALESCE(..., '') 兜底。
ALTER TABLE service_agents ADD COLUMN self_intro_description TEXT NULL;
ALTER TABLE service_agents ADD COLUMN self_intro_input_schema TEXT NULL;

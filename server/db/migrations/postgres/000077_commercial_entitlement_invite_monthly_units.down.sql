-- 回滚 000077：邀请兑换的期限点数快照。
--
-- 先摘约束再删列：约束依赖该列，顺序反了会失败。删列即丢掉已经写下的快照
-- （邀请权益本身、gift、机器额度都不受影响），所以这里不做任何数据搬运。
ALTER TABLE commercial_entitlements DROP CONSTRAINT IF EXISTS chk_commercial_entitlements_invite_monthly_units;
ALTER TABLE commercial_entitlements DROP COLUMN IF EXISTS invite_monthly_units;

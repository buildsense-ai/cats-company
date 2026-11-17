-- 人工复核云机器升配（machine upgrade review）的授权审计。
--
-- 为什么必须落库：升配重试是「把钱继续花下去」的唯一人工入口。原来这条路径只在
-- 日志里写一行 actor/uid/op/reason —— 日志既不是授权依据，也不可追溯：轮转就丢，
-- 改日志等于改历史，而且「谁授权了这次重跑」在事后核查时根本查不出来。
--
-- 三条规则：
--   1. 只有真的拿到租约认领（leasing claim）才算授权，空转（终态、pending、
--      保护期内的行）一律不写审计；
--   2. 写审计和认领在同一个事务里：审计写不进去就连认领一起回滚，绝不出现
--      「操作被授权重跑、却没留下谁授权的记录」；
--   3. 表只追加：UPDATE/DELETE 由触发器拒绝，历史不可改写。
--
-- 只记事实，不记凭据：授权人 uid、操作 id、调查理由、认领前后的现场。

CREATE TABLE IF NOT EXISTS cloud_worker_machine_upgrade_review_audits (
    audit_id BIGSERIAL PRIMARY KEY,
    -- 被授权重跑的那笔升配操作。审计依附于操作行：操作行被删（当前没有任何
    -- 代码路径会删它，只发生在整库/整 schema 销毁）时一起消失。
    operation_id VARCHAR(64) NOT NULL
        REFERENCES cloud_worker_machine_upgrades(operation_id) ON DELETE CASCADE,
    -- 授权人（管理员）uid。故意不加外键：删掉一个运维账号既不能抹掉授权历史，
    -- 也不能因为历史存在而失败。
    actor_uid BIGINT NOT NULL CHECK (actor_uid > 0),
    action VARCHAR(32) NOT NULL
        CHECK (action IN ('retry_machine_upgrade_review')),
    -- 调查理由，8~1000 个「字符」（不是字节）：中文理由按字节校验等于抬高标准，
    -- 按字符才是运维真实感受；上限挡住整段日志粘贴。
    reason TEXT NOT NULL
        CHECK (char_length(btrim(reason)) BETWEEN 8 AND 1000),
    -- 认领前的现场：重跑之前那一行长什么样，事后才能判断这次重试是否合理。
    previous_status VARCHAR(16) NOT NULL
        CHECK (previous_status IN ('pending','running','completed','failed','reconciling')),
    previous_message TEXT NOT NULL DEFAULT '',
    previous_attempts INT NOT NULL CHECK (previous_attempts >= 0),
    -- 认领本身：这次拿到的是第几次尝试、什么时刻拿到的。attempts 只会 +1，
    -- 所以 claimed_attempt 必然等于认领前的 attempts + 1。
    claimed_attempt INT NOT NULL CHECK (claimed_attempt = previous_attempts + 1),
    claimed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_machine_upgrade_review_audits_operation
    ON cloud_worker_machine_upgrade_review_audits (operation_id, claimed_at DESC);
CREATE INDEX IF NOT EXISTS idx_machine_upgrade_review_audits_actor
    ON cloud_worker_machine_upgrade_review_audits (actor_uid, claimed_at DESC);

-- 只追加：授权历史一旦写下就不允许被改写或删除。
CREATE OR REPLACE FUNCTION guard_machine_upgrade_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'machine upgrade review audit is append-only';
END;
$$;
DROP TRIGGER IF EXISTS guard_machine_upgrade_review_audit ON cloud_worker_machine_upgrade_review_audits;
CREATE TRIGGER guard_machine_upgrade_review_audit BEFORE UPDATE OR DELETE ON cloud_worker_machine_upgrade_review_audits
FOR EACH ROW EXECUTE FUNCTION guard_machine_upgrade_review_audit();

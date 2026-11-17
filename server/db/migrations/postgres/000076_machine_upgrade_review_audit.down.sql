-- 回滚 000076 只允许没有任何人工复核授权的库。生产回退应用版本时保留审计表结构：
-- 授权历史是「谁决定把这笔钱继续花下去」的唯一证据，删掉它等于删掉责任。
DO $$
BEGIN
    IF to_regclass('cloud_worker_machine_upgrade_review_audits') IS NOT NULL THEN
        LOCK TABLE cloud_worker_machine_upgrade_review_audits IN ACCESS EXCLUSIVE MODE;
        IF EXISTS (SELECT 1 FROM cloud_worker_machine_upgrade_review_audits) THEN
            RAISE EXCEPTION 'machine upgrade review history prevents rollback; keep the audit trail and roll back the application only';
        END IF;
        DROP TABLE cloud_worker_machine_upgrade_review_audits;
    END IF;
    DROP FUNCTION IF EXISTS guard_machine_upgrade_review_audit();
END;
$$;

-- 回滚 000070：去掉订阅日锚点、邀请赠送额度列，并把账本事件类型收回到 069 的集合。
-- 已经写入的 expire 事件不会被删除（历史账不能篡改），因此只有在不存在 expire
-- 事件时才能收紧约束；存在时保留更宽的事件类型，回滚只撤列。

DROP INDEX IF EXISTS idx_commercial_entitlements_period_anchor;

DO $$
BEGIN
    IF to_regclass('model_credit_ledger_events') IS NULL THEN
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM model_credit_ledger_events WHERE event_type = 'expire') THEN
        RETURN;
    END IF;
    ALTER TABLE model_credit_ledger_events DROP CONSTRAINT IF EXISTS ck_model_credit_ledger_events_event_type;
    ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_event_type
        CHECK (event_type IN ('grant','reserve','settle','release','debt_accrued','debt_repaid','refund'));
    ALTER TABLE model_credit_ledger_events DROP CONSTRAINT IF EXISTS ck_model_credit_ledger_events_request_scope;
    ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_request_scope
        CHECK (request_id IS NOT NULL OR event_type IN ('grant','debt_accrued','debt_repaid','refund'));
END $$;

ALTER TABLE commercial_invite_codes DROP CONSTRAINT IF EXISTS chk_commercial_invites_model_credit_units;
ALTER TABLE commercial_invite_codes DROP COLUMN IF EXISTS model_credit_units;
ALTER TABLE commercial_entitlements DROP COLUMN IF EXISTS period_anchor_at;

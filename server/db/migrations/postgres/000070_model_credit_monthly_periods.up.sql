-- 000070: 月额度自然月周期 + 邀请自己的赠送额度
--
-- 背景见 docs/credit-plan-rules-20261109.md：
--   * P0-5 套餐按自然月对日结算（1/31 → 2/28 → 3/31 不漂移），提前续费只延长，
--     下一周期开始时才发下一期月额度。
--   * P1-1 每个邀请码赠送 20 credit，面额属于邀请本身，不是套餐的月额度。
--   * P0-2 月额度当期有效、不结转：余额被清零这件事必须在账本里留痕，所以新增
--     expire 事件类型（只带正数、没有 request_id）。
--
-- 迁移是纯增量的：既有套餐、既有邀请码、既有用户都不会被改写。旧邀请码
-- model_credit_units = 0 仍走原来的 CNY/token 路径，不会把老用户迁进 credit 账本。

ALTER TABLE commercial_entitlements ADD COLUMN IF NOT EXISTS period_anchor_at TIMESTAMPTZ;
ALTER TABLE commercial_invite_codes ADD COLUMN IF NOT EXISTS model_credit_units BIGINT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF to_regclass('commercial_invite_codes') IS NULL THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'chk_commercial_invites_model_credit_units' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE commercial_invite_codes ADD CONSTRAINT chk_commercial_invites_model_credit_units CHECK (model_credit_units >= 0);
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('model_credit_ledger_events') IS NULL THEN
        RETURN;
    END IF;
    ALTER TABLE model_credit_ledger_events DROP CONSTRAINT IF EXISTS ck_model_credit_ledger_events_event_type;
    ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_event_type
        CHECK (event_type IN ('grant','reserve','settle','release','debt_accrued','debt_repaid','refund','expire'));
    ALTER TABLE model_credit_ledger_events DROP CONSTRAINT IF EXISTS ck_model_credit_ledger_events_request_scope;
    ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_request_scope
        CHECK (request_id IS NOT NULL OR event_type IN ('grant','debt_accrued','debt_repaid','refund','expire'));
END $$;

CREATE INDEX IF NOT EXISTS idx_commercial_entitlements_period_anchor
    ON commercial_entitlements (uid, period_anchor_at)
    WHERE period_anchor_at IS NOT NULL;

-- 000077: 邀请兑换也交付套餐期限内的模型点数（快照）
--
-- 兑换一张落在「真在卖」的套餐上（public、有限期、model_credit_units > 0）的邀请码，
-- 要交付这个套餐期限自带的模型点数。点数在兑换这一刻快照到权益行上，理由和订单路径
-- 快照 commercial_orders.plan_model_credit_units 完全一样：后台改套餐（甚至把套餐点数
-- 清零）不能改写兑换者已经拿到的东西。
--
-- 为什么不复用现有列：
--   * commercial_plans.model_credit_units 是「现在卖什么」，不是「当时给了他什么」；
--   * commercial_invite_codes.model_credit_units 是邀请的一次性 gift（运营可配，默认
--     20 credit，也可以是 0），和套餐期限内的点数不是一个东西：gift=0 的邀请仍要交付
--     期限点数。
-- 所以快照单独一列，既不和 gift 合并，也不受套餐后续编辑影响。
--
-- 迁移是纯增量的、无回填：
--   * 列可空，NULL 表示「没有快照」——所有既有权益行（order/admin/internal/invite/
--     free/trial）都保持 NULL，不回填、不改写；
--   * 列只在来源为 'invite' 且套餐合格的兑换路径上写入，写入值恒 > 0（CHECK 保证）。
--     历史邀请码和它们的兑换者因此完全按老路径结算，不会被迁移成月额度来源。
ALTER TABLE commercial_entitlements ADD COLUMN IF NOT EXISTS invite_monthly_units BIGINT;

DO $$
BEGIN
    IF to_regclass('commercial_entitlements') IS NULL THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'chk_commercial_entitlements_invite_monthly_units'
          AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE commercial_entitlements ADD CONSTRAINT chk_commercial_entitlements_invite_monthly_units
            CHECK (invite_monthly_units IS NULL OR invite_monthly_units > 0);
    END IF;
END $$;

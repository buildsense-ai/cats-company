package postgres

// migrateCommercialEntitlementInviteMonthlyUnits is migration 000077 applied to
// a database created from scratch: CreateSchema builds the final shape, so the
// statements here are the same idempotent ALTER the migration file carries.
//
// 邀请兑换也能带「套餐期限内的模型点数」：一张落在真在卖（public）、有限期、
// model_credit_units > 0 的套餐上的邀请码，兑换时把这个点数快照到权益行上，和运营
// 可配的一次性 gift 分开记账（gift 可以是 0，期限点数照交付）。
//
// 列可空、无回填：NULL 表示「没有快照」，所有既有权益行保持 NULL，历史邀请码和它们的
// 兑换者仍走原来的 CNY/token 路径；写入值恒 > 0，由 CHECK 兜住。快照不放在套餐行或
// 邀请码行上，因为那两处都会随运营编辑变化，而兑换者已经拿到的点数不能被改写。
const migrateCommercialEntitlementInviteMonthlyUnits = `
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
`

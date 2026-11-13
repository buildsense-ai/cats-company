package postgres

// These statements mirror migration 000069_commercial_model_credit_units byte
// for byte (modulo whitespace), and CreateSchema runs them right after the
// commercial plan/order columns they alter.
//
// SQL migration files are not executed by CreateSchema, so the production
// initializer has to carry the same additive changes: the two credit columns,
// and the ledger guards that let exactly one negative refund row be booked
// while every other event stays strictly positive.
const migrateCommercialModelCreditUnits = `
-- Independent credit product: a package may sell paid model credit instead of
-- (or next to) the legacy CNY/token budget. The amount is snapshotted onto the
-- order at purchase time so editing the plan cannot change a sold order. 0
-- keeps every existing package exactly as it behaved before.
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS model_credit_units BIGINT NOT NULL DEFAULT 0;
ALTER TABLE commercial_orders ADD COLUMN IF NOT EXISTS plan_model_credit_units BIGINT NOT NULL DEFAULT 0;

DO $$
DECLARE
    item RECORD;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'chk_commercial_plans_model_credit_units' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_model_credit_units CHECK (model_credit_units >= 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'chk_commercial_orders_plan_model_credit_units' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE commercial_orders ADD CONSTRAINT chk_commercial_orders_plan_model_credit_units CHECK (plan_model_credit_units >= 0);
    END IF;
END $$;

-- A refund reverses a purchase, so the ledger accepts exactly one negative row
-- keyed refund:order:<orderNo>. Every other event stays strictly positive and
-- request-scoped events still have to name their request, so the credit
-- accounting invariant is unchanged for reserve/settle/release.
DO $$
DECLARE
    item RECORD;
BEGIN
    IF to_regclass('model_credit_ledger_events') IS NULL THEN
        RETURN;
    END IF;
    FOR item IN
        SELECT con.conname
        FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE rel.relname = 'model_credit_ledger_events'
          AND nsp.nspname = current_schema()
          AND con.contype = 'c'
          AND con.conname NOT IN (
              'ck_model_credit_ledger_events_event_type',
              'ck_model_credit_ledger_events_units_sign',
              'ck_model_credit_ledger_events_request_scope',
              'ck_model_credit_ledger_events_bucket_type')
          AND (pg_get_constraintdef(con.oid) LIKE '%event_type%' OR pg_get_constraintdef(con.oid) LIKE '%units > 0%')
    LOOP
        EXECUTE format('ALTER TABLE model_credit_ledger_events DROP CONSTRAINT %I', item.conname);
    END LOOP;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'ck_model_credit_ledger_events_event_type' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_event_type
            CHECK (event_type IN ('grant','reserve','settle','release','debt_accrued','debt_repaid','refund'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'ck_model_credit_ledger_events_units_sign' AND nsp.nspname = current_schema()
    ) THEN
        -- grant stays strictly positive so money can only enter the ledger
        -- through a real purchase or gift; only a refund may carry a negative
        -- amount, and it may only carry one.
        ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_units_sign
            CHECK (CASE WHEN event_type = 'refund' THEN units < 0 ELSE units > 0 END);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'ck_model_credit_ledger_events_request_scope' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_request_scope
            CHECK (request_id IS NOT NULL OR event_type IN ('grant','debt_accrued','debt_repaid','refund'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'ck_model_credit_ledger_events_bucket_type' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE model_credit_ledger_events ADD CONSTRAINT ck_model_credit_ledger_events_bucket_type
            CHECK ((bucket = 'debt') = (event_type IN ('debt_accrued','debt_repaid')));
    END IF;
END $$;
`

-- Roll back the independent credit product. The two columns are dropped, and
-- the ledger guards go back to their pre-refund shape. Rows already written by
-- the feature are kept, so the restored rules are added NOT VALID: deleting a
-- sold order's ledger rows on rollback would lose money, and Postgres would
-- otherwise refuse to add "units > 0" next to a booked refund row. The rules
-- therefore apply to everything written after the rollback.
ALTER TABLE commercial_orders DROP COLUMN IF EXISTS plan_model_credit_units;
ALTER TABLE commercial_plans DROP COLUMN IF EXISTS model_credit_units;

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
          AND con.conname IN (
              'ck_model_credit_ledger_events_event_type',
              'ck_model_credit_ledger_events_units_sign',
              'ck_model_credit_ledger_events_request_scope')
    LOOP
        EXECUTE format('ALTER TABLE model_credit_ledger_events DROP CONSTRAINT %I', item.conname);
    END LOOP;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'model_credit_ledger_events_event_type_check' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE model_credit_ledger_events ADD CONSTRAINT model_credit_ledger_events_event_type_check
            CHECK (event_type IN ('grant','reserve','settle','release','debt_accrued','debt_repaid')) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'model_credit_ledger_events_units_check' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE model_credit_ledger_events ADD CONSTRAINT model_credit_ledger_events_units_check
            CHECK (units > 0) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint con
        JOIN pg_class rel ON rel.oid = con.conrelid
        JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
        WHERE con.conname = 'model_credit_ledger_events_request_scope_check' AND nsp.nspname = current_schema()
    ) THEN
        ALTER TABLE model_credit_ledger_events ADD CONSTRAINT model_credit_ledger_events_request_scope_check
            CHECK (request_id IS NOT NULL OR event_type IN ('grant','debt_accrued','debt_repaid')) NOT VALID;
    END IF;
END $$;

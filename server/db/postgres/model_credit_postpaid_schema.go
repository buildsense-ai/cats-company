package postgres

// Keep historical funded holds and admit explicitly postpaid requests at zero.
// The old constraint implies the new one, so no table scan is needed on upgrade.
const migrateModelCreditPostpaid = `
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'model_credit_reservations'::regclass
          AND conname = 'chk_model_credit_reservations_postpaid'
    ) THEN
        ALTER TABLE model_credit_reservations
            ADD CONSTRAINT chk_model_credit_reservations_postpaid
            CHECK (reserved_units > 0 OR (reserved_units = 0 AND root_task_id IS NOT NULL)) NOT VALID,
            DROP CONSTRAINT IF EXISTS model_credit_reservations_reserved_units_check;
    END IF;
END $$;
`

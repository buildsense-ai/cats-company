DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM model_credit_accounts WHERE frozen_monthly_units > 0)
        OR EXISTS (SELECT 1 FROM model_credit_monthly_lots)
        OR EXISTS (SELECT 1 FROM model_credit_monthly_hold_lots) THEN
        RAISE EXCEPTION 'cannot roll back monthly rollover with recorded balances or allocations';
    END IF;
END $$;
DROP TABLE model_credit_monthly_hold_lots;
DROP TABLE model_credit_monthly_lots;
ALTER TABLE model_credit_accounts DROP COLUMN frozen_monthly_units;

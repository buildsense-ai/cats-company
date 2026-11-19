-- Fails if any postpaid receipts exist. Preserve those rows and roll back the
-- Relay to funded holds instead of dropping receipts or losing late settlement.
ALTER TABLE model_credit_reservations
    ADD CONSTRAINT model_credit_reservations_reserved_units_check CHECK (reserved_units > 0),
    DROP CONSTRAINT chk_model_credit_reservations_postpaid;

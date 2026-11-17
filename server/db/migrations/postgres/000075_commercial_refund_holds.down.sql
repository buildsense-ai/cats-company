-- Rolling 000075 back is only allowed while no refund ever froze credit. A hold
-- row is the local evidence that money was paid out while the bought credit was
-- pulled out of the balance; dropping it erases the only record of that refund
-- and hides the orders an operator still has to reconcile with the channel.
-- Production therefore keeps the table and rolls the application back instead.
DO $$
BEGIN
    IF to_regclass('commercial_refund_holds') IS NULL THEN
        RETURN;
    END IF;
    LOCK TABLE commercial_refund_holds IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM commercial_refund_holds) THEN
        RAISE EXCEPTION 'commercial refund hold history prevents rollback; keep the holds and roll back the application only';
    END IF;
    DROP TABLE commercial_refund_holds;
END;
$$;

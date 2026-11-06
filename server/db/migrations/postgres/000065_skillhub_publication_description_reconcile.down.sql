DO $$
BEGIN
    IF to_regclass('skillhub_publication_description_reconciles') IS NOT NULL THEN
        IF EXISTS(SELECT 1 FROM skillhub_publication_description_reconciles) THEN
            RAISE EXCEPTION 'cannot remove retained description reconciliation';
        END IF;
        DROP TABLE skillhub_publication_description_reconciles;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_description_reconcile();
END;
$$;

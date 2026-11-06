DO $$
BEGIN
    IF to_regclass('skillhub_publication_repair_model_calls') IS NOT NULL THEN
        IF EXISTS (SELECT 1 FROM skillhub_publication_repair_model_calls) THEN
            RAISE EXCEPTION 'Refusing to erase retained model dispatch evidence';
        END IF;
        DROP TABLE skillhub_publication_repair_model_calls;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_repair_model_call();
END;
$$;

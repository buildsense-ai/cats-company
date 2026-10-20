DO $$
BEGIN
    IF to_regclass('skillhub_publication_capture_deliveries') IS NOT NULL THEN
        IF EXISTS (SELECT 1 FROM skillhub_publication_capture_deliveries) THEN
            RAISE EXCEPTION 'Refusing to erase retained publication capture attempts';
        END IF;
        DROP TABLE skillhub_publication_capture_deliveries;
    END IF;
END;
$$;

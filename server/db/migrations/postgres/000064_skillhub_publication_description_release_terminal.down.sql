DO $$
BEGIN
    IF to_regclass('skillhub_publication_description_release_terminals') IS NOT NULL THEN
        IF EXISTS(SELECT 1 FROM skillhub_publication_description_release_terminals) THEN
            RAISE EXCEPTION 'cannot remove retained terminal publication evidence';
        END IF;
        DROP TABLE skillhub_publication_description_release_terminals;
    END IF;
    DROP FUNCTION IF EXISTS guard_skillhub_publication_description_release_terminal();
END;
$$;

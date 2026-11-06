CREATE TABLE IF NOT EXISTS skillhub_publication_description_reconciles (
    application_id VARCHAR(72) PRIMARY KEY REFERENCES skillhub_publication_description_release_permits(application_id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('pending','leased','recorded')),
    generation BIGINT NOT NULL CHECK (generation>0),
    failures INTEGER NOT NULL DEFAULT 0 CHECK (failures BETWEEN 0 AND 6),
    lease_token TEXT,
    lease_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    CHECK ((state='leased' AND lease_token IS NOT NULL AND lease_token ~ '^[a-f0-9]{32}$' AND lease_until IS NOT NULL) OR
           (state<>'leased' AND lease_token IS NULL AND lease_until IS NULL))
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_description_reconcile() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'retained description reconciliation cannot be deleted'; END IF;
    IF TG_OP='UPDATE' AND (NEW.application_id IS DISTINCT FROM OLD.application_id OR NEW.generation<OLD.generation
        OR (OLD.state='recorded' AND NEW IS DISTINCT FROM OLD)) THEN
        RAISE EXCEPTION 'description reconciliation cannot be reset';
    END IF;
    IF NEW.state='recorded' AND NOT EXISTS(SELECT 1 FROM skillhub_publication_description_release_terminals t WHERE t.application_id=NEW.application_id) THEN
        RAISE EXCEPTION 'recorded reconciliation requires terminal evidence';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_reconcile ON skillhub_publication_description_reconciles;
CREATE TRIGGER guard_skillhub_publication_description_reconcile BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_reconciles
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description_reconcile();

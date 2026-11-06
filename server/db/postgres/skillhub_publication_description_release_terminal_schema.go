package postgres

// Mirrors migration 000064. Historical evidence never changes repair state.
const createSkillHubPublicationDescriptionReleaseTerminalTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_description_release_terminals (
    application_id VARCHAR(72) PRIMARY KEY REFERENCES skillhub_publication_description_release_permits(application_id) ON DELETE RESTRICT,
    entry_id VARCHAR(41) NOT NULL UNIQUE REFERENCES skillhub_publication_entries(entry_id) ON DELETE RESTRICT,
    permit_id VARCHAR(46) NOT NULL UNIQUE,
    intent_identity_hash CHAR(64) NOT NULL CHECK (intent_identity_hash ~ '^[a-f0-9]{64}$'),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload)='object'),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_description_release_terminal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    i RECORD;
    p RECORD;
    v JSONB;
    r JSONB;
BEGIN
    IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'retained publication description terminal is immutable'; END IF;
    SELECT * INTO i FROM skillhub_publication_description_release_intents WHERE application_id=NEW.application_id;
    SELECT * INTO p FROM skillhub_publication_description_release_permits WHERE application_id=NEW.application_id;
    IF i.application_id IS NULL OR p.application_id IS NULL OR i.entry_id IS DISTINCT FROM NEW.entry_id
        OR p.entry_id IS DISTINCT FROM NEW.entry_id OR p.permit_id IS DISTINCT FROM NEW.permit_id
        OR i.identity_hash IS DISTINCT FROM NEW.intent_identity_hash OR p.intent_identity_hash IS DISTINCT FROM i.identity_hash
        OR NEW.recorded_at < p.authorized_at THEN RAISE EXCEPTION 'terminal does not match retained permit'; END IF;
    PERFORM 1 FROM skillhub_publication_entries e JOIN users u ON u.id=e.actor_uid
        WHERE e.entry_id=NEW.entry_id AND e.actor_uid=i.actor_uid AND u.account_type='human' AND u.state=0 FOR SHARE OF u;
    IF NOT FOUND THEN RAISE EXCEPTION 'terminal original publisher is inactive'; END IF;
    v:=NEW.payload;
    IF (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(v) k) IS DISTINCT FROM
        ARRAY['applicationId','entryId','errorCode','intentHash','permitId','result','state','target']::TEXT[]
        OR v->>'applicationId' IS DISTINCT FROM NEW.application_id OR v->>'entryId' IS DISTINCT FROM NEW.entry_id
        OR v->>'permitId' IS DISTINCT FROM NEW.permit_id OR v->>'intentHash' IS DISTINCT FROM NEW.intent_identity_hash
        OR v->'target' IS DISTINCT FROM i.target THEN RAISE EXCEPTION 'terminal payload binding mismatch'; END IF;
    r:=v->'result';
    IF v->>'state'='published' THEN
        IF jsonb_typeof(r) IS DISTINCT FROM 'object' OR v->>'errorCode' IS DISTINCT FROM ''
            THEN RAISE EXCEPTION 'invalid published terminal'; END IF;
        IF (SELECT array_agg(k ORDER BY k) FROM jsonb_object_keys(r) k) IS DISTINCT FROM
            ARRAY['checksumSha256','packageId','publishedAt','skillId','version']::TEXT[]
            OR r->>'skillId' IS DISTINCT FROM i.target->>'skillId' OR r->>'version' IS DISTINCT FROM i.target->>'version'
            OR r->>'publishedAt' IS DISTINCT FROM i.target->>'releaseAt'
            OR COALESCE(r->>'packageId','') !~ '^pkg_[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$'
            OR COALESCE(r->>'checksumSha256','') !~ '^[a-f0-9]{64}$'
            THEN RAISE EXCEPTION 'invalid published result'; END IF;
    ELSIF v->>'state'='publish_failed' THEN
        IF r IS DISTINCT FROM 'null'::JSONB OR COALESCE(v->>'errorCode','') NOT IN (
            'skillhub.publication_description_release_commit_unavailable','skillhub.publication_description_release_commit_copy_changed',
            'skillhub.publication_description_release_commit_rejected','skillhub.publication_description_release_commit_publisher_changed',
            'skillhub.publication_description_release_commit_conflict','skillhub.publication_description_release_commit_artifact_changed',
            'skillhub.publication_description_release_commit_invalid','skillhub.publication_description_release_commit_lease_lost')
            THEN RAISE EXCEPTION 'invalid failed terminal'; END IF;
    ELSE RAISE EXCEPTION 'terminal evidence required'; END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_release_terminal ON skillhub_publication_description_release_terminals;
CREATE TRIGGER guard_skillhub_publication_description_release_terminal BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_release_terminals
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description_release_terminal();
`

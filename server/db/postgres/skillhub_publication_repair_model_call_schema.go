package postgres

// Mirrors migration 000066. A dispatch fence, not a worker or paid-call retry queue.
const createSkillHubPublicationRepairModelCallsTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_repair_model_calls (
    entry_id VARCHAR(41) NOT NULL,
    round_number INTEGER NOT NULL CHECK (round_number BETWEEN 1 AND 3),
    call_id VARCHAR(41) NOT NULL UNIQUE CHECK (call_id ~ '^pubmodel_[a-f0-9]{32}$'),
    request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
    dispatch_lease JSONB NOT NULL CHECK (jsonb_typeof(dispatch_lease)='object' AND octet_length(dispatch_lease::text)<=4096),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    output_text TEXT,
    output_hash CHAR(64),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY(entry_id,round_number),
    FOREIGN KEY(entry_id,round_number) REFERENCES skillhub_publication_repair_rounds(entry_id,round_number) ON DELETE RESTRICT,
    CHECK ((output_text IS NULL AND output_hash IS NULL AND completed_at IS NULL) OR
        (output_text IS NOT NULL AND octet_length(output_text) BETWEEN 1 AND 65536 AND
         output_hash IS NOT NULL AND output_hash ~ '^[a-f0-9]{64}$' AND
         output_hash=encode(sha256(convert_to(output_text,'UTF8')),'hex') AND
         completed_at IS NOT NULL AND completed_at>=created_at))
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_repair_model_call() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    e RECORD;
    r RECORD;
    found_uid BIGINT;
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'retained model call cannot be erased'; END IF;
    IF TG_OP='UPDATE' AND (OLD.output_text IS NOT NULL OR NEW.output_text IS NULL OR
        (to_jsonb(NEW)-'output_text'-'output_hash'-'completed_at') IS DISTINCT FROM
        (to_jsonb(OLD)-'output_text'-'output_hash'-'completed_at')) THEN
        RAISE EXCEPTION 'retained model dispatch and output cannot be reset';
    END IF;
    IF TG_OP='INSERT' AND NEW.output_text IS NOT NULL THEN
        RAISE EXCEPTION 'model result requires a prior dispatch fence';
    END IF;
    SELECT * INTO e FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND OR e.state IS DISTINCT FROM 'waiting_runtime' OR e.entrypoint IS DISTINCT FROM 'workspace' THEN
        RAISE EXCEPTION 'model call entry is settled';
    END IF;
    SELECT id INTO found_uid FROM users WHERE id=e.actor_uid AND account_type='human' AND state=0 FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'model call actor inactive'; END IF;
    SELECT b.user_id INTO found_uid FROM bot_config b JOIN users u ON u.id=b.user_id
        WHERE b.user_id=e.bot_uid AND b.owner_id=e.actor_uid AND b.body_id=e.body_id
        AND b.enabled=true AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u;
    IF NOT FOUND THEN RAISE EXCEPTION 'model call authority changed'; END IF;
    IF EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=NEW.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'model call cancelled or committing';
    END IF;
    SELECT * INTO r FROM skillhub_publication_repair_rounds
        WHERE entry_id=NEW.entry_id AND round_number=NEW.round_number FOR UPDATE;
    IF NOT FOUND OR r.result IS NOT NULL OR r.lease_until<=clock_timestamp() OR
        NEW.dispatch_lease->>'EntryID' IS DISTINCT FROM NEW.entry_id OR
        (NEW.dispatch_lease->>'Round')::integer IS DISTINCT FROM NEW.round_number OR
        NEW.dispatch_lease->>'WorkerID' IS DISTINCT FROM r.lease_worker OR
        NEW.dispatch_lease->>'Token' IS DISTINCT FROM r.lease_token OR
        (NEW.dispatch_lease->>'Generation')::bigint IS DISTINCT FROM r.lease_generation OR
        (NEW.dispatch_lease->>'Until')::timestamptz IS DISTINCT FROM r.lease_until OR
        NEW.dispatch_lease->'Input' IS DISTINCT FROM jsonb_build_object('requestId',r.request_id,'evidence',r.input->'evidence') OR
        NEW.dispatch_lease->>'TaskID' IS DISTINCT FROM
            (SELECT task_id FROM skillhub_publication_repair_tasks WHERE entry_id=NEW.entry_id) OR
        NEW.created_at>=r.lease_until OR (NEW.completed_at IS NOT NULL AND NEW.completed_at>=r.lease_until) THEN
        RAISE EXCEPTION 'model call lease is stale';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_repair_model_call ON skillhub_publication_repair_model_calls;
CREATE TRIGGER guard_skillhub_publication_repair_model_call BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_repair_model_calls
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_repair_model_call();
`

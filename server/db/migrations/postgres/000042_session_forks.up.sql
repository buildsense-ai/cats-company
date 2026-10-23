ALTER TABLE topics ADD COLUMN IF NOT EXISTS history_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE topics ADD COLUMN IF NOT EXISTS fork_state VARCHAR(16) NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS session_forks (
    actor_user_id BIGINT NOT NULL,
    request_id VARCHAR(128) NOT NULL,
    source_topic_id VARCHAR(64) NOT NULL,
    target_topic_id VARCHAR(64) NOT NULL UNIQUE,
    agent_uid BIGINT NOT NULL,
    body_id VARCHAR(128) NOT NULL,
    request_name VARCHAR(80) NOT NULL DEFAULT '',
    source_revision BIGINT NOT NULL,
    target_revision BIGINT NOT NULL,
    source_last_seq BIGINT NOT NULL,
    message_id_map JSONB NOT NULL,
    state VARCHAR(16) NOT NULL CHECK (state IN ('pending','ready','failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (actor_user_id, request_id)
);
CREATE OR REPLACE FUNCTION session_fork_message_guard() RETURNS TRIGGER AS $$
DECLARE
    topic VARCHAR(64);
    current_state VARCHAR(16);
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.topic_id <> OLD.topic_id THEN
        RAISE EXCEPTION 'message_topic_is_immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN topic := OLD.topic_id; ELSE topic := NEW.topic_id; END IF;
    UPDATE topics SET history_revision = history_revision + 1
        WHERE id = topic RETURNING fork_state INTO current_state;
    IF TG_OP <> 'DELETE' AND current_state IN ('pending','failed') THEN
        RAISE EXCEPTION 'session_fork_not_ready';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS session_fork_message_guard ON messages;
CREATE TRIGGER session_fork_message_guard BEFORE INSERT OR UPDATE OR DELETE ON messages
FOR EACH ROW EXECUTE FUNCTION session_fork_message_guard();
ALTER TABLE session_forks ADD COLUMN IF NOT EXISTS source_turn_count BIGINT NOT NULL DEFAULT 0;

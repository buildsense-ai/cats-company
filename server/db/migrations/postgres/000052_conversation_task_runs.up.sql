ALTER TABLE conversation_task_status_sources
ADD COLUMN IF NOT EXISTS event_updated_at TIMESTAMPTZ;
UPDATE conversation_task_status_sources
SET event_updated_at = updated_at
WHERE event_updated_at IS NULL;
ALTER TABLE conversation_task_status_sources
ALTER COLUMN event_updated_at SET DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE conversation_task_status_sources
ALTER COLUMN event_updated_at SET NOT NULL;

CREATE TABLE IF NOT EXISTS conversation_task_runs (
    id BIGSERIAL PRIMARY KEY,
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    source_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    run_id VARCHAR(128) NOT NULL DEFAULT '',
    state VARCHAR(20) NOT NULL CHECK (state IN ('idle','running','completed','failed','cancelled','stale','waiting')),
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    event_updated_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (topic_id, source_uid, run_id)
);
CREATE INDEX IF NOT EXISTS idx_conversation_task_runs_topic ON conversation_task_runs (topic_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_task_runs_aggregate ON conversation_task_runs
    (topic_id, (CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END), event_updated_at DESC, source_uid DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_task_runs_source ON conversation_task_runs
    (topic_id, source_uid, (CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END), event_updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_task_runs_active ON conversation_task_runs (updated_at) WHERE state IN ('running','waiting');
CREATE TABLE IF NOT EXISTS conversation_task_status_outbox (
    id BIGSERIAL PRIMARY KEY,
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    source_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status_json TEXT NOT NULL,
    claim_token VARCHAR(64) NOT NULL DEFAULT '',
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_conversation_task_status_outbox_due ON conversation_task_status_outbox (available_at, id);

INSERT INTO conversation_task_runs
    (topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
SELECT topic_id, source_uid, COALESCE(run_id, ''), state, summary, error, expires_at, event_updated_at, updated_at
FROM conversation_task_status_sources
ON CONFLICT (topic_id, source_uid, run_id) DO NOTHING;

ALTER TABLE conversation_task_statuses
ADD COLUMN IF NOT EXISTS ledger_updated_at TIMESTAMPTZ;
CREATE OR REPLACE FUNCTION set_conversation_task_aggregate_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond', NEW.updated_at);
    IF NEW.ledger_updated_at IS DISTINCT FROM OLD.ledger_updated_at THEN
        NEW.ledger_updated_at = NEW.updated_at;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER trg_conversation_task_statuses_updated_at
BEFORE UPDATE ON conversation_task_statuses
FOR EACH ROW EXECUTE FUNCTION set_conversation_task_aggregate_updated_at();

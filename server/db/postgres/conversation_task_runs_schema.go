package postgres

// Kept additive: old binaries continue using the source and topic projections.
const createConversationTaskRunsTable = `
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
`

const backfillConversationTaskRuns = `
INSERT INTO conversation_task_runs
    (topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
SELECT topic_id, source_uid, COALESCE(run_id, ''), state, summary, error, expires_at, event_updated_at, updated_at
FROM conversation_task_status_sources
ON CONFLICT (topic_id, source_uid, run_id) DO NOTHING;
`

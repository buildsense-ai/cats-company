CREATE TABLE IF NOT EXISTS conversation_task_status_sources (
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    source_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    run_id VARCHAR(128) DEFAULT '',
    state VARCHAR(20) NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','running','completed','failed','cancelled','stale','waiting')),
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (topic_id, source_uid)
);

CREATE INDEX IF NOT EXISTS idx_conversation_task_status_sources_updated_at
    ON conversation_task_status_sources (updated_at);
CREATE INDEX IF NOT EXISTS idx_conversation_task_status_sources_state
    ON conversation_task_status_sources (state);

CREATE OR REPLACE TRIGGER trg_conversation_task_status_sources_updated_at
BEFORE UPDATE ON conversation_task_status_sources
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

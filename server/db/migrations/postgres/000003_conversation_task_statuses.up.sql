CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = CURRENT_TIMESTAMP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS conversation_task_statuses (
    topic_id VARCHAR(64) PRIMARY KEY REFERENCES topics(id) ON DELETE CASCADE,
    run_id VARCHAR(128) DEFAULT '',
    state VARCHAR(20) NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','running','completed','failed','cancelled','stale','waiting')),
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    source_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_conversation_task_statuses_updated_at ON conversation_task_statuses (updated_at);
CREATE INDEX IF NOT EXISTS idx_conversation_task_statuses_state ON conversation_task_statuses (state);

CREATE OR REPLACE TRIGGER trg_conversation_task_statuses_updated_at BEFORE UPDATE ON conversation_task_statuses
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

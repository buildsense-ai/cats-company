package mysql

const createConversationTaskRunsTable = `
CREATE TABLE IF NOT EXISTS conversation_task_runs (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    topic_id VARCHAR(64) NOT NULL,
    source_uid BIGINT NOT NULL,
    run_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
    state VARCHAR(20) NOT NULL,
    summary TEXT NOT NULL,
    error TEXT NOT NULL,
    expires_at DATETIME(6) DEFAULT NULL,
    event_updated_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL,
    UNIQUE KEY uk_conversation_task_run (topic_id, source_uid, run_id),
    INDEX idx_conversation_task_runs_topic (topic_id, id),
    INDEX idx_conversation_task_runs_aggregate (topic_id, (CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END), event_updated_at DESC, source_uid DESC, id DESC),
    INDEX idx_conversation_task_runs_source (topic_id, source_uid, (CASE WHEN state IN ('running','waiting') THEN 0 ELSE 1 END), event_updated_at DESC, id DESC),
    INDEX idx_conversation_task_runs_active (state, updated_at),
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    FOREIGN KEY (source_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createConversationTaskStatusOutboxTable = `
CREATE TABLE IF NOT EXISTS conversation_task_status_outbox (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    topic_id VARCHAR(64) NOT NULL,
    source_uid BIGINT NOT NULL,
    status_json TEXT NOT NULL,
    claim_token VARCHAR(64) NOT NULL DEFAULT '',
    available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    INDEX idx_conversation_task_status_outbox_due (available_at, id),
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    FOREIGN KEY (source_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const backfillConversationTaskRuns = `
INSERT IGNORE INTO conversation_task_runs
    (topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
SELECT topic_id, source_uid, COALESCE(run_id, ''), state, summary, error, expires_at, event_updated_at, updated_at
FROM conversation_task_status_sources;
`

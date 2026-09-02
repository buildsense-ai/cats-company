CREATE TABLE IF NOT EXISTS agent_artifact_tags (
    agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    artifact_id VARCHAR(200) NOT NULL,
    tag VARCHAR(128) NOT NULL,
    created_by BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent_uid, artifact_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_agent_artifact_tags_agent_tag
    ON agent_artifact_tags (agent_uid, tag);

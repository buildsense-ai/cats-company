CREATE OR REPLACE FUNCTION sync_legacy_conversation_task_status_source()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.source_uid IS NULL THEN
    RETURN NEW;
  END IF;

  INSERT INTO conversation_task_status_sources
    (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
  VALUES
    (NEW.topic_id, NEW.source_uid, NEW.run_id, NEW.state, NEW.summary, NEW.error, NEW.expires_at, NEW.updated_at)
  ON CONFLICT (topic_id, source_uid) DO UPDATE SET
    run_id = EXCLUDED.run_id,
    state = EXCLUDED.state,
    summary = EXCLUDED.summary,
    error = EXCLUDED.error,
    expires_at = EXCLUDED.expires_at,
    updated_at = EXCLUDED.updated_at
  WHERE NOT (
    conversation_task_status_sources.state IN ('running', 'waiting')
    AND (conversation_task_status_sources.expires_at IS NULL OR conversation_task_status_sources.expires_at > CURRENT_TIMESTAMP)
    AND conversation_task_status_sources.run_id <> EXCLUDED.run_id
    AND EXCLUDED.state NOT IN ('running', 'waiting')
  );
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_conversation_task_statuses_sync_source
ON conversation_task_statuses;

CREATE TRIGGER trg_conversation_task_statuses_sync_source
AFTER INSERT OR UPDATE ON conversation_task_statuses
FOR EACH ROW EXECUTE FUNCTION sync_legacy_conversation_task_status_source();

INSERT INTO conversation_task_status_sources
  (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
SELECT topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at
FROM conversation_task_statuses
WHERE source_uid IS NOT NULL
ON CONFLICT (topic_id, source_uid) DO UPDATE SET
  run_id = EXCLUDED.run_id,
  state = EXCLUDED.state,
  summary = EXCLUDED.summary,
  error = EXCLUDED.error,
  expires_at = EXCLUDED.expires_at,
  updated_at = EXCLUDED.updated_at
WHERE NOT (
  conversation_task_status_sources.state IN ('running', 'waiting')
  AND (conversation_task_status_sources.expires_at IS NULL OR conversation_task_status_sources.expires_at > CURRENT_TIMESTAMP)
  AND conversation_task_status_sources.run_id <> EXCLUDED.run_id
  AND EXCLUDED.state NOT IN ('running', 'waiting')
);

CREATE OR REPLACE TRIGGER trg_conversation_task_statuses_updated_at
BEFORE UPDATE ON conversation_task_statuses
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
DROP FUNCTION IF EXISTS set_conversation_task_aggregate_updated_at();
ALTER TABLE conversation_task_statuses DROP COLUMN IF EXISTS ledger_updated_at;
DROP TABLE IF EXISTS conversation_task_status_outbox;
DROP TABLE IF EXISTS conversation_task_runs;

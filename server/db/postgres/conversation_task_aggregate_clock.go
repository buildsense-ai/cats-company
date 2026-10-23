package postgres

// A transaction can begin before another transaction but obtain the topic lock
// afterwards. Transaction-start timestamps would then make a later aggregate
// look older to existing clients, which discard out-of-order updates.
const createConversationTaskAggregateClock = `
CREATE OR REPLACE FUNCTION set_conversation_task_aggregate_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond', NEW.updated_at);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER trg_conversation_task_statuses_updated_at
BEFORE UPDATE ON conversation_task_statuses
FOR EACH ROW EXECUTE FUNCTION set_conversation_task_aggregate_updated_at();
`

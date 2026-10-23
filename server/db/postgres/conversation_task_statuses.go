package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/db/taskstatus"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) taskLedger() taskstatus.Ledger {
	return taskstatus.Ledger{DB: a.db, Postgres: true, Reconcile: func(tx *sql.Tx, topic string) error {
		return reconcileLegacyConversationTaskStatuses(tx, "$1", topic)
	}}
}

// UpsertConversationTaskStatus preserves the run, refreshes the legacy
// projections and enqueues its notification in the same topic-locked transaction.
func (a *Adapter) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	return a.taskLedger().Upsert(status)
}

func (a *Adapter) GetConversationTaskStatusForSource(topic string, source int64) (*types.ConversationTaskStatus, error) {
	if err := reconcileLegacyConversationTaskStatuses(a.db, "$1", topic); err != nil {
		return nil, err
	}
	return a.taskLedger().ForSource(topic, source)
}

func (a *Adapter) GetConversationTaskStatuses(topics []string) (map[string]*types.ConversationTaskStatus, error) {
	if len(topics) == 0 {
		return map[string]*types.ConversationTaskStatus{}, nil
	}
	args := make([]interface{}, len(topics))
	for i, topic := range topics {
		args[i] = topic
	}
	if err := reconcileLegacyConversationTaskStatuses(a.db, inPlaceholders(1, len(topics)), args...); err != nil {
		return nil, err
	}
	return a.taskLedger().Summaries(topics)
}

func (a *Adapter) ListConversationTaskRuns(topic string, before int64, limit int) ([]store.ConversationTaskRun, error) {
	return a.taskLedger().ListRuns(topic, before, limit)
}
func (a *Adapter) ClaimConversationTaskStatusEvents(limit int, lease time.Duration) ([]store.ConversationTaskStatusEvent, error) {
	return a.taskLedger().ClaimEvents(limit, lease)
}
func (a *Adapter) CompleteConversationTaskStatusEvent(id int64, claim string) error {
	return a.taskLedger().CompleteEvent(id, claim)
}
func (a *Adapter) ListActiveConversationTaskStatusSources(topic string) ([]int64, error) {
	return a.taskLedger().ActiveSources(topic)
}
func (a *Adapter) ListAllActiveConversationTaskStatusesBefore(cutoff time.Time) ([]*types.ConversationTaskStatus, error) {
	return a.taskLedger().ActiveBefore(0, cutoff)
}
func (a *Adapter) ListActiveConversationTaskStatusesForSource(source int64, cutoff time.Time) ([]*types.ConversationTaskStatus, error) {
	return a.taskLedger().ActiveBefore(source, cutoff)
}
func (a *Adapter) MarkConversationTaskStatusStaleIfUnchanged(topic string, source int64, run string, cutoff time.Time, generation uint64) (*types.ConversationTaskStatus, bool, error) {
	return a.taskLedger().MarkStale(topic, source, run, cutoff, generation)
}

type conversationTaskStatusExecer interface {
	Exec(string, ...interface{}) (sql.Result, error)
}

// Ledger writes stamp the resulting aggregate revision atomically in the same
// row. Only an unmarked revision can originate from an older writer; comparing
// aggregate/source contents is insufficient when another source caused fallback.
func reconcileLegacyConversationTaskStatuses(execer conversationTaskStatusExecer, placeholders string, args ...interface{}) error {
	_, err := execer.Exec(fmt.Sprintf(`INSERT INTO conversation_task_status_sources
		(topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
		SELECT topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at, CURRENT_TIMESTAMP
		FROM conversation_task_statuses WHERE topic_id IN (%s) AND source_uid IS NOT NULL
		AND (ledger_updated_at IS NULL OR ledger_updated_at <> updated_at) FOR UPDATE
		ON CONFLICT (topic_id, source_uid) DO UPDATE SET
		run_id = EXCLUDED.run_id, state = EXCLUDED.state, summary = EXCLUDED.summary, error = EXCLUDED.error,
		expires_at = EXCLUDED.expires_at, event_updated_at = EXCLUDED.event_updated_at, updated_at = CURRENT_TIMESTAMP
		WHERE NOT (
		 (conversation_task_status_sources.state IN ('running','waiting')
		 AND (conversation_task_status_sources.expires_at IS NULL OR conversation_task_status_sources.expires_at > clock_timestamp())
		 AND conversation_task_status_sources.run_id <> EXCLUDED.run_id AND EXCLUDED.state NOT IN ('running','waiting'))
		 OR (conversation_task_status_sources.run_id = EXCLUDED.run_id
		 AND conversation_task_status_sources.state IN ('completed','failed','cancelled','stale')
		 AND EXCLUDED.state NOT IN ('completed','failed','cancelled','stale')))
		AND (conversation_task_status_sources.run_id, conversation_task_status_sources.state,
		 conversation_task_status_sources.summary, conversation_task_status_sources.error, conversation_task_status_sources.expires_at)
		IS DISTINCT FROM (EXCLUDED.run_id, EXCLUDED.state, EXCLUDED.summary, EXCLUDED.error, EXCLUDED.expires_at)`, placeholders), args...)
	return err
}

func (a *Adapter) BumpBotConnectionGeneration(botUID int64) (uint64, error) {
	if botUID <= 0 {
		return 0, fmt.Errorf("bot uid is required")
	}
	var generation uint64
	err := a.db.QueryRow(`INSERT INTO bot_connection_generations (bot_uid, generation) VALUES ($1, 1)
		ON CONFLICT (bot_uid) DO UPDATE SET generation = bot_connection_generations.generation + 1 RETURNING generation`, botUID).Scan(&generation)
	return generation, err
}
func (a *Adapter) BotConnectionGeneration(botUID int64) (uint64, error) {
	if botUID <= 0 {
		return 0, fmt.Errorf("bot uid is required")
	}
	var generation uint64
	err := a.db.QueryRow(`SELECT generation FROM bot_connection_generations WHERE bot_uid = $1`, botUID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return generation, err
}

var _ store.ConversationTaskRunStore = (*Adapter)(nil)
var _ store.ConversationTaskStatusOutboxStore = (*Adapter)(nil)

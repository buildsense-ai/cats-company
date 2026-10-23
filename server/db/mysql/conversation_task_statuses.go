package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/db/taskstatus"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) taskLedger() taskstatus.Ledger {
	return taskstatus.Ledger{DB: a.db, Reconcile: func(tx *sql.Tx, topic string) error {
		return reconcileLegacyConversationTaskStatuses(tx, "?", topic)
	}}
}

func (a *Adapter) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	return a.taskLedger().Upsert(status)
}
func (a *Adapter) GetConversationTaskStatusForSource(topic string, source int64) (*types.ConversationTaskStatus, error) {
	if err := reconcileLegacyConversationTaskStatuses(a.db, "?", topic); err != nil {
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
	if err := reconcileLegacyConversationTaskStatuses(a.db, strings.TrimRight(strings.Repeat("?,", len(topics)), ","), args...); err != nil {
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

func reconcileLegacyConversationTaskStatuses(execer conversationTaskStatusExecer, placeholders string, args ...interface{}) error {
	_, err := execer.Exec(fmt.Sprintf(`INSERT INTO conversation_task_status_sources
		(topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
		SELECT aggregate.topic_id, aggregate.source_uid, aggregate.run_id, aggregate.state,
		 aggregate.summary, aggregate.error, aggregate.expires_at, aggregate.updated_at, CURRENT_TIMESTAMP(6)
		FROM conversation_task_statuses AS aggregate WHERE aggregate.topic_id IN (%s)
		AND aggregate.source_uid IS NOT NULL AND NOT EXISTS (
		 SELECT 1 FROM conversation_task_status_sources AS source
		 WHERE source.topic_id = aggregate.topic_id AND source.source_uid = aggregate.source_uid AND (
		 (source.state IN ('running','waiting') AND (source.expires_at IS NULL OR source.expires_at > CURRENT_TIMESTAMP(6))
		 AND source.run_id <> aggregate.run_id AND aggregate.state NOT IN ('running','waiting'))
		 OR (source.run_id = aggregate.run_id AND source.state IN ('completed','failed','cancelled','stale')
		 AND aggregate.state NOT IN ('completed','failed','cancelled','stale'))
		 OR (source.run_id <=> aggregate.run_id AND source.state = aggregate.state AND source.summary = aggregate.summary
		 AND source.error = aggregate.error AND source.expires_at <=> aggregate.expires_at)))
		FOR UPDATE ON DUPLICATE KEY UPDATE run_id = VALUES(run_id), state = VALUES(state),
		summary = VALUES(summary), error = VALUES(error), expires_at = VALUES(expires_at),
		event_updated_at = VALUES(event_updated_at), updated_at = CURRENT_TIMESTAMP(6)`, placeholders), args...)
	return err
}

func (a *Adapter) BumpBotConnectionGeneration(botUID int64) (uint64, error) {
	if botUID <= 0 {
		return 0, fmt.Errorf("bot uid is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO bot_connection_generations (bot_uid, generation) VALUES (?, 1)
		ON DUPLICATE KEY UPDATE generation = generation + 1`, botUID); err != nil {
		return 0, err
	}
	var generation uint64
	if err := tx.QueryRow(`SELECT generation FROM bot_connection_generations WHERE bot_uid = ?`, botUID).Scan(&generation); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return generation, nil
}
func (a *Adapter) BotConnectionGeneration(botUID int64) (uint64, error) {
	if botUID <= 0 {
		return 0, fmt.Errorf("bot uid is required")
	}
	var generation uint64
	err := a.db.QueryRow(`SELECT generation FROM bot_connection_generations WHERE bot_uid = ?`, botUID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return generation, err
}

var _ store.ConversationTaskRunStore = (*Adapter)(nil)
var _ store.ConversationTaskStatusOutboxStore = (*Adapter)(nil)

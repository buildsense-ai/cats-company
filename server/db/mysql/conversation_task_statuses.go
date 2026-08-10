package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// UpsertConversationTaskStatus saves a source's latest status and refreshes
// the legacy per-topic aggregate in the same transaction.
func (a *Adapter) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	if status == nil {
		return nil, fmt.Errorf("conversation task status is nil")
	}
	if status.SourceUID <= 0 {
		return nil, fmt.Errorf("conversation task status source uid is required")
	}
	inputStatus := status
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin conversation task status transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT IGNORE INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES (?, '', 'idle', '', '', NULL, NULL, CURRENT_TIMESTAMP(6))`,
		status.TopicID,
	); err != nil {
		return nil, fmt.Errorf("ensure conversation task aggregate: %w", err)
	}
	var lockedTopicID string
	if err := tx.QueryRow(
		`SELECT topic_id FROM conversation_task_statuses WHERE topic_id = ? FOR UPDATE`,
		status.TopicID,
	).Scan(&lockedTopicID); err != nil {
		return nil, fmt.Errorf("lock conversation task aggregate: %w", err)
	}

	if err := reconcileLegacyConversationTaskStatuses(tx, "?", status.TopicID); err != nil {
		return nil, fmt.Errorf("reconcile legacy conversation task status: %w", err)
	}
	// When a legacy/direct caller provides no publisher event time, derive it
	// only after the per-topic lock is held so event order matches commit order.
	status = store.PrepareConversationTaskStatusForStore(status, time.Now().UTC())

	var currentRunID, currentState string
	var currentExpiresAt, currentUpdatedAt, currentEventUpdatedAt sql.NullTime
	err = tx.QueryRow(
		`SELECT run_id, state, expires_at, updated_at, event_updated_at FROM conversation_task_status_sources
		 WHERE topic_id = ? AND source_uid = ?`,
		status.TopicID,
		status.SourceUID,
	).Scan(&currentRunID, &currentState, &currentExpiresAt, &currentUpdatedAt, &currentEventUpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load current conversation task status: %w", err)
	}
	if err == nil {
		current := &types.ConversationTaskStatus{RunID: currentRunID, State: currentState}
		if currentUpdatedAt.Valid {
			current.UpdatedAt = currentUpdatedAt.Time
		}
		if currentEventUpdatedAt.Valid {
			current.EventUpdatedAt = currentEventUpdatedAt.Time
		}
		if currentExpiresAt.Valid {
			expiresAt := currentExpiresAt.Time
			current.ExpiresAt = &expiresAt
		}
		if err := store.ValidateConversationTaskStatusTransition(current, status, time.Now().UTC()); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP(6))
		 ON DUPLICATE KEY UPDATE
		   run_id = VALUES(run_id),
		   state = VALUES(state),
		   summary = VALUES(summary),
		   error = VALUES(error),
		   expires_at = VALUES(expires_at),
		   event_updated_at = VALUES(event_updated_at),
		   updated_at = CURRENT_TIMESTAMP(6)`,
		status.TopicID,
		status.SourceUID,
		status.RunID,
		status.State,
		status.Summary,
		status.Error,
		status.ExpiresAt,
		status.EventUpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("upsert conversation task source status: %w", err)
	}

	aggregate := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = ?
		   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		 ORDER BY
		   CASE WHEN state IN ('running', 'waiting') THEN 0 ELSE 1 END,
		   event_updated_at DESC,
		   source_uid DESC
		 LIMIT 1`,
		status.TopicID,
	).Scan(
		&aggregate.TopicID,
		&aggregate.RunID,
		&aggregate.State,
		&aggregate.Summary,
		&aggregate.Error,
		&aggregate.SourceUID,
		&aggregate.UpdatedAt,
		&aggregate.EventUpdatedAt,
		&aggregate.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		*aggregate = *status
	} else if err != nil {
		return nil, fmt.Errorf("load conversation task aggregate: %w", err)
	}

	if _, err := tx.Exec(
		`UPDATE conversation_task_statuses SET
		   run_id = ?,
		   state = ?,
		   summary = ?,
		   error = ?,
		   source_uid = NULLIF(?, 0),
		   expires_at = ?,
		   updated_at = CURRENT_TIMESTAMP(6)
		 WHERE topic_id = ?`,
		aggregate.RunID,
		aggregate.State,
		aggregate.Summary,
		aggregate.Error,
		aggregate.SourceUID,
		aggregate.ExpiresAt,
		aggregate.TopicID,
	); err != nil {
		return nil, fmt.Errorf("refresh conversation task aggregate: %w", err)
	}

	out := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at
		 FROM conversation_task_statuses WHERE topic_id = ?`,
		status.TopicID,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("reload conversation task aggregate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit conversation task status: %w", err)
	}
	// Propagate the post-lock causal time to the caller so in-memory observers
	// order the committed event exactly as the database did.
	inputStatus.EventUpdatedAt = status.EventUpdatedAt
	return out, nil
}

// GetConversationTaskStatusForSource returns the latest state owned by one
// bot/service. It reconciles legacy writes during rolling deployments.
func (a *Adapter) GetConversationTaskStatusForSource(topicID string, sourceUID int64) (*types.ConversationTaskStatus, error) {
	if err := reconcileLegacyConversationTaskStatuses(a.db, "?", topicID); err != nil {
		return nil, fmt.Errorf("reconcile legacy conversation task status: %w", err)
	}
	out := &types.ConversationTaskStatus{}
	err := a.db.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = ? AND source_uid = ?`,
		topicID,
		sourceUID,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.EventUpdatedAt, &out.ExpiresAt)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get conversation task source status: %w", err)
	}

	err = a.db.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at
		 FROM conversation_task_statuses
		 WHERE topic_id = ? AND source_uid = ?`,
		topicID,
		sourceUID,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get legacy conversation task source status: %w", err)
	}
	return out, nil
}

// GetConversationTaskStatuses returns an aggregate status keyed by topic id.
// It reconciles legacy writes; active sources otherwise take precedence.
func (a *Adapter) GetConversationTaskStatuses(topicIDs []string) (map[string]*types.ConversationTaskStatus, error) {
	if len(topicIDs) == 0 {
		return map[string]*types.ConversationTaskStatus{}, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(topicIDs)), ",")
	args := make([]interface{}, 0, len(topicIDs))
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}
	if err := reconcileLegacyConversationTaskStatuses(a.db, placeholders, args...); err != nil {
		return nil, fmt.Errorf("reconcile legacy conversation task statuses: %w", err)
	}

	rows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
			 FROM conversation_task_status_sources
			 WHERE topic_id IN (%s)
			   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
			 ORDER BY
			   topic_id,
			   CASE WHEN state IN ('running', 'waiting') THEN 0 ELSE 1 END,
			   event_updated_at DESC,
			   source_uid DESC`,
			placeholders,
		),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation task source aggregates: %w", err)
	}

	out := make(map[string]*types.ConversationTaskStatus, len(topicIDs))
	for rows.Next() {
		status := &types.ConversationTaskStatus{}
		if err := rows.Scan(&status.TopicID, &status.RunID, &status.State, &status.Summary, &status.Error, &status.SourceUID, &status.UpdatedAt, &status.EventUpdatedAt, &status.ExpiresAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan conversation task source aggregate: %w", err)
		}
		if _, exists := out[status.TopicID]; !exists {
			out[status.TopicID] = status
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	legacyRows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at
			 FROM conversation_task_statuses
			 WHERE topic_id IN (%s)
			   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`,
			placeholders,
		),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("get legacy conversation task statuses: %w", err)
	}
	defer legacyRows.Close()
	for legacyRows.Next() {
		status := &types.ConversationTaskStatus{}
		if err := legacyRows.Scan(&status.TopicID, &status.RunID, &status.State, &status.Summary, &status.Error, &status.SourceUID, &status.UpdatedAt, &status.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan legacy conversation task status: %w", err)
		}
		if _, exists := out[status.TopicID]; !exists {
			out[status.TopicID] = status
		}
	}
	return out, legacyRows.Err()
}

type conversationTaskStatusExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func reconcileLegacyConversationTaskStatuses(execer conversationTaskStatusExecer, placeholders string, args ...interface{}) error {
	_, err := execer.Exec(
		fmt.Sprintf(
			`INSERT INTO conversation_task_status_sources
	               (topic_id, source_uid, run_id, state, summary, error, expires_at, event_updated_at, updated_at)
	             SELECT aggregate.topic_id, aggregate.source_uid, aggregate.run_id, aggregate.state,
	                    aggregate.summary, aggregate.error, aggregate.expires_at, aggregate.updated_at, CURRENT_TIMESTAMP(6)
             FROM conversation_task_statuses AS aggregate
             WHERE aggregate.topic_id IN (%s)
               AND aggregate.source_uid IS NOT NULL
               AND NOT EXISTS (
                 SELECT 1
                 FROM conversation_task_status_sources AS source
                 WHERE source.topic_id = aggregate.topic_id
                   AND source.source_uid = aggregate.source_uid
                   AND (
                     (
                       source.state IN ('running', 'waiting')
                       AND (source.expires_at IS NULL OR source.expires_at > CURRENT_TIMESTAMP)
                       AND source.run_id <> aggregate.run_id
                       AND aggregate.state NOT IN ('running', 'waiting')
                     )
                     OR (
                       source.run_id = aggregate.run_id
                       AND source.state IN ('completed', 'failed', 'cancelled', 'stale')
                       AND aggregate.state NOT IN ('completed', 'failed', 'cancelled', 'stale')
                     )
                     OR (
                       source.run_id <=> aggregate.run_id
                       AND source.state = aggregate.state
                       AND source.summary = aggregate.summary
                       AND source.error = aggregate.error
                       AND source.expires_at <=> aggregate.expires_at
                     )
                   )
               )
             FOR UPDATE
             ON DUPLICATE KEY UPDATE
               run_id = VALUES(run_id),
               state = VALUES(state),
               summary = VALUES(summary),
	               error = VALUES(error),
	               expires_at = VALUES(expires_at),
	               event_updated_at = VALUES(event_updated_at),
	               updated_at = CURRENT_TIMESTAMP(6)`,
			placeholders,
		),
		args...,
	)
	return err
}

// ListAllActiveConversationTaskStatusesBefore returns every active source run
// last updated before the cutoff (feeds the periodic/startup reaper).
func (a *Adapter) ListAllActiveConversationTaskStatusesBefore(updatedBefore time.Time) ([]*types.ConversationTaskStatus, error) {
	rows, err := a.db.Query(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE state IN ('running', 'waiting')
		   AND updated_at <= ?
		   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP(6))
		 ORDER BY updated_at`,
		updatedBefore,
	)
	if err != nil {
		return nil, fmt.Errorf("list all active conversation task statuses before: %w", err)
	}
	defer rows.Close()

	var statuses []*types.ConversationTaskStatus
	for rows.Next() {
		status := &types.ConversationTaskStatus{}
		if err := rows.Scan(
			&status.TopicID,
			&status.RunID,
			&status.State,
			&status.Summary,
			&status.Error,
			&status.SourceUID,
			&status.UpdatedAt,
			&status.EventUpdatedAt,
			&status.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan active conversation task status: %w", err)
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

// ListActiveConversationTaskStatusesForSource returns active runs that were
// last updated before a bot connection disappeared.
func (a *Adapter) ListActiveConversationTaskStatusesForSource(sourceUID int64, updatedBefore time.Time) ([]*types.ConversationTaskStatus, error) {
	rows, err := a.db.Query(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
         FROM conversation_task_status_sources
         WHERE source_uid = ?
           AND state IN ('running', 'waiting')
           AND updated_at <= ?
           AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
         ORDER BY updated_at`,
		sourceUID,
		updatedBefore,
	)
	if err != nil {
		return nil, fmt.Errorf("list active conversation task statuses for source: %w", err)
	}
	defer rows.Close()

	var statuses []*types.ConversationTaskStatus
	for rows.Next() {
		status := &types.ConversationTaskStatus{}
		if err := rows.Scan(
			&status.TopicID,
			&status.RunID,
			&status.State,
			&status.Summary,
			&status.Error,
			&status.SourceUID,
			&status.UpdatedAt,
			&status.EventUpdatedAt,
			&status.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan active conversation task status for source: %w", err)
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

// MarkConversationTaskStatusStaleIfUnchanged atomically marks a source run
// stale, but only when the row still matches the disconnected run and was not
// updated after the disconnection (compare-and-set). This closes the
// check-then-write race in task recovery: a bot that reconnects and updates the
// same run before this write wins, and this call reports updated=false.
func (a *Adapter) MarkConversationTaskStatusStaleIfUnchanged(topicID string, sourceUID int64, runID string, disconnectedAt time.Time, generation uint64) (*types.ConversationTaskStatus, bool, error) {
	if topicID == "" || sourceUID <= 0 || runID == "" {
		return nil, false, fmt.Errorf("conversation task stale requires topic, source uid and run id")
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin conversation task stale transaction: %w", err)
	}
	defer tx.Rollback()

	// Fence on the cluster-wide bot connection generation inside the same
	// transaction: a newer connection generation (any node) must win, otherwise
	// an old recovery timer could mark work owned by a fresh connection stale.
	// The row is locked so a concurrent BumpBotConnectionGeneration cannot slip
	// between this check and the stale update (closes the cross-node TOCTOU).
	if _, err := tx.Exec(
		`INSERT IGNORE INTO bot_connection_generations (bot_uid, generation) VALUES (?, 0)`,
		sourceUID,
	); err != nil {
		return nil, false, fmt.Errorf("ensure bot connection generation: %w", err)
	}
	var currentGeneration uint64
	if err := tx.QueryRow(
		`SELECT generation FROM bot_connection_generations WHERE bot_uid = ? FOR UPDATE`,
		sourceUID,
	).Scan(&currentGeneration); err != nil {
		return nil, false, fmt.Errorf("lock bot connection generation: %w", err)
	}
	if currentGeneration != generation {
		return nil, false, nil
	}

	if _, err := tx.Exec(
		`INSERT IGNORE INTO conversation_task_statuses
		   (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES (?, '', 'idle', '', '', NULL, NULL, CURRENT_TIMESTAMP(6))`,
		topicID,
	); err != nil {
		return nil, false, fmt.Errorf("ensure conversation task aggregate: %w", err)
	}
	var lockedTopicID string
	if err := tx.QueryRow(
		`SELECT topic_id FROM conversation_task_statuses WHERE topic_id = ? FOR UPDATE`,
		topicID,
	).Scan(&lockedTopicID); err != nil {
		return nil, false, fmt.Errorf("lock conversation task aggregate: %w", err)
	}

	if err := reconcileLegacyConversationTaskStatuses(tx, "?", topicID); err != nil {
		return nil, false, fmt.Errorf("reconcile legacy conversation task status: %w", err)
	}

	res, err := tx.Exec(
		`UPDATE conversation_task_status_sources
		   SET state = 'stale',
		       summary = '机器人连接中断，任务已自动中止，可重新发送',
		       error = 'bot disconnected before terminal task status',
		       expires_at = NULL,
		       event_updated_at = GREATEST(event_updated_at, CURRENT_TIMESTAMP(6)),
		       updated_at = CURRENT_TIMESTAMP(6)
		 WHERE topic_id = ? AND source_uid = ?
		   AND run_id = ?
		   AND state IN ('running', 'waiting')
		   AND updated_at <= ?`,
		topicID,
		sourceUID,
		runID,
		disconnectedAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("mark conversation task stale: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("mark conversation task stale rows affected: %w", err)
	}
	if affected == 0 {
		return nil, false, nil
	}

	aggregate := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, event_updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = ?
		   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		 ORDER BY
		   CASE WHEN state IN ('running', 'waiting') THEN 0 ELSE 1 END,
		   event_updated_at DESC,
		   source_uid DESC
		 LIMIT 1`,
		topicID,
	).Scan(
		&aggregate.TopicID,
		&aggregate.RunID,
		&aggregate.State,
		&aggregate.Summary,
		&aggregate.Error,
		&aggregate.SourceUID,
		&aggregate.UpdatedAt,
		&aggregate.EventUpdatedAt,
		&aggregate.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		aggregate = &types.ConversationTaskStatus{
			TopicID:   topicID,
			RunID:     runID,
			State:     "stale",
			Summary:   "机器人连接中断，任务已自动中止，可重新发送",
			Error:     "bot disconnected before terminal task status",
			SourceUID: sourceUID,
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("load conversation task aggregate after stale: %w", err)
	}

	if _, err := tx.Exec(
		`UPDATE conversation_task_statuses SET
		   run_id = ?,
		   state = ?,
		   summary = ?,
		   error = ?,
		   source_uid = NULLIF(?, 0),
		   expires_at = ?,
		   updated_at = CURRENT_TIMESTAMP(6)
		 WHERE topic_id = ?`,
		aggregate.RunID,
		aggregate.State,
		aggregate.Summary,
		aggregate.Error,
		aggregate.SourceUID,
		aggregate.ExpiresAt,
		aggregate.TopicID,
	); err != nil {
		return nil, false, fmt.Errorf("refresh conversation task aggregate after stale: %w", err)
	}

	out := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at
		 FROM conversation_task_statuses WHERE topic_id = ?`,
		topicID,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.ExpiresAt)
	if err != nil {
		return nil, false, fmt.Errorf("reload conversation task aggregate after stale: %w", err)
	}
	out.EventUpdatedAt = aggregate.EventUpdatedAt
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit conversation task stale: %w", err)
	}
	return out, true, nil
}

// BumpBotConnectionGeneration atomically increments the cluster-wide connection
// generation for a bot and returns the new value. Missing rows start at 1.
func (a *Adapter) BumpBotConnectionGeneration(botUID int64) (uint64, error) {
	if botUID <= 0 {
		return 0, fmt.Errorf("bot uid is required")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin bot connection generation transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO bot_connection_generations (bot_uid, generation)
		 VALUES (?, 1)
		 ON DUPLICATE KEY UPDATE generation = generation + 1`,
		botUID,
	); err != nil {
		return 0, fmt.Errorf("bump bot connection generation: %w", err)
	}
	var generation uint64
	if err := tx.QueryRow(
		`SELECT generation FROM bot_connection_generations WHERE bot_uid = ?`,
		botUID,
	).Scan(&generation); err != nil {
		return 0, fmt.Errorf("reload bot connection generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit bot connection generation: %w", err)
	}
	return generation, nil
}

// BotConnectionGeneration returns the current cluster-wide connection
// generation for a bot, or 0 when the bot has never connected.
func (a *Adapter) BotConnectionGeneration(botUID int64) (uint64, error) {
	if botUID <= 0 {
		return 0, fmt.Errorf("bot uid is required")
	}
	var generation uint64
	err := a.db.QueryRow(
		`SELECT generation FROM bot_connection_generations WHERE bot_uid = ?`,
		botUID,
	).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load bot connection generation: %w", err)
	}
	return generation, nil
}

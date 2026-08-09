package postgres

import (
	"database/sql"
	"errors"
	"fmt"
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
	if status.UpdatedAt.IsZero() {
		copyStatus := *status
		copyStatus.UpdatedAt = time.Now().UTC()
		status = &copyStatus
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin conversation task status transaction: %w", err)
	}
	defer tx.Rollback()

	// The legacy row doubles as a per-topic transaction lock. This keeps two
	// sources finishing concurrently from leaving a stale running aggregate.
	if _, err := tx.Exec(
		`INSERT INTO conversation_task_statuses (topic_id, state, summary, error, updated_at)
		 VALUES ($1, 'idle', '', '', CURRENT_TIMESTAMP)
		 ON CONFLICT (topic_id) DO NOTHING`,
		status.TopicID,
	); err != nil {
		return nil, fmt.Errorf("ensure conversation task aggregate: %w", err)
	}
	var lockedTopicID string
	if err := tx.QueryRow(
		`SELECT topic_id FROM conversation_task_statuses WHERE topic_id = $1 FOR UPDATE`,
		status.TopicID,
	).Scan(&lockedTopicID); err != nil {
		return nil, fmt.Errorf("lock conversation task aggregate: %w", err)
	}

	if err := reconcileLegacyConversationTaskStatuses(tx, "$1", status.TopicID); err != nil {
		return nil, fmt.Errorf("reconcile legacy conversation task status: %w", err)
	}

	var currentRunID, currentState string
	var currentExpiresAt, currentUpdatedAt sql.NullTime
	err = tx.QueryRow(
		`SELECT run_id, state, expires_at, updated_at FROM conversation_task_status_sources
		 WHERE topic_id = $1 AND source_uid = $2`,
		status.TopicID,
		status.SourceUID,
	).Scan(&currentRunID, &currentState, &currentExpiresAt, &currentUpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load current conversation task status: %w", err)
	}
	if err == nil {
		current := &types.ConversationTaskStatus{RunID: currentRunID, State: currentState}
		if currentUpdatedAt.Valid {
			current.UpdatedAt = currentUpdatedAt.Time
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
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (topic_id, source_uid) DO UPDATE SET
		   run_id = EXCLUDED.run_id,
		   state = EXCLUDED.state,
		   summary = EXCLUDED.summary,
		   error = EXCLUDED.error,
		   expires_at = EXCLUDED.expires_at,
		   updated_at = EXCLUDED.updated_at`,
		status.TopicID,
		status.SourceUID,
		status.RunID,
		status.State,
		status.Summary,
		status.Error,
		status.ExpiresAt,
		status.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("upsert conversation task source status: %w", err)
	}

	aggregate := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = $1
		   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		 ORDER BY
		   CASE WHEN state IN ('running', 'waiting') THEN 0 ELSE 1 END,
		   updated_at DESC,
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
		&aggregate.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		*aggregate = *status
	} else if err != nil {
		return nil, fmt.Errorf("load conversation task aggregate: %w", err)
	}

	out := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`UPDATE conversation_task_statuses SET
		   run_id = $2,
		   state = $3,
		   summary = $4,
		   error = $5,
		   source_uid = NULLIF($6, 0),
		   expires_at = $7,
		   updated_at = CURRENT_TIMESTAMP
		 WHERE topic_id = $1
		 RETURNING topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at`,
		aggregate.TopicID,
		aggregate.RunID,
		aggregate.State,
		aggregate.Summary,
		aggregate.Error,
		aggregate.SourceUID,
		aggregate.ExpiresAt,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("refresh conversation task aggregate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit conversation task status: %w", err)
	}
	return out, nil
}

// GetConversationTaskStatusForSource returns the latest state owned by one
// bot/service. It reconciles legacy writes during rolling deployments.
func (a *Adapter) GetConversationTaskStatusForSource(topicID string, sourceUID int64) (*types.ConversationTaskStatus, error) {
	if err := reconcileLegacyConversationTaskStatuses(a.db, "$1", topicID); err != nil {
		return nil, fmt.Errorf("reconcile legacy conversation task status: %w", err)
	}
	out := &types.ConversationTaskStatus{}
	err := a.db.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = $1 AND source_uid = $2`,
		topicID,
		sourceUID,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.ExpiresAt)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get conversation task source status: %w", err)
	}

	err = a.db.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at
		 FROM conversation_task_statuses
		 WHERE topic_id = $1 AND source_uid = $2`,
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

	placeholders := inPlaceholders(1, len(topicIDs))
	args := make([]interface{}, 0, len(topicIDs))
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}
	if err := reconcileLegacyConversationTaskStatuses(a.db, placeholders, args...); err != nil {
		return nil, fmt.Errorf("reconcile legacy conversation task statuses: %w", err)
	}

	rows, err := a.db.Query(
		fmt.Sprintf(
			`SELECT DISTINCT ON (topic_id)
			   topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
			 FROM conversation_task_status_sources
			 WHERE topic_id IN (%s)
			   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
			 ORDER BY
			   topic_id,
			   CASE WHEN state IN ('running', 'waiting') THEN 0 ELSE 1 END,
			   updated_at DESC,
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
		if err := rows.Scan(&status.TopicID, &status.RunID, &status.State, &status.Summary, &status.Error, &status.SourceUID, &status.UpdatedAt, &status.ExpiresAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan conversation task source aggregate: %w", err)
		}
		out[status.TopicID] = status
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
			   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
			 SELECT topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at
			 FROM conversation_task_statuses
			 WHERE topic_id IN (%s) AND source_uid IS NOT NULL
			 FOR UPDATE
			 ON CONFLICT (topic_id, source_uid) DO UPDATE SET
			   run_id = EXCLUDED.run_id,
			   state = EXCLUDED.state,
			   summary = EXCLUDED.summary,
			   error = EXCLUDED.error,
			   expires_at = EXCLUDED.expires_at,
			   updated_at = EXCLUDED.updated_at
			 WHERE NOT (
			   (
			     conversation_task_status_sources.state IN ('running', 'waiting')
			     AND (conversation_task_status_sources.expires_at IS NULL OR conversation_task_status_sources.expires_at > clock_timestamp())
			     AND conversation_task_status_sources.run_id <> EXCLUDED.run_id
			     AND EXCLUDED.state NOT IN ('running', 'waiting')
			   )
			   OR (
			     conversation_task_status_sources.run_id = EXCLUDED.run_id
			     AND conversation_task_status_sources.state IN ('completed', 'failed', 'cancelled', 'stale')
			     AND EXCLUDED.state NOT IN ('completed', 'failed', 'cancelled', 'stale')
			   )
			 )
			 AND (
			   conversation_task_status_sources.run_id,
			   conversation_task_status_sources.state,
			   conversation_task_status_sources.summary,
			   conversation_task_status_sources.error,
			   conversation_task_status_sources.expires_at
			 ) IS DISTINCT FROM (
			   EXCLUDED.run_id,
			   EXCLUDED.state,
			   EXCLUDED.summary,
			   EXCLUDED.error,
			   EXCLUDED.expires_at
			 )`,
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
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE state IN ('running', 'waiting')
		   AND updated_at <= $1
		   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
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
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE source_uid = $1
		   AND state IN ('running', 'waiting')
		   AND updated_at <= $2
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
		`INSERT INTO bot_connection_generations (bot_uid, generation)
		 VALUES ($1, 0)
		 ON CONFLICT (bot_uid) DO NOTHING`,
		sourceUID,
	); err != nil {
		return nil, false, fmt.Errorf("ensure bot connection generation: %w", err)
	}
	var currentGeneration uint64
	if err := tx.QueryRow(
		`SELECT generation FROM bot_connection_generations WHERE bot_uid = $1 FOR UPDATE`,
		sourceUID,
	).Scan(&currentGeneration); err != nil {
		return nil, false, fmt.Errorf("lock bot connection generation: %w", err)
	}
	if currentGeneration != generation {
		return nil, false, nil
	}

	if _, err := tx.Exec(
		`INSERT INTO conversation_task_statuses (topic_id, state, summary, error, updated_at)
		 VALUES ($1, 'idle', '', '', CURRENT_TIMESTAMP)
		 ON CONFLICT (topic_id) DO NOTHING`,
		topicID,
	); err != nil {
		return nil, false, fmt.Errorf("ensure conversation task aggregate: %w", err)
	}
	var lockedTopicID string
	if err := tx.QueryRow(
		`SELECT topic_id FROM conversation_task_statuses WHERE topic_id = $1 FOR UPDATE`,
		topicID,
	).Scan(&lockedTopicID); err != nil {
		return nil, false, fmt.Errorf("lock conversation task aggregate: %w", err)
	}

	if err := reconcileLegacyConversationTaskStatuses(tx, "$1", topicID); err != nil {
		return nil, false, fmt.Errorf("reconcile legacy conversation task status: %w", err)
	}

	res, err := tx.Exec(
		`UPDATE conversation_task_status_sources
		   SET state = 'stale',
		       summary = '机器人连接中断，任务已自动中止，可重新发送',
		       error = 'bot disconnected before terminal task status',
		       expires_at = NULL,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE topic_id = $1 AND source_uid = $2
		   AND run_id = $3
		   AND state IN ('running', 'waiting')
		   AND updated_at <= $4`,
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
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = $1
		   AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		 ORDER BY
		   CASE WHEN state IN ('running', 'waiting') THEN 0 ELSE 1 END,
		   updated_at DESC,
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

	out := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`UPDATE conversation_task_statuses SET
		   run_id = $2,
		   state = $3,
		   summary = $4,
		   error = $5,
		   source_uid = NULLIF($6, 0),
		   expires_at = $7,
		   updated_at = CURRENT_TIMESTAMP
		 WHERE topic_id = $1
		 RETURNING topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at`,
		aggregate.TopicID,
		aggregate.RunID,
		aggregate.State,
		aggregate.Summary,
		aggregate.Error,
		aggregate.SourceUID,
		aggregate.ExpiresAt,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.ExpiresAt)
	if err != nil {
		return nil, false, fmt.Errorf("refresh conversation task aggregate after stale: %w", err)
	}
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
	var generation uint64
	err := a.db.QueryRow(
		`INSERT INTO bot_connection_generations (bot_uid, generation)
		 VALUES ($1, 1)
		 ON CONFLICT (bot_uid) DO UPDATE SET generation = bot_connection_generations.generation + 1
		 RETURNING generation`,
		botUID,
	).Scan(&generation)
	if err != nil {
		return 0, fmt.Errorf("bump bot connection generation: %w", err)
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
		`SELECT generation FROM bot_connection_generations WHERE bot_uid = $1`,
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

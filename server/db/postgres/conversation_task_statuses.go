package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

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
	if _, err := tx.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 SELECT topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at
		 FROM conversation_task_statuses
		 WHERE topic_id = $1 AND source_uid IS NOT NULL
		 ON CONFLICT (topic_id, source_uid) DO NOTHING`,
		status.TopicID,
	); err != nil {
		return nil, fmt.Errorf("backfill legacy conversation task source status: %w", err)
	}

	current := &types.ConversationTaskStatus{}
	err = tx.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = $1 AND source_uid = $2
		 FOR UPDATE`,
		status.TopicID,
		status.SourceUID,
	).Scan(
		&current.TopicID,
		&current.RunID,
		&current.State,
		&current.Summary,
		&current.Error,
		&current.SourceUID,
		&current.UpdatedAt,
		&current.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		var aggregateSource sql.NullInt64
		err = tx.QueryRow(
			`SELECT run_id, state, source_uid, expires_at
			 FROM conversation_task_statuses
			 WHERE topic_id = $1`,
			status.TopicID,
		).Scan(&current.RunID, &current.State, &aggregateSource, &current.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("load legacy conversation task status: %w", err)
		}
		if !aggregateSource.Valid || aggregateSource.Int64 != status.SourceUID {
			current = nil
		} else {
			current.TopicID = status.TopicID
			current.SourceUID = aggregateSource.Int64
		}
	} else if err != nil {
		return nil, fmt.Errorf("load current conversation task status: %w", err)
	}
	if err := types.ValidateConversationTaskStatusTransition(current, status, time.Now()); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO conversation_task_status_sources
		   (topic_id, source_uid, run_id, state, summary, error, expires_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, CURRENT_TIMESTAMP)
		 ON CONFLICT (topic_id, source_uid) DO UPDATE SET
		   run_id = EXCLUDED.run_id,
		   state = EXCLUDED.state,
		   summary = EXCLUDED.summary,
		   error = EXCLUDED.error,
		   expires_at = EXCLUDED.expires_at,
		   updated_at = CURRENT_TIMESTAMP`,
		status.TopicID,
		status.SourceUID,
		status.RunID,
		status.State,
		status.Summary,
		status.Error,
		status.ExpiresAt,
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
// bot/service. The legacy fallback keeps rolling deployments compatible.
func (a *Adapter) GetConversationTaskStatusForSource(topicID string, sourceUID int64) (*types.ConversationTaskStatus, error) {
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
// Active sources take precedence; otherwise the newest terminal status wins.
func (a *Adapter) GetConversationTaskStatuses(topicIDs []string) (map[string]*types.ConversationTaskStatus, error) {
	if len(topicIDs) == 0 {
		return map[string]*types.ConversationTaskStatus{}, nil
	}

	placeholders := inPlaceholders(1, len(topicIDs))
	args := make([]interface{}, 0, len(topicIDs))
	for _, topicID := range topicIDs {
		args = append(args, topicID)
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

package mysql

import (
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// UpsertConversationTaskStatus saves the latest task status for a topic.
func (a *Adapter) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	if status == nil {
		return nil, fmt.Errorf("conversation task status is nil")
	}
	_, err := a.db.Exec(
		`INSERT INTO conversation_task_statuses (topic_id, run_id, state, summary, error, source_uid, expires_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, NULLIF(?, 0), ?, CURRENT_TIMESTAMP)
		 ON DUPLICATE KEY UPDATE
		   run_id = VALUES(run_id),
		   state = VALUES(state),
		   summary = VALUES(summary),
		   error = VALUES(error),
		   source_uid = VALUES(source_uid),
		   expires_at = VALUES(expires_at),
		   updated_at = CURRENT_TIMESTAMP`,
		status.TopicID,
		status.RunID,
		status.State,
		status.Summary,
		status.Error,
		status.SourceUID,
		status.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert conversation task status: %w", err)
	}

	out := &types.ConversationTaskStatus{}
	err = a.db.QueryRow(
		`SELECT topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at
		 FROM conversation_task_statuses WHERE topic_id = ?`,
		status.TopicID,
	).Scan(&out.TopicID, &out.RunID, &out.State, &out.Summary, &out.Error, &out.SourceUID, &out.UpdatedAt, &out.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("reload conversation task status: %w", err)
	}
	return out, nil
}

// GetConversationTaskStatuses returns latest task statuses keyed by topic id.
func (a *Adapter) GetConversationTaskStatuses(topicIDs []string) (map[string]*types.ConversationTaskStatus, error) {
	if len(topicIDs) == 0 {
		return map[string]*types.ConversationTaskStatus{}, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(topicIDs)), ",")
	args := make([]interface{}, 0, len(topicIDs))
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}

	rows, err := a.db.Query(
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
		return nil, fmt.Errorf("get conversation task statuses: %w", err)
	}
	defer rows.Close()

	out := make(map[string]*types.ConversationTaskStatus, len(topicIDs))
	for rows.Next() {
		status := &types.ConversationTaskStatus{}
		if err := rows.Scan(&status.TopicID, &status.RunID, &status.State, &status.Summary, &status.Error, &status.SourceUID, &status.UpdatedAt, &status.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan conversation task status: %w", err)
		}
		out[status.TopicID] = status
	}
	return out, rows.Err()
}

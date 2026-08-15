package mysql

import (
	"context"
	"fmt"
	"strings"
)

// ListMutedConversationTopics returns the muted subset of the supplied topics.
func (a *Adapter) ListMutedConversationTopics(ctx context.Context, userID int64, topicIDs []string) (map[string]bool, error) {
	muted := make(map[string]bool)
	if userID <= 0 || len(topicIDs) == 0 {
		return muted, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(topicIDs)), ",")
	args := make([]interface{}, 0, len(topicIDs)+1)
	args = append(args, userID)
	for _, topicID := range topicIDs {
		args = append(args, topicID)
	}
	rows, err := a.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT topic_id FROM conversation_notification_mutes WHERE user_id = ? AND topic_id IN (%s)`, placeholders),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list muted conversations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var topicID string
		if err := rows.Scan(&topicID); err != nil {
			return nil, fmt.Errorf("scan muted conversation: %w", err)
		}
		muted[topicID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate muted conversations: %w", err)
	}
	return muted, nil
}

// SetConversationNotificationsMuted records or clears a user-level mute.
func (a *Adapter) SetConversationNotificationsMuted(ctx context.Context, userID int64, topicID string, muted bool) error {
	if muted {
		if _, err := a.db.ExecContext(ctx,
			`INSERT INTO conversation_notification_mutes (user_id, topic_id) VALUES (?, ?)
			 ON DUPLICATE KEY UPDATE user_id = VALUES(user_id)`,
			userID, topicID,
		); err != nil {
			return fmt.Errorf("mute conversation notifications: %w", err)
		}
		return nil
	}
	if _, err := a.db.ExecContext(ctx,
		`DELETE FROM conversation_notification_mutes WHERE user_id = ? AND topic_id = ?`,
		userID, topicID,
	); err != nil {
		return fmt.Errorf("unmute conversation notifications: %w", err)
	}
	return nil
}

// IsConversationNotificationsMuted reports whether a conversation is muted.
func (a *Adapter) IsConversationNotificationsMuted(ctx context.Context, userID int64, topicID string) (bool, error) {
	var muted bool
	if err := a.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM conversation_notification_mutes WHERE user_id = ? AND topic_id = ?)`,
		userID, topicID,
	).Scan(&muted); err != nil {
		return false, fmt.Errorf("check muted conversation: %w", err)
	}
	return muted, nil
}

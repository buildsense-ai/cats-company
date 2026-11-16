package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

const fileVersionBindingColumns = `id, topic_id, source_message_id, source_attachment_index,
source_file_key, latest_file_key, latest_url, latest_name, latest_size, latest_mime_type,
updated_by_uid, created_at, updated_at`

// UpsertFileVersionBinding atomically replaces the latest pointer for a source attachment.
func (a *Adapter) UpsertFileVersionBinding(ctx context.Context, binding *store.FileVersionBinding) (*store.FileVersionBinding, error) {
	if binding == nil {
		return nil, fmt.Errorf("file version binding is nil")
	}
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO file_version_bindings
		 (topic_id, source_message_id, source_attachment_index, source_file_key,
		  latest_file_key, latest_url, latest_name, latest_size, latest_mime_type, updated_by_uid)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		  source_file_key = VALUES(source_file_key),
		  latest_file_key = VALUES(latest_file_key),
		  latest_url = VALUES(latest_url),
		  latest_name = VALUES(latest_name),
		  latest_size = VALUES(latest_size),
		  latest_mime_type = VALUES(latest_mime_type),
		  updated_by_uid = VALUES(updated_by_uid),
		  updated_at = CURRENT_TIMESTAMP`,
		binding.TopicID, binding.SourceMessageID, binding.SourceAttachmentIndex, binding.SourceFileKey,
		binding.LatestFileKey, binding.LatestURL, binding.LatestName, binding.LatestSize,
		binding.LatestMimeType, binding.UpdatedByUID,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert file version binding: %w", err)
	}
	return a.getFileVersionBinding(ctx, binding.TopicID, binding.SourceMessageID,
		binding.SourceAttachmentIndex, "read file version binding after upsert")
}

// GetFileVersionBinding resolves the durable latest pointer by its source triple.
func (a *Adapter) GetFileVersionBinding(ctx context.Context, topicID string, sourceMessageID, sourceAttachmentIndex int64) (*store.FileVersionBinding, error) {
	return a.getFileVersionBinding(ctx, topicID, sourceMessageID, sourceAttachmentIndex,
		"read file version binding")
}

func (a *Adapter) getFileVersionBinding(ctx context.Context, topicID string, sourceMessageID, sourceAttachmentIndex int64, operation string) (*store.FileVersionBinding, error) {
	var binding store.FileVersionBinding
	err := a.db.QueryRowContext(ctx,
		`SELECT `+fileVersionBindingColumns+`
		 FROM file_version_bindings
		 WHERE topic_id = ? AND source_message_id = ? AND source_attachment_index = ?`,
		topicID, sourceMessageID, sourceAttachmentIndex,
	).Scan(&binding.ID, &binding.TopicID, &binding.SourceMessageID, &binding.SourceAttachmentIndex,
		&binding.SourceFileKey, &binding.LatestFileKey, &binding.LatestURL, &binding.LatestName,
		&binding.LatestSize, &binding.LatestMimeType, &binding.UpdatedByUID,
		&binding.CreatedAt, &binding.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return &binding, nil
}

var _ store.FileVersionBindingStore = (*Adapter)(nil)

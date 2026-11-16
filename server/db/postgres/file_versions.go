package postgres

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
	var result store.FileVersionBinding
	err := a.db.QueryRowContext(ctx,
		`INSERT INTO file_version_bindings
		 (topic_id, source_message_id, source_attachment_index, source_file_key,
		  latest_file_key, latest_url, latest_name, latest_size, latest_mime_type, updated_by_uid)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (topic_id, source_message_id, source_attachment_index) DO UPDATE SET
		  source_file_key = EXCLUDED.source_file_key,
		  latest_file_key = EXCLUDED.latest_file_key,
		  latest_url = EXCLUDED.latest_url,
		  latest_name = EXCLUDED.latest_name,
		  latest_size = EXCLUDED.latest_size,
		  latest_mime_type = EXCLUDED.latest_mime_type,
		  updated_by_uid = EXCLUDED.updated_by_uid,
		  updated_at = CURRENT_TIMESTAMP
		 RETURNING `+fileVersionBindingColumns,
		binding.TopicID, binding.SourceMessageID, binding.SourceAttachmentIndex, binding.SourceFileKey,
		binding.LatestFileKey, binding.LatestURL, binding.LatestName, binding.LatestSize,
		binding.LatestMimeType, binding.UpdatedByUID,
	).Scan(&result.ID, &result.TopicID, &result.SourceMessageID, &result.SourceAttachmentIndex,
		&result.SourceFileKey, &result.LatestFileKey, &result.LatestURL, &result.LatestName,
		&result.LatestSize, &result.LatestMimeType, &result.UpdatedByUID,
		&result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert file version binding: %w", err)
	}
	return &result, nil
}

// GetFileVersionBinding resolves the durable latest pointer by its source triple.
func (a *Adapter) GetFileVersionBinding(ctx context.Context, topicID string, sourceMessageID, sourceAttachmentIndex int64) (*store.FileVersionBinding, error) {
	var binding store.FileVersionBinding
	err := a.db.QueryRowContext(ctx,
		`SELECT `+fileVersionBindingColumns+`
		 FROM file_version_bindings
		 WHERE topic_id = $1 AND source_message_id = $2 AND source_attachment_index = $3`,
		topicID, sourceMessageID, sourceAttachmentIndex,
	).Scan(&binding.ID, &binding.TopicID, &binding.SourceMessageID, &binding.SourceAttachmentIndex,
		&binding.SourceFileKey, &binding.LatestFileKey, &binding.LatestURL, &binding.LatestName,
		&binding.LatestSize, &binding.LatestMimeType, &binding.UpdatedByUID,
		&binding.CreatedAt, &binding.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read file version binding: %w", err)
	}
	return &binding, nil
}

var _ store.FileVersionBindingStore = (*Adapter)(nil)

package store

import (
	"context"
	"time"
)

// FileVersionBinding is the durable pointer from one immutable source attachment
// to the newest uploaded Office file for that attachment. It deliberately is a
// separate interface instead of a Store method: focused fakes and non-SQL stores
// must keep compiling unchanged.
type FileVersionBinding struct {
	ID                    int64     `json:"id"`
	TopicID               string    `json:"topic_id"`
	SourceMessageID       int64     `json:"source_message_id"`
	SourceAttachmentIndex int64     `json:"source_attachment_index"`
	SourceFileKey         string    `json:"source_file_key"`
	LatestFileKey         string    `json:"latest_file_key"`
	LatestURL             string    `json:"latest_url"`
	LatestName            string    `json:"latest_name"`
	LatestSize            int64     `json:"latest_size"`
	LatestMimeType        string    `json:"latest_mime_type"`
	UpdatedByUID          int64     `json:"updated_by_uid"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// FileVersionBindingStore atomically replaces the latest pointer for one source
// attachment and resolves it by the unique source triple.
type FileVersionBindingStore interface {
	UpsertFileVersionBinding(ctx context.Context, binding *FileVersionBinding) (*FileVersionBinding, error)
	GetFileVersionBinding(ctx context.Context, topicID string, sourceMessageID, sourceAttachmentIndex int64) (*FileVersionBinding, error)
}

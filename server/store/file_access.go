package store

import (
	"context"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// FileUploadRecord records provenance, not permission to read an object.
type FileUploadRecord struct {
	ObjectKey   string
	UploaderUID int64
	CreatedAt   time.Time
	// Bound remains true after the last referencing message is deleted.
	Bound bool
}

// FileMessageBinding allows one resource to appear in multiple messages.
type FileMessageBinding struct {
	ResourceKey string
	MessageID   int64
	TopicID     string
}

// FileMediaSession contains hashes only; callers must hash bearer credentials.
type FileMediaSession struct {
	TokenHash  string
	ActorUID   int64
	ParentHash string
	ExpiresAt  time.Time
}

// FileAccessStore persists file provenance, message references and media sessions.
// It does not make authorization decisions. Missing single records return nil, nil.
// Adapters bound operations to 15 seconds, object keys to 1024 bytes, resource
// keys to 16384 bytes and binding batches to 256 keys. Binding reads are complete
// (no result cap). Backfill pages default to 100 and are capped at 1000 messages.
// Session hashes are 64 hex characters; a root session may have an empty parent.
type FileAccessStore interface {
	RecordFileUpload(context.Context, string, int64) error
	GetFileUpload(context.Context, string) (*FileUploadRecord, error)
	BindMessageFiles(context.Context, int64, string, []string) error
	ListFileMessageBindings(context.Context, string) ([]FileMessageBinding, error)
	GetFileAccessMessage(context.Context, int64) (*types.Message, error)
	ListFileBackfillMessages(context.Context, int64, int) ([]*types.Message, error)
	CreateFileMediaSession(context.Context, FileMediaSession) error
	GetFileMediaSession(context.Context, string) (*FileMediaSession, error)
	RevokeFileMediaSessions(context.Context, string) error
}

// FileMessageWriter atomically inserts a message and its file references.
// A repeated clientMsgID returns the original ID and duplicate=true without
// attaching any resourceKeys from the repeated request to that message.
type FileMessageWriter interface {
	SaveMessageWithFileBindings(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string, metadata map[string]interface{}, resourceKeys []string) (int64, bool, error)
}

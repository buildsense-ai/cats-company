package store

import (
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	MessageSearchAll      = "all"
	MessageSearchMessage  = "message"
	MessageSearchArtifact = "artifact"
)

// MessageSearchResult is a permission-scoped global message search hit.
type MessageSearchResult struct {
	MessageID    int64     `json:"message_id"`
	TopicID      string    `json:"topic_id"`
	TopicName    string    `json:"topic_name"`
	FromUID      int64     `json:"from_uid"`
	SenderName   string    `json:"sender_name"`
	Content      string    `json:"content"`
	Snippet      string    `json:"snippet"`
	ContentType  string    `json:"content_type"`
	ArtifactName string    `json:"artifact_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// MessageSearchStore is optional so narrow fake stores are not forced to implement search.
// Implementations must apply topic read access in the same query that selects results.
type MessageSearchStore interface {
	SearchMessages(viewerUID int64, query, searchType string, limit int) ([]*MessageSearchResult, error)
}

// MessageAroundStore is optional support for locating an old message in topic history.
type MessageAroundStore interface {
	GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error)
}

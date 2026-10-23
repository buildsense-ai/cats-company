package store

import (
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// ConversationTaskRun preserves one publisher run independently of the legacy
// per-source projection. ID is a stable, descending pagination cursor.
type ConversationTaskRun struct {
	ID int64 `json:"id"`
	*types.ConversationTaskStatus
}

type ConversationTaskRunStore interface {
	ListConversationTaskRuns(topicID string, beforeID int64, limit int) ([]ConversationTaskRun, error)
}

// ConversationTaskStatusEvent is committed atomically with its run. Delivery is
// at least once to the existing fanout transport, not a browser acknowledgement.
type ConversationTaskStatusEvent struct {
	ID     int64
	Claim  string
	Status *types.ConversationTaskStatus
}

type ConversationTaskStatusOutboxStore interface {
	ClaimConversationTaskStatusEvents(limit int, lease time.Duration) ([]ConversationTaskStatusEvent, error)
	CompleteConversationTaskStatusEvent(id int64, claim string) error
}

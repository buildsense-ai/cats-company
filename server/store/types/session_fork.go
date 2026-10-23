package types

import "time"

type SessionForkRequest struct {
	RequestID     string `json:"request_id"`
	SourceTopicID string `json:"source_topic_id"`
	Name          string `json:"name,omitempty"`
	// UntilMessageID copies history up to and including this source message
	// instead of the whole conversation, so a copy can branch from an earlier
	// point the way a checkpoint fork does. Zero keeps the original behaviour of
	// copying everything.
	//
	// The anchor is a source message ID, not a turn number: turn numbering is a
	// client-side view over the same rows, and a count would silently point at a
	// different place once history is compacted or a message is deleted.
	UntilMessageID int64 `json:"until_message_id,omitempty"`
	ActorUserID    int64 `json:"-"`
	AgentUID       int64 `json:"-"`
	BodyID         string `json:"-"`
}

// SessionFork is a durable operation, not a chat message or an execution request.
type SessionFork struct {
	RequestID      string           `json:"request_id"`
	SourceTopicID  string           `json:"source_topic_id"`
	TargetTopicID  string           `json:"target_topic_id"`
	State          string           `json:"state"`
	Error          string           `json:"error,omitempty"`
	ActorUserID    int64            `json:"-"`
	AgentUID       int64            `json:"-"`
	Name           string           `json:"-"`
	BodyID         string           `json:"-"`
	SourceRevision int64            `json:"-"`
	TargetRevision int64            `json:"-"`
	SourceLastSeq  int64            `json:"-"`
	// SourceTurnCount is how many complete user turns the copied range contains.
	// The runtime cannot match on cloud message IDs (a live session records them
	// only on the restore path), so the turn count is what tells it where to cut
	// its local transcript. Zero means the copy is unbounded or the count could
	// not be established.
	SourceTurnCount int64            `json:"-"`
	MessageIDMap   map[string]int64 `json:"-"`
	CreatedAt      time.Time        `json:"-"`
}

// Terminal failure reasons the server records on its own behalf, as opposed to
// reasons reported by the runtime. Clients map these to actionable guidance
// instead of showing the raw code.
const (
	// SessionForkErrorAckTimeout marks a copy whose runtime never acknowledged
	// it within the acknowledgement window: typically a device that is offline,
	// or a runtime built before session forks existed. Such a runtime ignores
	// the control frame entirely, so without this bound the copy stays pending
	// forever and the UI waits on it indefinitely.
	SessionForkErrorAckTimeout = "session_fork_ack_timeout"
	// SessionForkErrorCancelled marks a copy the user abandoned while it was
	// still pending. A copy that already reached a terminal state is never
	// rewritten, so this can only ever replace a pending state.
	SessionForkErrorCancelled = "session_fork_cancelled"
)

type SessionForkCompletion struct {
	BodyID        string `json:"-"`
	RequestID     string `json:"request_id"`
	TargetTopicID string `json:"target_topic_id"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
}

type SessionForkControl struct {
	RequestID     string           `json:"request_id"`
	SourceTopicID string           `json:"source_topic_id"`
	TargetTopicID string           `json:"target_topic_id"`
	ActorUserID   int64            `json:"actor_user_id"`
	AgentUID      int64            `json:"agent_uid"`
	SourceLastSeq int64            `json:"source_last_seq"`
	// SourceTurnCount bounds the runtime's local transcript. It counts complete
	// user turns in the copied range using the same rule the server applied, so
	// both sides cut at the same place. A runtime must not infer this from message
	// IDs: a live session records cloud IDs only on the restore path.
	SourceTurnCount int64            `json:"source_turn_count,omitempty"`
	MessageIDMap    map[string]int64 `json:"message_id_map"`
}

func (f *SessionFork) Control() *SessionForkControl {
	return &SessionForkControl{f.RequestID, f.SourceTopicID, f.TargetTopicID, f.ActorUserID, f.AgentUID, f.SourceLastSeq, f.SourceTurnCount, f.MessageIDMap}
}

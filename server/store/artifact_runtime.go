package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrArtifactRuntimeRevisionConflict marks a compare-and-swap failure. Use
// ArtifactRuntimeRevisionConflict to expose the current revision to callers.
var ErrArtifactRuntimeRevisionConflict = errors.New("artifact runtime revision conflict")

// ArtifactRuntimeRevisionConflict is returned when base_revision no longer
// matches the durable document. CurrentRevision is zero when the document is
// missing.
type ArtifactRuntimeRevisionConflict struct {
	CurrentRevision int64
}

func (e *ArtifactRuntimeRevisionConflict) Error() string {
	return fmt.Sprintf("%s: current revision is %d", ErrArtifactRuntimeRevisionConflict, e.CurrentRevision)
}

func (e *ArtifactRuntimeRevisionConflict) Is(target error) bool {
	return target == ErrArtifactRuntimeRevisionConflict
}

// ArtifactRuntimeState is one durable namespaced JSON document. It belongs to
// a stable Agent-owned Artifact and deliberately has no publication version.
type ArtifactRuntimeState struct {
	AgentUID     int64
	ArtifactID   string
	Namespace    string
	Key          string
	Value        json.RawMessage
	Revision     int64
	UpdatedByUID int64
	UpdatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ArtifactRuntimeEvent is appended in the same transaction as a successful
// State write. EventID is commit-serialized within one Artifact; the State
// document remains truth.
type ArtifactRuntimeEvent struct {
	EventID      int64
	EventType    string
	AgentUID     int64
	ArtifactID   string
	Namespace    string
	Key          string
	Revision     int64
	UpdatedByUID int64
	UpdatedBy    string
	CreatedAt    time.Time
}

// ArtifactRuntimeStateStore is intentionally optional instead of widening the
// main Store interface and every lightweight test double.
type ArtifactRuntimeStateStore interface {
	GetArtifactRuntimeState(ctx context.Context, agentUID int64, artifactID, namespace, key string) (*ArtifactRuntimeState, bool, error)
	ListArtifactRuntimeStates(ctx context.Context, agentUID int64, artifactID string, limit int) ([]*ArtifactRuntimeState, error)
	PutArtifactRuntimeState(ctx context.Context, candidate *ArtifactRuntimeState, baseRevision int64) (*ArtifactRuntimeState, *ArtifactRuntimeEvent, error)
	ListArtifactRuntimeEvents(ctx context.Context, agentUID int64, artifactID string, afterEventID int64, limit int) ([]*ArtifactRuntimeEvent, error)
	LatestArtifactRuntimeEventID(ctx context.Context, agentUID int64, artifactID string) (int64, error)
}

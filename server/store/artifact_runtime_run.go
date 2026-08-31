package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrArtifactRuntimeRunNotFound     = errors.New("artifact runtime run not found")
	ErrArtifactRuntimeRunConflict     = errors.New("artifact runtime run conflict")
	ErrArtifactRuntimeDeliveryPending = errors.New("artifact runtime delivery pending")
	ErrArtifactRuntimeEvidenceInvalid = errors.New("artifact runtime result evidence is invalid")
)

// ArtifactRuntimeRun is the durable identity and lifecycle of one persistent
// Artifact Action. The XiaoBa executor run is recorded separately so retries
// can later reuse the same Runtime Run without changing its public identity.
type ArtifactRuntimeRun struct {
	TaskID              string
	TaskRefHash         string
	RunID               string
	ActorUID            int64
	TopicID             string
	AgentUID            int64
	ArtifactID          string
	ArtifactTitle       string
	ArtifactKind        string
	ArtifactURL         string
	PublishVersion      int
	DisplayedVersion    int64
	PreviewNodeID       string
	PreviewConnectionID string
	ActionID            string
	ActionTitle         string
	ActionDescription   string
	InputSchema         json.RawMessage
	Payload             json.RawMessage
	PageContext         json.RawMessage
	CompletionMode      string
	Status              string
	Code                string
	Message             string
	DeliveryClaimed     bool
	DeliveryClientID    string
	Delivered           bool
	ExecutorRunID       string
	ExecutorState       string
	ExecutorFinishedAt  *time.Time
	ResultID            string
	AppliedEventIDs     []int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ExpiresAt           time.Time
	StartedAt           *time.Time
	FinishedAt          *time.Time
}

type ArtifactRuntimeDeliveryClaim struct {
	Run              *ArtifactRuntimeRun
	AlreadyDelivered bool
}

// ArtifactRuntimeRunStore is optional so lightweight Store test doubles do
// not need to implement Runtime 0.2. Every mutation is expected to be
// transactional and terminal Run states must never regress.
type ArtifactRuntimeRunStore interface {
	CreateArtifactRuntimeRun(ctx context.Context, run *ArtifactRuntimeRun) (*ArtifactRuntimeRun, error)
	GetArtifactRuntimeRunByTask(ctx context.Context, taskID string, actorUID int64) (*ArtifactRuntimeRun, bool, error)
	GetArtifactRuntimeRunByRef(ctx context.Context, taskRefHash string, agentUID int64) (*ArtifactRuntimeRun, bool, error)
	GetArtifactRuntimeRun(ctx context.Context, runID string, actorUID, agentUID int64, artifactID string) (*ArtifactRuntimeRun, bool, error)
	ListArtifactRuntimeRuns(ctx context.Context, actorUID, agentUID int64, artifactID string, limit int) ([]*ArtifactRuntimeRun, error)
	ReserveArtifactRuntimeDelivery(ctx context.Context, taskRefHash string, actorUID int64, topicID string, agentUID int64, clientMessageID string) (*ArtifactRuntimeDeliveryClaim, error)
	ConfirmArtifactRuntimeDelivery(ctx context.Context, taskID, taskRefHash, clientMessageID string) (bool, error)
	ReleaseArtifactRuntimeDelivery(ctx context.Context, taskID, taskRefHash, clientMessageID string) error
	FailArtifactRuntimeRun(ctx context.Context, taskID string, actorUID int64, code, message string) (*ArtifactRuntimeRun, *ArtifactRuntimeEvent, error)
	ObserveArtifactRuntimeExecutor(ctx context.Context, taskRefHash string, agentUID int64, topicID, executorRunID, executorState, executorError string) (*ArtifactRuntimeRun, *ArtifactRuntimeEvent, error)
	PutArtifactRuntimeStateForRun(ctx context.Context, candidate *ArtifactRuntimeState, baseRevision int64, taskRefHash string) (*ArtifactRuntimeState, *ArtifactRuntimeEvent, error)
	CompleteArtifactRuntimeRun(ctx context.Context, taskRefHash string, agentUID int64, resultID string, appliedEventIDs []int64) (*ArtifactRuntimeRun, []*ArtifactRuntimeEvent, error)
	ConvergeArtifactRuntimeRuns(ctx context.Context, now time.Time, resultGrace time.Duration, limit int) (int, error)
}

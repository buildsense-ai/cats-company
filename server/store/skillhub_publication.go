package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

var (
	ErrSkillHubPublicationInvalid   = errors.New("invalid SkillHub publication entry")
	ErrSkillHubPublicationDenied    = errors.New("SkillHub publication authority denied")
	ErrSkillHubPublicationConflict  = errors.New("SkillHub publication request conflicts with existing entry")
	ErrSkillHubPublicationCapacity  = errors.New("SkillHub publication entry capacity reached")
	ErrSkillHubPublicationRateLimit = errors.New("SkillHub publication entry rate limit reached")
)

var publicationRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var publicationReference = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func ValidSkillHubPublicationRequestID(id string) bool { return publicationRequestID.MatchString(id) }

// SkillHubPublicationSource is a locator, not permission to read files. Only a
// trusted routing layer may construct it after validating the exact Bot/runtime.
// Never put paths, source bytes, credentials or model text in these fields.
type SkillHubPublicationSource struct {
	DeviceID            string `json:"deviceId"`
	BodyID              string `json:"bodyId"`
	InstallationID      string `json:"installationId"`
	RouteGeneration     int64  `json:"routeGeneration"`
	WorkspaceGeneration string `json:"workspaceGeneration"`
	LocalSkillID        string `json:"localSkillId"`
}

// SkillHubPublicationEntryInput is internal, not an HTTP body. ActorUID must
// come from authenticated human context, never a model or browser-supplied UID.
// This first slice accepts only workspace intents; chat needs a trusted turn
// grant and private topic checks before its entrypoint can be implemented.
type SkillHubPublicationEntryInput struct {
	RequestID  string                    `json:"requestId"`
	ActorUID   int64                     `json:"actorUid"`
	BotUID     int64                     `json:"botUid"`
	Entrypoint string                    `json:"entrypoint"`
	Source     SkillHubPublicationSource `json:"source"`
}

func (in SkillHubPublicationEntryInput) Validate() error {
	if in.ActorUID <= 0 || in.BotUID <= 0 || in.ActorUID == in.BotUID ||
		!publicationRequestID.MatchString(in.RequestID) ||
		in.Entrypoint != "workspace" || in.Source.RouteGeneration <= 0 {
		return ErrSkillHubPublicationInvalid
	}
	for _, value := range []string{in.Source.DeviceID, in.Source.BodyID, in.Source.InstallationID,
		in.Source.WorkspaceGeneration, in.Source.LocalSkillID} {
		if !publicationReference.MatchString(value) {
			return ErrSkillHubPublicationInvalid
		}
	}
	return nil
}

// IdentityHash compares exact immutable intent facts, NOT file contents. It is
// neither Cloud's candidateHash nor the legacy contentHash, and grants no rights.
func (in SkillHubPublicationEntryInput) IdentityHash() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("catsco-publication-entry-v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

type SkillHubPublicationEntry struct {
	ID           string
	Input        SkillHubPublicationEntryInput
	IdentityHash string
	State        string
	CreatedAt    time.Time
}

type SkillHubPublicationEntryPolicy struct {
	MaxEntries          int
	MaxEntriesPerActor  int
	MaxCreatesPerMinute int
}

func DefaultSkillHubPublicationEntryPolicy() SkillHubPublicationEntryPolicy {
	return SkillHubPublicationEntryPolicy{MaxEntries: 10000, MaxEntriesPerActor: 100, MaxCreatesPerMinute: 20}
}

func (p SkillHubPublicationEntryPolicy) Validate() error {
	if p.MaxEntries < 1 || p.MaxEntries > 100000 || p.MaxEntriesPerActor < 1 ||
		p.MaxEntriesPerActor > p.MaxEntries || p.MaxEntriesPerActor > 1000 ||
		p.MaxCreatesPerMinute < 1 || p.MaxCreatesPerMinute > 600 {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// SkillHubPublicationEntryStore is optional. Default-off workspace admission and
// status reads use it; opt-in source probing and capture scheduling use separate
// durable leases. Upload delivery remains separate work. Unsupported
// adapters must fail closed, never use an in-memory substitute. Creation saves
// identity, replay evidence and an inert capture event atomically, with current
// human/owner/body checks in the same transaction.
// Implementers must also bound database waits; caller deadlines still apply.
type SkillHubPublicationEntryStore interface {
	CreateSkillHubPublicationEntry(context.Context, SkillHubPublicationEntryInput, SkillHubPublicationEntryPolicy) (*SkillHubPublicationEntry, bool, error)
	GetSkillHubPublicationEntry(context.Context, int64, string) (*SkillHubPublicationEntry, error)
}

// SkillHubPublicationEntryRequestReader recovers a committed intent when its
// response/entry ID was lost. Like ID reads, this is original-publisher history,
// not renewed authority, and requires an active human on every read.
type SkillHubPublicationEntryRequestReader interface {
	GetSkillHubPublicationEntryByRequest(context.Context, int64, string) (*SkillHubPublicationEntry, error)
}

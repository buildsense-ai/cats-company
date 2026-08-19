package server

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const defaultSkillMutationCoordinatorLeaseTTL = 2 * time.Minute

var (
	errSkillMutationCoordinatorUnavailable = errors.New("skill mutation coordinator is unavailable")
	errSkillMutationCandidateRequired      = errors.New("skill mutation candidate is required")
	errSkillMutationAlreadyTerminal        = errors.New("skill mutation is already terminal")
	errSkillMutationRecoveryRequired       = errors.New("skill mutation recovery is required")
	errSkillMutationPersistenceFailed      = errors.New("skill mutation persistence failed")
	errSkillMutationVersionFactsInvalid    = errors.New("skill mutation version facts invalid")
)

// skillMutationPublicError keeps the externally visible error text stable
// while retaining the underlying cause for errors.Is-based recovery logic.
// Writer and database errors can contain paths, URLs, or response bodies.
type skillMutationPublicError struct {
	message string
	cause   error
}

func (e *skillMutationPublicError) Error() string {
	if e == nil || e.message == "" {
		return "skill mutation failed"
	}
	return e.message
}

func (e *skillMutationPublicError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func safeSkillMutationError(message string, cause error) error {
	return &skillMutationPublicError{message: message, cause: cause}
}

func skillMutationPersistenceError(cause error) error {
	return safeSkillMutationError(
		errSkillMutationPersistenceFailed.Error(),
		errors.Join(errSkillMutationPersistenceFailed, cause),
	)
}

type skillMutationVersionWriteRequest struct {
	MutationID           int64
	GrantID              string
	BotUID               int64
	LocalSkillID         string
	ActorUserUID         int64
	SourceTopicID        string
	SourceMessageID      int64
	RuntimeBodyID        string
	ClientRequestID      string
	Operation            types.BotSkillMutationOperation
	CandidateContentHash string
	CandidateSizeBytes   int64
	ExpectedPreviousHash string
	BeforeReference      *types.BotSkillRef
	Candidate            io.Reader
}

type skillMutationVersionWriteResult struct {
	AfterReference types.BotSkillRef
	GitCommitSHA   string
}

// skillMutationVersionWriter is the C2 boundary. Implementations must make
// MutationID idempotent and validate the candidate against the expected hash
// and size before returning immutable SkillHub version facts.
type skillMutationVersionWriter interface {
	WriteBotPrivateSkillVersion(context.Context, skillMutationVersionWriteRequest) (skillMutationVersionWriteResult, error)
}

type skillMutationCoordinatorStore interface {
	store.BotSkillMutationStore
}

type skillMutationCoordinationResult struct {
	Mutation   *types.BotSkillMutation
	Definition *types.BotDefinitionRecord
}

// skillMutationCoordinator is deliberately not registered as an HTTP or model
// entrypoint yet. C2 and the Runtime activation path can be added behind this
// orchestration boundary without changing first-phase SkillHub behavior.
type skillMutationCoordinator struct {
	mutations skillMutationCoordinatorStore
	grants    *skillMutationGrantSigner
	versions  skillMutationVersionWriter
	now       func() time.Time
	leaseTTL  time.Duration
}

func newSkillMutationCoordinator(
	mutations skillMutationCoordinatorStore,
	grants *skillMutationGrantSigner,
	versions skillMutationVersionWriter,
	now func() time.Time,
) *skillMutationCoordinator {
	if now == nil {
		now = time.Now
	}
	return &skillMutationCoordinator{
		mutations: mutations,
		grants:    grants,
		versions:  versions,
		now:       now,
		leaseTTL:  defaultSkillMutationCoordinatorLeaseTTL,
	}
}

func (c *skillMutationCoordinator) Coordinate(
	ctx context.Context,
	rawGrant string,
	candidate io.Reader,
) (*skillMutationCoordinationResult, error) {
	if c == nil || c.mutations == nil || c.grants == nil || c.versions == nil || c.now == nil {
		return nil, errSkillMutationCoordinatorUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	claims, err := c.grants.verify(rawGrant)
	if err != nil {
		return nil, safeSkillMutationError("invalid skill mutation grant", err)
	}
	if candidate == nil {
		return nil, errSkillMutationCandidateRequired
	}
	mutation, _, err := c.mutations.BeginBotSkillMutation(
		claims.mutationInput(), c.now().UTC(), c.leaseTTL,
	)
	if err != nil {
		return nil, skillMutationPersistenceError(err)
	}
	if !mutationMatchesGrant(mutation, claims) {
		return nil, skillMutationPersistenceError(store.ErrBotSkillMutationIdempotencyConflict)
	}
	return c.resume(ctx, claims, mutation, candidate)
}

func (c *skillMutationCoordinator) resume(
	ctx context.Context,
	claims *skillMutationGrantClaims,
	mutation *types.BotSkillMutation,
	candidate io.Reader,
) (*skillMutationCoordinationResult, error) {
	if claims == nil || mutation == nil || mutation.ID <= 0 || !mutationMatchesGrant(mutation, claims) {
		return nil, store.ErrBotSkillMutationIdempotencyConflict
	}
	result := &skillMutationCoordinationResult{Mutation: mutation}
	var err error

	if mutation.Status == types.BotSkillMutationValidating {
		version, err := c.versions.WriteBotPrivateSkillVersion(ctx, skillMutationVersionWriteRequest{
			MutationID:           mutation.ID,
			GrantID:              claims.ID,
			BotUID:               claims.BotUID,
			LocalSkillID:         claims.LocalSkillID,
			ActorUserUID:         claims.ActorUserUID,
			SourceTopicID:        claims.SourceTopicID,
			SourceMessageID:      claims.SourceMessageID,
			RuntimeBodyID:        claims.RuntimeBodyID,
			ClientRequestID:      claims.ClientRequestID,
			Operation:            claims.Operation,
			CandidateContentHash: claims.CandidateContentHash,
			CandidateSizeBytes:   claims.CandidateSizeBytes,
			ExpectedPreviousHash: claims.ExpectedPreviousHash,
			BeforeReference:      claims.BeforeReference,
			Candidate:            candidate,
		})
		if err != nil {
			return c.rejectVersionWrite(mutation, err)
		}
		transition, factsErr := validateSkillMutationVersionFacts(claims, mutation, version)
		if factsErr != nil {
			return c.rejectMutation(
				mutation, types.BotSkillMutationValidating,
				"version_facts_invalid", "SkillHub returned invalid version facts", factsErr,
			)
		}
		advanced, advanceErr := c.mutations.AdvanceBotSkillMutation(
			mutation.BotUID, mutation.ID, mutation.LeaseGeneration,
			types.BotSkillMutationValidating, types.BotSkillMutationVersionReady,
			transition,
			c.now().UTC(), c.leaseTTL,
		)
		if advanceErr != nil {
			return result, skillMutationPersistenceError(advanceErr)
		}
		if advanced == nil || advanced.ID != mutation.ID || advanced.Status != types.BotSkillMutationVersionReady {
			return result, skillMutationPersistenceError(store.ErrBotSkillMutationStateConflict)
		}
		mutation = advanced
		result.Mutation = mutation
	}

	if mutation.Status == types.BotSkillMutationVersionReady {
		committed, definition, commitErr := c.mutations.CommitBotSkillMutationDefinition(
			mutation.BotUID, mutation.ID, mutation.LeaseGeneration,
			c.now().UTC(), c.leaseTTL,
		)
		if commitErr != nil {
			if errors.Is(commitErr, store.ErrBotSkillMutationDefinitionStale) ||
				errors.Is(commitErr, store.ErrBotSkillMutationVersionFactsConflict) {
				return c.rejectMutation(
					mutation, types.BotSkillMutationVersionReady,
					"workspace_stale", "BotDefinition no longer matches the Skill mutation base", commitErr,
				)
			}
			return result, skillMutationPersistenceError(commitErr)
		}
		if committed == nil || committed.ID != mutation.ID || committed.Status != types.BotSkillMutationDefinitionCommitted || definition == nil {
			return result, skillMutationPersistenceError(store.ErrBotSkillMutationStateConflict)
		}
		mutation = committed
		result.Mutation = mutation
		result.Definition = definition
	}

	if mutation.Status == types.BotSkillMutationDefinitionCommitted {
		mutation, err = c.mutations.AdvanceBotSkillMutation(
			mutation.BotUID, mutation.ID, mutation.LeaseGeneration,
			types.BotSkillMutationDefinitionCommitted, types.BotSkillMutationActivationPending,
			types.BotSkillMutationTransition{},
			c.now().UTC(), c.leaseTTL,
		)
		if err != nil {
			return result, skillMutationPersistenceError(err)
		}
		if mutation == nil || mutation.ID <= 0 || mutation.Status != types.BotSkillMutationActivationPending {
			return result, skillMutationPersistenceError(store.ErrBotSkillMutationStateConflict)
		}
		result.Mutation = mutation
	}

	switch mutation.Status {
	case types.BotSkillMutationActivationPending, types.BotSkillMutationActive:
		return result, nil
	case types.BotSkillMutationRejected, types.BotSkillMutationRolledBack:
		return result, errSkillMutationAlreadyTerminal
	case types.BotSkillMutationCompensationPending:
		return result, errSkillMutationRecoveryRequired
	default:
		return result, store.ErrBotSkillMutationStateConflict
	}
}

func (c *skillMutationCoordinator) rejectVersionWrite(
	mutation *types.BotSkillMutation,
	writeErr error,
) (*skillMutationCoordinationResult, error) {
	return c.rejectMutation(
		mutation, types.BotSkillMutationValidating,
		"version_write_failed", "Skill private version could not be created", writeErr,
	)
}

func (c *skillMutationCoordinator) rejectMutation(
	mutation *types.BotSkillMutation,
	expected types.BotSkillMutationStatus,
	code, summary string,
	cause error,
) (*skillMutationCoordinationResult, error) {
	rejected, transitionErr := c.mutations.AdvanceBotSkillMutation(
		mutation.BotUID, mutation.ID, mutation.LeaseGeneration,
		expected, types.BotSkillMutationRejected,
		types.BotSkillMutationTransition{ErrorCode: &code, ErrorSummary: &summary},
		c.now().UTC(), c.leaseTTL,
	)
	if transitionErr != nil {
		return &skillMutationCoordinationResult{Mutation: mutation}, safeSkillMutationError(summary, errors.Join(cause, transitionErr))
	}
	return &skillMutationCoordinationResult{Mutation: rejected}, safeSkillMutationError(summary, cause)
}

func mutationMatchesGrant(mutation *types.BotSkillMutation, claims *skillMutationGrantClaims) bool {
	if mutation == nil || claims == nil || mutation.ID <= 0 {
		return false
	}
	return mutation.BotUID == claims.BotUID &&
		mutation.LocalSkillID == claims.LocalSkillID &&
		mutation.ActorUserUID == claims.ActorUserUID &&
		mutation.SourceTopicID == claims.SourceTopicID &&
		mutation.SourceMessageID == claims.SourceMessageID &&
		mutation.RuntimeBodyID == claims.RuntimeBodyID &&
		mutation.ClientRequestID == claims.ClientRequestID &&
		mutation.Operation == claims.Operation &&
		mutation.CandidateContentHash == claims.CandidateContentHash &&
		mutation.ExpectedDefinitionRevision == claims.ExpectedDefinitionRevision &&
		mutation.ExpectedPreviousContentHash == claims.ExpectedPreviousHash &&
		sameSkillReference(mutation.BeforeReference, claims.BeforeReference)
}

func sameSkillReference(left, right *types.BotSkillRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateSkillMutationVersionFacts(
	claims *skillMutationGrantClaims,
	mutation *types.BotSkillMutation,
	version skillMutationVersionWriteResult,
) (types.BotSkillMutationTransition, error) {
	if claims == nil || mutation == nil {
		return types.BotSkillMutationTransition{}, errSkillMutationVersionFactsInvalid
	}
	transition, err := store.NormalizeBotSkillMutationTransition(
		types.BotSkillMutationVersionReady,
		types.BotSkillMutationTransition{
			AfterReference: &version.AfterReference,
			GitCommitSHA:   &version.GitCommitSHA,
		},
	)
	if err != nil {
		return types.BotSkillMutationTransition{}, errors.Join(errSkillMutationVersionFactsInvalid, err)
	}
	after := *transition.AfterReference
	if after.Source != "skillhub" || after.SkillID != claims.LocalSkillID ||
		after.ContentHash != claims.CandidateContentHash ||
		mutation.Operation != claims.Operation {
		return types.BotSkillMutationTransition{}, errSkillMutationVersionFactsInvalid
	}
	if claims.Operation == types.BotSkillMutationReplace &&
		(mutation.BeforeReference == nil || after.SkillID != mutation.BeforeReference.SkillID || after.Source != mutation.BeforeReference.Source) {
		return types.BotSkillMutationTransition{}, errSkillMutationVersionFactsInvalid
	}
	return transition, nil
}

package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type coordinatorMutationStore struct {
	mutation         *types.BotSkillMutation
	definition       *types.BotDefinitionRecord
	calls            []string
	commitErr        error
	advanceErr       error
	beginResultNil   bool
	advanceResultNil bool
	commitResultNil  bool
	beginErr         error
}

func (s *coordinatorMutationStore) BeginBotSkillMutation(
	input types.BotSkillMutationCreateInput,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, bool, error) {
	s.calls = append(s.calls, "begin")
	if s.beginErr != nil {
		return nil, false, s.beginErr
	}
	if s.mutation != nil {
		return cloneCoordinatorMutation(s.mutation), false, nil
	}
	s.mutation = &types.BotSkillMutation{
		ID: 101, BotUID: input.BotUID, LocalSkillID: input.LocalSkillID,
		ActorUserUID: input.ActorUserUID, SourceTopicID: input.SourceTopicID,
		SourceMessageID: input.SourceMessageID, RuntimeBodyID: input.RuntimeBodyID,
		ClientRequestID: input.ClientRequestID, Operation: input.Operation,
		CandidateContentHash:        input.CandidateContentHash,
		ExpectedDefinitionRevision:  input.ExpectedDefinitionRevision,
		ExpectedPreviousContentHash: input.ExpectedPreviousContentHash,
		BeforeReference:             input.BeforeReference, Status: types.BotSkillMutationValidating,
		LeaseGeneration: 1, LeaseExpiresAt: now.Add(leaseTTL), CreatedAt: now, UpdatedAt: now,
	}
	if s.beginResultNil {
		return nil, false, nil
	}
	return cloneCoordinatorMutation(s.mutation), true, nil
}

func (s *coordinatorMutationStore) GetBotSkillMutation(botUID, mutationID int64) (*types.BotSkillMutation, error) {
	s.calls = append(s.calls, "get")
	if s.mutation == nil || s.mutation.BotUID != botUID || s.mutation.ID != mutationID {
		return nil, store.ErrBotSkillMutationNotFound
	}
	return cloneCoordinatorMutation(s.mutation), nil
}

func (s *coordinatorMutationStore) GetBotSkillMutationByRequest(
	input types.BotSkillMutationCreateInput,
) (*types.BotSkillMutation, error) {
	s.calls = append(s.calls, "get_by_request")
	if s.mutation == nil || s.mutation.BotUID != input.BotUID ||
		s.mutation.ActorUserUID != input.ActorUserUID || s.mutation.ClientRequestID != input.ClientRequestID {
		return nil, store.ErrBotSkillMutationNotFound
	}
	return cloneCoordinatorMutation(s.mutation), nil
}

func (s *coordinatorMutationStore) AdvanceBotSkillMutation(
	botUID, mutationID, expectedLeaseGeneration int64,
	expected, next types.BotSkillMutationStatus,
	patch types.BotSkillMutationTransition,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, error) {
	s.calls = append(s.calls, string(expected)+"->"+string(next))
	if s.mutation == nil || s.mutation.BotUID != botUID || s.mutation.ID != mutationID ||
		s.mutation.LeaseGeneration != expectedLeaseGeneration || s.mutation.Status != expected {
		return nil, store.ErrBotSkillMutationStateConflict
	}
	patch, err := store.NormalizeBotSkillMutationTransition(next, patch)
	if err != nil {
		return nil, err
	}
	if s.advanceErr != nil {
		return nil, s.advanceErr
	}
	s.mutation.Status = next
	s.mutation.UpdatedAt = now
	s.mutation.LeaseExpiresAt = now.Add(leaseTTL)
	if patch.AfterReference != nil {
		ref := *patch.AfterReference
		s.mutation.AfterReference = &ref
	}
	if patch.GitCommitSHA != nil {
		s.mutation.GitCommitSHA = *patch.GitCommitSHA
	}
	if patch.ErrorCode != nil {
		s.mutation.ErrorCode = *patch.ErrorCode
	}
	if patch.ErrorSummary != nil {
		s.mutation.ErrorSummary = *patch.ErrorSummary
	}
	if s.advanceResultNil {
		return nil, nil
	}
	return cloneCoordinatorMutation(s.mutation), nil
}

func (s *coordinatorMutationStore) CommitBotSkillMutationDefinition(
	botUID, mutationID, expectedLeaseGeneration int64,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, *types.BotDefinitionRecord, error) {
	s.calls = append(s.calls, "commit_definition")
	if s.commitErr != nil {
		return nil, nil, s.commitErr
	}
	if s.mutation == nil || s.mutation.Status != types.BotSkillMutationVersionReady ||
		s.mutation.BotUID != botUID || s.mutation.ID != mutationID ||
		s.mutation.LeaseGeneration != expectedLeaseGeneration {
		return nil, nil, store.ErrBotSkillMutationStateConflict
	}
	revision := s.mutation.ExpectedDefinitionRevision + 1
	s.mutation.Status = types.BotSkillMutationDefinitionCommitted
	s.mutation.DefinitionRevision = &revision
	s.mutation.UpdatedAt = now
	s.mutation.LeaseExpiresAt = now.Add(leaseTTL)
	if s.definition == nil {
		s.definition = &types.BotDefinitionRecord{
			Definition: types.BotDefinition{Skills: []types.BotSkillRef{*s.mutation.AfterReference}},
			Runtime:    types.BotDefinitionRuntime{DesiredRevision: revision}, Exists: true,
		}
	}
	if s.commitResultNil {
		return nil, nil, nil
	}
	return cloneCoordinatorMutation(s.mutation), s.definition, nil
}

func (s *coordinatorMutationStore) RenewBotSkillMutationLease(
	botUID, mutationID, expectedLeaseGeneration int64,
	expected types.BotSkillMutationStatus,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, error) {
	s.calls = append(s.calls, "renew")
	if s.mutation == nil || s.mutation.BotUID != botUID || s.mutation.ID != mutationID ||
		s.mutation.LeaseGeneration != expectedLeaseGeneration || s.mutation.Status != expected {
		return nil, store.ErrBotSkillMutationStateConflict
	}
	if !s.mutation.LeaseExpiresAt.After(now) {
		return nil, store.ErrBotSkillMutationLeaseExpired
	}
	s.mutation.LeaseGeneration++
	s.mutation.LeaseExpiresAt = now.Add(leaseTTL)
	s.mutation.UpdatedAt = now
	return cloneCoordinatorMutation(s.mutation), nil
}

func (s *coordinatorMutationStore) RecoverBotSkillMutationLease(
	botUID, mutationID, expectedLeaseGeneration int64,
	expected types.BotSkillMutationStatus,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, error) {
	s.calls = append(s.calls, "recover")
	if s.mutation == nil || s.mutation.BotUID != botUID || s.mutation.ID != mutationID ||
		s.mutation.LeaseGeneration != expectedLeaseGeneration || s.mutation.Status != expected {
		return nil, store.ErrBotSkillMutationStateConflict
	}
	if s.mutation.LeaseExpiresAt.After(now) {
		return nil, store.ErrBotSkillMutationBusy
	}
	s.mutation.LeaseGeneration++
	s.mutation.LeaseExpiresAt = now.Add(leaseTTL)
	s.mutation.UpdatedAt = now
	return cloneCoordinatorMutation(s.mutation), nil
}

type coordinatorVersionWriter struct {
	request     *skillMutationVersionWriteRequest
	mutationIDs []int64
	result      skillMutationVersionWriteResult
	err         error
	errors      []error
	calls       int
}

func (w *coordinatorVersionWriter) WriteBotPrivateSkillVersion(
	_ context.Context,
	request skillMutationVersionWriteRequest,
) (skillMutationVersionWriteResult, error) {
	w.calls++
	w.request = &request
	w.mutationIDs = append(w.mutationIDs, request.MutationID)
	if len(w.errors) > 0 {
		err := w.errors[0]
		w.errors = w.errors[1:]
		return w.result, err
	}
	return w.result, w.err
}

func TestSkillMutationCoordinatorOrchestratesVersionDefinitionAndActivationPending(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, claims := coordinatorGrant(t, signer)
	ref := types.BotSkillRef{
		Source: "skillhub", SkillID: claims.LocalSkillID, Version: "1.0.1",
		ContentHash: claims.CandidateContentHash,
	}
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorVersionWriter{result: skillMutationVersionWriteResult{
		AfterReference: ref,
		GitCommitSHA:   strings.Repeat("e", 40),
	}}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if err != nil {
		t.Fatalf("coordinate mutation: %v", err)
	}
	if result.Mutation.Status != types.BotSkillMutationActivationPending ||
		result.Mutation.AfterReference == nil || *result.Mutation.AfterReference != ref ||
		result.Definition == nil || result.Definition.Runtime.DesiredRevision != 11 {
		t.Fatalf("unexpected result: %#v", result)
	}
	wantCalls := []string{
		"begin", "renew", "validating->version_ready", "renew",
		"commit_definition", "renew", "definition_committed->activation_pending",
	}
	if strings.Join(mutations.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls=%v, want %v", mutations.calls, wantCalls)
	}
	if versions.calls != 1 || versions.request == nil ||
		versions.request.MutationID != 101 || versions.request.GrantID != claims.ID ||
		versions.request.ActorUserUID != claims.ActorUserUID ||
		versions.request.RuntimeBodyID != claims.RuntimeBodyID ||
		versions.request.CandidateSizeBytes != claims.CandidateSizeBytes {
		t.Fatalf("version request was not built from grant claims: %#v", versions.request)
	}
	if content, _ := io.ReadAll(versions.request.Candidate); string(content) != "candidate" {
		t.Fatalf("candidate content=%q", content)
	}
}

func TestSkillMutationCoordinatorResumesWithoutRepeatingVersionWrite(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, claims := coordinatorGrant(t, signer)
	mutations := &coordinatorMutationStore{mutation: &types.BotSkillMutation{
		ID: 101, BotUID: claims.BotUID, LocalSkillID: claims.LocalSkillID,
		ActorUserUID: claims.ActorUserUID, SourceTopicID: claims.SourceTopicID,
		SourceMessageID: claims.SourceMessageID, RuntimeBodyID: claims.RuntimeBodyID,
		ClientRequestID: claims.ClientRequestID, Operation: claims.Operation,
		CandidateContentHash:        claims.CandidateContentHash,
		ExpectedDefinitionRevision:  claims.ExpectedDefinitionRevision,
		ExpectedPreviousContentHash: claims.ExpectedPreviousHash,
		Status:                      types.BotSkillMutationActivationPending, LeaseGeneration: 1,
	}}
	versions := &coordinatorVersionWriter{err: errors.New("must not be called")}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if err != nil || result.Mutation.Status != types.BotSkillMutationActivationPending {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if versions.calls != 0 || strings.Join(mutations.calls, ",") != "begin" {
		t.Fatalf("calls=%v writerCalls=%d", mutations.calls, versions.calls)
	}
}

func TestSkillMutationCoordinatorResumesVersionReadyWithoutRepeatingVersionWrite(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, claims := coordinatorGrant(t, signer)
	ref := types.BotSkillRef{
		Source: "skillhub", SkillID: claims.LocalSkillID, Version: "1.0.1",
		ContentHash: claims.CandidateContentHash,
	}
	mutations := &coordinatorMutationStore{mutation: &types.BotSkillMutation{
		ID: 101, BotUID: claims.BotUID, LocalSkillID: claims.LocalSkillID,
		ActorUserUID: claims.ActorUserUID, SourceTopicID: claims.SourceTopicID,
		SourceMessageID: claims.SourceMessageID, RuntimeBodyID: claims.RuntimeBodyID,
		ClientRequestID: claims.ClientRequestID, Operation: claims.Operation,
		CandidateContentHash:       claims.CandidateContentHash,
		ExpectedDefinitionRevision: 10, AfterReference: &ref,
		ExpectedPreviousContentHash: claims.ExpectedPreviousHash,
		GitCommitSHA:                strings.Repeat("e", 40),
		Status:                      types.BotSkillMutationVersionReady, LeaseGeneration: 1,
		LeaseExpiresAt: now.Add(2 * time.Minute),
	}}
	versions := &coordinatorVersionWriter{err: errors.New("must not be called")}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if err != nil || result.Mutation.Status != types.BotSkillMutationActivationPending ||
		result.Definition == nil || result.Definition.Runtime.DesiredRevision != 11 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if versions.calls != 0 {
		t.Fatalf("writerCalls=%d, want 0", versions.calls)
	}
	want := "begin,renew,commit_definition,renew,definition_committed->activation_pending"
	if strings.Join(mutations.calls, ",") != want {
		t.Fatalf("calls=%v, want %s", mutations.calls, want)
	}
}

func TestSkillMutationCoordinatorRequiresCandidateBeforePersistence(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, _ := coordinatorGrant(t, signer)
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorVersionWriter{}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	if _, err := coordinator.Coordinate(context.Background(), rawGrant, nil); !errors.Is(err, errSkillMutationCandidateRequired) {
		t.Fatalf("err=%v, want candidate required", err)
	}
	if len(mutations.calls) != 0 || versions.calls != 0 {
		t.Fatalf("missing candidate reached side effects: store=%v writer=%d", mutations.calls, versions.calls)
	}
}

func TestSkillMutationCoordinatorKeepsUncertainVersionWritePendingWithoutDefinitionChange(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, _ := coordinatorGrant(t, signer)
	writeErr := errors.New("skillhub unavailable")
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorVersionWriter{err: writeErr}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if !errors.Is(err, writeErr) || result.Mutation.Status != types.BotSkillMutationValidating ||
		result.Mutation.ErrorCode != "" || result.Definition != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if strings.Join(mutations.calls, ",") != "begin,renew" {
		t.Fatalf("unexpected calls: %v", mutations.calls)
	}
}

func TestSkillMutationCoordinatorRetriesUncertainVersionWriteAfterLeaseRecovery(t *testing.T) {
	issuedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	current := issuedAt
	signer := coordinatorGrantSigner(t, current)
	rawGrant, claims := coordinatorGrant(t, signer)
	ref := types.BotSkillRef{
		Source: "skillhub", SkillID: claims.LocalSkillID, Version: "1.0.1",
		ContentHash: claims.CandidateContentHash,
	}
	writeErr := errors.New("response lost after remote commit")
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorVersionWriter{
		result: skillMutationVersionWriteResult{AfterReference: ref, GitCommitSHA: strings.Repeat("e", 40)},
		errors: []error{writeErr},
	}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return current })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if !errors.Is(err, writeErr) || result.Mutation.Status != types.BotSkillMutationValidating {
		t.Fatalf("first result=%#v err=%v", result, err)
	}
	current = issuedAt.Add(2*time.Minute + 6*time.Second)
	result, err = coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if err != nil || result.Mutation.Status != types.BotSkillMutationActivationPending || result.Definition == nil {
		t.Fatalf("retry result=%#v err=%v", result, err)
	}
	if versions.calls != 2 || len(versions.mutationIDs) != 2 || versions.mutationIDs[0] != versions.mutationIDs[1] {
		t.Fatalf("writer calls=%d mutationIDs=%v", versions.calls, versions.mutationIDs)
	}
	if !strings.Contains(strings.Join(mutations.calls, ","), "recover") {
		t.Fatalf("expired retry did not use recovery path: %v", mutations.calls)
	}
}

func TestSkillMutationCoordinatorRecoversExpiredLeaseWithValidGrant(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, claims := coordinatorGrant(t, signer)
	ref := types.BotSkillRef{Source: "skillhub", SkillID: claims.LocalSkillID, Version: "1.0.1", ContentHash: claims.CandidateContentHash}
	mutations := &coordinatorMutationStore{
		beginErr: store.ErrBotSkillMutationRecoveryRequired,
		mutation: &types.BotSkillMutation{
			ID: 101, BotUID: claims.BotUID, LocalSkillID: claims.LocalSkillID,
			ActorUserUID: claims.ActorUserUID, SourceTopicID: claims.SourceTopicID,
			SourceMessageID: claims.SourceMessageID, RuntimeBodyID: claims.RuntimeBodyID,
			ClientRequestID: claims.ClientRequestID, Operation: claims.Operation,
			CandidateContentHash:       claims.CandidateContentHash,
			ExpectedDefinitionRevision: claims.ExpectedDefinitionRevision,
			Status:                     types.BotSkillMutationValidating, LeaseGeneration: 1,
			LeaseExpiresAt: now.Add(-time.Second),
		},
	}
	versions := &coordinatorVersionWriter{result: skillMutationVersionWriteResult{AfterReference: ref, GitCommitSHA: strings.Repeat("e", 40)}}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if err != nil || result.Mutation.Status != types.BotSkillMutationActivationPending {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !strings.Contains(strings.Join(mutations.calls, ","), "get_by_request") || versions.calls != 1 {
		t.Fatalf("calls=%v writerCalls=%d", mutations.calls, versions.calls)
	}
}

type coordinatorDeadlineWriter struct {
	remaining time.Duration
}

func (w *coordinatorDeadlineWriter) WriteBotPrivateSkillVersion(
	ctx context.Context,
	_ skillMutationVersionWriteRequest,
) (skillMutationVersionWriteResult, error) {
	var ok bool
	deadline, ok := ctx.Deadline()
	if !ok {
		return skillMutationVersionWriteResult{}, errors.New("writer deadline missing")
	}
	w.remaining = time.Until(deadline)
	return skillMutationVersionWriteResult{}, context.DeadlineExceeded
}

func TestSkillMutationCoordinatorBoundsVersionWriterByLease(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, _ := coordinatorGrant(t, signer)
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorDeadlineWriter{}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if !errors.Is(err, context.DeadlineExceeded) || result.Mutation.Status != types.BotSkillMutationValidating {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if versions.remaining < 100*time.Second || versions.remaining > 110*time.Second {
		t.Fatalf("writer budget=%s, want about 105s", versions.remaining)
	}
}

func TestSkillMutationCoordinatorRejectsInvalidVersionFactsWithoutDefinitionChange(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, claims := coordinatorGrant(t, signer)
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorVersionWriter{result: skillMutationVersionWriteResult{
		AfterReference: types.BotSkillRef{
			Source: "skillhub", SkillID: claims.LocalSkillID, Version: "",
			ContentHash: claims.CandidateContentHash,
		},
		GitCommitSHA: strings.Repeat("e", 40),
	}}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if err == nil || result.Mutation.Status != types.BotSkillMutationRejected ||
		result.Mutation.ErrorCode != "version_facts_invalid" || result.Definition != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	want := "begin,renew,validating->rejected"
	if strings.Join(mutations.calls, ",") != want {
		t.Fatalf("calls=%v, want %s", mutations.calls, want)
	}
}

func TestSkillMutationCoordinatorRejectsInvalidGrantBeforePersistence(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorVersionWriter{}
	coordinator := newSkillMutationCoordinator(
		mutations, coordinatorGrantSigner(t, now), versions, func() time.Time { return now },
	)

	if _, err := coordinator.Coordinate(context.Background(), "forged", strings.NewReader("candidate")); err == nil {
		t.Fatal("forged grant must fail")
	}
	if len(mutations.calls) != 0 || versions.calls != 0 {
		t.Fatalf("forged grant reached side effects: store=%v writer=%d", mutations.calls, versions.calls)
	}
}

func TestSkillMutationCoordinatorRedactsVersionWriterError(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, _ := coordinatorGrant(t, signer)
	writeErr := errors.New(`upload https://skillhub.internal/private?token=secret failed for C:\private\candidate.zip`)
	mutations := &coordinatorMutationStore{}
	versions := &coordinatorVersionWriter{err: writeErr}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if !errors.Is(err, writeErr) || result.Mutation.Status != types.BotSkillMutationValidating {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if strings.Contains(err.Error(), "token=secret") || strings.Contains(err.Error(), "candidate.zip") {
		t.Fatalf("writer details leaked through error: %v", err)
	}
}

func TestSkillMutationCoordinatorRejectsNilBeginResultWithoutPanic(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, _ := coordinatorGrant(t, signer)
	mutations := &coordinatorMutationStore{beginResultNil: true}
	coordinator := newSkillMutationCoordinator(mutations, signer, &coordinatorVersionWriter{}, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if result != nil || !errors.Is(err, errSkillMutationPersistenceFailed) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestSkillMutationCoordinatorKeepsValidatingOnAdvancePersistenceFailure(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, claims := coordinatorGrant(t, signer)
	mutations := &coordinatorMutationStore{advanceErr: errors.New("database temporarily unavailable")}
	versions := &coordinatorVersionWriter{result: skillMutationVersionWriteResult{
		AfterReference: types.BotSkillRef{Source: "skillhub", SkillID: claims.LocalSkillID, Version: "1.0.1", ContentHash: claims.CandidateContentHash},
		GitCommitSHA:   strings.Repeat("e", 40),
	}}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if !errors.Is(err, mutations.advanceErr) || result.Mutation.Status != types.BotSkillMutationValidating {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if strings.Join(mutations.calls, ",") != "begin,renew,validating->version_ready" {
		t.Fatalf("calls=%v", mutations.calls)
	}
}

func TestSkillMutationCoordinatorRejectsStaleDefinitionAfterVersionWrite(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	signer := coordinatorGrantSigner(t, now)
	rawGrant, claims := coordinatorGrant(t, signer)
	mutations := &coordinatorMutationStore{commitErr: store.ErrBotSkillMutationDefinitionStale}
	versions := &coordinatorVersionWriter{result: skillMutationVersionWriteResult{
		AfterReference: types.BotSkillRef{
			Source: "skillhub", SkillID: claims.LocalSkillID, Version: "1.0.1",
			ContentHash: claims.CandidateContentHash,
		},
		GitCommitSHA: strings.Repeat("e", 40),
	}}
	coordinator := newSkillMutationCoordinator(mutations, signer, versions, func() time.Time { return now })

	result, err := coordinator.Coordinate(context.Background(), rawGrant, strings.NewReader("candidate"))
	if !errors.Is(err, store.ErrBotSkillMutationDefinitionStale) ||
		result.Mutation.Status != types.BotSkillMutationRejected ||
		result.Mutation.ErrorCode != "workspace_stale" || result.Definition != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	want := "begin,renew,validating->version_ready,renew,commit_definition,version_ready->rejected"
	if strings.Join(mutations.calls, ",") != want {
		t.Fatalf("calls=%v, want %s", mutations.calls, want)
	}
}

func coordinatorGrantSigner(t *testing.T, now time.Time) *skillMutationGrantSigner {
	t.Helper()
	signer, err := newSkillMutationGrantSigner([]byte(strings.Repeat("s", 48)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func coordinatorGrant(
	t *testing.T,
	signer *skillMutationGrantSigner,
) (string, *skillMutationGrantClaims) {
	return coordinatorGrantWithTTL(t, signer, 0)
}

func coordinatorGrantWithTTL(
	t *testing.T,
	signer *skillMutationGrantSigner,
	ttl time.Duration,
) (string, *skillMutationGrantClaims) {
	t.Helper()
	raw, claims, err := signer.issue(skillMutationGrantInput{
		Mutation: types.BotSkillMutationCreateInput{
			BotUID: 42, LocalSkillID: "review-pr", ActorUserUID: 7,
			SourceTopicID: "p2p_7_42", SourceMessageID: 99,
			RuntimeBodyID: "runtime:cloud-1", ClientRequestID: "request-0001",
			Operation:                  types.BotSkillMutationCreate,
			CandidateContentHash:       strings.Repeat("a", 64),
			ExpectedDefinitionRevision: 10,
		},
		CandidateSizeBytes: 9,
		TTL:                ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw, claims
}

func cloneCoordinatorMutation(input *types.BotSkillMutation) *types.BotSkillMutation {
	if input == nil {
		return nil
	}
	clone := *input
	if input.BeforeReference != nil {
		ref := *input.BeforeReference
		clone.BeforeReference = &ref
	}
	if input.AfterReference != nil {
		ref := *input.AfterReference
		clone.AfterReference = &ref
	}
	if input.DefinitionRevision != nil {
		revision := *input.DefinitionRevision
		clone.DefinitionRevision = &revision
	}
	return &clone
}

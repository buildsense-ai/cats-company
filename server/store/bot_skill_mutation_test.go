package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func validMutationCreateInput() types.BotSkillMutationCreateInput {
	return types.BotSkillMutationCreateInput{
		BotUID:                     42,
		LocalSkillID:               "review-pr",
		ActorUserUID:               7,
		SourceTopicID:              "p2p_7_42",
		SourceMessageID:            99,
		RuntimeBodyID:              "runtime:cloud-1",
		ClientRequestID:            "request-0001",
		Operation:                  types.BotSkillMutationCreate,
		CandidateContentHash:       strings.Repeat("a", 64),
		ExpectedDefinitionRevision: 10,
	}
}

func TestNormalizeBotSkillMutationCreateInputFingerprint(t *testing.T) {
	input := validMutationCreateInput()
	normalized, first, err := NormalizeBotSkillMutationCreateInput(input)
	if err != nil {
		t.Fatalf("normalize mutation: %v", err)
	}
	if normalized.LocalSkillID != input.LocalSkillID || len(first) != 64 {
		t.Fatalf("normalized=%#v fingerprint=%q", normalized, first)
	}
	_, retry, err := NormalizeBotSkillMutationCreateInput(input)
	if err != nil || retry != first {
		t.Fatalf("identical retry fingerprint=%q err=%v, want %q", retry, err, first)
	}
	input.CandidateContentHash = strings.Repeat("b", 64)
	_, changed, err := NormalizeBotSkillMutationCreateInput(input)
	if err != nil || changed == first {
		t.Fatalf("changed payload fingerprint=%q err=%v, must differ from %q", changed, err, first)
	}
}

func TestNormalizeBotSkillMutationCreateInputRequiresPreviousVersionForReplace(t *testing.T) {
	input := validMutationCreateInput()
	input.Operation = types.BotSkillMutationReplace
	if _, _, err := NormalizeBotSkillMutationCreateInput(input); err == nil {
		t.Fatal("replace without previous version must fail")
	}
	input.ExpectedPreviousContentHash = strings.Repeat("c", 64)
	input.BeforeReference = &types.BotSkillRef{
		Source: "skillhub", SkillID: "private-1", Version: "1.0.0",
		ContentHash: input.ExpectedPreviousContentHash,
	}
	if _, _, err := NormalizeBotSkillMutationCreateInput(input); err != nil {
		t.Fatalf("valid replace rejected: %v", err)
	}
}

func TestBotSkillMutationTransitionGraph(t *testing.T) {
	allowed := [][2]types.BotSkillMutationStatus{
		{types.BotSkillMutationValidating, types.BotSkillMutationVersionReady},
		{types.BotSkillMutationValidating, types.BotSkillMutationRejected},
		{types.BotSkillMutationVersionReady, types.BotSkillMutationDefinitionCommitted},
		{types.BotSkillMutationDefinitionCommitted, types.BotSkillMutationActivationPending},
		{types.BotSkillMutationDefinitionCommitted, types.BotSkillMutationCompensationPending},
		{types.BotSkillMutationActivationPending, types.BotSkillMutationActive},
		{types.BotSkillMutationActivationPending, types.BotSkillMutationCompensationPending},
		{types.BotSkillMutationCompensationPending, types.BotSkillMutationRolledBack},
	}
	for _, pair := range allowed {
		if !CanAdvanceBotSkillMutation(pair[0], pair[1]) {
			t.Fatalf("expected transition %s -> %s to be allowed", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]types.BotSkillMutationStatus{
		{types.BotSkillMutationValidating, types.BotSkillMutationActive},
		{types.BotSkillMutationVersionReady, types.BotSkillMutationActivationPending},
		{types.BotSkillMutationActive, types.BotSkillMutationValidating},
		{types.BotSkillMutationRejected, types.BotSkillMutationVersionReady},
	} {
		if CanAdvanceBotSkillMutation(pair[0], pair[1]) {
			t.Fatalf("transition %s -> %s must be rejected", pair[0], pair[1])
		}
	}
}

func TestNormalizeBotSkillMutationTransitionRequiresStageFacts(t *testing.T) {
	if _, err := NormalizeBotSkillMutationTransition(types.BotSkillMutationVersionReady, types.BotSkillMutationTransition{}); err == nil {
		t.Fatal("version_ready without immutable version facts must fail")
	}
	ref := &types.BotSkillRef{
		Source: "skillhub", SkillID: "private-1", Version: "1.0.1",
		ContentHash: strings.Repeat("d", 64),
	}
	commit := strings.Repeat("e", 40)
	if _, err := NormalizeBotSkillMutationTransition(types.BotSkillMutationVersionReady, types.BotSkillMutationTransition{
		AfterReference: ref,
		GitCommitSHA:   &commit,
	}); err != nil {
		t.Fatalf("valid version_ready facts rejected: %v", err)
	}
	if _, err := NormalizeBotSkillMutationTransition(types.BotSkillMutationActive, types.BotSkillMutationTransition{}); err == nil {
		t.Fatal("active without activation timestamp must fail")
	}
}

func TestValidateBotSkillMutationLease(t *testing.T) {
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.FixedZone("test", 8*60*60))
	expires, err := ValidateBotSkillMutationLease(now, 2*time.Minute)
	if err != nil {
		t.Fatalf("valid lease rejected: %v", err)
	}
	if !expires.Equal(now.UTC().Add(2*time.Minute)) || expires.Location() != time.UTC {
		t.Fatalf("expires=%v, want canonical UTC expiry", expires)
	}
	if _, err := ValidateBotSkillMutationLease(now, 11*time.Minute); err == nil {
		t.Fatal("oversized lease must fail")
	}
}

func TestApplyBotSkillMutationDefinitionUsesExactRevisionAndReference(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	before := types.BotSkillRef{Source: "skillhub", SkillID: "review-pr", Version: "1.0.0", ContentHash: strings.Repeat("a", 64)}
	after := types.BotSkillRef{Source: "skillhub", SkillID: "review-pr", Version: "1.0.1", ContentHash: strings.Repeat("b", 64)}
	record := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{Skills: []types.BotSkillRef{before}},
		Runtime:    types.BotDefinitionRuntime{DesiredRevision: 10, LastError: "old failure"},
		Exists:     true,
	}
	mutation := &types.BotSkillMutation{
		Operation:                  types.BotSkillMutationReplace,
		Status:                     types.BotSkillMutationVersionReady,
		CandidateContentHash:       after.ContentHash,
		ExpectedDefinitionRevision: 10,
		BeforeReference:            &before,
		AfterReference:             &after,
	}

	if err := ApplyBotSkillMutationDefinition(record, mutation, now); err != nil {
		t.Fatalf("apply definition mutation: %v", err)
	}
	if record.Runtime.DesiredRevision != 11 || record.Definition.Skills[0] != after || record.Runtime.LastError != "" {
		t.Fatalf("unexpected committed definition: %+v", record)
	}

	staleRecord := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{Skills: []types.BotSkillRef{before}},
		Runtime:    types.BotDefinitionRuntime{DesiredRevision: 11},
	}
	if err := ApplyBotSkillMutationDefinition(staleRecord, mutation, now); !errors.Is(err, ErrBotSkillMutationDefinitionStale) {
		t.Fatalf("stale revision error=%v", err)
	}
	if staleRecord.Definition.Skills[0] != before || staleRecord.Runtime.DesiredRevision != 11 {
		t.Fatal("stale mutation changed the definition")
	}

	missingBase := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{Skills: []types.BotSkillRef{}},
		Runtime:    types.BotDefinitionRuntime{DesiredRevision: 10},
	}
	if err := ApplyBotSkillMutationDefinition(missingBase, mutation, now); !errors.Is(err, ErrBotSkillMutationDefinitionStale) {
		t.Fatalf("missing base error=%v", err)
	}
}

func TestApplyBotSkillMutationDefinitionCreatesWithoutOverwriting(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	after := types.BotSkillRef{Source: "skillhub", SkillID: "new-skill", Version: "1.0.0", ContentHash: strings.Repeat("c", 64)}
	mutation := &types.BotSkillMutation{
		Operation:                  types.BotSkillMutationCreate,
		Status:                     types.BotSkillMutationVersionReady,
		CandidateContentHash:       after.ContentHash,
		ExpectedDefinitionRevision: 4,
		AfterReference:             &after,
	}
	record := &types.BotDefinitionRecord{Runtime: types.BotDefinitionRuntime{DesiredRevision: 4}}
	if err := ApplyBotSkillMutationDefinition(record, mutation, now); err != nil {
		t.Fatalf("create skill reference: %v", err)
	}
	if len(record.Definition.Skills) != 1 || record.Definition.Skills[0] != after || record.Runtime.DesiredRevision != 5 {
		t.Fatalf("unexpected created definition: %+v", record)
	}

	duplicate := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{Skills: []types.BotSkillRef{after}},
		Runtime:    types.BotDefinitionRuntime{DesiredRevision: 4},
	}
	if err := ApplyBotSkillMutationDefinition(duplicate, mutation, now); !errors.Is(err, ErrBotSkillMutationDefinitionStale) {
		t.Fatalf("duplicate create error=%v", err)
	}
}

func TestApplyBotSkillMutationDefinitionRejectsCrossSkillReplacement(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	beforeA := types.BotSkillRef{Source: "skillhub", SkillID: "skill-a", Version: "1.0.0", ContentHash: strings.Repeat("a", 64)}
	beforeB := types.BotSkillRef{Source: "skillhub", SkillID: "skill-b", Version: "1.0.0", ContentHash: strings.Repeat("b", 64)}
	afterB := types.BotSkillRef{Source: "skillhub", SkillID: "skill-b", Version: "2.0.0", ContentHash: strings.Repeat("c", 64)}

	for _, operation := range []types.BotSkillMutationOperation{
		types.BotSkillMutationReplace,
		types.BotSkillMutationRollback,
	} {
		t.Run(string(operation), func(t *testing.T) {
			record := &types.BotDefinitionRecord{
				Definition: types.BotDefinition{Skills: []types.BotSkillRef{beforeA, beforeB}},
				Runtime:    types.BotDefinitionRuntime{DesiredRevision: 10},
			}
			mutation := &types.BotSkillMutation{
				Operation:                  operation,
				Status:                     types.BotSkillMutationVersionReady,
				CandidateContentHash:       afterB.ContentHash,
				ExpectedDefinitionRevision: 10,
				BeforeReference:            &beforeA,
				AfterReference:             &afterB,
			}

			err := ApplyBotSkillMutationDefinition(record, mutation, now)
			if !errors.Is(err, ErrBotSkillMutationVersionFactsConflict) {
				t.Fatalf("error=%v, want version facts conflict", err)
			}
			if record.Runtime.DesiredRevision != 10 ||
				len(record.Definition.Skills) != 2 ||
				record.Definition.Skills[0] != beforeA ||
				record.Definition.Skills[1] != beforeB {
				t.Fatalf("cross-skill replacement changed definition: %+v", record)
			}
		})
	}
}

func TestApplyBotSkillMutationDefinitionRejectsDuplicateTargetSkillID(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	before := types.BotSkillRef{Source: "skillhub", SkillID: "review-pr", Version: "1.0.0", ContentHash: strings.Repeat("a", 64)}
	duplicate := types.BotSkillRef{Source: "skillhub", SkillID: "review-pr", Version: "0.9.0", ContentHash: strings.Repeat("b", 64)}
	after := types.BotSkillRef{Source: "skillhub", SkillID: "review-pr", Version: "1.0.1", ContentHash: strings.Repeat("c", 64)}
	record := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{Skills: []types.BotSkillRef{before, duplicate}},
		Runtime:    types.BotDefinitionRuntime{DesiredRevision: 10},
	}
	mutation := &types.BotSkillMutation{
		Operation:                  types.BotSkillMutationReplace,
		Status:                     types.BotSkillMutationVersionReady,
		CandidateContentHash:       after.ContentHash,
		ExpectedDefinitionRevision: 10,
		BeforeReference:            &before,
		AfterReference:             &after,
	}

	err := ApplyBotSkillMutationDefinition(record, mutation, now)
	if !errors.Is(err, ErrBotSkillMutationDefinitionStale) {
		t.Fatalf("error=%v, want stale definition", err)
	}
	if record.Runtime.DesiredRevision != 10 ||
		len(record.Definition.Skills) != 2 ||
		record.Definition.Skills[0] != before ||
		record.Definition.Skills[1] != duplicate {
		t.Fatalf("duplicate target check changed definition: %+v", record)
	}
}

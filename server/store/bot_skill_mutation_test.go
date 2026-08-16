package store

import (
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

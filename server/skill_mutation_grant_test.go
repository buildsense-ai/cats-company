package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openchat/openchat/server/store/types"
)

const skillMutationGrantTestSecret = "0123456789abcdef0123456789abcdef"

func skillMutationGrantTestInput(operation types.BotSkillMutationOperation) skillMutationGrantInput {
	hashA := strings.Repeat("a", 64)
	input := skillMutationGrantInput{
		Mutation: types.BotSkillMutationCreateInput{
			BotUID:                     42,
			LocalSkillID:               "review-pr",
			ActorUserUID:               7,
			SourceTopicID:              "p2p_7_42",
			SourceMessageID:            99,
			RuntimeBodyID:              "body:prod-1",
			ClientRequestID:            "mutation-001",
			Operation:                  operation,
			CandidateContentHash:       strings.Repeat("b", 64),
			ExpectedDefinitionRevision: 8,
		},
		CandidateSizeBytes: 4096,
	}
	if operation == types.BotSkillMutationReplace || operation == types.BotSkillMutationRollback {
		input.Mutation.ExpectedPreviousContentHash = hashA
		input.Mutation.BeforeReference = &types.BotSkillRef{
			Source: "skillhub", SkillID: "skill-review-pr", Version: "1.2.3", ContentHash: hashA,
		}
	}
	if operation == types.BotSkillMutationRollback {
		rollbackOf := int64(5)
		input.Mutation.RollbackOf = &rollbackOf
	}
	return input
}

func TestSkillMutationGrantRoundTripBindsCanonicalFacts(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	signer, err := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	raw, issued, err := signer.issue(skillMutationGrantTestInput(types.BotSkillMutationReplace))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := signer.verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if verified.TokenType != skillMutationGrantTokenType || verified.BotUID != 42 || verified.ActorUserUID != 7 ||
		verified.SourceTopicID != "p2p_7_42" || verified.SourceMessageID != 99 ||
		verified.RuntimeBodyID != "body:prod-1" || verified.ClientRequestID != "mutation-001" ||
		verified.CandidateSizeBytes != 4096 || verified.RequestFingerprint == "" ||
		verified.RequestFingerprint != issued.RequestFingerprint {
		t.Fatalf("unexpected verified grant: %#v", verified)
	}
	if got := verified.mutationInput(); got.BotUID != 42 || got.BeforeReference == nil || got.BeforeReference.Version != "1.2.3" {
		t.Fatalf("unexpected mutation input: %#v", got)
	}
}

func TestSkillMutationGrantRejectsTamperingAndWrongKey(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	signer, _ := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	raw, _, err := signer.issue(skillMutationGrantTestInput(types.BotSkillMutationCreate))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(raw, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	claims["actor_user_uid"] = float64(999)
	payload, _ = json.Marshal(claims)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]
	if _, err := signer.verify(tampered); err == nil {
		t.Fatal("tampered actor identity was accepted")
	}
	other, _ := newSkillMutationGrantSigner([]byte("abcdef0123456789abcdef0123456789"), func() time.Time { return now })
	if _, err := other.verify(raw); err == nil {
		t.Fatal("grant signed by a different root key was accepted")
	}
}

func TestSkillMutationGrantRejectsResignedFingerprintMismatch(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	signer, _ := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	_, claims, err := signer.issue(skillMutationGrantTestInput(types.BotSkillMutationCreate))
	if err != nil {
		t.Fatal(err)
	}
	claims.ActorUserUID = 999
	resigned := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	raw, err := resigned.SignedString(signer.key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.verify(raw); err == nil {
		t.Fatal("grant with mismatched canonical fingerprint was accepted")
	}
}

func TestSkillMutationGrantRejectsExpiredAndOversizedLifetime(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	signer, _ := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	input := skillMutationGrantTestInput(types.BotSkillMutationCreate)
	input.TTL = maxSkillMutationGrantTTL + time.Second
	if _, _, err := signer.issue(input); err == nil {
		t.Fatal("oversized grant lifetime was accepted")
	}
	input.TTL = time.Minute
	raw, _, err := signer.issue(input)
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return now.Add(time.Minute + skillMutationGrantClockSkew + time.Second) }
	if _, err := signer.verify(raw); err == nil {
		t.Fatal("expired grant was accepted")
	}
}

func TestSkillMutationGrantRejectsRollbackAndInvalidCandidate(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	signer, _ := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if _, _, err := signer.issue(skillMutationGrantTestInput(types.BotSkillMutationRollback)); err == nil {
		t.Fatal("chat grant authorized rollback")
	}
	input := skillMutationGrantTestInput(types.BotSkillMutationCreate)
	input.CandidateSizeBytes = 0
	if _, _, err := signer.issue(input); err == nil {
		t.Fatal("empty candidate was accepted")
	}
}

func TestSkillMutationGrantRejectsAlgorithmConfusion(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	signer, _ := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	input := skillMutationGrantTestInput(types.BotSkillMutationCreate)
	_, claims, err := signer.issue(input)
	if err != nil {
		t.Fatal(err)
	}
	wrongAlg := jwt.NewWithClaims(jwt.SigningMethodHS384, claims)
	raw, err := wrongAlg.SignedString(signer.key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.verify(raw); err == nil {
		t.Fatal("HS384 grant was accepted")
	}
}

func TestSkillMutationGrantUsesDomainSeparatedSigningKey(t *testing.T) {
	signer, err := newSkillMutationGrantSigner([]byte(skillMutationGrantTestSecret), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if string(signer.key) == skillMutationGrantTestSecret {
		t.Fatal("grant signer reused the root JWT key directly")
	}
	if _, err := newSkillMutationGrantSigner([]byte("too-short"), time.Now); err == nil {
		t.Fatal("short root secret was accepted")
	}
}

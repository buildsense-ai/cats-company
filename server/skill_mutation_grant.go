package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	skillMutationGrantTokenType = "skill_mutation_grant"
	skillMutationGrantIssuer    = "catscompany"
	skillMutationGrantAudience  = "catscompany-skill-mutation"
	skillMutationGrantKeyLabel  = "catscompany/skill-mutation-grant/v1"

	defaultSkillMutationGrantTTL = 2 * time.Minute
	maxSkillMutationGrantTTL     = 5 * time.Minute
	skillMutationGrantClockSkew  = 5 * time.Second
)

// skillMutationGrantClaims is a short-lived, candidate-bound capability. It is
// deliberately separate from the ordinary CatsCo user token and the long-lived
// Bot API key. A later mutation request must use these server-attributed facts
// verbatim instead of accepting actor or conversation identity from its body.
type skillMutationGrantClaims struct {
	TokenType                  string                          `json:"token_type"`
	BotUID                     int64                           `json:"bot_uid"`
	LocalSkillID               string                          `json:"local_skill_id"`
	ActorUserUID               int64                           `json:"actor_user_uid"`
	SourceTopicID              string                          `json:"source_topic_id"`
	SourceMessageID            int64                           `json:"source_message_id"`
	RuntimeBodyID              string                          `json:"runtime_body_id"`
	ClientRequestID            string                          `json:"client_request_id"`
	Operation                  types.BotSkillMutationOperation `json:"operation"`
	CandidateContentHash       string                          `json:"candidate_content_hash"`
	CandidateSizeBytes         int64                           `json:"candidate_size_bytes"`
	ExpectedDefinitionRevision int64                           `json:"expected_definition_revision"`
	ExpectedPreviousHash       string                          `json:"expected_previous_content_hash,omitempty"`
	BeforeReference            *types.BotSkillRef              `json:"before_reference,omitempty"`
	RequestFingerprint         string                          `json:"request_fingerprint"`
	jwt.RegisteredClaims
}

type skillMutationGrantInput struct {
	Mutation           types.BotSkillMutationCreateInput
	CandidateSizeBytes int64
	TTL                time.Duration
}

type skillMutationGrantSigner struct {
	key []byte
	now func() time.Time
}

func newSkillMutationGrantSigner(rootSecret []byte, now func() time.Time) (*skillMutationGrantSigner, error) {
	if len(rootSecret) < 32 {
		return nil, errors.New("skill mutation grant secret must contain at least 32 bytes")
	}
	if now == nil {
		now = time.Now
	}
	mac := hmac.New(sha256.New, rootSecret)
	_, _ = mac.Write([]byte(skillMutationGrantKeyLabel))
	return &skillMutationGrantSigner{key: mac.Sum(nil), now: now}, nil
}

func (s *skillMutationGrantSigner) issue(input skillMutationGrantInput) (string, *skillMutationGrantClaims, error) {
	if s == nil || len(s.key) == 0 || s.now == nil {
		return "", nil, errors.New("skill mutation grant signer is not configured")
	}
	if input.CandidateSizeBytes <= 0 {
		return "", nil, errors.New("candidate size must be positive")
	}
	normalized, fingerprint, err := store.NormalizeBotSkillMutationCreateInput(input.Mutation)
	if err != nil {
		return "", nil, err
	}
	if normalized.Operation == types.BotSkillMutationRollback {
		return "", nil, errors.New("chat mutation grants do not authorize rollback")
	}
	ttl := input.TTL
	if ttl == 0 {
		ttl = defaultSkillMutationGrantTTL
	}
	if ttl <= 0 || ttl > maxSkillMutationGrantTTL {
		return "", nil, errors.New("invalid skill mutation grant ttl")
	}
	jti, err := randomHex(16)
	if err != nil {
		return "", nil, fmt.Errorf("create skill mutation grant id: %w", err)
	}
	now := s.now().UTC()
	claims := &skillMutationGrantClaims{
		TokenType:                  skillMutationGrantTokenType,
		BotUID:                     normalized.BotUID,
		LocalSkillID:               normalized.LocalSkillID,
		ActorUserUID:               normalized.ActorUserUID,
		SourceTopicID:              normalized.SourceTopicID,
		SourceMessageID:            normalized.SourceMessageID,
		RuntimeBodyID:              normalized.RuntimeBodyID,
		ClientRequestID:            normalized.ClientRequestID,
		Operation:                  normalized.Operation,
		CandidateContentHash:       normalized.CandidateContentHash,
		CandidateSizeBytes:         input.CandidateSizeBytes,
		ExpectedDefinitionRevision: normalized.ExpectedDefinitionRevision,
		ExpectedPreviousHash:       normalized.ExpectedPreviousContentHash,
		BeforeReference:            normalized.BeforeReference,
		RequestFingerprint:         fingerprint,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "smg_" + jti,
			Issuer:    skillMutationGrantIssuer,
			Subject:   fmt.Sprintf("bot:%d:skill:%s", normalized.BotUID, normalized.LocalSkillID),
			Audience:  jwt.ClaimStrings{skillMutationGrantAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-skillMutationGrantClockSkew)),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	raw, err := token.SignedString(s.key)
	if err != nil {
		return "", nil, fmt.Errorf("sign skill mutation grant: %w", err)
	}
	return raw, claims, nil
}

func (s *skillMutationGrantSigner) verify(raw string) (*skillMutationGrantClaims, error) {
	if s == nil || len(s.key) == 0 || s.now == nil {
		return nil, errors.New("skill mutation grant signer is not configured")
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("skill mutation grant is required")
	}
	claims := &skillMutationGrantClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(skillMutationGrantIssuer),
		jwt.WithAudience(skillMutationGrantAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(skillMutationGrantClockSkew),
		jwt.WithTimeFunc(func() time.Time { return s.now().UTC() }),
	)
	token, err := parser.ParseWithClaims(strings.TrimSpace(raw), claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("invalid skill mutation grant signing method")
		}
		return s.key, nil
	})
	if err != nil || token == nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid skill mutation grant")
		}
		return nil, err
	}
	if err := validateSkillMutationGrantClaims(claims, s.now().UTC()); err != nil {
		return nil, err
	}
	return claims, nil
}

// verifyExpiredForRecovery authenticates an expired grant only for resuming an
// already persisted, fact-matched mutation. Callers must never use these claims
// to create a new mutation.
func (s *skillMutationGrantSigner) verifyExpiredForRecovery(raw string) (*skillMutationGrantClaims, error) {
	if s == nil || len(s.key) == 0 || s.now == nil {
		return nil, errors.New("skill mutation grant signer is not configured")
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("skill mutation grant is required")
	}
	claims := &skillMutationGrantClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithoutClaimsValidation(),
	)
	token, err := parser.ParseWithClaims(strings.TrimSpace(raw), claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("invalid skill mutation grant signing method")
		}
		return s.key, nil
	})
	if err != nil || token == nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid skill mutation grant")
		}
		return nil, err
	}
	now := s.now().UTC()
	if err := validateSkillMutationGrantClaims(claims, now); err != nil {
		return nil, err
	}
	if !now.After(claims.ExpiresAt.Time.UTC().Add(skillMutationGrantClockSkew)) {
		return nil, errors.New("skill mutation grant is not expired")
	}
	return claims, nil
}

func validateSkillMutationGrantClaims(claims *skillMutationGrantClaims, now time.Time) error {
	if claims == nil || claims.TokenType != skillMutationGrantTokenType ||
		!strings.HasPrefix(claims.ID, "smg_") || len(claims.ID) <= len("smg_") ||
		claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.NotBefore == nil {
		return errors.New("invalid skill mutation grant claims")
	}
	if claims.Issuer != skillMutationGrantIssuer || !containsSkillMutationAudience(claims.Audience, skillMutationGrantAudience) {
		return errors.New("invalid skill mutation grant issuer or audience")
	}
	issuedAt := claims.IssuedAt.Time.UTC()
	expiresAt := claims.ExpiresAt.Time.UTC()
	notBefore := claims.NotBefore.Time.UTC()
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maxSkillMutationGrantTTL ||
		issuedAt.After(now.Add(skillMutationGrantClockSkew)) || notBefore.After(now.Add(skillMutationGrantClockSkew)) {
		return errors.New("invalid skill mutation grant lifetime")
	}
	if claims.Subject != fmt.Sprintf("bot:%d:skill:%s", claims.BotUID, claims.LocalSkillID) {
		return errors.New("invalid skill mutation grant subject")
	}
	if claims.CandidateSizeBytes <= 0 {
		return errors.New("invalid skill mutation candidate size")
	}
	input := types.BotSkillMutationCreateInput{
		BotUID:                      claims.BotUID,
		LocalSkillID:                claims.LocalSkillID,
		ActorUserUID:                claims.ActorUserUID,
		SourceTopicID:               claims.SourceTopicID,
		SourceMessageID:             claims.SourceMessageID,
		RuntimeBodyID:               claims.RuntimeBodyID,
		ClientRequestID:             claims.ClientRequestID,
		Operation:                   claims.Operation,
		CandidateContentHash:        claims.CandidateContentHash,
		ExpectedDefinitionRevision:  claims.ExpectedDefinitionRevision,
		ExpectedPreviousContentHash: claims.ExpectedPreviousHash,
		BeforeReference:             claims.BeforeReference,
	}
	normalized, fingerprint, err := store.NormalizeBotSkillMutationCreateInput(input)
	if err != nil || normalized.Operation == types.BotSkillMutationRollback {
		return errors.New("invalid skill mutation grant facts")
	}
	if claims.RequestFingerprint == "" || !hmac.Equal([]byte(claims.RequestFingerprint), []byte(fingerprint)) {
		return errors.New("skill mutation grant fingerprint mismatch")
	}
	// Return normalized strings to future consumers so downstream persistence
	// sees the exact same facts that were fingerprinted at issuance.
	claims.LocalSkillID = normalized.LocalSkillID
	claims.SourceTopicID = normalized.SourceTopicID
	claims.RuntimeBodyID = normalized.RuntimeBodyID
	claims.ClientRequestID = normalized.ClientRequestID
	claims.CandidateContentHash = normalized.CandidateContentHash
	claims.ExpectedPreviousHash = normalized.ExpectedPreviousContentHash
	claims.BeforeReference = normalized.BeforeReference
	return nil
}

func containsSkillMutationAudience(audience jwt.ClaimStrings, expected string) bool {
	for _, value := range audience {
		if value == expected {
			return true
		}
	}
	return false
}

func (c *skillMutationGrantClaims) mutationInput() types.BotSkillMutationCreateInput {
	if c == nil {
		return types.BotSkillMutationCreateInput{}
	}
	return types.BotSkillMutationCreateInput{
		BotUID:                      c.BotUID,
		LocalSkillID:                c.LocalSkillID,
		ActorUserUID:                c.ActorUserUID,
		SourceTopicID:               c.SourceTopicID,
		SourceMessageID:             c.SourceMessageID,
		RuntimeBodyID:               c.RuntimeBodyID,
		ClientRequestID:             c.ClientRequestID,
		Operation:                   c.Operation,
		CandidateContentHash:        c.CandidateContentHash,
		ExpectedDefinitionRevision:  c.ExpectedDefinitionRevision,
		ExpectedPreviousContentHash: c.ExpectedPreviousHash,
		BeforeReference:             c.BeforeReference,
	}
}

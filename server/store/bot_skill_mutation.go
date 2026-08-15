package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const maxBotSkillMutationLeaseTTL = 10 * time.Minute

func NormalizeBotSkillMutationCreateInput(input types.BotSkillMutationCreateInput) (types.BotSkillMutationCreateInput, string, error) {
	input.LocalSkillID = strings.TrimSpace(input.LocalSkillID)
	input.SourceTopicID = strings.TrimSpace(input.SourceTopicID)
	input.RuntimeBodyID = strings.TrimSpace(input.RuntimeBodyID)
	input.ClientRequestID = strings.TrimSpace(input.ClientRequestID)
	input.CandidateContentHash = strings.ToLower(strings.TrimSpace(input.CandidateContentHash))
	input.ExpectedPreviousContentHash = strings.ToLower(strings.TrimSpace(input.ExpectedPreviousContentHash))

	if input.BotUID <= 0 || input.ActorUserUID <= 0 || input.SourceMessageID <= 0 || input.ExpectedDefinitionRevision < 0 {
		return input, "", errors.New("invalid mutation identity or revision")
	}
	if !validMutationIdentifier(input.LocalSkillID, 128, false) {
		return input, "", errors.New("invalid local skill id")
	}
	if input.SourceTopicID == "" || len(input.SourceTopicID) > 255 || strings.ContainsAny(input.SourceTopicID, "\r\n\x00") {
		return input, "", errors.New("invalid source topic id")
	}
	if !validMutationIdentifier(input.RuntimeBodyID, 128, true) {
		return input, "", errors.New("invalid runtime body id")
	}
	if !validMutationIdentifier(input.ClientRequestID, 128, true) {
		return input, "", errors.New("invalid client request id")
	}
	if !validSHA256(input.CandidateContentHash) {
		return input, "", errors.New("invalid candidate content hash")
	}
	switch input.Operation {
	case types.BotSkillMutationCreate:
		if input.ExpectedPreviousContentHash != "" || input.BeforeReference != nil || input.RollbackOf != nil {
			return input, "", errors.New("create mutation must not have a previous version")
		}
	case types.BotSkillMutationReplace:
		if !validSHA256(input.ExpectedPreviousContentHash) || input.BeforeReference == nil || input.RollbackOf != nil {
			return input, "", errors.New("replace mutation requires the previous version")
		}
	case types.BotSkillMutationRollback:
		if !validSHA256(input.ExpectedPreviousContentHash) || input.BeforeReference == nil || input.RollbackOf == nil || *input.RollbackOf <= 0 {
			return input, "", errors.New("rollback mutation requires previous and rollback references")
		}
	default:
		return input, "", errors.New("invalid mutation operation")
	}
	if input.BeforeReference != nil {
		ref := *input.BeforeReference
		ref.Source = strings.ToLower(strings.TrimSpace(ref.Source))
		ref.SkillID = strings.TrimSpace(ref.SkillID)
		ref.Version = strings.TrimSpace(ref.Version)
		ref.ContentHash = strings.ToLower(strings.TrimSpace(ref.ContentHash))
		if ref.Source != "skillhub" || ref.SkillID == "" || len(ref.SkillID) > 255 ||
			ref.Version == "" || len(ref.Version) > 128 || !validSHA256(ref.ContentHash) ||
			ref.ContentHash != input.ExpectedPreviousContentHash {
			return input, "", errors.New("invalid previous skill reference")
		}
		input.BeforeReference = &ref
	}

	fingerprintPayload := struct {
		BotUID                      int64                           `json:"bot_uid"`
		LocalSkillID                string                          `json:"local_skill_id"`
		ActorUserUID                int64                           `json:"actor_user_uid"`
		SourceTopicID               string                          `json:"source_topic_id"`
		SourceMessageID             int64                           `json:"source_message_id"`
		RuntimeBodyID               string                          `json:"runtime_body_id"`
		ClientRequestID             string                          `json:"client_request_id"`
		Operation                   types.BotSkillMutationOperation `json:"operation"`
		CandidateContentHash        string                          `json:"candidate_content_hash"`
		ExpectedDefinitionRevision  int64                           `json:"expected_definition_revision"`
		ExpectedPreviousContentHash string                          `json:"expected_previous_content_hash,omitempty"`
		BeforeReference             *types.BotSkillRef              `json:"before_reference,omitempty"`
		RollbackOf                  *int64                          `json:"rollback_of,omitempty"`
	}{
		BotUID: input.BotUID, LocalSkillID: input.LocalSkillID, ActorUserUID: input.ActorUserUID,
		SourceTopicID: input.SourceTopicID, SourceMessageID: input.SourceMessageID,
		RuntimeBodyID: input.RuntimeBodyID, ClientRequestID: input.ClientRequestID,
		Operation: input.Operation, CandidateContentHash: input.CandidateContentHash,
		ExpectedDefinitionRevision:  input.ExpectedDefinitionRevision,
		ExpectedPreviousContentHash: input.ExpectedPreviousContentHash,
		BeforeReference:             input.BeforeReference, RollbackOf: input.RollbackOf,
	}
	raw, err := json.Marshal(fingerprintPayload)
	if err != nil {
		return input, "", fmt.Errorf("encode mutation fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return input, hex.EncodeToString(sum[:]), nil
}

func ValidateBotSkillMutationLease(now time.Time, leaseTTL time.Duration) (time.Time, error) {
	if now.IsZero() || leaseTTL <= 0 || leaseTTL > maxBotSkillMutationLeaseTTL {
		return time.Time{}, errors.New("invalid mutation lease")
	}
	return now.UTC().Add(leaseTTL), nil
}

func CanAdvanceBotSkillMutation(current, next types.BotSkillMutationStatus) bool {
	switch current {
	case types.BotSkillMutationValidating:
		return next == types.BotSkillMutationVersionReady || next == types.BotSkillMutationRejected
	case types.BotSkillMutationVersionReady:
		return next == types.BotSkillMutationDefinitionCommitted || next == types.BotSkillMutationRejected
	case types.BotSkillMutationDefinitionCommitted:
		return next == types.BotSkillMutationActivationPending || next == types.BotSkillMutationCompensationPending
	case types.BotSkillMutationActivationPending:
		return next == types.BotSkillMutationActive || next == types.BotSkillMutationCompensationPending
	case types.BotSkillMutationCompensationPending:
		return next == types.BotSkillMutationRolledBack
	default:
		return false
	}
}

func NormalizeBotSkillMutationTransition(next types.BotSkillMutationStatus, patch types.BotSkillMutationTransition) (types.BotSkillMutationTransition, error) {
	if patch.AfterReference != nil {
		ref := *patch.AfterReference
		ref.Source = strings.ToLower(strings.TrimSpace(ref.Source))
		ref.SkillID = strings.TrimSpace(ref.SkillID)
		ref.Version = strings.TrimSpace(ref.Version)
		ref.ContentHash = strings.ToLower(strings.TrimSpace(ref.ContentHash))
		if ref.Source != "skillhub" || ref.SkillID == "" || len(ref.SkillID) > 255 ||
			ref.Version == "" || len(ref.Version) > 128 || !validSHA256(ref.ContentHash) {
			return patch, errors.New("invalid next skill reference")
		}
		patch.AfterReference = &ref
	}
	if patch.GitCommitSHA != nil {
		value := strings.ToLower(strings.TrimSpace(*patch.GitCommitSHA))
		if !validHexDigest(value, 40, 64) {
			return patch, errors.New("invalid git commit sha")
		}
		patch.GitCommitSHA = &value
	}
	if patch.DefinitionRevision != nil && *patch.DefinitionRevision < 0 {
		return patch, errors.New("invalid definition revision")
	}
	if patch.ErrorCode != nil {
		value := strings.TrimSpace(*patch.ErrorCode)
		if !validMutationIdentifier(value, 64, false) {
			return patch, errors.New("invalid mutation error code")
		}
		patch.ErrorCode = &value
	}
	if patch.ErrorSummary != nil {
		value := strings.TrimSpace(*patch.ErrorSummary)
		if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return patch, errors.New("invalid mutation error summary")
		}
		patch.ErrorSummary = &value
	}

	switch next {
	case types.BotSkillMutationVersionReady:
		if patch.AfterReference == nil || patch.GitCommitSHA == nil {
			return patch, errors.New("version_ready requires immutable version facts")
		}
	case types.BotSkillMutationDefinitionCommitted:
		if patch.DefinitionRevision == nil {
			return patch, errors.New("definition_committed requires revision")
		}
	case types.BotSkillMutationActive:
		if patch.ActivatedAt == nil || patch.ActivatedAt.IsZero() {
			return patch, errors.New("active requires activation timestamp")
		}
	case types.BotSkillMutationRejected, types.BotSkillMutationCompensationPending:
		if patch.ErrorCode == nil || patch.ErrorSummary == nil || *patch.ErrorSummary == "" {
			return patch, errors.New("failure transition requires a safe error")
		}
	}
	return patch, nil
}

func IsTerminalBotSkillMutationStatus(status types.BotSkillMutationStatus) bool {
	return status == types.BotSkillMutationActive || status == types.BotSkillMutationRejected || status == types.BotSkillMutationRolledBack
}

func validMutationIdentifier(value string, maxBytes int, allowColon bool) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	for index, char := range value {
		allowed := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-'
		if allowColon && char == ':' {
			allowed = true
		}
		if !allowed || (index == 0 && (char == '.' || char == '_' || char == '-' || char == ':')) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validHexDigest(value string, allowedLengths ...int) bool {
	lengthAllowed := false
	for _, length := range allowedLengths {
		if len(value) == length {
			lengthAllowed = true
			break
		}
	}
	if !lengthAllowed {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

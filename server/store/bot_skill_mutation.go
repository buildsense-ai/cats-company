package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

const maxBotSkillMutationLeaseTTL = 10 * time.Minute

const (
	maxActivationSkillRefs         = 256
	maxActivationSkillIDBytes      = 240
	maxActivationSkillVersionBytes = 120
)

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

// ApplyBotSkillMutationDefinition applies immutable version facts to the exact
// BotDefinition revision on which the mutation was based. Database adapters
// call this only while both rows are locked in one transaction.
func ApplyBotSkillMutationDefinition(
	record *types.BotDefinitionRecord,
	mutation *types.BotSkillMutation,
	now time.Time,
) error {
	if record == nil || mutation == nil || mutation.Status != types.BotSkillMutationVersionReady ||
		mutation.AfterReference == nil || now.IsZero() {
		return ErrBotSkillMutationStateConflict
	}
	if record.Runtime.DesiredRevision != mutation.ExpectedDefinitionRevision {
		return ErrBotSkillMutationDefinitionStale
	}
	if mutation.AfterReference.ContentHash != mutation.CandidateContentHash {
		return ErrBotSkillMutationVersionFactsConflict
	}

	skills := append([]types.BotSkillRef(nil), record.Definition.Skills...)
	switch mutation.Operation {
	case types.BotSkillMutationCreate:
		for _, current := range skills {
			if current.SkillID == mutation.AfterReference.SkillID {
				return ErrBotSkillMutationDefinitionStale
			}
		}
		skills = append(skills, *mutation.AfterReference)
	case types.BotSkillMutationReplace, types.BotSkillMutationRollback:
		if mutation.BeforeReference == nil {
			return ErrBotSkillMutationStateConflict
		}
		if mutation.AfterReference.Source != mutation.BeforeReference.Source ||
			mutation.AfterReference.SkillID != mutation.BeforeReference.SkillID {
			return ErrBotSkillMutationVersionFactsConflict
		}
		matched := -1
		for index, current := range skills {
			if current.SkillID != mutation.BeforeReference.SkillID {
				continue
			}
			if current != *mutation.BeforeReference || matched >= 0 {
				return ErrBotSkillMutationDefinitionStale
			}
			matched = index
		}
		if matched < 0 {
			return ErrBotSkillMutationDefinitionStale
		}
		skills[matched] = *mutation.AfterReference
	default:
		return ErrBotSkillMutationStateConflict
	}

	record.Definition.Skills = skills
	if record.Definition.Model.Kind == "" {
		record.Definition.Model = types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"}
	}
	if record.Definition.Prompt == nil {
		record.Definition.Prompt = &types.BotPromptDefinition{Selected: "default"}
	}
	record.Runtime.DesiredRevision++
	record.Runtime.UpdatedAt = now.UTC().Format(time.RFC3339)
	record.Runtime.LastAttemptRevision = 0
	record.Runtime.LastAttemptAt = ""
	record.Runtime.LastError = ""
	record.Exists = true
	return nil
}

// NormalizeBotSkillMutationActivationInput validates the complete fact that a
// Runtime will persist as the idempotency key for activation acknowledgement.
func NormalizeBotSkillMutationActivationInput(input types.BotSkillMutationActivationInput) (types.BotSkillMutationActivationInput, error) {
	input.SkillSetHash = strings.ToLower(strings.TrimSpace(input.SkillSetHash))
	input.RuntimeBodyID = strings.TrimSpace(input.RuntimeBodyID)
	input.RuntimeInstallationID = strings.TrimSpace(input.RuntimeInstallationID)
	if input.BotUID <= 0 || input.MutationID <= 0 || input.AppliedDefinitionRevision < 0 ||
		!validSHA256(input.SkillSetHash) ||
		!validMutationIdentifier(input.RuntimeBodyID, 128, true) ||
		!validMutationIdentifier(input.RuntimeInstallationID, 128, true) {
		return input, errors.New("invalid bot skill activation fact")
	}
	return input, nil
}

func NormalizeBotSkillMutationActivationFailureInput(input types.BotSkillMutationActivationFailureInput) (types.BotSkillMutationActivationFailureInput, error) {
	input.RuntimeBodyID = strings.TrimSpace(input.RuntimeBodyID)
	input.RuntimeInstallationID = strings.TrimSpace(input.RuntimeInstallationID)
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	input.ErrorSummary = strings.TrimSpace(input.ErrorSummary)
	if input.BotUID <= 0 || input.MutationID <= 0 || input.AttemptedDefinitionRevision < 0 ||
		!validMutationIdentifier(input.RuntimeBodyID, 128, true) ||
		!validMutationIdentifier(input.RuntimeInstallationID, 128, true) ||
		!validMutationIdentifier(input.ErrorCode, 64, false) || input.ErrorSummary == "" ||
		len(input.ErrorSummary) > 512 || strings.ContainsAny(input.ErrorSummary, "\r\n\x00") {
		return input, errors.New("invalid bot skill activation failure")
	}
	return input, nil
}

// ValidateBotSkillMutationActivationTarget proves that the acknowledged
// complete Skill set is still the current desired Definition and still
// contains the exact immutable version produced by the mutation.
func ValidateBotSkillMutationActivationTarget(
	record *types.BotDefinitionRecord,
	mutation *types.BotSkillMutation,
	input types.BotSkillMutationActivationInput,
) error {
	input, err := NormalizeBotSkillMutationActivationInput(input)
	if err != nil {
		return err
	}
	if record == nil || mutation == nil || mutation.AfterReference == nil || mutation.DefinitionRevision == nil ||
		mutation.BotUID != input.BotUID || mutation.ID != input.MutationID {
		return ErrBotSkillMutationStateConflict
	}
	if mutation.RuntimeBodyID != input.RuntimeBodyID {
		return ErrBotSkillMutationRuntimeMismatch
	}
	if input.AppliedDefinitionRevision < *mutation.DefinitionRevision ||
		record.Runtime.DesiredRevision != input.AppliedDefinitionRevision {
		return ErrBotSkillMutationDefinitionStale
	}
	matches := 0
	for _, current := range record.Definition.Skills {
		if current == *mutation.AfterReference {
			matches++
		}
	}
	if matches != 1 {
		return ErrBotSkillMutationDefinitionStale
	}
	actualHash, err := CanonicalBotSkillSetHash(record.Definition.Skills)
	if err != nil || actualHash != input.SkillSetHash {
		return ErrBotSkillMutationVersionFactsConflict
	}
	return nil
}

// CanonicalBotSkillSetHash intentionally matches XiaoBa's
// computeCanonicalBotSkillSetHash: normalize references, sort by UTF-8 skillId
// bytes, JSON encode the four reference fields, then SHA-256 the bytes.
func CanonicalBotSkillSetHash(input []types.BotSkillRef) (string, error) {
	if len(input) > maxActivationSkillRefs {
		return "", errors.New("too many bot skill references")
	}
	canonical := make([]types.BotSkillRef, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		ref := types.BotSkillRef{
			Source:      strings.ToLower(trimXiaoBaString(item.Source)),
			SkillID:     trimXiaoBaString(item.SkillID),
			Version:     trimXiaoBaString(item.Version),
			ContentHash: trimXiaoBaString(item.ContentHash),
		}
		if ref.Source != "skillhub" || !validActivationReferencePart(ref.SkillID, maxActivationSkillIDBytes, true) ||
			!validActivationReferencePart(ref.Version, maxActivationSkillVersionBytes, false) || !validSHA256(ref.ContentHash) {
			return "", errors.New("invalid bot skill reference")
		}
		if _, exists := seen[ref.SkillID]; exists {
			return "", errors.New("duplicate bot skill reference")
		}
		seen[ref.SkillID] = struct{}{}
		canonical = append(canonical, ref)
	}
	sort.Slice(canonical, func(i, j int) bool {
		return bytes.Compare([]byte(canonical[i].SkillID), []byte(canonical[j].SkillID)) < 0
	})
	raw := marshalCanonicalBotSkillRefsForXiaoBa(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// encoding/json escapes HTML characters and U+2028/U+2029, while JavaScript's
// JSON.stringify (used by XiaoBa) does not. Keep this tiny encoder explicit so
// valid Unicode Skill IDs produce the same cross-runtime hash.
func marshalCanonicalBotSkillRefsForXiaoBa(skills []types.BotSkillRef) []byte {
	var output bytes.Buffer
	output.WriteByte('[')
	for index, ref := range skills {
		if index > 0 {
			output.WriteByte(',')
		}
		output.WriteString(`{"source":"skillhub","skillId":`)
		writeXiaoBaJSONString(&output, ref.SkillID)
		output.WriteString(`,"version":`)
		writeXiaoBaJSONString(&output, ref.Version)
		output.WriteString(`,"contentHash":`)
		writeXiaoBaJSONString(&output, ref.ContentHash)
		output.WriteByte('}')
	}
	output.WriteByte(']')
	return output.Bytes()
}

func writeXiaoBaJSONString(output *bytes.Buffer, value string) {
	const hexDigits = "0123456789abcdef"
	output.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			output.WriteString(`\"`)
		case '\\':
			output.WriteString(`\\`)
		case '\b':
			output.WriteString(`\b`)
		case '\f':
			output.WriteString(`\f`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if char < 0x20 {
				output.WriteString(`\u00`)
				output.WriteByte(hexDigits[byte(char)>>4])
				output.WriteByte(hexDigits[byte(char)&0x0f])
			} else {
				output.WriteRune(char)
			}
		}
	}
	output.WriteByte('"')
}

func validActivationReferencePart(value string, maxBytes int, requireSafeSegments bool) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	if requireSafeSegments {
		for _, segment := range strings.Split(value, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return false
			}
		}
	}
	return true
}

// JavaScript String.prototype.trim uses the ECMAScript WhiteSpace and line
// terminator set. Keep the normalization exact: Go's strings.TrimSpace also
// removes U+0085, which XiaoBa would retain and then reject as a control byte.
func trimXiaoBaString(value string) string {
	return strings.TrimFunc(value, func(char rune) bool {
		switch char {
		case '\u0009', '\u000a', '\u000b', '\u000c', '\u000d', '\u0020', '\u00a0', '\u1680',
			'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007',
			'\u2008', '\u2009', '\u200a', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
			return true
		default:
			return false
		}
	})
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

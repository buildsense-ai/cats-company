package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const postgresBotSkillActivationFactColumns = "activation_definition_revision, activation_skill_set_hash, activation_runtime_body_id, activation_installation_id"

type postgresBotSkillActivationFact struct {
	definitionRevision sql.NullInt64
	skillSetHash       sql.NullString
	runtimeBodyID      sql.NullString
	installationID     sql.NullString
}

func scanPostgresBotSkillActivationFact(row *sql.Row) (postgresBotSkillActivationFact, error) {
	var fact postgresBotSkillActivationFact
	err := row.Scan(&fact.definitionRevision, &fact.skillSetHash, &fact.runtimeBodyID, &fact.installationID)
	return fact, err
}

func (fact postgresBotSkillActivationFact) matches(input types.BotSkillMutationActivationInput) bool {
	return fact.definitionRevision.Valid && fact.definitionRevision.Int64 == input.AppliedDefinitionRevision &&
		fact.skillSetHash.Valid && fact.skillSetHash.String == input.SkillSetHash &&
		fact.runtimeBodyID.Valid && fact.runtimeBodyID.String == input.RuntimeBodyID &&
		fact.installationID.Valid && fact.installationID.String == input.RuntimeInstallationID
}

func (a *Adapter) ActivateBotSkillMutation(
	input types.BotSkillMutationActivationInput,
	now time.Time,
) (*types.BotSkillMutation, *types.BotDefinitionRecord, bool, error) {
	input, err := store.NormalizeBotSkillMutationActivationInput(input)
	if err != nil || now.IsZero() {
		return nil, nil, false, store.ErrBotSkillMutationStateConflict
	}
	now = now.UTC()
	tx, err := a.db.Begin()
	if err != nil {
		return nil, nil, false, fmt.Errorf("begin bot skill activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var raw []byte
	if err := tx.QueryRow(
		"SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE",
		input.BotUID,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, false, store.ErrBotSkillMutationNotFound
		}
		return nil, nil, false, fmt.Errorf("lock bot definition for skill activation: %w", err)
	}
	record, err := store.DecodeBotDefinitionJSON(raw, input.BotUID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode bot definition for skill activation: %w", err)
	}
	mutation, _, err := scanPostgresBotSkillMutation(tx.QueryRow(
		"SELECT "+postgresBotSkillMutationColumns+" FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2 FOR UPDATE",
		input.BotUID, input.MutationID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, store.ErrBotSkillMutationNotFound
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("lock bot skill mutation for activation: %w", err)
	}

	if mutation.Status == types.BotSkillMutationActive {
		fact, factErr := scanPostgresBotSkillActivationFact(tx.QueryRow(
			"SELECT "+postgresBotSkillActivationFactColumns+" FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2",
			input.BotUID, input.MutationID,
		))
		if factErr != nil {
			return nil, nil, false, fmt.Errorf("read bot skill activation fact: %w", factErr)
		}
		if !fact.matches(input) {
			return nil, nil, false, store.ErrBotSkillMutationActivationFactConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, false, fmt.Errorf("commit idempotent bot skill activation: %w", err)
		}
		return mutation, record, true, nil
	}
	if mutation.Status != types.BotSkillMutationActivationPending {
		return nil, nil, false, store.ErrBotSkillMutationStateConflict
	}
	if err := store.ValidateBotSkillMutationActivationTarget(record, mutation, input); err != nil {
		return nil, nil, false, err
	}

	appliedAt := now.Format(time.RFC3339)
	markBotDefinitionSkillActivationApplied(record, input.AppliedDefinitionRevision, appliedAt)
	next, err := store.EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		return nil, nil, false, fmt.Errorf("encode bot definition for skill activation: %w", err)
	}
	if _, err := tx.Exec("UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2", next, input.BotUID); err != nil {
		return nil, nil, false, fmt.Errorf("save bot definition for skill activation: %w", err)
	}
	mutation, _, err = scanPostgresBotSkillMutation(tx.QueryRow(
		"UPDATE bot_skill_mutations SET status = $1, activated_at = $2, activation_definition_revision = $3, "+
			"activation_skill_set_hash = $4, activation_runtime_body_id = $5, activation_installation_id = $6, "+
			"error_code = NULL, error_summary = NULL WHERE bot_uid = $7 AND id = $8 AND status = $9 RETURNING "+
			postgresBotSkillMutationColumns,
		string(types.BotSkillMutationActive), now, input.AppliedDefinitionRevision,
		input.SkillSetHash, input.RuntimeBodyID, input.RuntimeInstallationID,
		input.BotUID, input.MutationID, string(types.BotSkillMutationActivationPending),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, store.ErrBotSkillMutationStateConflict
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("activate bot skill mutation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, fmt.Errorf("commit bot skill activation: %w", err)
	}
	return mutation, record, false, nil
}

func markBotDefinitionSkillActivationApplied(record *types.BotDefinitionRecord, revision int64, at string) {
	record.Runtime.LastAttemptRevision = revision
	record.Runtime.LastAttemptAt = at
	record.Runtime.LastError = ""
	record.Runtime.AppliedKind = record.Definition.Model.Kind
	record.Runtime.AppliedModelID = record.Definition.Model.ModelID
	if record.Runtime.AppliedModelID == "" {
		record.Runtime.AppliedModelID = record.Definition.Model.Model
	}
	record.Runtime.AppliedReasoning = record.Definition.Model.ReasoningEffort
	record.Runtime.AppliedRevision = revision
	record.Runtime.AppliedAt = at
}

func definitionContainsExactMutationReference(record *types.BotDefinitionRecord, expected *types.BotSkillRef) bool {
	if record == nil || expected == nil {
		return false
	}
	matches := 0
	for _, current := range record.Definition.Skills {
		if current == *expected {
			matches++
		}
	}
	return matches == 1
}

func (a *Adapter) RecordBotSkillMutationActivationFailure(
	input types.BotSkillMutationActivationFailureInput,
	now time.Time,
) (*types.BotSkillMutation, *types.BotDefinitionRecord, bool, error) {
	input, err := store.NormalizeBotSkillMutationActivationFailureInput(input)
	if err != nil || now.IsZero() {
		return nil, nil, false, store.ErrBotSkillMutationStateConflict
	}
	now = now.UTC()
	tx, err := a.db.Begin()
	if err != nil {
		return nil, nil, false, fmt.Errorf("begin bot skill activation failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var raw []byte
	if err := tx.QueryRow(
		"SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE",
		input.BotUID,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, false, store.ErrBotSkillMutationNotFound
		}
		return nil, nil, false, fmt.Errorf("lock bot definition for activation failure: %w", err)
	}
	record, err := store.DecodeBotDefinitionJSON(raw, input.BotUID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode bot definition for activation failure: %w", err)
	}
	mutation, _, err := scanPostgresBotSkillMutation(tx.QueryRow(
		"SELECT "+postgresBotSkillMutationColumns+" FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2 FOR UPDATE",
		input.BotUID, input.MutationID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, store.ErrBotSkillMutationNotFound
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("lock bot skill mutation for activation failure: %w", err)
	}
	targetStatus := types.BotSkillMutationActivationPending
	if input.Permanent {
		targetStatus = types.BotSkillMutationCompensationPending
	}
	// A Runtime may retry the exact same receipt after the coordinator has
	// already advanced BotDefinition (for example, while compensating a
	// permanent failure). The persisted receipt is the idempotency authority;
	// do not reject that retry against the newer desired revision.
	if mutation.Status == targetStatus && mutation.ErrorCode == input.ErrorCode && mutation.ErrorSummary == input.ErrorSummary {
		fact, factErr := scanPostgresBotSkillActivationFact(tx.QueryRow(
			"SELECT "+postgresBotSkillActivationFactColumns+" FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2",
			input.BotUID, input.MutationID,
		))
		if factErr == nil && fact.definitionRevision.Valid &&
			fact.definitionRevision.Int64 == input.AttemptedDefinitionRevision &&
			fact.runtimeBodyID.Valid && fact.runtimeBodyID.String == input.RuntimeBodyID &&
			fact.installationID.Valid && fact.installationID.String == input.RuntimeInstallationID {
			if err := tx.Commit(); err != nil {
				return nil, nil, false, fmt.Errorf("commit idempotent activation failure: %w", err)
			}
			return mutation, record, true, nil
		}
	}
	if mutation.RuntimeBodyID != input.RuntimeBodyID {
		return nil, nil, false, store.ErrBotSkillMutationRuntimeMismatch
	}
	if mutation.DefinitionRevision == nil || input.AttemptedDefinitionRevision < *mutation.DefinitionRevision ||
		record.Runtime.DesiredRevision != input.AttemptedDefinitionRevision ||
		mutation.AfterReference == nil || !definitionContainsExactMutationReference(record, mutation.AfterReference) {
		return nil, nil, false, store.ErrBotSkillMutationDefinitionStale
	}
	if mutation.Status != types.BotSkillMutationActivationPending {
		return nil, nil, false, store.ErrBotSkillMutationStateConflict
	}

	attemptedAt := now.Format(time.RFC3339)
	record.Runtime.LastAttemptRevision = input.AttemptedDefinitionRevision
	record.Runtime.LastAttemptAt = attemptedAt
	record.Runtime.LastError = input.ErrorSummary
	next, err := store.EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		return nil, nil, false, fmt.Errorf("encode activation failure definition: %w", err)
	}
	if _, err := tx.Exec("UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2", next, input.BotUID); err != nil {
		return nil, nil, false, fmt.Errorf("save activation failure definition: %w", err)
	}
	mutation, _, err = scanPostgresBotSkillMutation(tx.QueryRow(
		"UPDATE bot_skill_mutations SET status = $1, error_code = $2, error_summary = $3, "+
			"activation_definition_revision = $4, activation_skill_set_hash = NULL, "+
			"activation_runtime_body_id = $5, activation_installation_id = $6 "+
			"WHERE bot_uid = $7 AND id = $8 AND status = $9 RETURNING "+postgresBotSkillMutationColumns,
		string(targetStatus), input.ErrorCode, input.ErrorSummary,
		input.AttemptedDefinitionRevision, input.RuntimeBodyID, input.RuntimeInstallationID,
		input.BotUID, input.MutationID, string(types.BotSkillMutationActivationPending),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, store.ErrBotSkillMutationStateConflict
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("record bot skill activation failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, fmt.Errorf("commit bot skill activation failure: %w", err)
	}
	return mutation, record, false, nil
}

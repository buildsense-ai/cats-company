package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const postgresBotSkillMutationColumns = `
id, bot_uid, local_skill_id, actor_user_uid, source_topic_id, source_message_id,
runtime_body_id, client_request_id, request_fingerprint, operation,
candidate_content_hash, expected_definition_revision,
COALESCE(expected_previous_content_hash, ''), before_reference, after_reference,
COALESCE(git_commit_sha, ''), definition_revision, status,
COALESCE(error_code, ''), COALESCE(error_summary, ''), rollback_of,
lease_generation, lease_expires_at, created_at, updated_at, activated_at`

type postgresBotSkillMutationScanner interface {
	Scan(dest ...any) error
}

func scanPostgresBotSkillMutation(row postgresBotSkillMutationScanner) (*types.BotSkillMutation, string, error) {
	mutation := &types.BotSkillMutation{}
	var requestFingerprint, operation, status string
	var beforeRaw, afterRaw []byte
	var definitionRevision, rollbackOf sql.NullInt64
	var activatedAt sql.NullTime
	if err := row.Scan(
		&mutation.ID, &mutation.BotUID, &mutation.LocalSkillID, &mutation.ActorUserUID,
		&mutation.SourceTopicID, &mutation.SourceMessageID, &mutation.RuntimeBodyID,
		&mutation.ClientRequestID, &requestFingerprint, &operation,
		&mutation.CandidateContentHash, &mutation.ExpectedDefinitionRevision,
		&mutation.ExpectedPreviousContentHash, &beforeRaw, &afterRaw,
		&mutation.GitCommitSHA, &definitionRevision, &status,
		&mutation.ErrorCode, &mutation.ErrorSummary, &rollbackOf,
		&mutation.LeaseGeneration, &mutation.LeaseExpiresAt, &mutation.CreatedAt,
		&mutation.UpdatedAt, &activatedAt,
	); err != nil {
		return nil, "", err
	}
	parsedOperation, ok := types.ParseBotSkillMutationOperation(operation)
	if !ok {
		return nil, "", fmt.Errorf("invalid stored bot skill mutation operation %q", operation)
	}
	parsedStatus, ok := types.ParseBotSkillMutationStatus(status)
	if !ok {
		return nil, "", fmt.Errorf("invalid stored bot skill mutation status %q", status)
	}
	mutation.Operation = parsedOperation
	mutation.Status = parsedStatus
	if definitionRevision.Valid {
		mutation.DefinitionRevision = &definitionRevision.Int64
	}
	if rollbackOf.Valid {
		mutation.RollbackOf = &rollbackOf.Int64
	}
	if activatedAt.Valid {
		value := activatedAt.Time.UTC()
		mutation.ActivatedAt = &value
	}
	var err error
	if mutation.BeforeReference, err = decodePostgresBotSkillMutationRef(beforeRaw); err != nil {
		return nil, "", fmt.Errorf("decode before reference: %w", err)
	}
	if mutation.AfterReference, err = decodePostgresBotSkillMutationRef(afterRaw); err != nil {
		return nil, "", fmt.Errorf("decode after reference: %w", err)
	}
	return mutation, requestFingerprint, nil
}

func decodePostgresBotSkillMutationRef(raw []byte) (*types.BotSkillRef, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var ref types.BotSkillRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, err
	}
	return &ref, nil
}

func encodePostgresBotSkillMutationRef(ref *types.BotSkillRef) (any, error) {
	if ref == nil {
		return nil, nil
	}
	return json.Marshal(ref)
}

func (a *Adapter) BeginBotSkillMutation(
	input types.BotSkillMutationCreateInput,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, bool, error) {
	input, fingerprint, err := store.NormalizeBotSkillMutationCreateInput(input)
	if err != nil {
		return nil, false, err
	}
	leaseExpiresAt, err := store.ValidateBotSkillMutationLease(now, leaseTTL)
	if err != nil {
		return nil, false, err
	}
	now = now.UTC()
	beforeRaw, err := encodePostgresBotSkillMutationRef(input.BeforeReference)
	if err != nil {
		return nil, false, fmt.Errorf("encode before reference: %w", err)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin bot skill mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lockedBotUID int64
	if err := tx.QueryRow(`SELECT user_id FROM bot_config WHERE user_id = $1 FOR UPDATE`, input.BotUID).Scan(&lockedBotUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, store.ErrBotSkillMutationNotFound
		}
		return nil, false, fmt.Errorf("lock bot skill mutation target: %w", err)
	}

	existing, existingFingerprint, err := scanPostgresBotSkillMutation(tx.QueryRow(
		`SELECT `+postgresBotSkillMutationColumns+`
		 FROM bot_skill_mutations
		 WHERE actor_user_uid = $1 AND bot_uid = $2 AND client_request_id = $3`,
		input.ActorUserUID, input.BotUID, input.ClientRequestID,
	))
	if err == nil {
		if existingFingerprint != fingerprint {
			return nil, false, store.ErrBotSkillMutationIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit idempotent bot skill mutation: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("lookup idempotent bot skill mutation: %w", err)
	}

	active, _, err := scanPostgresBotSkillMutation(tx.QueryRow(
		`SELECT `+postgresBotSkillMutationColumns+`
		 FROM bot_skill_mutations
		 WHERE bot_uid = $1
		   AND status IN ('validating','version_ready','definition_committed','activation_pending','compensation_pending')
		 ORDER BY id DESC LIMIT 1`,
		input.BotUID,
	))
	if err == nil {
		if active.LeaseExpiresAt.After(now) {
			return nil, false, store.ErrBotSkillMutationBusy
		}
		return nil, false, store.ErrBotSkillMutationRecoveryRequired
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("lookup active bot skill mutation: %w", err)
	}

	mutation, _, err := scanPostgresBotSkillMutation(tx.QueryRow(
		`INSERT INTO bot_skill_mutations (
			bot_uid, local_skill_id, actor_user_uid, source_topic_id, source_message_id,
			runtime_body_id, client_request_id, request_fingerprint, operation,
			candidate_content_hash, expected_definition_revision,
			expected_previous_content_hash, before_reference, rollback_of, lease_expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15)
		RETURNING `+postgresBotSkillMutationColumns,
		input.BotUID, input.LocalSkillID, input.ActorUserUID, input.SourceTopicID,
		input.SourceMessageID, input.RuntimeBodyID, input.ClientRequestID, fingerprint,
		string(input.Operation), input.CandidateContentHash, input.ExpectedDefinitionRevision,
		skillMutationNullableString(input.ExpectedPreviousContentHash), beforeRaw,
		skillMutationNullableInt64(input.RollbackOf), leaseExpiresAt,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "uk_bot_skill_mutations_request" {
				return nil, false, store.ErrBotSkillMutationIdempotencyConflict
			}
			if pgErr.ConstraintName == "uk_bot_skill_mutations_active" {
				return nil, false, store.ErrBotSkillMutationBusy
			}
		}
		return nil, false, fmt.Errorf("insert bot skill mutation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit bot skill mutation: %w", err)
	}
	return mutation, true, nil
}

func (a *Adapter) GetBotSkillMutation(botUID, mutationID int64) (*types.BotSkillMutation, error) {
	mutation, _, err := scanPostgresBotSkillMutation(a.db.QueryRow(
		`SELECT `+postgresBotSkillMutationColumns+` FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2`,
		botUID, mutationID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrBotSkillMutationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bot skill mutation: %w", err)
	}
	return mutation, nil
}

func (a *Adapter) AdvanceBotSkillMutation(
	botUID, mutationID, expectedLeaseGeneration int64,
	expected, next types.BotSkillMutationStatus,
	patch types.BotSkillMutationTransition,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, error) {
	if expectedLeaseGeneration <= 0 || !store.CanAdvanceBotSkillMutation(expected, next) {
		return nil, store.ErrBotSkillMutationStateConflict
	}
	if next == types.BotSkillMutationDefinitionCommitted {
		return nil, store.ErrBotSkillMutationAtomicCommitRequired
	}
	patch, err := store.NormalizeBotSkillMutationTransition(next, patch)
	if err != nil {
		return nil, err
	}
	if next == types.BotSkillMutationVersionReady {
		current, err := a.GetBotSkillMutation(botUID, mutationID)
		if err != nil {
			return nil, err
		}
		if current.Status != expected || current.LeaseGeneration != expectedLeaseGeneration {
			return nil, store.ErrBotSkillMutationStateConflict
		}
		if current.CandidateContentHash != patch.AfterReference.ContentHash {
			return nil, store.ErrBotSkillMutationVersionFactsConflict
		}
	}
	leaseExpiresAt, err := store.ValidateBotSkillMutationLease(now, leaseTTL)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	if store.IsTerminalBotSkillMutationStatus(next) {
		leaseExpiresAt = now
	}
	afterRaw, err := encodePostgresBotSkillMutationRef(patch.AfterReference)
	if err != nil {
		return nil, fmt.Errorf("encode after reference: %w", err)
	}
	mutation, _, err := scanPostgresBotSkillMutation(a.db.QueryRow(
		`UPDATE bot_skill_mutations SET
			status = $1,
			after_reference = COALESCE($2::jsonb, after_reference),
			git_commit_sha = COALESCE($3, git_commit_sha),
			definition_revision = COALESCE($4, definition_revision),
			error_code = COALESCE($5, error_code),
			error_summary = COALESCE($6, error_summary),
			activated_at = COALESCE($7, activated_at),
			lease_expires_at = $8,
			updated_at = CURRENT_TIMESTAMP
		 WHERE bot_uid = $9 AND id = $10 AND status = $11
		   AND lease_generation = $12 AND lease_expires_at > $13
		 RETURNING `+postgresBotSkillMutationColumns,
		string(next), afterRaw, patch.GitCommitSHA, patch.DefinitionRevision,
		patch.ErrorCode, patch.ErrorSummary, skillMutationNullableTime(patch.ActivatedAt),
		leaseExpiresAt, botUID, mutationID, string(expected), expectedLeaseGeneration, now,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, a.classifyBotSkillMutationCASFailure(botUID, mutationID, expectedLeaseGeneration, expected, now)
	}
	if err != nil {
		return nil, fmt.Errorf("advance bot skill mutation: %w", err)
	}
	return mutation, nil
}

func (a *Adapter) CommitBotSkillMutationDefinition(
	botUID, mutationID, expectedLeaseGeneration int64,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, *types.BotDefinitionRecord, error) {
	if expectedLeaseGeneration <= 0 {
		return nil, nil, store.ErrBotSkillMutationStateConflict
	}
	leaseExpiresAt, err := store.ValidateBotSkillMutationLease(now, leaseTTL)
	if err != nil {
		return nil, nil, err
	}
	now = now.UTC()

	tx, err := a.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("begin bot skill definition commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var raw []byte
	if err := tx.QueryRow(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE`,
		botUID,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, store.ErrBotSkillMutationNotFound
		}
		return nil, nil, fmt.Errorf("lock bot definition for skill mutation: %w", err)
	}
	record, err := store.DecodeBotDefinitionJSON(raw, botUID)
	if err != nil {
		return nil, nil, fmt.Errorf("decode bot definition for skill mutation: %w", err)
	}

	mutation, _, err := scanPostgresBotSkillMutation(tx.QueryRow(
		`SELECT `+postgresBotSkillMutationColumns+`
		 FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2 FOR UPDATE`,
		botUID, mutationID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, store.ErrBotSkillMutationNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock bot skill mutation for definition commit: %w", err)
	}
	if mutation.Status != types.BotSkillMutationVersionReady || mutation.LeaseGeneration != expectedLeaseGeneration {
		return nil, nil, store.ErrBotSkillMutationStateConflict
	}
	if !mutation.LeaseExpiresAt.After(now) {
		return nil, nil, store.ErrBotSkillMutationLeaseExpired
	}
	if err := store.ApplyBotSkillMutationDefinition(record, mutation, now); err != nil {
		return nil, nil, err
	}
	next, err := store.EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		return nil, nil, fmt.Errorf("encode bot definition for skill mutation: %w", err)
	}
	if _, err := tx.Exec(`UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2`, next, botUID); err != nil {
		return nil, nil, fmt.Errorf("save bot definition for skill mutation: %w", err)
	}

	mutation, _, err = scanPostgresBotSkillMutation(tx.QueryRow(
		`UPDATE bot_skill_mutations
		 SET status = $1, definition_revision = $2, lease_expires_at = $3,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE bot_uid = $4 AND id = $5 AND status = $6 AND lease_generation = $7
		 RETURNING `+postgresBotSkillMutationColumns,
		string(types.BotSkillMutationDefinitionCommitted), record.Runtime.DesiredRevision,
		leaseExpiresAt, botUID, mutationID, string(types.BotSkillMutationVersionReady), expectedLeaseGeneration,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, store.ErrBotSkillMutationStateConflict
	}
	if err != nil {
		return nil, nil, fmt.Errorf("commit bot skill mutation definition fact: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit bot skill mutation definition: %w", err)
	}
	return mutation, record, nil
}

func (a *Adapter) RenewBotSkillMutationLease(
	botUID, mutationID, expectedLeaseGeneration int64,
	expected types.BotSkillMutationStatus,
	now time.Time,
	leaseTTL time.Duration,
) (*types.BotSkillMutation, error) {
	if expectedLeaseGeneration <= 0 || store.IsTerminalBotSkillMutationStatus(expected) {
		return nil, store.ErrBotSkillMutationStateConflict
	}
	if _, ok := types.ParseBotSkillMutationStatus(string(expected)); !ok {
		return nil, store.ErrBotSkillMutationStateConflict
	}
	leaseExpiresAt, err := store.ValidateBotSkillMutationLease(now, leaseTTL)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	mutation, _, err := scanPostgresBotSkillMutation(a.db.QueryRow(
		`UPDATE bot_skill_mutations
		 SET lease_generation = lease_generation + 1, lease_expires_at = $1,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE bot_uid = $2 AND id = $3 AND status = $4 AND lease_generation = $5
		   AND lease_expires_at > $6
		 RETURNING `+postgresBotSkillMutationColumns,
		leaseExpiresAt, botUID, mutationID, string(expected), expectedLeaseGeneration, now,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, a.classifyBotSkillMutationCASFailure(botUID, mutationID, expectedLeaseGeneration, expected, now)
	}
	if err != nil {
		return nil, fmt.Errorf("renew bot skill mutation lease: %w", err)
	}
	return mutation, nil
}

func (a *Adapter) classifyBotSkillMutationCASFailure(
	botUID, mutationID, expectedLeaseGeneration int64,
	expected types.BotSkillMutationStatus,
	now time.Time,
) error {
	mutation, err := a.GetBotSkillMutation(botUID, mutationID)
	if err != nil {
		return err
	}
	if mutation.Status != expected || mutation.LeaseGeneration != expectedLeaseGeneration {
		return store.ErrBotSkillMutationStateConflict
	}
	if !now.IsZero() && !mutation.LeaseExpiresAt.After(now) {
		return store.ErrBotSkillMutationLeaseExpired
	}
	return store.ErrBotSkillMutationStateConflict
}

func skillMutationNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func skillMutationNullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func skillMutationNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

package mysql

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const mysqlBotSkillMutationColumns = `
id, bot_uid, local_skill_id, actor_user_uid, source_topic_id, source_message_id,
runtime_body_id, client_request_id, request_fingerprint, operation,
candidate_content_hash, expected_definition_revision,
COALESCE(expected_previous_content_hash, ''), before_reference, after_reference,
COALESCE(git_commit_sha, ''), definition_revision, status,
COALESCE(error_code, ''), COALESCE(error_summary, ''), rollback_of,
lease_generation, lease_expires_at, created_at, updated_at, activated_at`

type mysqlBotSkillMutationScanner interface {
	Scan(dest ...any) error
}

func scanMySQLBotSkillMutation(row mysqlBotSkillMutationScanner) (*types.BotSkillMutation, string, error) {
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
	if mutation.BeforeReference, err = decodeMySQLBotSkillMutationRef(beforeRaw); err != nil {
		return nil, "", fmt.Errorf("decode before reference: %w", err)
	}
	if mutation.AfterReference, err = decodeMySQLBotSkillMutationRef(afterRaw); err != nil {
		return nil, "", fmt.Errorf("decode after reference: %w", err)
	}
	return mutation, requestFingerprint, nil
}

func decodeMySQLBotSkillMutationRef(raw []byte) (*types.BotSkillRef, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var ref types.BotSkillRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, err
	}
	return &ref, nil
}

func encodeMySQLBotSkillMutationRef(ref *types.BotSkillRef) (any, error) {
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
	beforeRaw, err := encodeMySQLBotSkillMutationRef(input.BeforeReference)
	if err != nil {
		return nil, false, fmt.Errorf("encode before reference: %w", err)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin bot skill mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lockedBotUID int64
	if err := tx.QueryRow(`SELECT user_id FROM bot_config WHERE user_id = ? FOR UPDATE`, input.BotUID).Scan(&lockedBotUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, store.ErrBotSkillMutationNotFound
		}
		return nil, false, fmt.Errorf("lock bot skill mutation target: %w", err)
	}

	existing, existingFingerprint, err := scanMySQLBotSkillMutation(tx.QueryRow(
		`SELECT `+mysqlBotSkillMutationColumns+`
		 FROM bot_skill_mutations
		 WHERE actor_user_uid = ? AND bot_uid = ? AND client_request_id = ?`,
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

	active, _, err := scanMySQLBotSkillMutation(tx.QueryRow(
		`SELECT `+mysqlBotSkillMutationColumns+`
		 FROM bot_skill_mutations
		 WHERE bot_uid = ?
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

	result, err := tx.Exec(
		`INSERT INTO bot_skill_mutations (
			bot_uid, local_skill_id, actor_user_uid, source_topic_id, source_message_id,
			runtime_body_id, client_request_id, request_fingerprint, operation,
			candidate_content_hash, expected_definition_revision,
			expected_previous_content_hash, before_reference, rollback_of, lease_expires_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,CAST(? AS JSON),?,?)`,
		input.BotUID, input.LocalSkillID, input.ActorUserUID, input.SourceTopicID,
		input.SourceMessageID, input.RuntimeBodyID, input.ClientRequestID, fingerprint,
		string(input.Operation), input.CandidateContentHash, input.ExpectedDefinitionRevision,
		skillMutationNullableString(input.ExpectedPreviousContentHash), beforeRaw,
		skillMutationNullableInt64(input.RollbackOf), leaseExpiresAt,
	)
	if err != nil {
		var mysqlErr *mysqlDriver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			if strings.Contains(mysqlErr.Message, "uk_bot_skill_mutations_request") {
				return nil, false, store.ErrBotSkillMutationIdempotencyConflict
			}
			if strings.Contains(mysqlErr.Message, "uk_bot_skill_mutations_active") {
				return nil, false, store.ErrBotSkillMutationBusy
			}
		}
		return nil, false, fmt.Errorf("insert bot skill mutation: %w", err)
	}
	mutationID, err := result.LastInsertId()
	if err != nil {
		return nil, false, fmt.Errorf("read bot skill mutation id: %w", err)
	}
	mutation, _, err := scanMySQLBotSkillMutation(tx.QueryRow(
		`SELECT `+mysqlBotSkillMutationColumns+` FROM bot_skill_mutations WHERE bot_uid = ? AND id = ?`,
		input.BotUID, mutationID,
	))
	if err != nil {
		return nil, false, fmt.Errorf("read inserted bot skill mutation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit bot skill mutation: %w", err)
	}
	return mutation, true, nil
}

func (a *Adapter) GetBotSkillMutation(botUID, mutationID int64) (*types.BotSkillMutation, error) {
	mutation, _, err := scanMySQLBotSkillMutation(a.db.QueryRow(
		`SELECT `+mysqlBotSkillMutationColumns+` FROM bot_skill_mutations WHERE bot_uid = ? AND id = ?`,
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
	afterRaw, err := encodeMySQLBotSkillMutationRef(patch.AfterReference)
	if err != nil {
		return nil, fmt.Errorf("encode after reference: %w", err)
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin bot skill mutation advance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(
		`UPDATE bot_skill_mutations SET
			status = ?,
			after_reference = COALESCE(CAST(? AS JSON), after_reference),
			git_commit_sha = COALESCE(?, git_commit_sha),
			definition_revision = COALESCE(?, definition_revision),
			error_code = COALESCE(?, error_code),
			error_summary = COALESCE(?, error_summary),
			activated_at = COALESCE(?, activated_at),
			lease_expires_at = ?
		 WHERE bot_uid = ? AND id = ? AND status = ?
		   AND lease_generation = ? AND lease_expires_at > ?`,
		string(next), afterRaw, patch.GitCommitSHA, patch.DefinitionRevision,
		patch.ErrorCode, patch.ErrorSummary, skillMutationNullableTime(patch.ActivatedAt),
		leaseExpiresAt, botUID, mutationID, string(expected), expectedLeaseGeneration, now,
	)
	if err != nil {
		return nil, fmt.Errorf("advance bot skill mutation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read bot skill mutation advance result: %w", err)
	}
	if affected == 0 {
		_ = tx.Rollback()
		return nil, a.classifyBotSkillMutationCASFailure(botUID, mutationID, expectedLeaseGeneration, expected, now)
	}
	mutation, _, err := scanMySQLBotSkillMutation(tx.QueryRow(
		`SELECT `+mysqlBotSkillMutationColumns+` FROM bot_skill_mutations WHERE bot_uid = ? AND id = ?`,
		botUID, mutationID,
	))
	if err != nil {
		return nil, fmt.Errorf("read advanced bot skill mutation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bot skill mutation advance: %w", err)
	}
	return mutation, nil
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
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin bot skill mutation lease renewal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(
		`UPDATE bot_skill_mutations
		 SET lease_generation = lease_generation + 1, lease_expires_at = ?
		 WHERE bot_uid = ? AND id = ? AND status = ? AND lease_generation = ?
		   AND lease_expires_at > ?`,
		leaseExpiresAt, botUID, mutationID, string(expected), expectedLeaseGeneration, now,
	)
	if err != nil {
		return nil, fmt.Errorf("renew bot skill mutation lease: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read bot skill mutation lease renewal: %w", err)
	}
	if affected == 0 {
		_ = tx.Rollback()
		return nil, a.classifyBotSkillMutationCASFailure(botUID, mutationID, expectedLeaseGeneration, expected, now)
	}
	mutation, _, err := scanMySQLBotSkillMutation(tx.QueryRow(
		`SELECT `+mysqlBotSkillMutationColumns+` FROM bot_skill_mutations WHERE bot_uid = ? AND id = ?`,
		botUID, mutationID,
	))
	if err != nil {
		return nil, fmt.Errorf("read renewed bot skill mutation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bot skill mutation lease renewal: %w", err)
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

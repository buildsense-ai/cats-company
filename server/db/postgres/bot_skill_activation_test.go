package postgres

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func postgresActivationFixture(t *testing.T) ([]byte, []byte, types.BotSkillMutationActivationInput, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	ref := types.BotSkillRef{
		Source: "skillhub", SkillID: "private-1", Version: "1.0.1", ContentHash: strings.Repeat("a", 64),
	}
	hash, err := store.CanonicalBotSkillSetHash([]types.BotSkillRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.EncodeBotDefinitionJSON(nil, &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema, BotID: "42",
			Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
			Skills: []types.BotSkillRef{ref},
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 11},
		Exists:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, err := encodePostgresBotSkillMutationRef(&ref)
	if err != nil {
		t.Fatal(err)
	}
	return raw, afterRaw.([]byte), types.BotSkillMutationActivationInput{
		BotUID: 42, MutationID: 101, AppliedDefinitionRevision: 11,
		SkillSetHash: hash, RuntimeBodyID: "runtime:cloud-1", RuntimeInstallationID: "install-prod-1",
	}, now
}

func postgresBotSkillMutationFailureRow(after []byte, expires time.Time, code, summary string) *sqlmock.Rows {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	return sqlmock.NewRows(postgresBotSkillMutationTestColumns).AddRow(
		int64(101), int64(42), "review-pr", int64(7), "p2p_7_42", int64(99),
		"runtime:cloud-1", "request-0001", strings.Repeat("f", 64), "create",
		strings.Repeat("a", 64), int64(10), "", nil, after, strings.Repeat("e", 40), int64(11),
		string(types.BotSkillMutationCompensationPending), code, summary, nil, int64(1), expires, now, now, nil,
	)
}

func TestActivateBotSkillMutationCommitsDefinitionAndFactAtomically(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	raw, afterRaw, input, now := postgresActivationFixture(t)
	expires := now.Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE")).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectQuery(regexp.QuoteMeta("FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2 FOR UPDATE")).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(postgresBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationActivationPending, 1, expires, afterRaw, strings.Repeat("e", 40)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2")).
		WithArgs(sqlmock.AnyArg(), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE bot_skill_mutations SET status = $1, activated_at = $2")).
		WithArgs("active", now, int64(11), input.SkillSetHash, input.RuntimeBodyID, input.RuntimeInstallationID,
			int64(42), int64(101), "activation_pending").
		WillReturnRows(postgresBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationActive, 1, expires, afterRaw, strings.Repeat("e", 40)))
	mock.ExpectCommit()

	mutation, definition, idempotent, err := (&Adapter{db: sqlDB}).ActivateBotSkillMutation(input, now)
	if err != nil {
		t.Fatalf("activate mutation: %v", err)
	}
	if idempotent || mutation.Status != types.BotSkillMutationActive || definition.Runtime.AppliedRevision != 11 {
		t.Fatalf("mutation=%#v definition=%#v idempotent=%v", mutation, definition, idempotent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestActivateBotSkillMutationRollsBackDefinitionWhenFactWriteFails(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	raw, afterRaw, input, now := postgresActivationFixture(t)
	expires := now.Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE")).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectQuery(regexp.QuoteMeta("FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2 FOR UPDATE")).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(postgresBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationActivationPending, 1, expires, afterRaw, strings.Repeat("e", 40)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2")).
		WithArgs(sqlmock.AnyArg(), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE bot_skill_mutations SET status = $1, activated_at = $2")).
		WillReturnError(errors.New("fact write failed"))
	mock.ExpectRollback()

	_, _, _, err = (&Adapter{db: sqlDB}).ActivateBotSkillMutation(input, now)
	if err == nil {
		t.Fatal("activation unexpectedly succeeded")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordBotSkillMutationActivationFailureReplaysPersistedFactAfterDefinitionAdvances(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	raw, afterRaw, activation, now := postgresActivationFixture(t)
	record, err := store.DecodeBotDefinitionJSON(raw, activation.BotUID)
	if err != nil {
		t.Fatal(err)
	}
	record.Runtime.DesiredRevision = 12
	raw, err = store.EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(time.Minute)
	summary := "A Skill package failed integrity verification"
	input := types.BotSkillMutationActivationFailureInput{
		BotUID: 42, MutationID: 101, AttemptedDefinitionRevision: 11,
		RuntimeBodyID: "runtime:cloud-1", RuntimeInstallationID: "install-prod-1",
		ErrorCode: "PACKAGE_HASH_MISMATCH", ErrorSummary: summary, Permanent: true,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE")).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectQuery(regexp.QuoteMeta("FROM bot_skill_mutations WHERE bot_uid = $1 AND id = $2 FOR UPDATE")).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(postgresBotSkillMutationFailureRow(afterRaw, expires, input.ErrorCode, summary))
	mock.ExpectQuery("SELECT activation_definition_revision").
		WithArgs(int64(42), int64(101)).
		WillReturnRows(sqlmock.NewRows([]string{
			"activation_definition_revision", "activation_skill_set_hash",
			"activation_runtime_body_id", "activation_installation_id",
		}).AddRow(int64(11), nil, input.RuntimeBodyID, input.RuntimeInstallationID))
	mock.ExpectCommit()

	_, _, idempotent, err := (&Adapter{db: sqlDB}).RecordBotSkillMutationActivationFailure(input, now)
	if err != nil || !idempotent {
		t.Fatalf("idempotent=%v err=%v", idempotent, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

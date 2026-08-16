package mysql

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var mysqlBotSkillMutationTestColumns = []string{
	"id", "bot_uid", "local_skill_id", "actor_user_uid", "source_topic_id", "source_message_id",
	"runtime_body_id", "client_request_id", "request_fingerprint", "operation",
	"candidate_content_hash", "expected_definition_revision", "expected_previous_content_hash",
	"before_reference", "after_reference", "git_commit_sha", "definition_revision", "status",
	"error_code", "error_summary", "rollback_of", "lease_generation", "lease_expires_at",
	"created_at", "updated_at", "activated_at",
}

func mysqlBotSkillMutationTestRow(
	id int64,
	fingerprint string,
	status types.BotSkillMutationStatus,
	leaseGeneration int64,
	leaseExpiresAt time.Time,
	after any,
	gitCommit string,
) *sqlmock.Rows {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	var definitionRevision any
	if status == types.BotSkillMutationDefinitionCommitted {
		definitionRevision = int64(11)
	}
	return sqlmock.NewRows(mysqlBotSkillMutationTestColumns).AddRow(
		id, int64(42), "review-pr", int64(7), "p2p_7_42", int64(99),
		"runtime:cloud-1", "request-0001", fingerprint, "create",
		strings.Repeat("a", 64), int64(10), "", nil, after, gitCommit, definitionRevision, string(status),
		"", "", nil, leaseGeneration, leaseExpiresAt, now, now, nil,
	)
}

func mysqlMutationCreateInput() types.BotSkillMutationCreateInput {
	return types.BotSkillMutationCreateInput{
		BotUID: 42, LocalSkillID: "review-pr", ActorUserUID: 7,
		SourceTopicID: "p2p_7_42", SourceMessageID: 99,
		RuntimeBodyID: "runtime:cloud-1", ClientRequestID: "request-0001",
		Operation: types.BotSkillMutationCreate, CandidateContentHash: strings.Repeat("a", 64),
		ExpectedDefinitionRevision: 10,
	}
}

func TestBeginBotSkillMutationCreatesSerializedFact(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	input := mysqlMutationCreateInput()
	_, fingerprint, err := store.NormalizeBotSkillMutationCreateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id FROM bot_config WHERE user_id = ? FOR UPDATE`)).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(42)))
	mock.ExpectQuery(`FROM bot_skill_mutations\s+WHERE actor_user_uid = \? AND bot_uid = \? AND client_request_id = \?`).
		WithArgs(int64(7), int64(42), "request-0001").WillReturnRows(sqlmock.NewRows(mysqlBotSkillMutationTestColumns))
	mock.ExpectQuery(`FROM bot_skill_mutations\s+WHERE bot_uid = \?\s+AND status IN`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows(mysqlBotSkillMutationTestColumns))
	mock.ExpectExec(`INSERT INTO bot_skill_mutations`).
		WithArgs(
			int64(42), "review-pr", int64(7), "p2p_7_42", int64(99),
			"runtime:cloud-1", "request-0001", fingerprint, "create", strings.Repeat("a", 64),
			int64(10), nil, nil, nil, expires,
		).WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \?`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(101, fingerprint, types.BotSkillMutationValidating, 1, expires, nil, ""))
	mock.ExpectCommit()

	mutation, created, err := (&Adapter{db: sqlDB}).BeginBotSkillMutation(input, now, 2*time.Minute)
	if err != nil {
		t.Fatalf("begin mutation: %v", err)
	}
	if !created || mutation.ID != 101 || mutation.Status != types.BotSkillMutationValidating || mutation.LeaseGeneration != 1 {
		t.Fatalf("created=%v mutation=%#v", created, mutation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginBotSkillMutationRejectsChangedIdempotentPayload(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	input := mysqlMutationCreateInput()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id FROM bot_config WHERE user_id = ? FOR UPDATE`)).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(42)))
	mock.ExpectQuery(`FROM bot_skill_mutations`).
		WithArgs(int64(7), int64(42), "request-0001").
		WillReturnRows(mysqlBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationValidating, 1, now.Add(time.Minute), nil, ""))
	mock.ExpectRollback()

	if _, _, err := (&Adapter{db: sqlDB}).BeginBotSkillMutation(input, now, time.Minute); err != store.ErrBotSkillMutationIdempotencyConflict {
		t.Fatalf("err=%v, want idempotency conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginBotSkillMutationBlocksActiveOrExpiredRecovery(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		expires time.Time
		wantErr error
	}{
		{name: "active lease", expires: now.Add(time.Minute), wantErr: store.ErrBotSkillMutationBusy},
		{name: "expired lease", expires: now.Add(-time.Second), wantErr: store.ErrBotSkillMutationRecoveryRequired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT user_id FROM bot_config WHERE user_id = ? FOR UPDATE`)).
				WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(42)))
			mock.ExpectQuery(`FROM bot_skill_mutations\s+WHERE actor_user_uid`).
				WithArgs(int64(7), int64(42), "request-0001").
				WillReturnRows(sqlmock.NewRows(mysqlBotSkillMutationTestColumns))
			mock.ExpectQuery(`FROM bot_skill_mutations\s+WHERE bot_uid = \?\s+AND status IN`).
				WithArgs(int64(42)).
				WillReturnRows(mysqlBotSkillMutationTestRow(
					100, strings.Repeat("f", 64), types.BotSkillMutationVersionReady, 3, tt.expires, nil, "",
				))
			mock.ExpectRollback()

			if _, _, err := (&Adapter{db: sqlDB}).BeginBotSkillMutation(
				mysqlMutationCreateInput(), now, time.Minute,
			); err != tt.wantErr {
				t.Fatalf("err=%v, want %v", err, tt.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdvanceAndRenewBotSkillMutationUseStateAndLeaseCAS(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Minute)
	ref := &types.BotSkillRef{Source: "skillhub", SkillID: "private-1", Version: "1.0.1", ContentHash: strings.Repeat("a", 64)}
	commit := strings.Repeat("e", 40)
	afterRaw, _ := encodeMySQLBotSkillMutationRef(ref)

	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \?`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationValidating, 1, expires, nil, ""))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE bot_skill_mutations SET`).
		WithArgs(
			"version_ready", afterRaw, commit, nil, nil, nil, nil, expires,
			int64(42), int64(101), "validating", int64(1), now,
		).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \?`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationVersionReady, 1, expires, afterRaw, commit))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE bot_skill_mutations\s+SET lease_generation = lease_generation \+ 1`).
		WithArgs(expires, int64(42), int64(101), "version_ready", int64(1), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \?`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationVersionReady, 2, expires, afterRaw, commit))
	mock.ExpectCommit()

	adapter := &Adapter{db: sqlDB}
	mutation, err := adapter.AdvanceBotSkillMutation(
		42, 101, 1, types.BotSkillMutationValidating, types.BotSkillMutationVersionReady,
		types.BotSkillMutationTransition{AfterReference: ref, GitCommitSHA: &commit}, now, 2*time.Minute,
	)
	if err != nil || mutation.Status != types.BotSkillMutationVersionReady {
		t.Fatalf("advance mutation=%#v err=%v", mutation, err)
	}
	mutation, err = adapter.RenewBotSkillMutationLease(42, 101, 1, types.BotSkillMutationVersionReady, now, 2*time.Minute)
	if err != nil || mutation.LeaseGeneration != 2 {
		t.Fatalf("renew mutation=%#v err=%v", mutation, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRenewBotSkillMutationLeaseRejectsExpiredLease(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	expiredAt := now.Add(-time.Second)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE bot_skill_mutations\s+SET lease_generation = lease_generation \+ 1`).
		WithArgs(now.Add(time.Minute), int64(42), int64(101), "version_ready", int64(1), now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \?`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(
			101, strings.Repeat("f", 64), types.BotSkillMutationVersionReady, 1, expiredAt, nil, "",
		))

	_, err = (&Adapter{db: sqlDB}).RenewBotSkillMutationLease(
		42, 101, 1, types.BotSkillMutationVersionReady, now, time.Minute,
	)
	if err != store.ErrBotSkillMutationLeaseExpired {
		t.Fatalf("err=%v, want lease expired", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdvanceBotSkillMutationRequiresAtomicDefinitionCommitPath(t *testing.T) {
	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	revision := int64(11)
	_, err = (&Adapter{db: sqlDB}).AdvanceBotSkillMutation(
		42, 101, 1,
		types.BotSkillMutationVersionReady,
		types.BotSkillMutationDefinitionCommitted,
		types.BotSkillMutationTransition{DefinitionRevision: &revision},
		time.Now(), time.Minute,
	)
	if err != store.ErrBotSkillMutationAtomicCommitRequired {
		t.Fatalf("err=%v, want atomic commit required", err)
	}
}

func TestCommitBotSkillMutationDefinitionIsOneTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Minute)
	ref := &types.BotSkillRef{Source: "skillhub", SkillID: "private-1", Version: "1.0.1", ContentHash: strings.Repeat("a", 64)}
	afterRaw, _ := encodeMySQLBotSkillMutationRef(ref)
	raw, err := store.EncodeBotDefinitionJSON(nil, &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "42",
			Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
			Prompt: &types.BotPromptDefinition{Selected: "default"},
			Skills: []types.BotSkillRef{},
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 10},
		Exists:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(config, JSON_OBJECT\(\)\) FROM bot_config WHERE user_id = \? FOR UPDATE`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \? FOR UPDATE`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationVersionReady, 1, expires, afterRaw, strings.Repeat("e", 40)))
	mock.ExpectExec(`UPDATE bot_config SET config = \? WHERE user_id = \?`).
		WithArgs(sqlmock.AnyArg(), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE bot_skill_mutations\s+SET status = \?, definition_revision = \?, lease_expires_at = \?`).
		WithArgs("definition_committed", int64(11), expires, int64(42), int64(101), "version_ready", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \?`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(101, strings.Repeat("f", 64), types.BotSkillMutationDefinitionCommitted, 1, expires, afterRaw, strings.Repeat("e", 40)))
	mock.ExpectCommit()

	mutation, definition, err := (&Adapter{db: sqlDB}).CommitBotSkillMutationDefinition(42, 101, 1, now, 2*time.Minute)
	if err != nil {
		t.Fatalf("commit definition mutation: %v", err)
	}
	if mutation.Status != types.BotSkillMutationDefinitionCommitted || mutation.DefinitionRevision == nil || *mutation.DefinitionRevision != 11 {
		t.Fatalf("unexpected mutation: %#v", mutation)
	}
	if definition.Runtime.DesiredRevision != 11 || len(definition.Definition.Skills) != 1 || definition.Definition.Skills[0] != *ref {
		t.Fatalf("unexpected definition: %#v", definition)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCommitBotSkillMutationDefinitionRollsBackWhenFactCommitFails(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Minute)
	ref := &types.BotSkillRef{Source: "skillhub", SkillID: "private-1", Version: "1.0.1", ContentHash: strings.Repeat("a", 64)}
	afterRaw, _ := encodeMySQLBotSkillMutationRef(ref)
	raw, err := store.EncodeBotDefinitionJSON(nil, &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "42",
			Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
			Prompt: &types.BotPromptDefinition{Selected: "default"},
			Skills: []types.BotSkillRef{},
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 10},
		Exists:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(config, JSON_OBJECT\(\)\) FROM bot_config WHERE user_id = \? FOR UPDATE`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"config"}).AddRow(raw))
	mock.ExpectQuery(`FROM bot_skill_mutations WHERE bot_uid = \? AND id = \? FOR UPDATE`).
		WithArgs(int64(42), int64(101)).
		WillReturnRows(mysqlBotSkillMutationTestRow(
			101, strings.Repeat("f", 64), types.BotSkillMutationVersionReady, 1, expires, afterRaw, strings.Repeat("e", 40),
		))
	mock.ExpectExec(`UPDATE bot_config SET config = \? WHERE user_id = \?`).
		WithArgs(sqlmock.AnyArg(), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE bot_skill_mutations\s+SET status = \?, definition_revision = \?, lease_expires_at = \?`).
		WithArgs("definition_committed", int64(11), expires, int64(42), int64(101), "version_ready", int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, _, err = (&Adapter{db: sqlDB}).CommitBotSkillMutationDefinition(42, 101, 1, now, 2*time.Minute)
	if err != store.ErrBotSkillMutationStateConflict {
		t.Fatalf("err=%v, want state conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

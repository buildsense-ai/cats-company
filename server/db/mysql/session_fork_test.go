package mysql

import (
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var sessionForkColumns = []string{"actor_user_id", "request_id", "source_topic_id", "target_topic_id", "agent_uid", "body_id", "request_name", "source_revision", "target_revision", "source_last_seq", "message_id_map", "state", "error", "created_at", "source_turn_count"}

func TestSessionForkMySQLCloneTransaction(t *testing.T) {
	for _, failCopy := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "rollback"}[failCopy], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			a := &Adapter{db: db}
			ctx := context.Background()
			stamp := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT account_type FROM users").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_type"}).AddRow("human"))
			mock.ExpectQuery("SELECT .* FROM session_forks WHERE actor_user_id").WithArgs(int64(7), "one").WillReturnRows(sqlmock.NewRows(sessionForkColumns))
			mock.ExpectQuery("SELECT history_revision, fork_state FROM topics").WithArgs("p2p_7_9").WillReturnRows(sqlmock.NewRows([]string{"history_revision", "fork_state"}).AddRow(2, ""))
			mock.ExpectQuery("SELECT b.owner_id,b.body_id,u.account_type,u.state FROM bot_config").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id", "body_id", "account_type", "state"}).AddRow(7, "body", "bot", 0))
			mock.ExpectQuery("SELECT id,from_uid,content,msg_type,content_blocks,mode,role,metadata,reply_to,created_at FROM messages").WithArgs("p2p_7_9").WillReturnRows(sqlmock.NewRows([]string{"id", "from_uid", "content", "msg_type", "content_blocks", "mode", "role", "metadata", "reply_to", "created_at"}).AddRow(10, 7, "file", "file", `[{"type":"file","payload":{"url":"/uploads/files/report.pdf"}}]`, "normal", "user", `{"custom":"keep"}`, nil, stamp).AddRow(11, 9, "answer", "text", nil, "normal", "assistant", nil, 10, stamp))
			mock.ExpectExec("INSERT INTO `groups`").WithArgs("\u4f1a\u8bdd\u526f\u672c", int64(7)).WillReturnResult(sqlmock.NewResult(20, 1))
			mock.ExpectExec("INSERT INTO group_members").WithArgs(int64(20), int64(7), int64(20), int64(9)).WillReturnResult(sqlmock.NewResult(0, 2))
			mock.ExpectExec("INSERT INTO topics").WithArgs("grp_20", "\u4f1a\u8bdd\u526f\u672c", int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
			copyExpectation := mock.ExpectExec("INSERT INTO messages").WithArgs("grp_20", int64(7), "file", "file", `[{"type":"file","payload":{"url":"/uploads/files/report.pdf"}}]`, "normal", "user", `{"custom":"keep"}`, stamp)
			if failCopy {
				copyExpectation.WillReturnError(errors.New("copy failure"))
				mock.ExpectRollback()
			} else {
				copyExpectation.WillReturnResult(sqlmock.NewResult(100, 1))
				mock.ExpectExec("INSERT INTO messages").WithArgs("grp_20", int64(9), "answer", "text", nil, "normal", "assistant", nil, stamp).WillReturnResult(sqlmock.NewResult(101, 1))
				mock.ExpectExec("UPDATE messages SET reply_to").WithArgs(nil, `{"custom":"keep"}`, int64(100)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE messages SET reply_to").WithArgs(int64(100), nil, int64(101)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("SELECT history_revision FROM topics").WithArgs("grp_20").WillReturnRows(sqlmock.NewRows([]string{"history_revision"}).AddRow(4))
				mock.ExpectExec("INSERT INTO session_forks").WithArgs(int64(7), "one", "p2p_7_9", "grp_20", int64(9), "body", "", int64(2), int64(4), int64(11), `{"10":100,"11":101}`, "pending", "", sqlmock.AnyArg(), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE topics SET fork_state = 'pending'").WithArgs("grp_20").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			got, err := a.CreateSessionFork(ctx, types.SessionForkRequest{RequestID: "one", SourceTopicID: "p2p_7_9", ActorUserID: 7, AgentUID: 9, BodyID: "body"})
			if failCopy {
				if err == nil || got != nil {
					t.Fatal("copy failure committed")
				}
			} else if err != nil || got.State != "pending" || got.TargetTopicID != "grp_20" || got.MessageIDMap["11"] != 101 {
				t.Fatalf("clone %#v %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLCloneIsBoundedByAnchor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &Adapter{db: db}
	ctx := context.Background()
	stamp := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_type FROM users").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_type"}).AddRow("human"))
	mock.ExpectQuery("SELECT .* FROM session_forks WHERE actor_user_id").WithArgs(int64(7), "cut").WillReturnRows(sqlmock.NewRows(sessionForkColumns))
	mock.ExpectQuery("SELECT history_revision, fork_state FROM topics").WithArgs("p2p_7_9").WillReturnRows(sqlmock.NewRows([]string{"history_revision", "fork_state"}).AddRow(6, ""))
	mock.ExpectQuery("SELECT b.owner_id,b.body_id,u.account_type,u.state FROM bot_config").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id", "body_id", "account_type", "state"}).AddRow(7, "body", "bot", 0))
	// Only history up to the anchor is read; the later turns of the conversation
	// must never be copied into the branch.
	mock.ExpectQuery("SELECT .* FROM messages WHERE topic_id = \\? AND id <= \\?").WithArgs("p2p_7_9", int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "from_uid", "content", "msg_type", "content_blocks", "mode", "role", "metadata", "reply_to", "created_at"}).
			AddRow(9, 7, "first", "text", nil, "normal", "user", nil, nil, stamp).
			AddRow(10, 9, "first answer", "text", nil, "normal", "assistant", nil, 9, stamp))
	mock.ExpectExec("INSERT INTO `groups`").WithArgs("\u4f1a\u8bdd\u526f\u672c", int64(7)).WillReturnResult(sqlmock.NewResult(20, 1))
	mock.ExpectExec("INSERT INTO group_members").WithArgs(int64(20), int64(7), int64(20), int64(9)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("INSERT INTO topics").WithArgs("grp_20", "\u4f1a\u8bdd\u526f\u672c", int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO messages").WithArgs("grp_20", int64(7), "first", "text", nil, "normal", "user", nil, stamp).WillReturnResult(sqlmock.NewResult(100, 1))
	mock.ExpectExec("INSERT INTO messages").WithArgs("grp_20", int64(9), "first answer", "text", nil, "normal", "assistant", nil, stamp).WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectExec("UPDATE messages SET reply_to").WithArgs(nil, nil, int64(100)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE messages SET reply_to").WithArgs(int64(100), nil, int64(101)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT history_revision FROM topics").WithArgs("grp_20").WillReturnRows(sqlmock.NewRows([]string{"history_revision"}).AddRow(2))
	mock.ExpectExec("INSERT INTO session_forks").WithArgs(int64(7), "cut", "p2p_7_9", "grp_20", int64(9), "body", "", int64(6), int64(2), int64(10), `{"10":101,"9":100}`, "pending", "", sqlmock.AnyArg(), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE topics SET fork_state = 'pending'").WithArgs("grp_20").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, err := a.CreateSessionFork(ctx, types.SessionForkRequest{RequestID: "cut", SourceTopicID: "p2p_7_9", ActorUserID: 7, AgentUID: 9, BodyID: "body", UntilMessageID: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The recorded boundary is the anchor, so the runtime copies the same range.
	if got.SourceLastSeq != 10 || len(got.MessageIDMap) != 2 || got.MessageIDMap["11"] != 0 {
		t.Fatalf("bounded clone %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionForkMySQLAnchorOutsideSourceFailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_type FROM users").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_type"}).AddRow("human"))
	mock.ExpectQuery("SELECT .* FROM session_forks WHERE actor_user_id").WithArgs(int64(7), "bad").WillReturnRows(sqlmock.NewRows(sessionForkColumns))
	mock.ExpectQuery("SELECT history_revision, fork_state FROM topics").WithArgs("p2p_7_9").WillReturnRows(sqlmock.NewRows([]string{"history_revision", "fork_state"}).AddRow(6, ""))
	mock.ExpectQuery("SELECT b.owner_id,b.body_id,u.account_type,u.state FROM bot_config").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id", "body_id", "account_type", "state"}).AddRow(7, "body", "bot", 0))
	// The anchor does not exist in this conversation, so the last row is not it.
	mock.ExpectQuery("SELECT .* FROM messages WHERE topic_id = \\? AND id <= \\?").WithArgs("p2p_7_9", int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "from_uid", "content", "msg_type", "content_blocks", "mode", "role", "metadata", "reply_to", "created_at"}).
			AddRow(9, 7, "first", "text", nil, "normal", "user", nil, nil, time.Now()))
	mock.ExpectRollback()
	got, err := (&Adapter{db: db}).CreateSessionFork(context.Background(), types.SessionForkRequest{RequestID: "bad", SourceTopicID: "p2p_7_9", ActorUserID: 7, AgentUID: 9, BodyID: "body", UntilMessageID: 99})
	if !errors.Is(err, store.ErrSessionForkSource) || got != nil {
		t.Fatalf("anchor outside source must fail closed: %#v %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionForkMySQLAnchorMustCloseATurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows [][]sqldriver.Value
	}{{
		// The anchor is the user message that starts the next turn, so the copied
		// range would end mid-turn.
		name: "anchor opens a new turn",
		rows: [][]sqldriver.Value{
			{9, 7, "first", "text", nil, "normal", "user", nil, nil, time.Now()},
			{10, 9, "first answer", "text", nil, "normal", "assistant", nil, 9, time.Now()},
			{11, 7, "second", "text", nil, "normal", "user", nil, nil, time.Now()},
		},
	}, {
		// The anchor stops between a question and its answer.
		name: "anchor stops before the answer",
		rows: [][]sqldriver.Value{
			{9, 7, "first", "text", nil, "normal", "user", nil, nil, time.Now()},
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			anchor := int64(tc.rows[len(tc.rows)-1][0].(int))
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT account_type FROM users").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_type"}).AddRow("human"))
			mock.ExpectQuery("SELECT .* FROM session_forks WHERE actor_user_id").WithArgs(int64(7), "mid").WillReturnRows(sqlmock.NewRows(sessionForkColumns))
			mock.ExpectQuery("SELECT history_revision, fork_state FROM topics").WithArgs("p2p_7_9").WillReturnRows(sqlmock.NewRows([]string{"history_revision", "fork_state"}).AddRow(6, ""))
			mock.ExpectQuery("SELECT b.owner_id,b.body_id,u.account_type,u.state FROM bot_config").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"owner_id", "body_id", "account_type", "state"}).AddRow(7, "body", "bot", 0))
			mock.ExpectQuery("SELECT .* FROM messages WHERE topic_id = \\? AND id <= \\?").WithArgs("p2p_7_9", anchor).
				WillReturnRows(sqlmock.NewRows([]string{"id", "from_uid", "content", "msg_type", "content_blocks", "mode", "role", "metadata", "reply_to", "created_at"}).AddRows(tc.rows...))
			mock.ExpectRollback()
			got, err := (&Adapter{db: db}).CreateSessionFork(context.Background(), types.SessionForkRequest{RequestID: "mid", SourceTopicID: "p2p_7_9", ActorUserID: 7, AgentUID: 9, BodyID: "body", UntilMessageID: anchor})
			// The runtime cuts at a turn boundary, so accepting this would make the
			// copy's visible history and its model context disagree.
			if !errors.Is(err, store.ErrSessionForkSource) || got != nil {
				t.Fatalf("anchor inside a turn must fail closed: %#v %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLTerminalCompletionPrecedesSourceCheck(t *testing.T) {
	for _, state := range []string{"ready", "failed"} {
		t.Run(state, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .* FROM session_forks WHERE agent_uid").WithArgs(int64(9), "grp_20", "one").WillReturnRows(sqlmock.NewRows(sessionForkColumns).AddRow(7, "one", "p2p_7_9", "grp_20", 9, "body", "", 2, 4, 11, `{"10":100,"11":101}`, state, "", time.Now(), int64(1)))
			mock.ExpectCommit()
			got, err := (&Adapter{db: db}).CompleteSessionFork(context.Background(), 9, types.SessionForkCompletion{RequestID: "one", TargetTopicID: "grp_20", State: "ready", BodyID: "changed"})
			if err != nil || got.State != state {
				t.Fatalf("terminal %#v %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLCancelIsTerminalWins(t *testing.T) {
	for _, tc := range []struct {
		name, state, wantState, wantError string
		expectWrites                      bool
	}{
		{"pending is cancelled", "pending", "failed", types.SessionForkErrorCancelled, true},
		{"ready is untouched", "ready", "ready", "", false},
		{"failed keeps its reason", "failed", "failed", "source_changed_retry_needed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			initial := ""
			if tc.state == "failed" {
				initial = "source_changed_retry_needed"
			}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT .* FROM session_forks WHERE actor_user_id").WithArgs(int64(7), "one").
				WillReturnRows(sqlmock.NewRows(sessionForkColumns).AddRow(7, "one", "p2p_7_9", "grp_20", 9, "body", "", 2, 4, 11, `{}`, tc.state, initial, time.Now(), int64(1)))
			if tc.expectWrites {
				mock.ExpectExec("UPDATE session_forks SET state = ").WithArgs("failed", types.SessionForkErrorCancelled, int64(7), "one").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE topics SET fork_state").WithArgs("failed", "grp_20").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			got, err := (&Adapter{db: db}).CancelSessionFork(context.Background(), 7, "one")
			if err != nil || got.State != tc.wantState || got.Error != tc.wantError {
				t.Fatalf("cancel %#v %v want %q/%q", got, err, tc.wantState, tc.wantError)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLExpiryCountsOnlyRowsItMoved(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cutoff := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT actor_user_id, request_id, target_topic_id FROM session_forks WHERE state = 'pending'").WithArgs(cutoff, 100).
		WillReturnRows(sqlmock.NewRows([]string{"actor_user_id", "request_id", "target_topic_id"}).
			AddRow(7, "one", "grp_20").AddRow(8, "two", "grp_21"))
	mock.ExpectExec("UPDATE session_forks SET state = 'failed'").WithArgs(types.SessionForkErrorAckTimeout, int64(7), "one").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE topics SET fork_state = 'failed'").WithArgs("grp_20").WillReturnResult(sqlmock.NewResult(0, 1))
	// A concurrent completion already moved this row, so the reaper must not
	// count it as expired.
	mock.ExpectExec("UPDATE session_forks SET state = 'failed'").WithArgs(types.SessionForkErrorAckTimeout, int64(8), "two").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	expired, err := (&Adapter{db: db}).ExpireSessionForks(context.Background(), cutoff, 0)
	if err != nil || expired != 1 {
		t.Fatalf("expired=%d err=%v", expired, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionForkMySQLMigrationMatchesStartup(t *testing.T) {
	raw, err := os.ReadFile("../migrations/mysql/000041_session_forks.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, eol := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("EOL=%q", eol), func(t *testing.T) {
			migration := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", eol)
			for _, statement := range sessionForkSchema {
				if !strings.Contains(strings.ReplaceAll(migration, "\r\n", "\n"), strings.ReplaceAll(statement, "\r\n", "\n")) {
					t.Fatalf("startup statement absent from migration: %s", statement)
				}
			}
		})
	}
}

func TestSessionForkMySQLSchemaPermissionFailureIsActionableAndClosed(t *testing.T) {
	for _, code := range []uint16{1419, 1142, 1143, 1227} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cause := &driver.MySQLError{Number: code, Message: "permission denied"}
			for i, statement := range sessionForkSchema[:4] {
				expect := mock.ExpectExec(regexp.QuoteMeta(statement))
				if i == 3 {
					expect.WillReturnError(cause)
				} else {
					expect.WillReturnResult(sqlmock.NewResult(0, 0))
				}
			}
			err = (&Adapter{db: db}).ensureSessionForkSchema()
			if !errors.Is(err, ErrSessionForkSchema) {
				t.Fatalf("required schema failure must stop startup in every environment: %v", err)
			}
			var got *driver.MySQLError
			if !errors.As(err, &got) || got != cause {
				t.Fatalf("permission error must remain fatal and unwrap to original cause: %v", err)
			}
			for _, want := range []string{"administrator", "log_bin_trust_function_creators=1", "TRIGGER", "will not change global settings", "SUPER"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in startup diagnostic: %v", want, err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLIntegrationGrantGuard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grants []string
		ok     bool
	}{
		{"ordinary", []string{"GRANT USAGE ON *.* TO `catsco`@`%`", "GRANT ALL PRIVILEGES ON `cats\\_test`.* TO `catsco`@`%`"}, true},
		{"unescaped_wildcard", []string{"GRANT ALL PRIVILEGES ON `cats_test`.* TO `catsco`@`%`"}, false},
		{"double_escape", []string{"GRANT ALL PRIVILEGES ON `cats\\\\_test`.* TO `catsco`@`%`"}, false},
		{"super", []string{"GRANT SUPER ON *.* TO `catsco`@`%`"}, false},
		{"global_create", []string{"GRANT CREATE ON *.* TO `catsco`@`%`"}, false},
		{"global_all", []string{"GRANT ALL PRIVILEGES ON *.* TO `catsco`@`%`"}, false},
		{"global_dynamic", []string{"GRANT SYSTEM_VARIABLES_ADMIN ON *.* TO `catsco`@`%`"}, false},
		{"definer_privilege", []string{"GRANT SET_USER_ID ON *.* TO `catsco`@`%`"}, false},
		{"grant_option", []string{"GRANT ALL PRIVILEGES ON `cats_test`.* TO `catsco`@`%` WITH GRANT OPTION"}, false},
		{"role", []string{"GRANT `admin`@`%` TO `catsco`@`%`"}, false},
		{"proxy", []string{"GRANT PROXY ON 'root'@'localhost' TO 'catsco'@'%'"}, false},
		{"other_database", []string{"GRANT SELECT ON `other_db`.* TO `catsco`@`%`"}, false},
		{"system_database", []string{"GRANT SELECT ON `mysql`.* TO `catsco`@`%`"}, false},
		{"extra_database", []string{"GRANT ALL PRIVILEGES ON `cats_test`.* TO `catsco`@`%`", "GRANT SELECT ON `other_db`.* TO `catsco`@`%`"}, false},
		{"no_evidence", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := sessionForkMySQLCheckGrants("cats_test", tc.grants); (err == nil) != tc.ok {
				t.Fatalf("grant guard error=%v wantAllowed=%v", err, tc.ok)
			}
		})
	}
}

func TestSessionForkMySQLEscapedDatabaseGrantGuard(t *testing.T) {
	if err := sessionForkMySQLCheckGrants("cats`test", []string{"GRANT ALL PRIVILEGES ON `cats``test`.* TO `catsco`@`%`"}); err != nil {
		t.Fatal(err)
	}
	if err := sessionForkMySQLCheckGrants("cats`test", []string{"GRANT SELECT ON `cats_test`.* TO `catsco`@`%`"}); err == nil {
		t.Fatal("escaped database name accepted a different scope")
	}
	if err := sessionForkMySQLCheckGrants("cats%test", []string{"GRANT SELECT ON `cats\\%test`.* TO `catsco`@`%`"}); err != nil {
		t.Fatal(err)
	}
	if err := sessionForkMySQLCheckGrants("cats%test", []string{"GRANT SELECT ON `cats%test`.* TO `catsco`@`%`"}); err == nil {
		t.Fatal("unescaped percent wildcard accepted as a literal database")
	}
}

func TestSessionForkMySQLSchemaOtherErrorsAndReinstall(t *testing.T) {
	for _, code := range []uint16{0, 1064} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cause := &driver.MySQLError{Number: code, Message: "syntax error"}
			if code != 0 {
				mock.ExpectExec(regexp.QuoteMeta(sessionForkSchema[0])).WillReturnError(cause)
			} else {
				for i, statement := range sessionForkSchema {
					expect := mock.ExpectExec(regexp.QuoteMeta(statement))
					switch {
					case i < 2:
						expect.WillReturnError(&driver.MySQLError{Number: 1060})
					case i >= 3:
						expect.WillReturnError(&driver.MySQLError{Number: 1359})
					default:
						expect.WillReturnResult(sqlmock.NewResult(0, 0))
					}
				}
			}
			err = (&Adapter{db: db}).ensureSessionForkSchema()
			if code == 0 && err != nil {
				t.Fatalf("existing schema must reinstall: %v", err)
			} else if code != 0 && (!errors.Is(err, ErrSessionForkSchema) || !errors.Is(err, cause) || strings.Contains(err.Error(), "administrator")) {
				t.Fatalf("non-permission error misclassified: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionForkMySQLSchemaNonDriverFailure(t *testing.T) {
	// Any incomplete fork initialization must stop startup, including failures
	// after earlier DDL has committed. Preserve non-driver causes too.
	for failedStatement := range sessionForkSchema {
		t.Run(fmt.Sprint(failedStatement), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cause := errors.New("connection lost")
			for i, statement := range sessionForkSchema[:failedStatement+1] {
				expect := mock.ExpectExec(regexp.QuoteMeta(statement))
				if i == failedStatement {
					expect.WillReturnError(cause)
				} else {
					expect.WillReturnResult(sqlmock.NewResult(0, 0))
				}
			}
			err = (&Adapter{db: db}).ensureSessionForkSchema()
			if !errors.Is(err, ErrSessionForkSchema) || !errors.Is(err, cause) {
				t.Fatalf("required schema failure must preserve its cause: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

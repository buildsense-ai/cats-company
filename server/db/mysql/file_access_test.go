package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func newFileAccessMock(t *testing.T) (*Adapter, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return &Adapter{db: db}, mock
}

func expectFileMessageInsert(mock sqlmock.Sqlmock, args []driver.Value, cause error) {
	expect := mock.ExpectExec(`INSERT INTO messages .*NULLIF`).WithArgs(args...)
	if cause != nil {
		expect.WillReturnError(cause)
	} else {
		expect.WillReturnResult(sqlmock.NewResult(41, 1))
	}
}

func fileMessageDuplicateError() error { return &mysqlDriver.MySQLError{Number: 1062} }

const fileBindingConflictPattern = `ON DUPLICATE KEY UPDATE resource_hash = resource_hash`

func TestFileAccessUploadOwnershipAndMissing(t *testing.T) {
	a, mock := newFileAccessMock(t)
	ctx := context.Background()
	for _, owner := range []int64{7, 7, 9} {
		mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO file_uploads (object_key, uploader_uid) VALUES (?, ?) ON DUPLICATE KEY UPDATE object_key = object_key`)+`$`).WithArgs("objects/a", owner).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT uploader_uid FROM file_uploads`).WithArgs("objects/a").WillReturnRows(sqlmock.NewRows([]string{"uploader_uid"}).AddRow(7))
		err := a.RecordFileUpload(ctx, "objects/a", owner)
		if (err != nil) != (owner != 7) {
			t.Fatalf("owner %d: %v", owner, err)
		}
	}
	created := time.Now().UTC()
	for _, bound := range []bool{false, true} {
		mock.ExpectQuery(`SELECT object_key, uploader_uid, bound, created_at FROM file_uploads`).WithArgs("objects/a").WillReturnRows(sqlmock.NewRows([]string{"object_key", "uploader_uid", "bound", "created_at"}).AddRow("objects/a", 7, bound, created))
		upload, err := a.GetFileUpload(ctx, "objects/a")
		if err != nil || upload == nil || upload.ObjectKey != "objects/a" || upload.UploaderUID != 7 || upload.Bound != bound || !upload.CreatedAt.Equal(created) {
			t.Fatalf("upload: %+v %v", upload, err)
		}
	}
	mock.ExpectQuery(`SELECT object_key, uploader_uid, bound, created_at FROM file_uploads`).WithArgs("missing").WillReturnRows(sqlmock.NewRows([]string{"object_key", "uploader_uid", "bound", "created_at"}))
	if got, err := a.GetFileUpload(ctx, "missing"); got != nil || err != nil {
		t.Fatalf("missing: %+v %v", got, err)
	}
}

func TestFileAccessBindingsAreIdempotentAndManyToMany(t *testing.T) {
	a, mock := newFileAccessMock(t)
	ctx := context.Background()
	key := strings.Repeat("long/", 500)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	for _, id := range []int64{41, 41, 42} {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT topic_id FROM messages .*FOR UPDATE`).WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow("topic"))
		mock.ExpectExec(`INSERT INTO file_message_bindings .*`+fileBindingConflictPattern).WithArgs(hash, key, id, "topic").WillReturnResult(sqlmock.NewResult(0, 1))
		// Legacy resources have no upload owner record; a zero-row update is valid.
		mock.ExpectExec(`^UPDATE file_uploads SET bound = true WHERE object_key = (\?|\$1)$`).WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		if err := a.BindMessageFiles(ctx, id, "topic", []string{key, key}); err != nil {
			t.Fatal(err)
		}
	}
	mock.ExpectQuery(`SELECT resource_key, message_id, topic_id FROM file_message_bindings .* ORDER BY message_id$`).WithArgs(hash, key).WillReturnRows(sqlmock.NewRows([]string{"resource_key", "message_id", "topic_id"}).AddRow(key, 41, "topic").AddRow(key, 42, "another-topic"))
	bindings, err := a.ListFileMessageBindings(ctx, key)
	want := []store.FileMessageBinding{{ResourceKey: key, MessageID: 41, TopicID: "topic"}, {ResourceKey: key, MessageID: 42, TopicID: "another-topic"}}
	if err != nil || !reflect.DeepEqual(bindings, want) {
		t.Fatalf("bindings: %+v %v", bindings, err)
	}
}

func TestFileAccessBindingRejectsWrongOrMissingMessageAndRollsBack(t *testing.T) {
	for _, test := range []string{"wrong-topic", "missing", "partial-failure", "bound-update-failure"} {
		t.Run(test, func(t *testing.T) {
			a, mock := newFileAccessMock(t)
			mock.ExpectBegin()
			rows := sqlmock.NewRows([]string{"topic_id"})
			if test != "missing" {
				rows.AddRow("topic")
			}
			mock.ExpectQuery(`SELECT topic_id FROM messages .*FOR UPDATE`).WithArgs(int64(41)).WillReturnRows(rows)
			cause := errors.New("write failed")
			if test == "partial-failure" || test == "bound-update-failure" {
				mock.ExpectExec(`INSERT INTO file_message_bindings`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`^UPDATE file_uploads SET bound = true WHERE object_key = (\?|\$1)$`).WithArgs("a").WillReturnResult(sqlmock.NewResult(0, 1))
				second := mock.ExpectExec(`INSERT INTO file_message_bindings`)
				if test == "partial-failure" {
					second.WillReturnError(cause)
				} else {
					second.WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectExec(`^UPDATE file_uploads SET bound = true WHERE object_key = (\?|\$1)$`).WithArgs("b").WillReturnError(cause)
				}
			}
			mock.ExpectRollback()
			topic := "topic"
			if test == "wrong-topic" {
				topic = "forged"
			}
			err := a.BindMessageFiles(context.Background(), 41, topic, []string{"a", "b"})
			if err == nil {
				t.Fatal("binding should fail")
			}
			if (test == "partial-failure" || test == "bound-update-failure") && !errors.Is(err, cause) {
				t.Fatalf("expected write failure, got %v", err)
			}
		})
	}
}

func TestFileAccessMessageAndGlobalBackfill(t *testing.T) {
	a, mock := newFileAccessMock(t)
	created := time.Now().UTC()
	columns := []string{"id", "topic_id", "from_uid", "content", "msg_type", "created_at", "content_blocks", "mode", "role", "metadata", "reply_to"}
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows(columns).AddRow(41, "topic", 7, "message", "text", created, `[{"type":"file","payload":{"url":"/old/url"}}]`, "code", "assistant", `{"source":"test"}`, 10)
	}
	mock.ExpectQuery(`SELECT .*metadata.* FROM messages WHERE id = `).WithArgs(int64(41)).WillReturnRows(row())
	msg, err := a.GetFileAccessMessage(context.Background(), 41)
	if err != nil || msg == nil || msg.ID != 41 || msg.TopicID != "topic" || msg.FromUID != 7 || msg.Content != "message" || msg.MsgType != "text" || msg.Mode != "code" || msg.Role != "assistant" || msg.ReplyTo != 10 || !msg.CreatedAt.Equal(created) || len(msg.ContentBlocks) != 1 || msg.Metadata["source"] != "test" {
		t.Fatalf("message: %+v %v", msg, err)
	}
	mock.ExpectQuery(`SELECT .* FROM messages WHERE id = `).WithArgs(int64(99)).WillReturnRows(sqlmock.NewRows(columns))
	if got, err := a.GetFileAccessMessage(context.Background(), 99); got != nil || err != nil {
		t.Fatalf("missing message: %+v %v", got, err)
	}
	for _, limits := range [][2]int{{0, 100}, {5000, 1000}} {
		// Exact suffix forbids accidental type/topic filters or offset pagination.
		pattern := `SELECT ` + regexp.QuoteMeta(fileAccessMessageColumns) + ` FROM messages WHERE id > (\?|\$1) ORDER BY id ASC LIMIT (\?|\$2)$`
		mock.ExpectQuery(pattern).WithArgs(int64(40), limits[1]).WillReturnRows(row().AddRow(42, "other", 8, "legacy", "file", created, nil, nil, nil, nil, 0))
		messages, err := a.ListFileBackfillMessages(context.Background(), 40, limits[0])
		if err != nil || len(messages) != 2 || messages[0].ID != 41 || messages[1].ID != 42 || messages[1].MsgType != "file" {
			t.Fatalf("backfill: %+v %v", messages, err)
		}
	}
}

func TestFileAccessMediaSessionExpiryAndParentRevocation(t *testing.T) {
	a, mock := newFileAccessMock(t)
	ctx := context.Background()
	token, parent := strings.Repeat("a", 64), strings.Repeat("b", 64)
	session := store.FileMediaSession{TokenHash: token, ActorUID: 7, ParentHash: parent, ExpiresAt: time.Now().Add(time.Hour).UTC()}
	mock.ExpectExec(`INSERT INTO file_media_sessions`).WithArgs(token, int64(7), parent, session.ExpiresAt).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := a.CreateFileMediaSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	for _, expiry := range []time.Time{session.ExpiresAt, time.Now().Add(-time.Hour)} {
		mock.ExpectQuery(`SELECT token_hash, actor_uid, parent_hash, expires_at FROM file_media_sessions .*expires_at > CURRENT_TIMESTAMP`).WithArgs(token).WillReturnRows(sqlmock.NewRows([]string{"token_hash", "actor_uid", "parent_hash", "expires_at"}).AddRow(token, 7, parent, expiry))
		got, err := a.GetFileMediaSession(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if expiry.After(time.Now()) {
			if got == nil || !reflect.DeepEqual(*got, session) {
				t.Fatalf("session: %+v", got)
			}
		} else if got != nil {
			t.Fatalf("expired session returned: %+v", got)
		}
	}
	mock.ExpectExec(`DELETE FROM file_media_sessions WHERE parent_hash = (\?|\$1)$`).WithArgs(parent).WillReturnResult(sqlmock.NewResult(0, 3))
	if err := a.RevokeFileMediaSessions(ctx, parent); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT token_hash, actor_uid, parent_hash, expires_at FROM file_media_sessions`).WithArgs(token).WillReturnRows(sqlmock.NewRows([]string{"token_hash", "actor_uid", "parent_hash", "expires_at"}))
	if got, err := a.GetFileMediaSession(ctx, token); got != nil || err != nil {
		t.Fatalf("missing session: %+v %v", got, err)
	}
}

func TestFileAccessMessageTransactionAndDuplicateSafety(t *testing.T) {
	for _, scenario := range []string{"success", "binding-failure", "bound-update-failure", "duplicate", "concurrent-duplicate", "insert-failure", "commit-failure"} {
		t.Run(scenario, func(t *testing.T) {
			a, mock := newFileAccessMock(t)
			lookupRows := sqlmock.NewRows([]string{"id"})
			if scenario == "duplicate" {
				lookupRows.AddRow(41)
			}
			mock.ExpectQuery(`SELECT id FROM messages`).WithArgs("topic", int64(7), "client-1").WillReturnRows(lookupRows)
			args := []driver.Value{"topic", int64(7), "changed request", []byte(nil), "normal", "user", "text", int64(10), "client-1", jsonArgument{"source": "test"}}
			if scenario != "duplicate" {
				mock.ExpectBegin()
				var cause error
				if scenario == "concurrent-duplicate" {
					cause = fileMessageDuplicateError()
				}
				if scenario == "insert-failure" {
					cause = errors.New("insert failed")
				}
				expectFileMessageInsert(mock, args, cause)
				switch scenario {
				case "concurrent-duplicate":
					mock.ExpectRollback()
					mock.ExpectQuery(`SELECT id FROM messages`).WithArgs("topic", int64(7), "client-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(41))
				case "insert-failure":
					mock.ExpectRollback()
				default:
					cause := errors.New("write failed")
					for _, key := range []string{"new-a", "new-b"} {
						binding := mock.ExpectExec(`INSERT INTO file_message_bindings`).WithArgs(fmt.Sprintf("%x", sha256.Sum256([]byte(key))), key, int64(41), "topic")
						if key == "new-b" && scenario == "binding-failure" {
							binding.WillReturnError(cause)
							break
						}
						binding.WillReturnResult(sqlmock.NewResult(0, 1))
						update := mock.ExpectExec(`^UPDATE file_uploads SET bound = true WHERE object_key = (\?|\$1)$`).WithArgs(key)
						if key == "new-b" && scenario == "bound-update-failure" {
							update.WillReturnError(cause)
							break
						}
						update.WillReturnResult(sqlmock.NewResult(0, 1))
					}
					if scenario == "binding-failure" || scenario == "bound-update-failure" {
						mock.ExpectRollback()
					} else {
						commit := mock.ExpectCommit()
						if scenario == "commit-failure" {
							commit.WillReturnError(errors.New("commit failed"))
						}
					}
				}
			}
			id, duplicate, err := a.SaveMessageWithFileBindings("topic", 7, "changed request", nil, "", "user", "text", 10, "client-1", map[string]interface{}{"source": "test"}, []string{"new-a", "new-b"})
			wantError := scenario == "binding-failure" || scenario == "bound-update-failure" || scenario == "insert-failure" || scenario == "commit-failure"
			wantDuplicate := scenario == "duplicate" || scenario == "concurrent-duplicate"
			if (err != nil) != wantError || duplicate != wantDuplicate || (!wantError && id != 41) || (wantError && id != 0) {
				t.Fatalf("id=%d duplicate=%v err=%v", id, duplicate, err)
			}
		})
	}
}

func TestFileAccessWriterBlocksDefaultsAndNoClientID(t *testing.T) {
	a, mock := newFileAccessMock(t)
	mock.ExpectBegin()
	expectFileMessageInsert(mock, []driver.Value{"topic", int64(7), "", sqlmock.AnyArg(), "code", "assistant", "text", int64(0), "", []byte(nil)}, nil)
	mock.ExpectExec(`INSERT INTO file_message_bindings`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`^UPDATE file_uploads SET bound = true WHERE object_key = (\?|\$1)$`).WithArgs("key").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	id, duplicate, err := a.SaveMessageWithFileBindings("topic", 7, "", []types.ContentBlock{{Type: "file"}}, "", "assistant", "text", 0, "", nil, []string{"key"})
	if err != nil || duplicate || id != 41 {
		t.Fatalf("id=%d duplicate=%v err=%v", id, duplicate, err)
	}
}

func TestFileAccessRejectsInvalidInputAndHonorsCanceledContext(t *testing.T) {
	a, _ := newFileAccessMock(t)
	ctx := context.Background()
	checks := []func() error{
		func() error { return a.RecordFileUpload(ctx, "key", 0) },
		func() error { _, err := a.GetFileUpload(ctx, strings.Repeat("x", 1025)); return err },
		func() error { return a.BindMessageFiles(ctx, 1, "topic", []string{strings.Repeat("x", 16385)}) },
		func() error { return a.BindMessageFiles(ctx, 1, "topic", make([]string, 257)) },
		func() error { _, err := a.ListFileMessageBindings(ctx, ""); return err },
		func() error { _, err := a.GetFileAccessMessage(ctx, 0); return err },
		func() error { _, err := a.ListFileBackfillMessages(ctx, -1, 10); return err },
		func() error { return a.CreateFileMediaSession(ctx, store.FileMediaSession{}) },
		func() error { _, err := a.GetFileMediaSession(ctx, "raw-token"); return err },
		func() error { return a.RevokeFileMediaSessions(ctx, "") },
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("invalid input %d accepted", i)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.GetFileUpload(canceled, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
}

package mysql

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

func TestUpsertConversationTaskStatusPreservesLegacyActiveSource(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	topicID := "grp_legacy"
	legacyUID := int64(41)
	incomingUID := int64(42)
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO conversation_task_statuses")).
		WithArgs(topicID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT topic_id FROM conversation_task_statuses WHERE topic_id = ? FOR UPDATE",
	)).
		WithArgs(topicID).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow(topicID))
	mock.ExpectExec(`INSERT INTO conversation_task_status_sources[\s\S]+ON DUPLICATE KEY UPDATE[\s\S]+IF\(VALUES\(updated_at\) > updated_at`).
		WithArgs(topicID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = ? AND source_uid = ?
		 FOR UPDATE`,
	)).
		WithArgs(topicID, incomingUID).
		WillReturnRows(sqlmock.NewRows([]string{
			"topic_id", "run_id", "state", "summary", "error", "source_uid", "updated_at", "expires_at",
		}))
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT run_id, state, source_uid, expires_at
			 FROM conversation_task_statuses
			 WHERE topic_id = ?`,
	)).
		WithArgs(topicID).
		WillReturnRows(sqlmock.NewRows([]string{
			"run_id", "state", "source_uid", "expires_at",
		}).AddRow("legacy-run", "running", legacyUID, expiry))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO conversation_task_status_sources")).
		WithArgs(topicID, incomingUID, "incoming-run", "completed", "done", "", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT topic_id, run_id, state, summary, error, source_uid, updated_at, expires_at
		 FROM conversation_task_status_sources
		 WHERE topic_id = ?`,
	)).
		WithArgs(topicID).
		WillReturnRows(sqlmock.NewRows([]string{
			"topic_id", "run_id", "state", "summary", "error", "source_uid", "updated_at", "expires_at",
		}).AddRow(topicID, "legacy-run", "running", "legacy running", "", legacyUID, now, expiry))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE conversation_task_statuses SET")).
		WithArgs("legacy-run", "running", "legacy running", "", legacyUID, expiry, topicID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(
		`SELECT topic_id, run_id, state, summary, error, COALESCE(source_uid, 0), updated_at, expires_at
		 FROM conversation_task_statuses WHERE topic_id = ?`,
	)).
		WithArgs(topicID).
		WillReturnRows(sqlmock.NewRows([]string{
			"topic_id", "run_id", "state", "summary", "error", "source_uid", "updated_at", "expires_at",
		}).AddRow(topicID, "legacy-run", "running", "legacy running", "", legacyUID, now, expiry))
	mock.ExpectCommit()

	aggregate, err := adapter.UpsertConversationTaskStatus(&types.ConversationTaskStatus{
		TopicID:   topicID,
		RunID:     "incoming-run",
		State:     "completed",
		Summary:   "done",
		SourceUID: incomingUID,
	})
	if err != nil {
		t.Fatalf("upsert task status: %v", err)
	}
	if aggregate.State != "running" || aggregate.SourceUID != legacyUID {
		t.Fatalf("incoming source overwrote legacy active source: %+v", aggregate)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

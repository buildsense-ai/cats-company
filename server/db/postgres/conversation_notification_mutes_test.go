package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestConversationNotificationMutesStoreAndReadByUserAndTopic(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	const (
		uid       = int64(42)
		mutedID   = "p2p_7_42"
		unmutedID = "grp_9"
	)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO conversation_notification_mutes (user_id, topic_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`)).
		WithArgs(uid, mutedID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT topic_id FROM conversation_notification_mutes WHERE user_id = $1 AND topic_id IN ($2,$3)`)).
		WithArgs(uid, mutedID, unmutedID).
		WillReturnRows(sqlmock.NewRows([]string{"topic_id"}).AddRow(mutedID))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM conversation_notification_mutes WHERE user_id = $1 AND topic_id = $2)`)).
		WithArgs(uid, mutedID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM conversation_notification_mutes WHERE user_id = $1 AND topic_id = $2`)).
		WithArgs(uid, mutedID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	adapter := &Adapter{db: sqlDB}
	if err := adapter.SetConversationNotificationsMuted(context.Background(), uid, mutedID, true); err != nil {
		t.Fatalf("mute conversation notifications: %v", err)
	}
	muted, err := adapter.ListMutedConversationTopics(context.Background(), uid, []string{mutedID, unmutedID})
	if err != nil {
		t.Fatalf("list muted conversation topics: %v", err)
	}
	if !muted[mutedID] || muted[unmutedID] {
		t.Fatalf("muted topics = %#v", muted)
	}
	isMuted, err := adapter.IsConversationNotificationsMuted(context.Background(), uid, mutedID)
	if err != nil {
		t.Fatalf("check muted conversation: %v", err)
	}
	if !isMuted {
		t.Fatal("muted conversation was not found")
	}
	if err := adapter.SetConversationNotificationsMuted(context.Background(), uid, mutedID, false); err != nil {
		t.Fatalf("unmute conversation notifications: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

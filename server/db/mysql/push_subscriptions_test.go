package mysql

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDeletePushSubscriptionMatchesExactRegistrationID(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	const (
		uid            = int64(42)
		endpoint       = "https://push.example.test/exact-generation"
		registrationID = "Session-ABC"
	)
	mock.ExpectExec(regexp.QuoteMeta(
		"DELETE FROM push_subscriptions WHERE uid = ? AND endpoint = ? AND registration_id = ?",
	)).
		WithArgs(uid, endpoint, registrationID).
		WillReturnResult(sqlmock.NewResult(0, 0))

	adapter := &Adapter{db: sqlDB}
	if err := adapter.DeletePushSubscription(context.Background(), uid, endpoint, registrationID); err != nil {
		t.Fatalf("delete push subscription: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

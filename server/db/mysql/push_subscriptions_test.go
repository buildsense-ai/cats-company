package mysql

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store/types"
)

func TestUpsertPushSubscriptionTransfersCurrentEndpointAtLimit(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	const (
		newUID       = int64(42)
		oldUID       = int64(11)
		endpoint     = "https://push.example.test/current-browser"
		obsoleteID   = int64(84)
		registration = "registration-current"
	)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(newUID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newUID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT uid FROM push_subscriptions WHERE endpoint = ? FOR UPDATE")).
		WithArgs(endpoint).
		WillReturnRows(sqlmock.NewRows([]string{"uid"}).AddRow(oldUID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM push_subscriptions WHERE uid = ?")).
		WithArgs(newUID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(10))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id
			 FROM push_subscriptions
			 WHERE uid = ?
			 ORDER BY updated_at ASC, id ASC
			 LIMIT 1
			 FOR UPDATE`)).
		WithArgs(newUID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(obsoleteID))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM push_subscriptions WHERE id = ?")).
		WithArgs(obsoleteID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id)
			 VALUES (?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE
			   uid = VALUES(uid),
			   p256dh = VALUES(p256dh),
			   auth = VALUES(auth),
			   registration_id = VALUES(registration_id)`)).
		WithArgs(newUID, endpoint, "p256dh", "auth", registration).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	adapter := &Adapter{db: sqlDB}
	stored, err := adapter.UpsertPushSubscription(context.Background(), &types.PushSubscription{
		UID:            newUID,
		Endpoint:       endpoint,
		P256DH:         "p256dh",
		Auth:           "auth",
		RegistrationID: registration,
	}, 10)
	if err != nil {
		t.Fatalf("upsert push subscription: %v", err)
	}
	if !stored {
		t.Fatal("current browser endpoint was not transferred to the full account")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestUpsertPushSubscriptionRetriesDeadlock(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	const (
		uid      = int64(42)
		endpoint = "https://push.example.test/retry-after-deadlock"
	)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(uid).
		WillReturnError(&driver.MySQLError{Number: 1213, Message: "deadlock found"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = ? FOR UPDATE")).
		WithArgs(uid).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uid))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT uid FROM push_subscriptions WHERE endpoint = ? FOR UPDATE")).
		WithArgs(endpoint).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM push_subscriptions WHERE uid = ?")).
		WithArgs(uid).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id)
			 VALUES (?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE
			   uid = VALUES(uid),
			   p256dh = VALUES(p256dh),
			   auth = VALUES(auth),
			   registration_id = VALUES(registration_id)`)).
		WithArgs(uid, endpoint, "p256dh", "auth", "registration-current").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	stored, err := (&Adapter{db: sqlDB}).UpsertPushSubscription(context.Background(), &types.PushSubscription{
		UID:            uid,
		Endpoint:       endpoint,
		P256DH:         "p256dh",
		Auth:           "auth",
		RegistrationID: "registration-current",
	}, 10)
	if err != nil {
		t.Fatalf("upsert after deadlock: %v", err)
	}
	if !stored {
		t.Fatal("upsert did not retry the transient deadlock")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

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

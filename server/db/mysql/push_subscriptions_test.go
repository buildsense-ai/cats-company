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

func TestDeletePushSubscriptionsByRegistrationIDIgnoresEndpoint(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	const (
		uid            = int64(42)
		registrationID = "registration-current"
	)
	mock.ExpectExec(regexp.QuoteMeta(
		"DELETE FROM push_subscriptions WHERE uid = ? AND registration_id = ?",
	)).
		WithArgs(uid, registrationID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	adapter := &Adapter{db: sqlDB}
	if err := adapter.DeletePushSubscriptionsByRegistrationID(context.Background(), uid, registrationID); err != nil {
		t.Fatalf("delete push subscriptions by registration id: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestDeletePushSubscriptionsByEndpointIgnoresRegistrationID(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	const (
		uid      = int64(42)
		endpoint = "https://push.example.test/all-registrations"
	)
	mock.ExpectExec(regexp.QuoteMeta(
		"DELETE FROM push_subscriptions WHERE uid = ? AND endpoint = ?",
	)).
		WithArgs(uid, endpoint).
		WillReturnResult(sqlmock.NewResult(0, 1))

	adapter := &Adapter{db: sqlDB}
	if err := adapter.DeletePushSubscriptionsByEndpoint(context.Background(), uid, endpoint); err != nil {
		t.Fatalf("delete push subscriptions by endpoint: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestPushSubscriptionSurfaceVersionFencing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &Adapter{db: db}
	for _, affected := range []int64{1, 0} {
		mock.ExpectExec(regexp.QuoteMeta(`UPDATE push_subscriptions SET auth = ? WHERE uid = ? AND endpoint = ? AND registration_id = ? AND p256dh = ? AND auth = ?`)).WithArgs("new-auth", int64(7), "activitykit://sandbox/bundle/token", "registration-current", "metadata", "old-auth").WillReturnResult(sqlmock.NewResult(0, affected))
		claimed, err := adapter.CompareAndSwapPushSubscriptionAuth(context.Background(), 7, "activitykit://sandbox/bundle/token", "registration-current", "metadata", "old-auth", "new-auth")
		if err != nil || claimed != (affected == 1) {
			t.Fatalf("claimed=%v affected=%d err=%v", claimed, affected, err)
		}
	}
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM push_subscriptions WHERE uid = ? AND endpoint = ? AND registration_id = ? AND p256dh = ? AND auth = ?`)).WithArgs(int64(7), "surface://7/registration-current", "registration-current", "metadata", "hash").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := adapter.DeletePushSubscriptionVersion(context.Background(), 7, "surface://7/registration-current", "registration-current", "metadata", "hash"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE push_subscriptions SET p256dh = ? WHERE uid = ? AND endpoint = ? AND registration_id = ? AND p256dh = ? AND auth = ?`)).WithArgs("new-metadata", int64(7), "activitykit://sandbox/bundle/token", "registration-current", "metadata", "new-auth").WillReturnResult(sqlmock.NewResult(0, 1))
	changed, err := adapter.CompareAndSwapPushSubscriptionMetadata(context.Background(), 7, "activitykit://sandbox/bundle/token", "registration-current", "metadata", "new-metadata", "new-auth")
	if err != nil || !changed {
		t.Fatalf("metadata CAS changed=%v err=%v", changed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

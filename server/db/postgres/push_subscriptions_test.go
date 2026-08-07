package postgres

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = $1 FOR UPDATE")).
		WithArgs(newUID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newUID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT uid FROM push_subscriptions WHERE endpoint = $1 FOR UPDATE")).
		WithArgs(endpoint).
		WillReturnRows(sqlmock.NewRows([]string{"uid"}).AddRow(oldUID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM push_subscriptions WHERE uid = $1")).
		WithArgs(newUID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(10))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id
			 FROM push_subscriptions
			 WHERE uid = $1
			 ORDER BY updated_at ASC, id ASC
			 LIMIT 1
			 FOR UPDATE`)).
		WithArgs(newUID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(obsoleteID))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM push_subscriptions WHERE id = $1")).
		WithArgs(obsoleteID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (endpoint) DO UPDATE SET
			   uid = EXCLUDED.uid,
			   p256dh = EXCLUDED.p256dh,
			   auth = EXCLUDED.auth,
			   registration_id = EXCLUDED.registration_id`)).
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = $1 FOR UPDATE")).
		WithArgs(uid).
		WillReturnError(&pgconn.PgError{Code: "40P01"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM users WHERE id = $1 FOR UPDATE")).
		WithArgs(uid).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uid))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT uid FROM push_subscriptions WHERE endpoint = $1 FOR UPDATE")).
		WithArgs(endpoint).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM push_subscriptions WHERE uid = $1")).
		WithArgs(uid).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO push_subscriptions (uid, endpoint, p256dh, auth, registration_id)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (endpoint) DO UPDATE SET
			   uid = EXCLUDED.uid,
			   p256dh = EXCLUDED.p256dh,
			   auth = EXCLUDED.auth,
			   registration_id = EXCLUDED.registration_id`)).
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
		"DELETE FROM push_subscriptions WHERE uid = $1 AND endpoint = $2",
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

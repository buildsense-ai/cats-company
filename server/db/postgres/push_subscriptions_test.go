package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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

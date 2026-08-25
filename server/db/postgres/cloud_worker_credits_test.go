package postgres

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMarkCloudWorkerLifecyclePendingRechecksExpiry(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	deleteAfter := time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE cloud_worker_lifecycles
		SET state = 'delete_pending', archived_at = COALESCE(archived_at, CURRENT_TIMESTAMP),
		    delete_after = $2, updated_at = CURRENT_TIMESTAMP
		-- The due list is a snapshot. A payment can renew the package between
		-- ListCloudWorkerLifecycleDue and this update. Re-check the expiry under
		-- the same row update so that stale sweeper work cannot overwrite the
		-- renewed active state and schedule an already-paid worker for deletion.
		WHERE id = $1 AND state = 'active' AND package_expires_at <= CURRENT_TIMESTAMP`)).
		WithArgs(int64(7), deleteAfter).
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := adapter.MarkCloudWorkerLifecyclePending(7, deleteAfter); err != nil {
		t.Fatalf("mark pending: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestExtendCloudWorkerLifecyclesDoesNotResurrectDeleteRunning(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	expiresAt := time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE cloud_worker_lifecycles
		SET package_expires_at = $2::timestamptz,
		    delete_after = $2::timestamptz + ($3::int * INTERVAL '1 day'),
		    state = 'active', archived_at = NULL, delete_started_at = NULL,
		    last_error = '', updated_at = CURRENT_TIMESTAMP
		-- Never move a deletion already claimed by the sweeper back to active:
		-- the provider-side destroy is running outside this transaction. A
		-- delete_failed row has no active provider operation and is recoverable
		-- after a successful renewal or upgrade.
		WHERE owner_uid = $1 AND state IN ('active','delete_pending','delete_failed')`)).
		WithArgs(int64(38), expiresAt, 15).
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := adapter.ExtendCloudWorkerLifecycles(38, expiresAt, 15); err != nil {
		t.Fatalf("extend lifecycle: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestReleaseCloudWorkerCreditRevokesSupersededOrderCredit(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE cloud_worker_credits c
		SET state = CASE
			WHEN c.source_ref LIKE 'order:%'
			 AND NOT EXISTS (
				SELECT 1
				FROM commercial_entitlements e
				WHERE e.uid = c.uid AND e.source = 'order'
				  AND c.source_ref = 'order:' || e.source_ref
				  AND e.state = 'active' AND e.starts_at <= CURRENT_TIMESTAMP
				  AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
			 ) THEN 'revoked'
			ELSE 'available'
		END,
		reservation_ref = '', reserved_at = NULL
		WHERE c.uid = $1 AND c.reservation_ref = $2 AND c.state = 'reserved'`)).
		WithArgs(int64(38), "create-38-old").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := adapter.ReleaseCloudWorkerCredit(38, "create-38-old"); err != nil {
		t.Fatalf("release superseded cloud-worker credit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestCommitCloudWorkerCreditAllowsPerpetualManualGrant(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`
		UPDATE cloud_worker_credits
		SET state = 'consumed', worker_uid = $3, consumed_at = CURRENT_TIMESTAMP
		WHERE uid = $1 AND reservation_ref = $2 AND state = 'reserved'
		RETURNING expires_at`)).
		WithArgs(int64(784), "create-784-test", int64(928)).
		WillReturnRows(sqlmock.NewRows([]string{"expires_at"}).AddRow(nil))
	mock.ExpectCommit()

	if err := adapter.CommitCloudWorkerCredit(784, "create-784-test", 928, "bot-bot-123-9879", 15); err != nil {
		t.Fatalf("perpetual credit commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

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
		-- the provider-side destroy is running outside this transaction.
		WHERE owner_uid = $1 AND state IN ('active','delete_pending')`)).
		WithArgs(int64(38), expiresAt, 15).
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := adapter.ExtendCloudWorkerLifecycles(38, expiresAt, 15); err != nil {
		t.Fatalf("extend lifecycle: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

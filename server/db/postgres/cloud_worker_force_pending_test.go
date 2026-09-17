package postgres

import (
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The pending-cleanup path must be able to override a future paid expiry:
// without this the unconfirmed destroy of a failed provision waits for the
// credit to expire instead of being retried by the next sweep.
func TestForceCloudWorkerLifecyclePendingOverridesFutureExpiry(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	due := time.Date(2026, time.September, 17, 8, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE cloud_worker_lifecycles
		SET state = 'delete_pending', archived_at = COALESCE(archived_at, CURRENT_TIMESTAMP),
		    delete_after = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND state IN ('active','delete_pending')`)).
		WithArgs(int64(19), due).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := adapter.ForceCloudWorkerLifecyclePending(19, due); err != nil {
		t.Fatalf("force pending: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

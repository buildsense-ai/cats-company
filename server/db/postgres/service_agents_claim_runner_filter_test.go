package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// worker 的 claim 只能命中 codex / worker 这类「外部执行」的 run。
//
// bot / http / mcp 由平台自己派活：被 worker 抢走后 run 会落到一个没有
// runner.command 的执行器上，最终以 no_command 失败，而真正的执行方再也等不到它。
func TestClaimServiceRunsFiltersPlatformRunners(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`runner.*IN \('codex','worker'\)`).
		WithArgs(5, int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"run_id"}))
	mock.ExpectCommit()

	adapter := &Adapter{db: db}
	runs, err := adapter.ClaimServiceRunsOwnedBy(context.Background(), "inst-1", 5, 7)
	if err != nil {
		t.Fatalf("claim owned runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("claimed runs = %d, want 0", len(runs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

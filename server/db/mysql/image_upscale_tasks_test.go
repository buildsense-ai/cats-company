package mysql

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestImageUpscaleTaskOwnerRoundTripQueries(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	expiresAt := now.Add(24 * time.Hour)
	mock.ExpectExec(`DELETE FROM image_upscale_tasks WHERE expires_at <= CURRENT_TIMESTAMP\(6\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO image_upscale_tasks`).
		WithArgs("process-1", int64(42), expiresAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := adapter.UpsertImageUpscaleTaskOwner(context.Background(), "process-1", 42, expiresAt); err != nil {
		t.Fatalf("upsert task owner: %v", err)
	}

	mock.ExpectQuery(`SELECT owner_uid\s+FROM image_upscale_tasks`).
		WithArgs("process-1", now).
		WillReturnRows(sqlmock.NewRows([]string{"owner_uid"}).AddRow(int64(42)))
	ownerUID, found, err := adapter.GetImageUpscaleTaskOwner(context.Background(), "process-1", now)
	if err != nil {
		t.Fatalf("get task owner: %v", err)
	}
	if !found || ownerUID != 42 {
		t.Fatalf("owner uid=%d found=%v", ownerUID, found)
	}

	mock.ExpectQuery(`SELECT owner_uid\s+FROM image_upscale_tasks`).
		WithArgs("process-1", expiresAt).
		WillReturnError(sql.ErrNoRows)
	ownerUID, found, err = adapter.GetImageUpscaleTaskOwner(context.Background(), "process-1", expiresAt)
	if err != nil {
		t.Fatalf("get expired task owner: %v", err)
	}
	if found || ownerUID != 0 {
		t.Fatalf("expired owner uid=%d found=%v", ownerUID, found)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

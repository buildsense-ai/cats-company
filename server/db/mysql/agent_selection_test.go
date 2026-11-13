package mysql

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListAgentSelectionMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createdAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	acceptedAt := createdAt.Add(24 * time.Hour)
	mock.ExpectQuery(`SELECT u.id, COALESCE\(b.tenant_name, ''\) <> '',\s*CASE WHEN b.owner_id = \? THEN u.created_at ELSE f.updated_at END`).
		WithArgs(int64(7), int64(7), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "is_cloud", "added_at"}).
			AddRow(int64(42), true, createdAt).
			AddRow(int64(43), false, acceptedAt))
	metadata, err := (&Adapter{db: db}).ListAgentSelectionMetadata(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 2 || !metadata[42].IsCloud || !metadata[42].AddedAt.Equal(createdAt) || metadata[43].IsCloud || !metadata[43].AddedAt.Equal(acceptedAt) {
		t.Fatalf("unexpected selection metadata: %+v", metadata)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

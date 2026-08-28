package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store"
)

func TestPostgresArtifactRuntimeStateAndEventShareTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	createdAt := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Second)
	value := json.RawMessage(`{"items":[]}`)
	candidate := &store.ArtifactRuntimeState{
		AgentUID: 440, ArtifactID: "risk-register", Namespace: "risks", Key: "main",
		Value: value, UpdatedByUID: 7, UpdatedBy: "viewer",
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO artifact_runtime_states`).
		WithArgs(int64(440), "risk-register", "risks", "main", string(value), int64(7), "viewer").
		WillReturnRows(sqlmock.NewRows([]string{"value_json", "revision", "created_at", "updated_at"}).
			AddRow(value, int64(1), createdAt, updatedAt))
	mock.ExpectQuery(`INSERT INTO artifact_runtime_event_sequences`).
		WithArgs(int64(440), "risk-register").
		WillReturnRows(sqlmock.NewRows([]string{"last_event_id"}).AddRow(int64(19)))
	mock.ExpectQuery(`INSERT INTO artifact_runtime_events`).
		WithArgs(int64(19), "state.updated", int64(440), "risk-register", "risks", "main", int64(1), int64(7), "viewer").
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(updatedAt))
	mock.ExpectCommit()

	state, event, err := adapter.PutArtifactRuntimeState(context.Background(), candidate, 0)
	if err != nil {
		t.Fatalf("put Runtime State: %v", err)
	}
	if state.Revision != 1 || event.EventID != 19 || event.Revision != state.Revision {
		t.Fatalf("unexpected state/event: state=%+v event=%+v", state, event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestPostgresArtifactRuntimeConflictDoesNotAppendEvent(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	adapter := &Adapter{db: sqlDB}
	candidate := &store.ArtifactRuntimeState{
		AgentUID: 440, ArtifactID: "risk-register", Namespace: "risks", Key: "main",
		Value: json.RawMessage(`{"items":[]}`), UpdatedByUID: 440, UpdatedBy: "agent",
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE artifact_runtime_states`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT revision FROM artifact_runtime_states`).
		WithArgs(int64(440), "risk-register", "risks", "main").
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(4)))
	mock.ExpectRollback()

	_, _, err = adapter.PutArtifactRuntimeState(context.Background(), candidate, 3)
	var conflict *store.ArtifactRuntimeRevisionConflict
	if !errors.As(err, &conflict) || conflict.CurrentRevision != 4 {
		t.Fatalf("conflict=%v, want current revision 4", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestPostgresArtifactRuntimeListReadsReferencesOnly(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	updatedAt := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT namespace, document_key, revision, updated_at`).
		WithArgs(int64(440), "risk-register", 256).
		WillReturnRows(sqlmock.NewRows([]string{"namespace", "document_key", "revision", "updated_at"}).
			AddRow("risks", "main", int64(3), updatedAt))
	states, err := (&Adapter{db: sqlDB}).ListArtifactRuntimeStates(
		context.Background(), 440, "risk-register", 256,
	)
	if err != nil || len(states) != 1 || states[0].Revision != 3 || len(states[0].Value) != 0 {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

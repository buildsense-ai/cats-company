package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store"
)

func TestTruncateRuntimeRunTextPreservesUTF8(t *testing.T) {
	got := truncateRuntimeRunText("  中文错误信息  ", 4)
	if got != "中文错误" || !utf8.ValidString(got) {
		t.Fatalf("truncateRuntimeRunText() = %q", got)
	}
	if got := truncateRuntimeRunText(strings.Repeat("a", 65), 64); len(got) != 64 {
		t.Fatalf("ASCII truncation length = %d", len(got))
	}
}

func TestObserveArtifactRuntimeExecutorDoesNotMutateTerminalRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
		WithArgs("ref-hash").
		WillReturnRows(postgresRuntimeRunTestRow("completed", "completed", now))
	mock.ExpectRollback()

	run, event, err := (&Adapter{db: db}).ObserveArtifactRuntimeExecutor(
		context.Background(), "ref-hash", 440, "p2p_7_440", "executor-1", "waiting", "",
	)
	if err != nil || event != nil || run == nil || run.Status != "completed" || run.ExecutorState != "completed" {
		t.Fatalf("terminal executor update changed run: run=%#v event=%#v err=%v", run, event, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestObserveArtifactRuntimeExecutorDoesNotRegressCompletedExecutor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
		WithArgs("ref-hash").
		WillReturnRows(postgresRuntimeRunTestRow("running", "completed", now))
	mock.ExpectRollback()

	run, event, err := (&Adapter{db: db}).ObserveArtifactRuntimeExecutor(
		context.Background(), "ref-hash", 440, "p2p_7_440", "executor-1", "waiting", "",
	)
	if err != nil || event != nil || run == nil || run.Status != "running" || run.ExecutorState != "completed" {
		t.Fatalf("late executor state regressed run: run=%#v event=%#v err=%v", run, event, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestPutArtifactRuntimeStateStartsRunBeforeStateEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	value := json.RawMessage(`{"items":[]}`)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
		WithArgs("ref-hash").WillReturnRows(postgresRuntimeRunTestRow("submitted", "", now))
	mock.ExpectExec(`UPDATE artifact_runtime_runs`).WithArgs(sqlmock.AnyArg(), "task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO artifact_runtime_event_sequences`).WithArgs(int64(440), "project-board").
		WillReturnRows(sqlmock.NewRows([]string{"last_event_id"}).AddRow(int64(20)))
	mock.ExpectQuery(`INSERT INTO artifact_runtime_events`).
		WithArgs(int64(20), "run.started", int64(440), "project-board", "runtime", "run-1", int64(1),
			int64(440), "agent", "task-1", "run-1", "executor-1", "", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(now))
	mock.ExpectQuery(`INSERT INTO artifact_runtime_states`).
		WithArgs(int64(440), "project-board", "tasks", "main", string(value), int64(440), "agent").
		WillReturnRows(sqlmock.NewRows([]string{"value_json", "revision", "created_at", "updated_at"}).
			AddRow(value, int64(1), now, now))
	mock.ExpectQuery(`INSERT INTO artifact_runtime_event_sequences`).WithArgs(int64(440), "project-board").
		WillReturnRows(sqlmock.NewRows([]string{"last_event_id"}).AddRow(int64(21)))
	mock.ExpectQuery(`INSERT INTO artifact_runtime_events`).
		WithArgs(int64(21), "state.updated", int64(440), "project-board", "tasks", "main", int64(1),
			int64(440), "agent", "task-1", "run-1", "executor-1", "", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(now))
	mock.ExpectExec(`UPDATE artifact_runtime_runs SET updated_at`).WithArgs("task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	state, event, err := (&Adapter{db: db}).PutArtifactRuntimeStateForRun(
		context.Background(), &store.ArtifactRuntimeState{
			AgentUID: 440, ArtifactID: "project-board", Namespace: "tasks", Key: "main",
			Value: value, UpdatedByUID: 440, UpdatedBy: "agent",
		}, 0, "ref-hash",
	)
	if err != nil || state == nil || state.Revision != 1 || event == nil || event.EventID != 21 {
		t.Fatalf("state write result: state=%#v event=%#v err=%v", state, event, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("run.started was not committed before state.updated: %v", err)
	}
}

func postgresRuntimeRunTestRow(status, executorState string, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(postgresRuntimeRunTestColumns()).AddRow(
		"task-1", "ref-hash", "run-1", int64(7), "p2p_7_440", int64(440),
		"project-board", "Project board", "html", "https://example.test", 3,
		int64(3), "node-1", "connection-1", "tasks.plan.v1", "Plan", "Plan tasks",
		[]byte(`{"type":"object"}`), []byte(`{"scope":"week"}`), []byte(`{}`),
		"runtime_state", status, "", "", false, "message-1", true,
		"executor-1", executorState, now, "result-1", []byte(`[11]`),
		now, now, now.Add(time.Hour), now, now,
	)
}

func postgresRuntimeRunTestColumns() []string {
	return []string{
		"task_id", "task_ref_hash", "run_id", "actor_uid", "topic_id", "agent_uid",
		"artifact_id", "artifact_title", "artifact_kind", "artifact_url", "publish_version",
		"displayed_version", "preview_node_id", "preview_connection_id",
		"action_id", "action_title", "action_description", "input_schema", "payload_json",
		"page_context_json", "completion_mode", "status", "code", "message",
		"delivery_claimed", "delivery_client_id", "delivered",
		"executor_run_id", "executor_state", "executor_finished_at",
		"result_id", "applied_event_ids", "created_at", "updated_at", "expires_at",
		"started_at", "finished_at",
	}
}

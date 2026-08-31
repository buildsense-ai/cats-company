package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCreateArtifactRuntimeRunEnforcesDatabaseAdmissionLimits(t *testing.T) {
	tests := []struct {
		name       string
		active     int
		recent     int
		want       error
		expectRate bool
	}{
		{name: "active cap includes persistent runs", active: 32, want: store.ErrArtifactRuntimeActorActiveCap},
		{name: "rate cap includes persistent runs", recent: 60, want: store.ErrArtifactRuntimeActorRateLimit, expectRate: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create mock database: %v", err)
			}
			defer db.Close()
			now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
			policy := postgresRuntimeRunCreatePolicy()
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT lock_id FROM artifact_runtime_admission_lock`).
				WillReturnRows(sqlmock.NewRows([]string{"lock_id"}).AddRow(1))
			mock.ExpectQuery(`SELECT task_id, run_id FROM artifact_runtime_runs`).
				WithArgs(now.Add(-policy.Retention), policy.CleanupLimit).
				WillReturnRows(sqlmock.NewRows([]string{"task_id", "run_id"}))
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND status`).
				WithArgs(int64(7), now).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.active))
			if test.expectRate {
				mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND created_at`).
					WithArgs(int64(7), now.Add(-policy.ActorRateWindow)).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.recent))
			}
			mock.ExpectRollback()
			_, err = (&Adapter{db: db}).CreateArtifactRuntimeRun(
				context.Background(), postgresRuntimeRunCandidate(now), policy,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet database expectations: %v", err)
			}
		})
	}
}

func TestCreateArtifactRuntimeRunPrunesRetainedRunAndEvents(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	policy := postgresRuntimeRunCreatePolicy()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT lock_id FROM artifact_runtime_admission_lock`).
		WillReturnRows(sqlmock.NewRows([]string{"lock_id"}).AddRow(1))
	mock.ExpectQuery(`SELECT task_id, run_id FROM artifact_runtime_runs`).
		WithArgs(now.Add(-policy.Retention), policy.CleanupLimit).
		WillReturnRows(sqlmock.NewRows([]string{"task_id", "run_id"}).AddRow("task-old", "run-old"))
	mock.ExpectExec(`DELETE FROM artifact_runtime_events WHERE run_id IN`).
		WithArgs("run-old").WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(`DELETE FROM artifact_runtime_runs WHERE task_id IN`).
		WithArgs("task-old").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND status`).
		WithArgs(int64(7), now).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND created_at`).
		WithArgs(int64(7), now.Add(-policy.ActorRateWindow)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs$`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(`INSERT INTO artifact_runtime_runs`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_id = \$1 AND actor_uid = \$2`).
		WithArgs("task-1", int64(7)).
		WillReturnRows(postgresRuntimeRunTestRowWithAccess("submitted", "", false, false, nil, nil, now.Add(time.Hour), now))
	mock.ExpectCommit()
	created, err := (&Adapter{db: db}).CreateArtifactRuntimeRun(
		context.Background(), postgresRuntimeRunCandidate(now), policy,
	)
	if err != nil || created == nil || created.TaskID != "task-1" {
		t.Fatalf("create after retention cleanup: run=%#v err=%v", created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestCreateArtifactRuntimeRunPrunesCapacityAcrossBatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	policy := postgresRuntimeRunCreatePolicy()
	policy.MaxEntries, policy.CleanupLimit = 2, 1
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT lock_id FROM artifact_runtime_admission_lock`).
		WillReturnRows(sqlmock.NewRows([]string{"lock_id"}).AddRow(1))
	mock.ExpectQuery(`SELECT task_id, run_id FROM artifact_runtime_runs`).
		WithArgs(now.Add(-policy.Retention), policy.CleanupLimit).
		WillReturnRows(sqlmock.NewRows([]string{"task_id", "run_id"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND status`).
		WithArgs(int64(7), now).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND created_at`).
		WithArgs(int64(7), now.Add(-policy.ActorRateWindow)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs$`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	for index := 1; index <= 2; index++ {
		taskID, runID := fmt.Sprintf("task-old-%d", index), fmt.Sprintf("run-old-%d", index)
		mock.ExpectQuery(`SELECT task_id, run_id FROM artifact_runtime_runs`).
			WithArgs(now.Add(-policy.ActorRateWindow), 1).
			WillReturnRows(sqlmock.NewRows([]string{"task_id", "run_id"}).AddRow(taskID, runID))
		mock.ExpectExec(`DELETE FROM artifact_runtime_events WHERE run_id IN`).
			WithArgs(runID).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`DELETE FROM artifact_runtime_runs WHERE task_id IN`).
			WithArgs(taskID).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(`INSERT INTO artifact_runtime_runs`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_id = \$1 AND actor_uid = \$2`).
		WithArgs("task-1", int64(7)).
		WillReturnRows(postgresRuntimeRunTestRowWithAccess("submitted", "", false, false, nil, nil, now.Add(time.Hour), now))
	mock.ExpectCommit()
	created, err := (&Adapter{db: db}).CreateArtifactRuntimeRun(
		context.Background(), postgresRuntimeRunCandidate(now), policy,
	)
	if err != nil || created == nil || created.TaskID != "task-1" {
		t.Fatalf("create after batched capacity cleanup: run=%#v err=%v", created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestCreateArtifactRuntimeRunRejectsFullPersistentStoreWithoutTerminalVictim(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	policy := postgresRuntimeRunCreatePolicy()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT lock_id FROM artifact_runtime_admission_lock`).
		WillReturnRows(sqlmock.NewRows([]string{"lock_id"}).AddRow(1))
	mock.ExpectQuery(`SELECT task_id, run_id FROM artifact_runtime_runs`).
		WithArgs(now.Add(-policy.Retention), policy.CleanupLimit).
		WillReturnRows(sqlmock.NewRows([]string{"task_id", "run_id"}))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND status`).
		WithArgs(int64(7), now).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs\s+WHERE actor_uid = \$1 AND created_at`).
		WithArgs(int64(7), now.Add(-policy.ActorRateWindow)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM artifact_runtime_runs$`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(policy.MaxEntries))
	mock.ExpectQuery(`SELECT task_id, run_id FROM artifact_runtime_runs`).
		WithArgs(now.Add(-policy.ActorRateWindow), 1).
		WillReturnRows(sqlmock.NewRows([]string{"task_id", "run_id"}))
	mock.ExpectRollback()
	_, err = (&Adapter{db: db}).CreateArtifactRuntimeRun(
		context.Background(), postgresRuntimeRunCandidate(now), policy,
	)
	if !errors.Is(err, store.ErrArtifactRuntimeRunStoreFull) {
		t.Fatalf("error = %v, want persistent store full", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
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

func TestObserveArtifactRuntimeExecutorCompletesMetadataAfterBusinessRunFinished(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
		WithArgs("ref-hash").
		WillReturnRows(postgresRuntimeRunTestRowWithAccess("completed", "running", false, true, nil, nil, now.Add(time.Hour), now))
	mock.ExpectExec(`UPDATE artifact_runtime_runs SET\s+executor_run_id = \$1, executor_state = \$2, executor_finished_at = \$3, updated_at = \$4`).
		WithArgs("executor-1", "completed", sqlmock.AnyArg(), sqlmock.AnyArg(), "task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	run, event, err := (&Adapter{db: db}).ObserveArtifactRuntimeExecutor(
		context.Background(), "ref-hash", 440, "p2p_7_440", "executor-1", "completed", "",
	)
	if err != nil || event != nil || run == nil || run.Status != "completed" ||
		run.ExecutorState != "completed" || run.ExecutorFinishedAt == nil {
		t.Fatalf("late executor completion was not recorded: run=%#v event=%#v err=%v", run, event, err)
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

func TestArtifactRuntimeRunMutationsRevalidateDeliveryAndExpiryUnderLock(t *testing.T) {
	tests := []struct {
		name      string
		complete  bool
		delivered bool
		expired   bool
	}{
		{name: "state write requires delivery", delivered: false},
		{name: "state write rejects expired ref", delivered: true, expired: true},
		{name: "completion requires delivery", complete: true, delivered: false},
		{name: "completion rejects expired ref", complete: true, delivered: true, expired: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create mock database: %v", err)
			}
			defer db.Close()
			now := time.Now().UTC()
			expiresAt := now.Add(time.Hour)
			if test.expired {
				expiresAt = now.Add(-time.Second)
			}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
				WithArgs("ref-hash").
				WillReturnRows(postgresRuntimeRunTestRowWithAccess("running", "running", false, test.delivered, nil, nil, expiresAt, now))
			mock.ExpectRollback()
			adapter := &Adapter{db: db}
			if test.complete {
				_, _, err = adapter.CompleteArtifactRuntimeRun(context.Background(), "ref-hash", 440, "result-new", []int64{11})
			} else {
				_, _, err = adapter.PutArtifactRuntimeStateForRun(context.Background(), &store.ArtifactRuntimeState{
					AgentUID: 440, ArtifactID: "project-board", Namespace: "tasks", Key: "main",
					Value: json.RawMessage(`{"items":[]}`), UpdatedByUID: 440, UpdatedBy: "agent",
				}, 0, "ref-hash")
			}
			if !errors.Is(err, store.ErrArtifactRuntimeRunConflict) {
				t.Fatalf("error = %v, want capability conflict", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet database expectations: %v", err)
			}
		})
	}
}

func TestReserveArtifactRuntimeDeliveryRecoversExpiredLeaseWithoutDuplicateDelivery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Now().UTC()
	staleClaim := now.Add(-time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
		WithArgs("ref-hash").
		WillReturnRows(postgresRuntimeRunTestRowWithAccess("submitted", "", true, false, &staleClaim, nil, now.Add(time.Hour), now))
	mock.ExpectExec(`UPDATE artifact_runtime_runs\s+SET delivery_claimed = TRUE`).
		WithArgs("message-1", sqlmock.AnyArg(), "task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	adapter := &Adapter{db: db}
	claim, err := adapter.ReserveArtifactRuntimeDelivery(
		context.Background(), "ref-hash", 7, "p2p_7_440", 440, "message-1", 30*time.Second,
	)
	if err != nil || claim == nil || !claim.Recovered || claim.AlreadyDelivered {
		t.Fatalf("expired lease was not recovered: claim=%#v err=%v", claim, err)
	}
	mock.ExpectExec(`UPDATE artifact_runtime_runs\s+SET delivery_claimed = FALSE, delivery_claimed_at = NULL, delivered = TRUE`).
		WithArgs("task-1", "ref-hash", "message-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	confirmed, err := adapter.ConfirmArtifactRuntimeDelivery(context.Background(), "task-1", "ref-hash", "message-1")
	if err != nil || !confirmed {
		t.Fatalf("recovered delivery was not confirmed: confirmed=%v err=%v", confirmed, err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
		WithArgs("ref-hash").
		WillReturnRows(postgresRuntimeRunTestRowWithAccess("submitted", "", false, true, nil, nil, now.Add(time.Hour), now))
	mock.ExpectRollback()
	duplicate, err := adapter.ReserveArtifactRuntimeDelivery(
		context.Background(), "ref-hash", 7, "p2p_7_440", 440, "message-1", 30*time.Second,
	)
	if err != nil || duplicate == nil || !duplicate.AlreadyDelivered {
		t.Fatalf("confirmed retry was not deduplicated: claim=%#v err=%v", duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func TestReserveArtifactRuntimeDeliveryKeepsLiveLeaseExclusive(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer db.Close()
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM artifact_runtime_runs WHERE task_ref_hash = \$1 FOR UPDATE`).
		WithArgs("ref-hash").
		WillReturnRows(postgresRuntimeRunTestRowWithAccess("submitted", "", true, false, &now, nil, now.Add(time.Hour), now))
	mock.ExpectRollback()
	_, err = (&Adapter{db: db}).ReserveArtifactRuntimeDelivery(
		context.Background(), "ref-hash", 7, "p2p_7_440", 440, "message-1", time.Minute,
	)
	if !errors.Is(err, store.ErrArtifactRuntimeDeliveryPending) {
		t.Fatalf("error = %v, want pending live lease", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet database expectations: %v", err)
	}
}

func postgresRuntimeRunCreatePolicy() store.ArtifactRuntimeRunCreatePolicy {
	return store.ArtifactRuntimeRunCreatePolicy{
		ActorActiveMax:  32,
		ActorRateMax:    60,
		ActorRateWindow: time.Minute,
		MaxEntries:      4096,
		Retention:       30 * 24 * time.Hour,
		CleanupLimit:    512,
	}
}

func postgresRuntimeRunCandidate(now time.Time) *store.ArtifactRuntimeRun {
	return &store.ArtifactRuntimeRun{
		TaskID: "task-1", TaskRefHash: "ref-hash", RunID: "run-1",
		ActorUID: 7, TopicID: "p2p_7_440", AgentUID: 440,
		ArtifactID: "project-board", ArtifactTitle: "Project board", ArtifactKind: "html",
		ArtifactURL: "https://example.test", PublishVersion: 3, DisplayedVersion: 3,
		PreviewNodeID: "node-1", PreviewConnectionID: "connection-1",
		ActionID: "tasks.plan.v1", ActionTitle: "Plan", ActionDescription: "Plan tasks",
		InputSchema: json.RawMessage(`{"type":"object"}`), Payload: json.RawMessage(`{"scope":"week"}`),
		PageContext: json.RawMessage(`{}`), CompletionMode: "runtime_state",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func postgresRuntimeRunTestRow(status, executorState string, now time.Time) *sqlmock.Rows {
	finishedAt := now
	return postgresRuntimeRunTestRowWithAccess(status, executorState, false, true, nil, &finishedAt, now.Add(time.Hour), now)
}

func postgresRuntimeRunTestRowWithAccess(
	status, executorState string,
	deliveryClaimed, delivered bool,
	deliveryClaimedAt, executorFinishedAt *time.Time,
	expiresAt, now time.Time,
) *sqlmock.Rows {
	var claimedAtValue, executorFinishedValue interface{}
	if deliveryClaimedAt != nil {
		claimedAtValue = *deliveryClaimedAt
	}
	if executorFinishedAt != nil {
		executorFinishedValue = *executorFinishedAt
	}
	return sqlmock.NewRows(postgresRuntimeRunTestColumns()).AddRow(
		"task-1", "ref-hash", "run-1", int64(7), "p2p_7_440", int64(440),
		"project-board", "Project board", "html", "https://example.test", 3,
		int64(3), "node-1", "connection-1", "tasks.plan.v1", "Plan", "Plan tasks",
		[]byte(`{"type":"object"}`), []byte(`{"scope":"week"}`), []byte(`{}`),
		"runtime_state", status, "", "", deliveryClaimed, "message-1", claimedAtValue, delivered,
		"executor-1", executorState, executorFinishedValue, "result-1", []byte(`[11]`),
		now, now, expiresAt, now, now,
	)
}

func postgresRuntimeRunTestColumns() []string {
	return []string{
		"task_id", "task_ref_hash", "run_id", "actor_uid", "topic_id", "agent_uid",
		"artifact_id", "artifact_title", "artifact_kind", "artifact_url", "publish_version",
		"displayed_version", "preview_node_id", "preview_connection_id",
		"action_id", "action_title", "action_description", "input_schema", "payload_json",
		"page_context_json", "completion_mode", "status", "code", "message",
		"delivery_claimed", "delivery_client_id", "delivery_claimed_at", "delivered",
		"executor_run_id", "executor_state", "executor_finished_at",
		"result_id", "applied_event_ids", "created_at", "updated_at", "expires_at",
		"started_at", "finished_at",
	}
}

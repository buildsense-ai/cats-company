package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.ArtifactRuntimeRunStore = (*Adapter)(nil)

const postgresArtifactRuntimeRunColumns = `
	task_id, task_ref_hash, run_id, actor_uid, topic_id, agent_uid,
	artifact_id, artifact_title, artifact_kind, artifact_url, publish_version,
	displayed_version, preview_node_id, preview_connection_id,
	action_id, action_title, action_description, input_schema, payload_json,
	page_context_json, completion_mode, status, code, message,
	delivery_claimed, delivery_client_id, delivered,
	executor_run_id, executor_state, executor_finished_at,
	result_id, applied_event_ids, created_at, updated_at, expires_at,
	started_at, finished_at`

type postgresRuntimeRunScanner interface {
	Scan(dest ...interface{}) error
}

func scanPostgresArtifactRuntimeRun(scanner postgresRuntimeRunScanner) (*store.ArtifactRuntimeRun, error) {
	run := &store.ArtifactRuntimeRun{}
	var appliedJSON []byte
	if err := scanner.Scan(
		&run.TaskID, &run.TaskRefHash, &run.RunID, &run.ActorUID, &run.TopicID, &run.AgentUID,
		&run.ArtifactID, &run.ArtifactTitle, &run.ArtifactKind, &run.ArtifactURL, &run.PublishVersion,
		&run.DisplayedVersion, &run.PreviewNodeID, &run.PreviewConnectionID,
		&run.ActionID, &run.ActionTitle, &run.ActionDescription, &run.InputSchema, &run.Payload,
		&run.PageContext, &run.CompletionMode, &run.Status, &run.Code, &run.Message,
		&run.DeliveryClaimed, &run.DeliveryClientID, &run.Delivered,
		&run.ExecutorRunID, &run.ExecutorState, &run.ExecutorFinishedAt,
		&run.ResultID, &appliedJSON, &run.CreatedAt, &run.UpdatedAt, &run.ExpiresAt,
		&run.StartedAt, &run.FinishedAt,
	); err != nil {
		return nil, err
	}
	if len(appliedJSON) > 0 && json.Unmarshal(appliedJSON, &run.AppliedEventIDs) != nil {
		return nil, errors.New("invalid artifact runtime applied event ids")
	}
	run.InputSchema = append(json.RawMessage(nil), run.InputSchema...)
	run.Payload = append(json.RawMessage(nil), run.Payload...)
	run.PageContext = append(json.RawMessage(nil), run.PageContext...)
	return run, nil
}

func (a *Adapter) CreateArtifactRuntimeRun(ctx context.Context, run *store.ArtifactRuntimeRun) (*store.ArtifactRuntimeRun, error) {
	if run == nil {
		return nil, errors.New("artifact runtime run is nil")
	}
	pageContext := run.PageContext
	if len(pageContext) == 0 {
		pageContext = json.RawMessage(`{}`)
	}
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO artifact_runtime_runs (
			task_id, task_ref_hash, run_id, actor_uid, topic_id, agent_uid,
			artifact_id, artifact_title, artifact_kind, artifact_url, publish_version,
			displayed_version, preview_node_id, preview_connection_id,
			action_id, action_title, action_description, input_schema, payload_json,
			page_context_json, completion_mode, status, expires_at, applied_event_ids
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18::jsonb, $19::jsonb, $20::jsonb,
			$21, 'submitted', $22, '[]'::jsonb
		)`,
		run.TaskID, run.TaskRefHash, run.RunID, run.ActorUID, run.TopicID, run.AgentUID,
		run.ArtifactID, run.ArtifactTitle, run.ArtifactKind, run.ArtifactURL, run.PublishVersion,
		run.DisplayedVersion, run.PreviewNodeID, run.PreviewConnectionID,
		run.ActionID, run.ActionTitle, run.ActionDescription, string(run.InputSchema), string(run.Payload),
		string(pageContext), run.CompletionMode, run.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create artifact runtime run: %w", err)
	}
	created, found, err := a.GetArtifactRuntimeRunByTask(ctx, run.TaskID, run.ActorUID)
	if err != nil || !found {
		if err == nil {
			err = store.ErrArtifactRuntimeRunNotFound
		}
		return nil, err
	}
	return created, nil
}

func (a *Adapter) GetArtifactRuntimeRunByTask(ctx context.Context, taskID string, actorUID int64) (*store.ArtifactRuntimeRun, bool, error) {
	return postgresReadArtifactRuntimeRun(a.db.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs WHERE task_id = $1 AND actor_uid = $2`,
		taskID, actorUID,
	), "get artifact runtime run by task")
}

func (a *Adapter) GetArtifactRuntimeRunByRef(ctx context.Context, taskRefHash string, agentUID int64) (*store.ArtifactRuntimeRun, bool, error) {
	return postgresReadArtifactRuntimeRun(a.db.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs WHERE task_ref_hash = $1 AND agent_uid = $2`,
		taskRefHash, agentUID,
	), "get artifact runtime run by ref")
}

func (a *Adapter) GetArtifactRuntimeRun(ctx context.Context, runID string, actorUID, agentUID int64, artifactID string) (*store.ArtifactRuntimeRun, bool, error) {
	return postgresReadArtifactRuntimeRun(a.db.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs
		 WHERE run_id = $1 AND actor_uid = $2 AND agent_uid = $3 AND artifact_id = $4`,
		runID, actorUID, agentUID, artifactID,
	), "get artifact runtime run")
}

func postgresReadArtifactRuntimeRun(row *sql.Row, operation string) (*store.ArtifactRuntimeRun, bool, error) {
	run, err := scanPostgresArtifactRuntimeRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", operation, err)
	}
	return run, true, nil
}

func (a *Adapter) ListArtifactRuntimeRuns(ctx context.Context, actorUID, agentUID int64, artifactID string, limit int) ([]*store.ArtifactRuntimeRun, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT `+postgresArtifactRuntimeRunColumns+`
		FROM artifact_runtime_runs
		WHERE actor_uid = $1 AND agent_uid = $2 AND artifact_id = $3
		ORDER BY created_at DESC LIMIT $4`, actorUID, agentUID, artifactID, limit)
	if err != nil {
		return nil, fmt.Errorf("list artifact runtime runs: %w", err)
	}
	defer rows.Close()
	runs := make([]*store.ArtifactRuntimeRun, 0)
	for rows.Next() {
		run, scanErr := scanPostgresArtifactRuntimeRun(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan artifact runtime run: %w", scanErr)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (a *Adapter) ReserveArtifactRuntimeDelivery(
	ctx context.Context,
	taskRefHash string,
	actorUID int64,
	topicID string,
	agentUID int64,
	clientMessageID string,
) (*store.ArtifactRuntimeDeliveryClaim, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin artifact runtime delivery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanPostgresArtifactRuntimeRun(tx.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs WHERE task_ref_hash = $1 FOR UPDATE`, taskRefHash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrArtifactRuntimeRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read artifact runtime delivery: %w", err)
	}
	if run.ActorUID != actorUID || run.TopicID != topicID || run.AgentUID != agentUID || runtimeRunTerminal(run.Status) || !time.Now().UTC().Before(run.ExpiresAt) {
		return nil, store.ErrArtifactRuntimeRunConflict
	}
	if run.Delivered {
		if run.DeliveryClientID != clientMessageID {
			return nil, store.ErrArtifactRuntimeRunConflict
		}
		return &store.ArtifactRuntimeDeliveryClaim{Run: run, AlreadyDelivered: true}, nil
	}
	if run.DeliveryClaimed {
		if run.DeliveryClientID == clientMessageID {
			return nil, store.ErrArtifactRuntimeDeliveryPending
		}
		return nil, store.ErrArtifactRuntimeRunConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_runtime_runs
		SET delivery_claimed = TRUE, delivery_client_id = $1, updated_at = CURRENT_TIMESTAMP
		WHERE task_id = $2`, clientMessageID, run.TaskID); err != nil {
		return nil, fmt.Errorf("reserve artifact runtime delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit artifact runtime delivery: %w", err)
	}
	run.DeliveryClaimed = true
	run.DeliveryClientID = clientMessageID
	return &store.ArtifactRuntimeDeliveryClaim{Run: run}, nil
}

func (a *Adapter) ConfirmArtifactRuntimeDelivery(ctx context.Context, taskID, taskRefHash, clientMessageID string) (bool, error) {
	result, err := a.db.ExecContext(ctx, `UPDATE artifact_runtime_runs
		SET delivery_claimed = FALSE, delivered = TRUE, updated_at = CURRENT_TIMESTAMP
		WHERE task_id = $1 AND task_ref_hash = $2 AND delivery_claimed = TRUE
			AND delivery_client_id = $3 AND delivered = FALSE
			AND status IN ('submitted','running') AND expires_at > CURRENT_TIMESTAMP`,
		taskID, taskRefHash, clientMessageID)
	if err != nil {
		return false, fmt.Errorf("confirm artifact runtime delivery: %w", err)
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (a *Adapter) ReleaseArtifactRuntimeDelivery(ctx context.Context, taskID, taskRefHash, clientMessageID string) error {
	_, err := a.db.ExecContext(ctx, `UPDATE artifact_runtime_runs
		SET delivery_claimed = FALSE, delivery_client_id = '', updated_at = CURRENT_TIMESTAMP
		WHERE task_id = $1 AND task_ref_hash = $2 AND delivery_claimed = TRUE
			AND delivery_client_id = $3 AND delivered = FALSE`, taskID, taskRefHash, clientMessageID)
	if err != nil {
		return fmt.Errorf("release artifact runtime delivery: %w", err)
	}
	return nil
}

func (a *Adapter) FailArtifactRuntimeRun(ctx context.Context, taskID string, actorUID int64, code, message string) (*store.ArtifactRuntimeRun, *store.ArtifactRuntimeEvent, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin artifact runtime failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanPostgresArtifactRuntimeRun(tx.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs WHERE task_id = $1 AND actor_uid = $2 FOR UPDATE`, taskID, actorUID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, store.ErrArtifactRuntimeRunNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read artifact runtime failure target: %w", err)
	}
	if runtimeRunTerminal(run.Status) {
		return run, nil, nil
	}
	if run.Status == "completed" {
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	now := time.Now().UTC()
	code = truncateRuntimeRunText(code, 64)
	message = truncateRuntimeRunText(message, 500)
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_runtime_runs
		SET status = 'failed', code = $1, message = $2, delivery_claimed = FALSE,
			updated_at = $3, finished_at = $3 WHERE task_id = $4`, code, message, now, run.TaskID); err != nil {
		return nil, nil, fmt.Errorf("fail artifact runtime run: %w", err)
	}
	event, err := appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
		EventType: "run.error", AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
		Namespace: "runtime", Key: run.RunID, Revision: 1,
		UpdatedByUID: actorUID, UpdatedBy: "viewer", TaskID: run.TaskID, RunID: run.RunID,
		ExecutorRunID: run.ExecutorRunID, Data: runtimeEventData(map[string]interface{}{
			"status": "failed", "code": code, "message": message,
		}),
	})
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit artifact runtime failure: %w", err)
	}
	run.Status, run.Code, run.Message = "failed", code, message
	run.UpdatedAt, run.FinishedAt = now, &now
	return run, event, nil
}

func (a *Adapter) ObserveArtifactRuntimeExecutor(
	ctx context.Context,
	taskRefHash string,
	agentUID int64,
	topicID, executorRunID, executorState, executorError string,
) (*store.ArtifactRuntimeRun, *store.ArtifactRuntimeEvent, error) {
	if strings.TrimSpace(executorRunID) == "" || !runtimeExecutorStateValid(executorState) {
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin artifact runtime executor update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanPostgresArtifactRuntimeRun(tx.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs WHERE task_ref_hash = $1 FOR UPDATE`, taskRefHash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, store.ErrArtifactRuntimeRunNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read artifact runtime executor target: %w", err)
	}
	if run.AgentUID != agentUID || run.TopicID != topicID || !run.Delivered ||
		(run.ExecutorRunID != "" && run.ExecutorRunID != executorRunID) {
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	now := time.Now().UTC()
	if runtimeRunTerminal(run.Status) {
		return run, nil, nil
	}
	if runtimeExecutorStateTerminal(run.ExecutorState) || run.ExecutorState == executorState ||
		(run.ExecutorState == "running" && executorState == "waiting") {
		return run, nil, nil
	}
	nextStatus := run.Status
	var finishedAt *time.Time
	var event *store.ArtifactRuntimeEvent
	switch executorState {
	case "running", "waiting":
		if run.Status == "submitted" {
			nextStatus = "running"
			if run.StartedAt == nil {
				run.StartedAt = &now
			}
			event, err = appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
				EventType: "run.started", AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
				Namespace: "runtime", Key: run.RunID, Revision: 1,
				UpdatedByUID: agentUID, UpdatedBy: "agent", TaskID: run.TaskID, RunID: run.RunID,
				ExecutorRunID: executorRunID, Data: runtimeEventData(map[string]interface{}{"status": "running"}),
			})
		}
	case "completed":
		finishedAt = &now
		if nextStatus == "submitted" {
			nextStatus = "running"
			if run.StartedAt == nil {
				run.StartedAt = &now
			}
			event, err = appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
				EventType: "run.started", AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
				Namespace: "runtime", Key: run.RunID, Revision: 1,
				UpdatedByUID: agentUID, UpdatedBy: "agent", TaskID: run.TaskID, RunID: run.RunID,
				ExecutorRunID: executorRunID, Data: runtimeEventData(map[string]interface{}{"status": "running"}),
			})
		}
	case "failed", "cancelled", "stale":
		finishedAt = &now
		nextStatus = "failed"
		code := "agent_" + executorState
		message := truncateRuntimeRunText(executorError, 500)
		event, err = appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
			EventType: "run.error", AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
			Namespace: "runtime", Key: run.RunID, Revision: 1,
			UpdatedByUID: agentUID, UpdatedBy: "agent", TaskID: run.TaskID, RunID: run.RunID,
			ExecutorRunID: executorRunID, Data: runtimeEventData(map[string]interface{}{
				"status": "failed", "code": code, "message": message,
			}),
		})
		run.Code, run.Message = code, message
		run.FinishedAt = &now
	default:
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	if err != nil {
		return nil, nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE artifact_runtime_runs SET
		executor_run_id = $1, executor_state = $2, executor_finished_at = $3,
		status = $4, code = $5, message = $6,
		started_at = $7, finished_at = $8, updated_at = $9
		WHERE task_id = $10`, executorRunID, executorState, finishedAt, nextStatus,
		run.Code, run.Message, run.StartedAt, run.FinishedAt, now, run.TaskID)
	if err != nil {
		return nil, nil, fmt.Errorf("update artifact runtime executor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit artifact runtime executor update: %w", err)
	}
	run.ExecutorRunID, run.ExecutorState, run.ExecutorFinishedAt = executorRunID, executorState, finishedAt
	run.Status, run.UpdatedAt = nextStatus, now
	return run, event, nil
}

func (a *Adapter) PutArtifactRuntimeStateForRun(
	ctx context.Context,
	candidate *store.ArtifactRuntimeState,
	baseRevision int64,
	taskRefHash string,
) (*store.ArtifactRuntimeState, *store.ArtifactRuntimeEvent, error) {
	if candidate == nil {
		return nil, nil, errors.New("artifact runtime state candidate is nil")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin artifact runtime state write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanPostgresArtifactRuntimeRun(tx.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs WHERE task_ref_hash = $1 FOR UPDATE`, taskRefHash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, store.ErrArtifactRuntimeRunNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read artifact runtime state run: %w", err)
	}
	if run.AgentUID != candidate.AgentUID || run.ArtifactID != candidate.ArtifactID ||
		run.CompletionMode != "runtime_state" || runtimeRunTerminal(run.Status) {
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	if run.Status == "submitted" {
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_runtime_runs
			SET status = 'running', started_at = COALESCE(started_at, $1), updated_at = $1
			WHERE task_id = $2`, now, run.TaskID); err != nil {
			return nil, nil, fmt.Errorf("start artifact runtime run from state write: %w", err)
		}
		if _, err := appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
			EventType: "run.started", AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
			Namespace: "runtime", Key: run.RunID, Revision: 1,
			UpdatedByUID: candidate.UpdatedByUID, UpdatedBy: "agent", TaskID: run.TaskID, RunID: run.RunID,
			ExecutorRunID: run.ExecutorRunID, Data: runtimeEventData(map[string]interface{}{"status": "running"}),
		}); err != nil {
			return nil, nil, err
		}
	}
	state, err := putPostgresArtifactRuntimeStateTx(ctx, tx, candidate, baseRevision)
	if err != nil {
		return nil, nil, err
	}
	event, err := appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
		EventType: "state.updated", AgentUID: state.AgentUID, ArtifactID: state.ArtifactID,
		Namespace: state.Namespace, Key: state.Key, Revision: state.Revision,
		UpdatedByUID: state.UpdatedByUID, UpdatedBy: state.UpdatedBy,
		TaskID: run.TaskID, RunID: run.RunID, ExecutorRunID: run.ExecutorRunID,
		Data: runtimeEventData(map[string]interface{}{
			"namespace": state.Namespace, "key": state.Key, "revision": state.Revision,
		}),
	})
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_runtime_runs SET updated_at = CURRENT_TIMESTAMP WHERE task_id = $1`, run.TaskID); err != nil {
		return nil, nil, fmt.Errorf("touch artifact runtime run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit artifact runtime state write: %w", err)
	}
	return state, event, nil
}

func putPostgresArtifactRuntimeStateTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate *store.ArtifactRuntimeState,
	baseRevision int64,
) (*store.ArtifactRuntimeState, error) {
	state := &store.ArtifactRuntimeState{
		AgentUID: candidate.AgentUID, ArtifactID: candidate.ArtifactID,
		Namespace: candidate.Namespace, Key: candidate.Key,
		UpdatedByUID: candidate.UpdatedByUID, UpdatedBy: candidate.UpdatedBy,
	}
	var row *sql.Row
	if baseRevision == 0 {
		row = tx.QueryRowContext(ctx, `INSERT INTO artifact_runtime_states (
			agent_uid, artifact_id, namespace, document_key, value_json,
			revision, updated_by_uid, updated_by_type
		) VALUES ($1, $2, $3, $4, $5::jsonb, 1, $6, $7)
		ON CONFLICT (agent_uid, artifact_id, namespace, document_key) DO NOTHING
		RETURNING value_json, revision, created_at, updated_at`,
			candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			string(candidate.Value), candidate.UpdatedByUID, candidate.UpdatedBy)
	} else {
		row = tx.QueryRowContext(ctx, `UPDATE artifact_runtime_states
		SET value_json = $1::jsonb, revision = revision + 1,
			updated_by_uid = $2, updated_by_type = $3, updated_at = CURRENT_TIMESTAMP
		WHERE agent_uid = $4 AND artifact_id = $5 AND namespace = $6
			AND document_key = $7 AND revision = $8
		RETURNING value_json, revision, created_at, updated_at`,
			string(candidate.Value), candidate.UpdatedByUID, candidate.UpdatedBy,
			candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key, baseRevision)
	}
	if err := row.Scan(&state.Value, &state.Revision, &state.CreatedAt, &state.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			currentRevision, revisionErr := postgresArtifactRuntimeRevision(ctx, tx,
				candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key)
			if revisionErr != nil {
				return nil, revisionErr
			}
			return nil, &store.ArtifactRuntimeRevisionConflict{CurrentRevision: currentRevision}
		}
		return nil, fmt.Errorf("write artifact runtime state: %w", err)
	}
	state.Value = append(json.RawMessage(nil), state.Value...)
	return state, nil
}

func (a *Adapter) CompleteArtifactRuntimeRun(
	ctx context.Context,
	taskRefHash string,
	agentUID int64,
	resultID string,
	appliedEventIDs []int64,
) (*store.ArtifactRuntimeRun, []*store.ArtifactRuntimeEvent, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin artifact runtime completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanPostgresArtifactRuntimeRun(tx.QueryRowContext(ctx,
		`SELECT `+postgresArtifactRuntimeRunColumns+` FROM artifact_runtime_runs WHERE task_ref_hash = $1 FOR UPDATE`, taskRefHash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, store.ErrArtifactRuntimeRunNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read artifact runtime completion target: %w", err)
	}
	if run.AgentUID != agentUID || run.CompletionMode != "runtime_state" {
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	if run.Status == "completed" {
		if run.ResultID == resultID && equalRuntimeEventIDs(run.AppliedEventIDs, appliedEventIDs) {
			return run, nil, nil
		}
		return nil, nil, store.ErrArtifactRuntimeRunConflict
	}
	if run.Status == "failed" || len(appliedEventIDs) == 0 || len(appliedEventIDs) > 32 {
		return nil, nil, store.ErrArtifactRuntimeEvidenceInvalid
	}
	seen := make(map[int64]bool, len(appliedEventIDs))
	for _, eventID := range appliedEventIDs {
		if eventID <= 0 || seen[eventID] {
			return nil, nil, store.ErrArtifactRuntimeEvidenceInvalid
		}
		seen[eventID] = true
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_runtime_events
			WHERE agent_uid = $1 AND artifact_id = $2 AND artifact_event_id = $3
				AND event_type = 'state.updated' AND task_id = $4 AND run_id = $5`,
			run.AgentUID, run.ArtifactID, eventID, run.TaskID, run.RunID).Scan(&found); err != nil || found != 1 {
			return nil, nil, store.ErrArtifactRuntimeEvidenceInvalid
		}
	}
	appliedJSON, _ := json.Marshal(appliedEventIDs)
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_runtime_runs
		SET status = 'completed', result_id = $1, applied_event_ids = $2::jsonb,
			code = '', message = '', updated_at = $3, finished_at = $3
		WHERE task_id = $4`, resultID, string(appliedJSON), now, run.TaskID); err != nil {
		return nil, nil, fmt.Errorf("complete artifact runtime run: %w", err)
	}
	resultEvent, err := appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
		EventType: "result.applied", AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
		Namespace: "runtime", Key: run.RunID, Revision: 1,
		UpdatedByUID: agentUID, UpdatedBy: "agent", TaskID: run.TaskID, RunID: run.RunID,
		ExecutorRunID: run.ExecutorRunID, ResultID: resultID,
		Data: runtimeEventData(map[string]interface{}{"applied_event_ids": appliedEventIDs}),
	})
	if err != nil {
		return nil, nil, err
	}
	finishedEvent, err := appendPostgresArtifactRuntimeEvent(ctx, tx, &store.ArtifactRuntimeEvent{
		EventType: "run.finished", AgentUID: run.AgentUID, ArtifactID: run.ArtifactID,
		Namespace: "runtime", Key: run.RunID, Revision: 1,
		UpdatedByUID: agentUID, UpdatedBy: "agent", TaskID: run.TaskID, RunID: run.RunID,
		ExecutorRunID: run.ExecutorRunID, ResultID: resultID,
		Data: runtimeEventData(map[string]interface{}{"status": "completed"}),
	})
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit artifact runtime completion: %w", err)
	}
	run.Status, run.ResultID, run.AppliedEventIDs = "completed", resultID, append([]int64(nil), appliedEventIDs...)
	run.Code, run.Message, run.UpdatedAt, run.FinishedAt = "", "", now, &now
	return run, []*store.ArtifactRuntimeEvent{resultEvent, finishedEvent}, nil
}

func (a *Adapter) ConvergeArtifactRuntimeRuns(ctx context.Context, now time.Time, resultGrace time.Duration, limit int) (int, error) {
	if resultGrace <= 0 {
		resultGrace = 5 * time.Second
	}
	resultDeadline := now.Add(-resultGrace)
	rows, err := a.db.QueryContext(ctx, `SELECT task_id, actor_uid, expires_at, executor_state, executor_finished_at
		FROM artifact_runtime_runs
		WHERE status IN ('submitted','running') AND (
			expires_at <= $1 OR (executor_state = 'completed' AND executor_finished_at <= $2)
		)
		ORDER BY expires_at LIMIT $3`, now, resultDeadline, limit)
	if err != nil {
		return 0, fmt.Errorf("list expired artifact runtime runs: %w", err)
	}
	type candidate struct {
		taskID             string
		actorUID           int64
		expiresAt          time.Time
		executorState      string
		executorFinishedAt *time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(
			&item.taskID, &item.actorUID, &item.expiresAt, &item.executorState, &item.executorFinishedAt,
		); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	_ = rows.Close()
	count := 0
	for _, item := range candidates {
		code, message := "task_expired", "Artifact Runtime Run expired before a result was applied"
		if now.Before(item.expiresAt) && item.executorState == "completed" && item.executorFinishedAt != nil &&
			!item.executorFinishedAt.After(resultDeadline) {
			code, message = "result_not_applied", "Agent turn finished without an applied Artifact Runtime result"
		}
		if _, _, failErr := a.FailArtifactRuntimeRun(ctx, item.taskID, item.actorUID, code, message); failErr == nil {
			count++
		}
	}
	return count, nil
}

func appendPostgresArtifactRuntimeEvent(
	ctx context.Context,
	tx *sql.Tx,
	event *store.ArtifactRuntimeEvent,
) (*store.ArtifactRuntimeEvent, error) {
	eventID, err := postgresNextArtifactRuntimeEventID(ctx, tx, event.AgentUID, event.ArtifactID)
	if err != nil {
		return nil, err
	}
	event.EventID = eventID
	data := event.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO artifact_runtime_events (
		artifact_event_id, event_type, agent_uid, artifact_id, namespace, document_key,
		revision, updated_by_uid, updated_by_type, task_id, run_id, executor_run_id,
		result_id, event_data
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),$14::jsonb)
	RETURNING created_at`, event.EventID, event.EventType, event.AgentUID, event.ArtifactID,
		event.Namespace, event.Key, event.Revision, event.UpdatedByUID, event.UpdatedBy,
		event.TaskID, event.RunID, event.ExecutorRunID, event.ResultID, string(data)).Scan(&event.CreatedAt); err != nil {
		return nil, fmt.Errorf("append artifact runtime event: %w", err)
	}
	event.Data = append(json.RawMessage(nil), data...)
	return event, nil
}

func runtimeEventData(value map[string]interface{}) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func runtimeRunTerminal(status string) bool {
	return status == "completed" || status == "failed"
}

func runtimeExecutorStateValid(status string) bool {
	switch status {
	case "waiting", "running", "completed", "failed", "cancelled", "stale":
		return true
	default:
		return false
	}
}

func runtimeExecutorStateTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "stale"
}

func truncateRuntimeRunText(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}

func equalRuntimeEventIDs(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

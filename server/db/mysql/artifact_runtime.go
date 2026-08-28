package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

var _ store.ArtifactRuntimeStateStore = (*Adapter)(nil)

func (a *Adapter) GetArtifactRuntimeState(
	ctx context.Context,
	agentUID int64,
	artifactID, namespace, key string,
) (*store.ArtifactRuntimeState, bool, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT value_json, revision, updated_by_uid, updated_by_type, created_at, updated_at
		FROM artifact_runtime_states
		WHERE agent_uid = ? AND artifact_id = ? AND namespace = ? AND document_key = ?`,
		agentUID, artifactID, namespace, key,
	)
	state := &store.ArtifactRuntimeState{
		AgentUID: agentUID, ArtifactID: artifactID, Namespace: namespace, Key: key,
	}
	if err := row.Scan(
		&state.Value,
		&state.Revision,
		&state.UpdatedByUID,
		&state.UpdatedBy,
		&state.CreatedAt,
		&state.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get artifact runtime state: %w", err)
	}
	state.Value = append(json.RawMessage(nil), state.Value...)
	return state, true, nil
}

func (a *Adapter) ListArtifactRuntimeStates(
	ctx context.Context,
	agentUID int64,
	artifactID string,
	limit int,
) ([]*store.ArtifactRuntimeState, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT namespace, document_key, revision, updated_at
		FROM artifact_runtime_states
		WHERE agent_uid = ? AND artifact_id = ?
		ORDER BY namespace, document_key
		LIMIT ?`, agentUID, artifactID, limit)
	if err != nil {
		return nil, fmt.Errorf("list artifact runtime states: %w", err)
	}
	defer rows.Close()
	states := make([]*store.ArtifactRuntimeState, 0)
	for rows.Next() {
		state := &store.ArtifactRuntimeState{AgentUID: agentUID, ArtifactID: artifactID}
		if err := rows.Scan(
			&state.Namespace,
			&state.Key,
			&state.Revision,
			&state.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan artifact runtime state: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact runtime states: %w", err)
	}
	return states, nil
}

func (a *Adapter) PutArtifactRuntimeState(
	ctx context.Context,
	candidate *store.ArtifactRuntimeState,
	baseRevision int64,
) (*store.ArtifactRuntimeState, *store.ArtifactRuntimeEvent, error) {
	if candidate == nil {
		return nil, nil, errors.New("artifact runtime state candidate is nil")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin artifact runtime state write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if baseRevision == 0 {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO artifact_runtime_states (
				agent_uid, artifact_id, namespace, document_key, value_json,
				revision, updated_by_uid, updated_by_type
			) VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
			candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			string(candidate.Value), candidate.UpdatedByUID, candidate.UpdatedBy,
		)
		if err != nil {
			currentRevision, revisionErr := mysqlArtifactRuntimeRevision(
				ctx, tx, candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			)
			if revisionErr == nil && currentRevision > 0 {
				return nil, nil, &store.ArtifactRuntimeRevisionConflict{CurrentRevision: currentRevision}
			}
			return nil, nil, fmt.Errorf("create artifact runtime state: %w", err)
		}
	} else {
		result, updateErr := tx.ExecContext(ctx, `
			UPDATE artifact_runtime_states
			SET value_json = ?, revision = revision + 1,
				updated_by_uid = ?, updated_by_type = ?, updated_at = CURRENT_TIMESTAMP(3)
			WHERE agent_uid = ? AND artifact_id = ? AND namespace = ?
				AND document_key = ? AND revision = ?`,
			string(candidate.Value), candidate.UpdatedByUID, candidate.UpdatedBy,
			candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			baseRevision,
		)
		if updateErr != nil {
			return nil, nil, fmt.Errorf("update artifact runtime state: %w", updateErr)
		}
		rowsAffected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, nil, fmt.Errorf("inspect artifact runtime update: %w", rowsErr)
		}
		if rowsAffected != 1 {
			currentRevision, revisionErr := mysqlArtifactRuntimeRevision(
				ctx, tx, candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			)
			if revisionErr != nil {
				return nil, nil, revisionErr
			}
			return nil, nil, &store.ArtifactRuntimeRevisionConflict{CurrentRevision: currentRevision}
		}
	}

	state, err := mysqlArtifactRuntimeStateInTx(
		ctx, tx, candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
	)
	if err != nil {
		return nil, nil, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_runtime_events (
			event_type, agent_uid, artifact_id, namespace, document_key,
			revision, updated_by_uid, updated_by_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"state.updated", state.AgentUID, state.ArtifactID, state.Namespace,
		state.Key, state.Revision, state.UpdatedByUID, state.UpdatedBy,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("append artifact runtime event: %w", err)
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		return nil, nil, fmt.Errorf("read artifact runtime event id: %w", err)
	}
	event := &store.ArtifactRuntimeEvent{
		EventID: eventID, EventType: "state.updated", AgentUID: state.AgentUID,
		ArtifactID: state.ArtifactID, Namespace: state.Namespace, Key: state.Key,
		Revision: state.Revision, UpdatedByUID: state.UpdatedByUID, UpdatedBy: state.UpdatedBy,
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT created_at FROM artifact_runtime_events WHERE id = ?`, eventID,
	).Scan(&event.CreatedAt); err != nil {
		return nil, nil, fmt.Errorf("read artifact runtime event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit artifact runtime state write: %w", err)
	}
	return state, event, nil
}

func mysqlArtifactRuntimeRevision(
	ctx context.Context,
	tx *sql.Tx,
	agentUID int64,
	artifactID, namespace, key string,
) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `
		SELECT revision FROM artifact_runtime_states
		WHERE agent_uid = ? AND artifact_id = ? AND namespace = ? AND document_key = ?`,
		agentUID, artifactID, namespace, key,
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read artifact runtime conflict revision: %w", err)
	}
	return revision, nil
}

func mysqlArtifactRuntimeStateInTx(
	ctx context.Context,
	tx *sql.Tx,
	agentUID int64,
	artifactID, namespace, key string,
) (*store.ArtifactRuntimeState, error) {
	state := &store.ArtifactRuntimeState{
		AgentUID: agentUID, ArtifactID: artifactID, Namespace: namespace, Key: key,
	}
	err := tx.QueryRowContext(ctx, `
		SELECT value_json, revision, updated_by_uid, updated_by_type, created_at, updated_at
		FROM artifact_runtime_states
		WHERE agent_uid = ? AND artifact_id = ? AND namespace = ? AND document_key = ?`,
		agentUID, artifactID, namespace, key,
	).Scan(
		&state.Value,
		&state.Revision,
		&state.UpdatedByUID,
		&state.UpdatedBy,
		&state.CreatedAt,
		&state.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("read written artifact runtime state: %w", err)
	}
	state.Value = append(json.RawMessage(nil), state.Value...)
	return state, nil
}

func (a *Adapter) ListArtifactRuntimeEvents(
	ctx context.Context,
	agentUID int64,
	artifactID string,
	afterEventID int64,
	limit int,
) ([]*store.ArtifactRuntimeEvent, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, event_type, namespace, document_key, revision,
			updated_by_uid, updated_by_type, created_at
		FROM artifact_runtime_events
		WHERE agent_uid = ? AND artifact_id = ? AND id > ?
		ORDER BY id
		LIMIT ?`, agentUID, artifactID, afterEventID, limit)
	if err != nil {
		return nil, fmt.Errorf("list artifact runtime events: %w", err)
	}
	defer rows.Close()
	events := make([]*store.ArtifactRuntimeEvent, 0)
	for rows.Next() {
		event := &store.ArtifactRuntimeEvent{AgentUID: agentUID, ArtifactID: artifactID}
		if err := rows.Scan(
			&event.EventID,
			&event.EventType,
			&event.Namespace,
			&event.Key,
			&event.Revision,
			&event.UpdatedByUID,
			&event.UpdatedBy,
			&event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan artifact runtime event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact runtime events: %w", err)
	}
	return events, nil
}

func (a *Adapter) LatestArtifactRuntimeEventID(
	ctx context.Context,
	agentUID int64,
	artifactID string,
) (int64, error) {
	var eventID int64
	if err := a.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(id), 0)
		FROM artifact_runtime_events
		WHERE agent_uid = ? AND artifact_id = ?`, agentUID, artifactID).Scan(&eventID); err != nil {
		return 0, fmt.Errorf("read latest artifact runtime event: %w", err)
	}
	return eventID, nil
}

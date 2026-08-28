package postgres

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
		WHERE agent_uid = $1 AND artifact_id = $2 AND namespace = $3 AND document_key = $4`,
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
		WHERE agent_uid = $1 AND artifact_id = $2
		ORDER BY namespace, document_key
		LIMIT $3`, agentUID, artifactID, limit)
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

	state := &store.ArtifactRuntimeState{
		AgentUID: candidate.AgentUID, ArtifactID: candidate.ArtifactID,
		Namespace: candidate.Namespace, Key: candidate.Key,
		UpdatedByUID: candidate.UpdatedByUID, UpdatedBy: candidate.UpdatedBy,
	}
	var row *sql.Row
	if baseRevision == 0 {
		row = tx.QueryRowContext(ctx, `
			INSERT INTO artifact_runtime_states (
				agent_uid, artifact_id, namespace, document_key, value_json,
				revision, updated_by_uid, updated_by_type
			) VALUES ($1, $2, $3, $4, $5::jsonb, 1, $6, $7)
			ON CONFLICT (agent_uid, artifact_id, namespace, document_key) DO NOTHING
			RETURNING value_json, revision, created_at, updated_at`,
			candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			string(candidate.Value), candidate.UpdatedByUID, candidate.UpdatedBy,
		)
	} else {
		row = tx.QueryRowContext(ctx, `
			UPDATE artifact_runtime_states
			SET value_json = $1::jsonb, revision = revision + 1,
				updated_by_uid = $2, updated_by_type = $3, updated_at = CURRENT_TIMESTAMP
			WHERE agent_uid = $4 AND artifact_id = $5 AND namespace = $6
				AND document_key = $7 AND revision = $8
			RETURNING value_json, revision, created_at, updated_at`,
			string(candidate.Value), candidate.UpdatedByUID, candidate.UpdatedBy,
			candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			baseRevision,
		)
	}
	if err := row.Scan(&state.Value, &state.Revision, &state.CreatedAt, &state.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			currentRevision, revisionErr := postgresArtifactRuntimeRevision(
				ctx, tx, candidate.AgentUID, candidate.ArtifactID, candidate.Namespace, candidate.Key,
			)
			if revisionErr != nil {
				return nil, nil, revisionErr
			}
			return nil, nil, &store.ArtifactRuntimeRevisionConflict{CurrentRevision: currentRevision}
		}
		return nil, nil, fmt.Errorf("write artifact runtime state: %w", err)
	}
	state.Value = append(json.RawMessage(nil), state.Value...)
	event := &store.ArtifactRuntimeEvent{
		EventType: "state.updated", AgentUID: state.AgentUID, ArtifactID: state.ArtifactID,
		Namespace: state.Namespace, Key: state.Key, Revision: state.Revision,
		UpdatedByUID: state.UpdatedByUID, UpdatedBy: state.UpdatedBy,
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO artifact_runtime_events (
			event_type, agent_uid, artifact_id, namespace, document_key,
			revision, updated_by_uid, updated_by_type
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		event.EventType, event.AgentUID, event.ArtifactID, event.Namespace,
		event.Key, event.Revision, event.UpdatedByUID, event.UpdatedBy,
	).Scan(&event.EventID, &event.CreatedAt); err != nil {
		return nil, nil, fmt.Errorf("append artifact runtime event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit artifact runtime state write: %w", err)
	}
	return state, event, nil
}

func postgresArtifactRuntimeRevision(
	ctx context.Context,
	tx *sql.Tx,
	agentUID int64,
	artifactID, namespace, key string,
) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `
		SELECT revision FROM artifact_runtime_states
		WHERE agent_uid = $1 AND artifact_id = $2 AND namespace = $3 AND document_key = $4`,
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
		WHERE agent_uid = $1 AND artifact_id = $2 AND id > $3
		ORDER BY id
		LIMIT $4`, agentUID, artifactID, afterEventID, limit)
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
		WHERE agent_uid = $1 AND artifact_id = $2`, agentUID, artifactID).Scan(&eventID); err != nil {
		return 0, fmt.Errorf("read latest artifact runtime event: %w", err)
	}
	return eventID, nil
}

package postgres

import (
	"context"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

const agentArtifactTagQueryChunk = 100

// ListAgentArtifactTags returns the agent-scoped tag sets for the requested
// artifacts. Artifacts without tags are absent from the result map.
func (a *Adapter) ListAgentArtifactTags(agentUID int64, artifactIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(artifactIDs))
	if agentUID <= 0 || len(artifactIDs) == 0 {
		return result, nil
	}
	ctx := context.Background()
	for start := 0; start < len(artifactIDs); start += agentArtifactTagQueryChunk {
		end := start + agentArtifactTagQueryChunk
		if end > len(artifactIDs) {
			end = len(artifactIDs)
		}
		chunk := artifactIDs[start:end]
		query := `
			SELECT artifact_id, tag
			FROM agent_artifact_tags
			WHERE agent_uid = $1 AND artifact_id IN (` + inPlaceholders(2, len(chunk)) + `)
			ORDER BY artifact_id ASC, created_at ASC, tag ASC`
		args := make([]interface{}, 0, len(chunk)+1)
		args = append(args, agentUID)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := a.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("list agent artifact tags: %w", err)
		}
		for rows.Next() {
			var artifactID, tag string
			if err := rows.Scan(&artifactID, &tag); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan agent artifact tag: %w", err)
			}
			result[artifactID] = append(result[artifactID], tag)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate agent artifact tags: %w", err)
		}
		rows.Close()
	}
	return result, nil
}

// ListAgentArtifactTagCounts aggregates the agent's tags across its artifacts.
func (a *Adapter) ListAgentArtifactTagCounts(agentUID int64) ([]store.AgentArtifactTagCount, error) {
	if agentUID <= 0 {
		return []store.AgentArtifactTagCount{}, nil
	}
	rows, err := a.db.Query(`
		SELECT tag, count(*) AS artifact_count
		FROM agent_artifact_tags
		WHERE agent_uid = $1
		GROUP BY tag
		ORDER BY artifact_count DESC, tag ASC`, agentUID)
	if err != nil {
		return nil, fmt.Errorf("list agent artifact tag counts: %w", err)
	}
	defer rows.Close()
	counts := []store.AgentArtifactTagCount{}
	for rows.Next() {
		var entry store.AgentArtifactTagCount
		if err := rows.Scan(&entry.Tag, &entry.Count); err != nil {
			return nil, fmt.Errorf("scan agent artifact tag count: %w", err)
		}
		counts = append(counts, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent artifact tag counts: %w", err)
	}
	return counts, nil
}

// ReplaceAgentArtifactTags atomically swaps one artifact's tag set. The
// returned slice mirrors the requested order after normalization already
// happened in the handler layer.
func (a *Adapter) ReplaceAgentArtifactTags(agentUID int64, artifactID string, tags []string, createdBy int64) ([]string, error) {
	ctx := context.Background()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin agent artifact tag replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_artifact_tags WHERE agent_uid = $1 AND artifact_id = $2`,
		agentUID, artifactID); err != nil {
		return nil, fmt.Errorf("clear agent artifact tags: %w", err)
	}
	for _, tag := range tags {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_artifact_tags (agent_uid, artifact_id, tag, created_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (agent_uid, artifact_id, tag) DO NOTHING`,
			agentUID, artifactID, tag, createdBy); err != nil {
			return nil, fmt.Errorf("insert agent artifact tag: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit agent artifact tag replace: %w", err)
	}
	return tags, nil
}

// DeleteAgentArtifactTag removes one tag from one artifact and reports whether
// a row was removed.
func (a *Adapter) DeleteAgentArtifactTag(agentUID int64, artifactID, tag string) (bool, error) {
	result, err := a.db.Exec(`
		DELETE FROM agent_artifact_tags
		WHERE agent_uid = $1 AND artifact_id = $2 AND tag = $3`,
		agentUID, artifactID, tag)
	if err != nil {
		return false, fmt.Errorf("delete agent artifact tag: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read agent artifact tag delete result: %w", err)
	}
	return affected > 0, nil
}

// PurgeAgentArtifactTags removes every tag row for one artifact. It is
// idempotent: purging an artifact without tags is a no-op.
func (a *Adapter) PurgeAgentArtifactTags(agentUID int64, artifactID string) error {
	if _, err := a.db.Exec(`
		DELETE FROM agent_artifact_tags
		WHERE agent_uid = $1 AND artifact_id = $2`,
		agentUID, artifactID); err != nil {
		return fmt.Errorf("purge agent artifact tags: %w", err)
	}
	return nil
}

// ListAgentArtifactTagArtifactIDs returns the distinct artifact IDs that
// currently have tag rows for the agent. It feeds orphan reconciliation:
// IDs absent from the active managed list get their rows purged.
func (a *Adapter) ListAgentArtifactTagArtifactIDs(agentUID int64) ([]string, error) {
	if agentUID <= 0 {
		return []string{}, nil
	}
	rows, err := a.db.Query(`
		SELECT DISTINCT artifact_id
		FROM agent_artifact_tags
		WHERE agent_uid = $1
		ORDER BY artifact_id ASC`, agentUID)
	if err != nil {
		return nil, fmt.Errorf("list agent artifact tag artifact ids: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan agent artifact tag artifact id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent artifact tag artifact ids: %w", err)
	}
	return ids, nil
}

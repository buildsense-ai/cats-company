package mysql

import (
	"context"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

const createArtifactAppMetadataTable = `CREATE TABLE IF NOT EXISTS artifact_app_metadata (
 app_id VARCHAR(48) PRIMARY KEY,
 agent_uid BIGINT NOT NULL,
 creator_uid BIGINT NOT NULL DEFAULT 0,
 title VARCHAR(128) NOT NULL,
 description TEXT NOT NULL,
 icon_url TEXT NOT NULL,
 KEY idx_artifact_app_metadata_agent (agent_uid)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

var _ store.ArtifactAppMetadataStore = (*Adapter)(nil)

func (a *Adapter) DeleteArtifactAppMetadata(ctx context.Context, agentUID int64, id string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM artifact_app_metadata WHERE agent_uid = ? AND app_id = ?`, agentUID, id)
	return err
}

func (a *Adapter) ListArtifactAppMetadata(ctx context.Context, agentUID int64) (map[string]store.ArtifactAppMetadata, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT app_id, agent_uid, creator_uid, title, description, icon_url FROM artifact_app_metadata WHERE agent_uid = ?`, agentUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]store.ArtifactAppMetadata)
	for rows.Next() {
		var item store.ArtifactAppMetadata
		if err := rows.Scan(&item.AppID, &item.AgentUID, &item.CreatorUID, &item.Title, &item.Description, &item.IconURL); err != nil {
			return nil, err
		}
		result[item.AppID] = item
	}
	return result, rows.Err()
}

// Initialization never overwrites provenance or edited presentation.
func (a *Adapter) EnsureArtifactAppMetadata(ctx context.Context, item store.ArtifactAppMetadata) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO artifact_app_metadata (app_id, agent_uid, creator_uid, title, description, icon_url) VALUES (?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE app_id = app_id`,
		item.AppID, item.AgentUID, item.CreatorUID, item.Title, item.Description, item.IconURL)
	if err != nil {
		return err
	}
	var agentUID int64
	if err := a.db.QueryRowContext(ctx, `SELECT agent_uid FROM artifact_app_metadata WHERE app_id = ?`, item.AppID).Scan(&agentUID); err != nil {
		return err
	}
	if agentUID != item.AgentUID {
		return fmt.Errorf("application metadata belongs to another Agent")
	}
	return nil
}

func (a *Adapter) UpdateArtifactAppMetadata(ctx context.Context, item store.ArtifactAppMetadata) error {
	result, err := a.db.ExecContext(ctx, `UPDATE artifact_app_metadata SET title = ?, description = ?, icon_url = ? WHERE app_id = ? AND agent_uid = ?`,
		item.Title, item.Description, item.IconURL, item.AppID, item.AgentUID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		// MySQL reports zero for an unchanged row. Verify it still exists.
		items, err := a.ListArtifactAppMetadata(ctx, item.AgentUID)
		if err != nil {
			return err
		}
		if _, ok := items[item.AppID]; !ok {
			return fmt.Errorf("application metadata not found")
		}
	}
	return nil
}

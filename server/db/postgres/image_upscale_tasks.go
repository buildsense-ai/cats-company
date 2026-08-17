package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UpsertImageUpscaleTaskOwner records which CatsCo user may poll a provider task.
func (a *Adapter) UpsertImageUpscaleTaskOwner(ctx context.Context, processID string, ownerUID int64, expiresAt time.Time) error {
	if _, err := a.db.ExecContext(ctx, `DELETE FROM image_upscale_tasks WHERE expires_at <= CURRENT_TIMESTAMP`); err != nil {
		return fmt.Errorf("delete expired image upscale tasks: %w", err)
	}
	_, err := a.db.ExecContext(ctx, `
		INSERT INTO image_upscale_tasks (process_id, owner_uid, expires_at, updated_at)
		VALUES ($1, $2, $3, CURRENT_TIMESTAMP)
		ON CONFLICT (process_id) DO UPDATE SET
			owner_uid = EXCLUDED.owner_uid,
			expires_at = EXCLUDED.expires_at,
			updated_at = CURRENT_TIMESTAMP`, processID, ownerUID, expiresAt)
	if err != nil {
		return fmt.Errorf("upsert image upscale task owner: %w", err)
	}
	return nil
}

// GetImageUpscaleTaskOwner returns only unexpired task ownership records.
func (a *Adapter) GetImageUpscaleTaskOwner(ctx context.Context, processID string, now time.Time) (int64, bool, error) {
	var ownerUID int64
	err := a.db.QueryRowContext(ctx, `
		SELECT owner_uid
		FROM image_upscale_tasks
		WHERE process_id = $1 AND expires_at > $2`, processID, now).Scan(&ownerUID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get image upscale task owner: %w", err)
	}
	return ownerUID, true, nil
}

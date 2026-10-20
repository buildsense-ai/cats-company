package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// StartServiceRun 把指定 run 从 submitted 推到 running（外部执行后端；PG 同语义镜像）。
func (a *Adapter) StartServiceRun(ctx context.Context, runID, instanceID string, leaseSeconds int) (bool, error) {
	if leaseSeconds <= 0 {
		leaseSeconds = 30
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin start service run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM service_runs WHERE run_id = ? FOR UPDATE`, runID).Scan(&state)
	if err == sql.ErrNoRows {
		return false, store.ErrServiceRunNotFound
	}
	if err != nil {
		return false, fmt.Errorf("start service run: %w", err)
	}
	if state != "submitted" {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE service_runs
		SET state = 'running',
		    instance_id = ?,
		    lease_epoch = lease_epoch + 1,
		    lease_expires_at = DATE_ADD(NOW(6), INTERVAL ? SECOND),
		    started_at = COALESCE(started_at, NOW(6))
		WHERE run_id = ?`, instanceID, leaseSeconds, runID); err != nil {
		return false, fmt.Errorf("start service run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("start service run: %w", err)
	}
	return true, nil
}

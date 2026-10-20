package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store"
)

// StartServiceRun 把指定 run 从 submitted 推到 running（外部执行后端；设计 §4.4 P3-a）。
// 与 ClaimServiceRuns 的区别：只推进**这一个** run，不参与队列抢占——
// 外部 runner 由调用面在创建 run 时直接指定，不需要 worker 来领。
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
	err = tx.QueryRowContext(ctx, `SELECT state FROM service_runs WHERE run_id = $1 FOR UPDATE`, runID).Scan(&state)
	if err == sql.ErrNoRows {
		return false, store.ErrServiceRunNotFound
	}
	if err != nil {
		return false, fmt.Errorf("start service run: %w", err)
	}
	if state != "submitted" {
		// 已被接管（running）或已终态：调用方放弃本次执行。
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE service_runs
		SET state = 'running',
		    instance_id = $2,
		    lease_epoch = lease_epoch + 1,
		    lease_expires_at = now() + make_interval(secs => $3),
		    started_at = COALESCE(started_at, now())
		WHERE run_id = $1`, runID, instanceID, leaseSeconds); err != nil {
		return false, fmt.Errorf("start service run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("start service run: %w", err)
	}
	return true, nil
}

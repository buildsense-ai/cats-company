package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// CloudWorkerCreditSummary reports paid one-time cloud-worker credits. A
// consumed credit is intentionally not returned when the worker is deleted.
func (a *Adapter) CloudWorkerCreditSummary(uid int64) (total, available int, err error) {
	if uid <= 0 {
		return 0, 0, fmt.Errorf("invalid cloud worker credit owner")
	}
	err = a.db.QueryRow(`
		SELECT COUNT(*) FILTER (WHERE state IN ('available','reserved','consumed')),
		       COUNT(*) FILTER (WHERE state = 'available')
		FROM cloud_worker_credits
		WHERE uid = $1 AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`, uid).Scan(&total, &available)
	return
}

// ReserveCloudWorkerCredit atomically claims one available paid credit. A
// stale reservation is released so a crashed provision request is retryable.
func (a *Adapter) ReserveCloudWorkerCredit(uid int64, reservation string) (bool, error) {
	reservation = strings.TrimSpace(reservation)
	if uid <= 0 || reservation == "" {
		return false, fmt.Errorf("invalid cloud worker credit reservation")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin cloud worker credit reservation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'available', reservation_ref = '', reserved_at = NULL
		WHERE uid = $1 AND state = 'reserved' AND reserved_at < CURRENT_TIMESTAMP - INTERVAL '20 minutes'`, uid); err != nil {
		return false, fmt.Errorf("release stale cloud worker credit reservation: %w", err)
	}
	var id int64
	err = tx.QueryRow(`
		SELECT id FROM cloud_worker_credits
		WHERE uid = $1 AND state = 'available'
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		ORDER BY created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`, uid).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select cloud worker credit: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'reserved', reservation_ref = $2, reserved_at = CURRENT_TIMESTAMP
		WHERE id = $1`, id, reservation); err != nil {
		return false, fmt.Errorf("mark cloud worker credit reserved: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit cloud worker credit reservation: %w", err)
	}
	return true, nil
}

func (a *Adapter) CommitCloudWorkerCredit(uid int64, reservation string, workerUID int64, tenantName string, graceDays int) error {
	if uid <= 0 || strings.TrimSpace(reservation) == "" || workerUID <= 0 {
		return fmt.Errorf("invalid cloud worker credit commit")
	}
	if strings.TrimSpace(tenantName) == "" || graceDays < 0 || graceDays > 90 {
		return fmt.Errorf("invalid cloud worker lifecycle metadata")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("begin cloud worker credit commit: %w", err)
	}
	defer tx.Rollback()
	var expiresAt time.Time
	if err := tx.QueryRow(`
		UPDATE cloud_worker_credits
		SET state = 'consumed', worker_uid = $3, consumed_at = CURRENT_TIMESTAMP
		WHERE uid = $1 AND reservation_ref = $2 AND state = 'reserved'
		RETURNING expires_at`, uid, strings.TrimSpace(reservation), workerUID).Scan(&expiresAt); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("cloud worker credit reservation not found")
		}
		return fmt.Errorf("commit cloud worker credit: %w", err)
	}
	deleteAfter := expiresAt.AddDate(0, 0, graceDays)
	if _, err := tx.Exec(`
		INSERT INTO cloud_worker_lifecycles(worker_uid, owner_uid, tenant_name, package_expires_at, delete_after, state)
		VALUES ($1, $2, $3, $4, $5, 'active')
		ON CONFLICT (worker_uid) DO UPDATE SET owner_uid = EXCLUDED.owner_uid,
		  tenant_name = EXCLUDED.tenant_name, package_expires_at = EXCLUDED.package_expires_at,
		  delete_after = EXCLUDED.delete_after, state = 'active', archived_at = NULL,
		  delete_started_at = NULL, last_error = '', updated_at = CURRENT_TIMESTAMP`,
		workerUID, uid, strings.TrimSpace(tenantName), expiresAt, deleteAfter); err != nil {
		return fmt.Errorf("register cloud worker lifecycle: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cloud worker lifecycle: %w", err)
	}
	return nil
}

func (a *Adapter) ReleaseCloudWorkerCredit(uid int64, reservation string) error {
	if uid <= 0 || strings.TrimSpace(reservation) == "" {
		return fmt.Errorf("invalid cloud worker credit release")
	}
	_, err := a.db.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'available', reservation_ref = '', reserved_at = NULL
		WHERE uid = $1 AND reservation_ref = $2 AND state = 'reserved'`, uid, strings.TrimSpace(reservation))
	if err != nil {
		return fmt.Errorf("release cloud worker credit: %w", err)
	}
	return nil
}

func (a *Adapter) ExtendCloudWorkerLifecycles(uid int64, expiresAt time.Time, graceDays int) error {
	if uid <= 0 || expiresAt.IsZero() || graceDays < 0 || graceDays > 90 {
		return fmt.Errorf("invalid cloud worker lifecycle extension")
	}
	_, err := a.db.Exec(`
		UPDATE cloud_worker_lifecycles
		SET package_expires_at = $2::timestamptz,
		    delete_after = $2::timestamptz + ($3::int * INTERVAL '1 day'),
		    state = 'active', archived_at = NULL, delete_started_at = NULL,
		    last_error = '', updated_at = CURRENT_TIMESTAMP
		WHERE owner_uid = $1 AND state <> 'deleted'`, uid, expiresAt, graceDays)
	if err != nil {
		return fmt.Errorf("extend cloud worker lifecycles: %w", err)
	}
	return nil
}

func (a *Adapter) ListCloudWorkerLifecycles(uid int64) ([]types.CloudWorkerLifecycle, error) {
	rows, err := a.db.Query(`
		SELECT id, worker_uid, owner_uid, tenant_name, package_expires_at, delete_after, state
		FROM cloud_worker_lifecycles WHERE owner_uid = $1 AND state <> 'deleted'
		ORDER BY created_at, id`, uid)
	if err != nil {
		return nil, fmt.Errorf("list cloud worker lifecycles: %w", err)
	}
	defer rows.Close()
	var records []types.CloudWorkerLifecycle
	for rows.Next() {
		var item types.CloudWorkerLifecycle
		if err := rows.Scan(&item.ID, &item.WorkerUID, &item.OwnerUID, &item.TenantName, &item.PackageExpiresAt, &item.DeleteAfter, &item.State); err != nil {
			return nil, fmt.Errorf("scan cloud worker lifecycle: %w", err)
		}
		records = append(records, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read cloud worker lifecycles: %w", err)
	}
	return records, nil
}

func (a *Adapter) ListCloudWorkerLifecycleDue(now time.Time, limit int) ([]types.CloudWorkerLifecycle, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := a.db.Query(`
		SELECT id, worker_uid, owner_uid, tenant_name, package_expires_at, delete_after, state
		FROM cloud_worker_lifecycles
		WHERE (state = 'active' AND package_expires_at <= $1)
		   OR (state IN ('delete_pending','delete_failed') AND delete_after <= $1)
		   OR (state = 'delete_running' AND delete_started_at <= $1 - INTERVAL '30 minutes')
		ORDER BY delete_after, id LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list due cloud worker lifecycles: %w", err)
	}
	defer rows.Close()
	var records []types.CloudWorkerLifecycle
	for rows.Next() {
		var item types.CloudWorkerLifecycle
		if err := rows.Scan(&item.ID, &item.WorkerUID, &item.OwnerUID, &item.TenantName, &item.PackageExpiresAt, &item.DeleteAfter, &item.State); err != nil {
			return nil, fmt.Errorf("scan due cloud worker lifecycle: %w", err)
		}
		records = append(records, item)
	}
	return records, rows.Err()
}

func (a *Adapter) MarkCloudWorkerLifecyclePending(id int64, deleteAfter time.Time) error {
	_, err := a.db.Exec(`
		UPDATE cloud_worker_lifecycles
		SET state = 'delete_pending', archived_at = COALESCE(archived_at, CURRENT_TIMESTAMP),
		    delete_after = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND state = 'active'`, id, deleteAfter)
	return err
}

func (a *Adapter) ClaimCloudWorkerLifecycleDeletion(id int64) (bool, error) {
	result, err := a.db.Exec(`
		UPDATE cloud_worker_lifecycles
		SET state = 'delete_running', delete_started_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND (state IN ('delete_pending','delete_failed')
		   OR (state = 'delete_running' AND delete_started_at <= CURRENT_TIMESTAMP - INTERVAL '30 minutes'))`, id)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

func (a *Adapter) MarkCloudWorkerLifecycleDeleted(id int64, errText string) error {
	state := "deleted"
	if strings.TrimSpace(errText) != "" {
		state = "delete_failed"
	}
	_, err := a.db.Exec(`
		UPDATE cloud_worker_lifecycles
		SET state = $2, deleted_at = CASE WHEN $2 = 'deleted' THEN CURRENT_TIMESTAMP ELSE deleted_at END,
		    last_error = $3, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, id, state, strings.TrimSpace(errText))
	return err
}

// grantCloudWorkerCredit is called inside the payment fulfillment transaction.
func grantCloudWorkerCredit(tx *sql.Tx, uid int64, sourceRef string, expiresAt time.Time) error {
	if uid <= 0 || strings.TrimSpace(sourceRef) == "" {
		return fmt.Errorf("invalid cloud worker credit grant")
	}
	_, err := tx.Exec(`
		INSERT INTO cloud_worker_credits(uid, source_ref, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (source_ref) DO NOTHING`, uid, strings.TrimSpace(sourceRef), expiresAt)
	if err != nil {
		return fmt.Errorf("grant cloud worker credit: %w", err)
	}
	return nil
}

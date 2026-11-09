package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// AcquireCommercialWorkerOwnerGate also guards ordinary HandleCreate calls,
// making ensure's roster check atomic with creates across server processes.
func (a *Adapter) AcquireCommercialWorkerOwnerGate(uid int64) (func(), bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := a.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended('cloud-worker-owner:' || $1::bigint::text,0))`, uid).Scan(&ok); err != nil || !ok {
		conn.Close()
		return nil, false, err
	}
	return func() {
		releaseCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, e := conn.ExecContext(releaseCtx, `SELECT pg_advisory_unlock(hashtextextended('cloud-worker-owner:' || $1::bigint::text,0))`, uid)
		if e != nil {
			_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
		}
		conn.Close()
	}, true, nil
}

// ReserveCommercialOperationCredit never considers generic credits or static
// quota. Only the durable action may revive its own queued/stale reservation.
func (a *Adapter) ReserveCommercialOperationCredit(uid int64, source, reservation string, p types.CommercialActionPreview) (types.CloudWorkerCreditSelection, bool, error) {
	var selection types.CloudWorkerCreditSelection
	err := a.db.QueryRow(`UPDATE cloud_worker_credits SET state='reserved',reservation_ref=$3,reserved_at=CURRENT_TIMESTAMP,queued_at=NULL
 WHERE uid=$1 AND source_ref=$2 AND source_ref LIKE 'ops:%'
 AND deployment_profile=$4 AND billing_mode=$5 AND COALESCE(runtime_plan,'')=$6
 AND expires_at IS NOT DISTINCT FROM $7::timestamptz AND expires_at>CURRENT_TIMESTAMP
 AND (state IN ('available','queued') OR (state='reserved' AND reserved_at<CURRENT_TIMESTAMP-INTERVAL '35 minutes'))
 RETURNING deployment_profile,billing_mode,runtime_plan,retention_days,expires_at`, uid, source, reservation, p.DeploymentProfile, p.BillingMode, p.RuntimePlan, p.WorkerExpiresAt).Scan(&selection.Profile, &selection.BillingMode, &selection.RuntimePlan, &selection.RetentionDays, &selection.ExpiresAt)
	if err == sql.ErrNoRows {
		return selection, false, nil
	}
	return selection, err == nil, err
}

// ReconcileCommercialOperationWorker distinguishes a confirmed completion from
// a bot persisted before provision, or a consumed credit held for cleanup.
func (a *Adapter) ReconcileCommercialOperationWorker(uid int64, id string) (string, int64, error) {
	source := types.CommercialActionSource(uid, id)
	var state string
	var worker sql.NullInt64
	var complete bool
	err := a.db.QueryRow(`SELECT state,worker_uid,operation_completed FROM cloud_worker_credits WHERE uid=$1 AND source_ref=$2`, uid, source).Scan(&state, &worker, &complete)
	if err != nil {
		return "", 0, err
	}
	if complete && state == "revoked" && worker.Valid {
		var exists bool
		if err := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM bot_config WHERE user_id=$1 AND owner_id=$2 AND COALESCE(tenant_name,'')<>'')`, worker.Int64, uid).Scan(&exists); err != nil {
			return "failed", 0, err
		}
		if exists {
			return "kept", worker.Int64, nil
		}
		return "failed", worker.Int64, fmt.Errorf("previously kept worker is missing; manual reconciliation required")
	}
	var botUID int64
	var userState int
	var accountType string
	var hasConfig, owned bool
	err = a.db.QueryRow(`SELECT u.id,u.state,u.account_type,
	EXISTS(SELECT 1 FROM bot_config b WHERE b.user_id=u.id),
	EXISTS(SELECT 1 FROM bot_config b WHERE b.user_id=u.id AND b.owner_id=$1 AND COALESCE(b.tenant_name,'')<>'')
	FROM users u WHERE u.username=$2`, uid, types.CommercialActionUsername(uid, id)).Scan(&botUID, &userState, &accountType, &hasConfig, &owned)
	if err != nil && err != sql.ErrNoRows {
		return "", 0, err
	}
	if complete && state == "consumed" && worker.Valid && err == nil && worker.Int64 == botUID && owned && userState == 0 && accountType == "bot" {
		var valid bool
		if e := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM cloud_worker_lifecycles WHERE worker_uid=$1 AND owner_uid=$2 AND state='active')`, botUID, uid).Scan(&valid); e != nil {
			return "", 0, e
		}
		if valid {
			return "created", botUID, nil
		}
	}
	// DeleteBot deliberately retains a disabled user. After confirmed cleanup
	// (including a capacity queue), release that tombstone's name so the next
	// HandleCreate attempt can use the same deterministic tenant identity.
	if err == nil && userState == 1 && accountType == "bot" && !hasConfig && (state == "available" || state == "queued") {
		_, e := a.db.Exec(`UPDATE users SET username=username || '-retired-' || id::text
		WHERE id=$1 AND state=1 AND account_type='bot' AND NOT EXISTS(SELECT 1 FROM bot_config WHERE user_id=$1)`, botUID)
		if e != nil {
			return "failed", botUID, e
		}
		return "pending", 0, nil
	}
	if err == nil || state == "consumed" {
		return "failed", botUID, fmt.Errorf("worker completion is ambiguous; manual reconciliation required")
	}
	if state == "revoked" {
		return "failed", 0, fmt.Errorf("operation credit revoked")
	}
	return "pending", 0, nil
}
func (a *Adapter) CompleteCommercialOperationWorker(uid int64, source string, worker int64) error {
	res, err := a.db.Exec(`UPDATE cloud_worker_credits SET operation_completed=TRUE WHERE uid=$1 AND source_ref=$2 AND state='consumed' AND worker_uid=$3`, uid, source, worker)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("operation credit completion not recorded")
	}
	return nil
}
func (a *Adapter) KeepCommercialOperationWorker(uid int64, source string, worker int64) error {
	res, err := a.db.Exec(`UPDATE cloud_worker_credits SET state='revoked',worker_uid=$3,operation_completed=TRUE,reservation_ref='',reserved_at=NULL WHERE uid=$1 AND source_ref=$2 AND state IN ('available','queued','reserved')`, uid, source, worker)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("operation keep marker not recorded")
	}
	return nil
}

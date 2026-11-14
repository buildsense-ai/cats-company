package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
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
 AND expires_at IS NOT DISTINCT FROM $7::timestamptz
 -- The reserved credit must still be worth spending. An unexpired paid window
 -- qualifies; so does an explicitly open-ended term (permanent, or follow_plan on
 -- a perpetual package), which has no date by design. A legacy credit without an
 -- expiry keeps the historical behaviour of never being reserved here, so old
 -- perpetual operator grants cannot start provisioning machines by accident.
 AND (expires_at > CURRENT_TIMESTAMP OR (expires_at IS NULL AND expiry_mode <> 'legacy'))
 AND (state IN ('available','queued') OR (state='reserved' AND reserved_at<CURRENT_TIMESTAMP-INTERVAL '35 minutes'))
 RETURNING deployment_profile,billing_mode,runtime_plan,retention_days,expires_at,COALESCE(NULLIF(expiry_mode,''),'legacy'),retain_data`, uid, source, reservation, p.DeploymentProfile, p.BillingMode, p.RuntimePlan, p.WorkerExpiresAt).Scan(&selection.Profile, &selection.BillingMode, &selection.RuntimePlan, &selection.RetentionDays, &selection.ExpiresAt, &selection.ExpiryMode, &selection.RetainData)
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

// KeepCommercialOperationWorker records that an existing machine is what a
// durable operation adopted, and carries that credit's retention intent onto the
// machine before the operation may be reported as kept (approved credit rules
// 2026-11-09, P1-2).
//
// It is deliberately narrow:
//   - only the machine this operation adopted is touched, and only when that
//     machine really is this owner's live worker with a tenant identity —
//     stamping a retention promise on another customer's machine would be worse
//     than not stamping it at all;
//   - the credit and the machine must be the same deployment class. A
//     shared-container credit and a dedicated ECS credit buy different
//     machines, so adopting across that boundary would spend a paid
//     entitlement on a product the buyer did not buy. The class is read from the
//     rows themselves, in this transaction, and an explicitly recorded profile
//     this build does not know fails closed instead of being defaulted;
//   - only a credit that says "credit environment" (retain_data) writes
//     anything: a legacy or operator-issued credit keeps the schedule it always
//     had, and worker_action=none never reaches this method;
//   - the marker, the cleared automatic deadline and the pending provider sync
//     land in one transaction. If the lifecycle row cannot be updated (missing
//     row, a destroy already in flight, an in-flight billing action) the credit
//     is left untouched and the call fails, so the operation is never reported
//     as kept while the protection the buyer paid for is missing.
func (a *Adapter) KeepCommercialOperationWorker(uid int64, source string, worker int64) error {
	if uid <= 0 || worker <= 0 || strings.TrimSpace(source) == "" {
		return fmt.Errorf("invalid operation keep target")
	}
	source = strings.TrimSpace(source)
	tx, err := a.db.Begin()
	if err != nil {
		return fmt.Errorf("begin operation keep: %w", err)
	}
	defer tx.Rollback()
	// Both rows are locked here rather than left to one UPDATE ... WHERE EXISTS:
	// the deployment class is part of the admission rule, so it has to be decided
	// from the same rows the marker is written to.
	var creditProfile, creditState string
	var retainData bool
	if err = tx.QueryRow(`SELECT deployment_profile, state, retain_data FROM cloud_worker_credits
		WHERE uid = $1 AND source_ref = $2 FOR UPDATE`, uid, source).Scan(&creditProfile, &creditState, &retainData); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("operation keep marker not recorded")
		}
		return fmt.Errorf("read operation keep credit: %w", err)
	}
	if creditState != "available" && creditState != "queued" && creditState != "reserved" {
		return fmt.Errorf("operation keep marker not recorded")
	}
	creditClass, err := cloudWorkerProfileClass(creditProfile)
	if err != nil {
		return fmt.Errorf("operation keep credit: %w", err)
	}
	var workerProfile string
	if err = tx.QueryRow(`SELECT COALESCE(cloud_deployment->>'profile','') FROM bot_config
		WHERE user_id = $1 AND owner_id = $2 AND COALESCE(tenant_name, '') <> '' FOR UPDATE`, worker, uid).Scan(&workerProfile); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("operation keep target %d is not this owner's cloud worker", worker)
		}
		return fmt.Errorf("read operation keep target: %w", err)
	}
	workerClass, err := cloudWorkerProfileClass(workerProfile)
	if err != nil {
		return fmt.Errorf("operation keep target %d: %w", worker, err)
	}
	if workerClass != creditClass {
		return fmt.Errorf("operation keep target %d is a %s worker but the operation credit is %s", worker, workerClass, creditClass)
	}
	var lifecycleState, billingAction string
	err = tx.QueryRow(`SELECT state, billing_action FROM cloud_worker_lifecycles
		WHERE worker_uid=$1 AND owner_uid=$2 FOR UPDATE`, worker, uid).Scan(&lifecycleState, &billingAction)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read operation keep lifecycle: %w", err)
	}
	if (err == sql.ErrNoRows && retainData) || !types.CloudWorkerCanBeAdopted(lifecycleState, billingAction) {
		return fmt.Errorf("operation keep target %d is not available for adoption", worker)
	}
	res, err := tx.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'revoked', worker_uid = $3, operation_completed = TRUE, reservation_ref = '', reserved_at = NULL
		WHERE uid = $1 AND source_ref = $2 AND state IN ('available','queued','reserved')`, uid, source, worker)
	if err != nil {
		return fmt.Errorf("record operation keep marker: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("operation keep marker not recorded")
	}
	if retainData {
		res, err := tx.Exec(`
			UPDATE cloud_worker_lifecycles l
			SET retain_data = TRUE, delete_after = NULL, sync_pending = TRUE,
			    state = 'active', archived_at = NULL, delete_started_at = NULL,
			    last_error = '', updated_at = CURRENT_TIMESTAMP
			WHERE l.worker_uid = $1 AND l.owner_uid = $2
			  AND l.billing_action = ''
			  -- A destroy already started (or finished) is an explicit decision an
			  -- adoption must not silently undo: the operator has to resolve it.
			  AND l.state IN ('active','delete_pending','delete_failed')`, worker, uid)
		if err != nil {
			return fmt.Errorf("keep credit environment worker %d: %w", worker, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("operation keep could not record the credit environment on worker %d", worker)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit operation keep: %w", err)
	}
	return nil
}

// cloudWorkerProfileClass maps a stored deployment profile onto the deployment
// class the keep rules compare. An empty profile is the historical private-NAT
// ECS default (the column is NOT NULL DEFAULT 'private_nat'), while an explicit
// profile this build does not know is an error: guessing the class is exactly
// what would let a credit be spent on the wrong machine.
func cloudWorkerProfileClass(profile string) (string, error) {
	normalized, valid := types.NormalizeCloudWorkerProfile(profile)
	if !valid {
		return "", fmt.Errorf("unsupported deployment profile %q", profile)
	}
	class, ok := types.CloudWorkerDeploymentClass(normalized)
	if !ok {
		return "", fmt.Errorf("unsupported deployment profile %q", profile)
	}
	return class, nil
}

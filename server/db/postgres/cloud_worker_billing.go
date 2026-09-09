package postgres

import (
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) GetCloudWorkerBillingLifecycle(tenant string) (*types.CloudWorkerLifecycle, error) {
	var item types.CloudWorkerLifecycle
	err := a.db.QueryRow(`SELECT id,worker_uid,owner_uid,tenant_name,package_expires_at,delete_after,state,billing_mode,conversion_pending,billing_action
		FROM cloud_worker_lifecycles WHERE tenant_name=$1 AND state<>'deleted'`, tenant).Scan(&item.ID, &item.WorkerUID, &item.OwnerUID, &item.TenantName, &item.PackageExpiresAt, &item.DeleteAfter, &item.State, &item.BillingMode, &item.ConversionPending, &item.BillingAction)
	return &item, err
}

func (a *Adapter) RequestCloudWorkerConversion(id int64) (bool, error) {
	result, err := a.db.Exec(`UPDATE cloud_worker_lifecycles SET conversion_pending=true,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND billing_mode='ondemand' AND state IN ('active','delete_pending','delete_failed')`, id)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

// Conversion intent prevents a stale expiry scan from claiming destruction.
// A retained action protects the row after a process crash. After the bounded
// provider timeout, a new action may reconcile it (for example payment after
// a crashed suspension). The provider conversion marker prevents double orders.
func (a *Adapter) ClaimCloudWorkerBillingAction(id int64, action string, automatic bool) (bool, error) {
	if action != "convert" && action != "suspend" && action != "start" && action != "cancel" {
		return false, fmt.Errorf("invalid billing action")
	}
	result, err := a.db.Exec(`UPDATE cloud_worker_lifecycles SET billing_action=$2,billing_action_started_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND state IN ('active','delete_pending','delete_failed')
		AND (billing_action='' OR billing_action_started_at<CURRENT_TIMESTAMP-INTERVAL '30 minutes')
		AND (CASE WHEN $2 IN ('convert','cancel') THEN conversion_pending
		          ELSE billing_mode='ondemand' AND NOT conversion_pending END)
		AND (NOT $3 OR $2='convert' OR package_expires_at<=CURRENT_TIMESTAMP)
		AND ($2<>'start' OR package_expires_at>CURRENT_TIMESTAMP)`, id, action, automatic)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

func (a *Adapter) CompleteCloudWorkerBillingAction(id int64, action string, expiresAt time.Time, errorText string) error {
	if errorText != "" {
		_, err := a.db.Exec(`UPDATE cloud_worker_lifecycles SET billing_action='',billing_action_started_at=NULL,last_error=$3,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND billing_action=$2`, id, action, errorText)
		return err
	}
	if action != "convert" {
		if action == "cancel" {
			_, err := a.db.Exec(`UPDATE cloud_worker_lifecycles SET conversion_pending=false,state='active',billing_action='',billing_action_started_at=NULL,last_error='',updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND billing_action='cancel' AND billing_mode='ondemand'`, id)
			return err
		}
		_, err := a.db.Exec(`UPDATE cloud_worker_lifecycles SET
			state=CASE WHEN $2='suspend' AND package_expires_at<=CURRENT_TIMESTAMP AND NOT conversion_pending THEN 'delete_pending' ELSE state END,
			delete_after=CASE WHEN $2='suspend' AND package_expires_at<=CURRENT_TIMESTAMP AND NOT conversion_pending THEN GREATEST(delete_after,CURRENT_TIMESTAMP+INTERVAL '3 days') ELSE delete_after END,
			billing_action='',billing_action_started_at=NULL,last_error='',updated_at=CURRENT_TIMESTAMP
			WHERE id=$1 AND billing_action=$2`, id, action)
		return err
	}
	if !expiresAt.After(time.Now()) {
		return fmt.Errorf("provider monthly expiry is not in the future")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workerUID int64
	err = tx.QueryRow(`UPDATE cloud_worker_lifecycles SET billing_mode='month',conversion_pending=false,
		package_expires_at=$2,delete_after=$2::timestamptz+INTERVAL '15 days',state='active',
		billing_action='',billing_action_started_at=NULL,archived_at=NULL,delete_started_at=NULL,last_error='',updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND billing_action='convert' AND state NOT IN ('delete_running','deleted') RETURNING worker_uid`, id, expiresAt).Scan(&workerUID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE bot_config SET cloud_deployment=jsonb_set(cloud_deployment,'{env,CTYUN_WORKER_BILLING_MODE}','"month"'::jsonb,true) WHERE user_id=$1`, workerUID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *Adapter) ClaimCloudWorkerTrialRelease(id int64) (bool, error) {
	result, err := a.db.Exec(`UPDATE cloud_worker_lifecycles SET state='delete_running',delete_started_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP
		WHERE id=$1 AND billing_mode='ondemand' AND NOT conversion_pending AND billing_action='' AND state IN ('active','delete_pending','delete_failed')`, id)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

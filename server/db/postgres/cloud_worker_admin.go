package postgres

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// commercialOpsActiveAction is the projection of one still-running durable
// operator action, keyed by the exact bot username that action provisions.
type commercialOpsActiveAction struct {
	OperationID      string
	OperationStatus  string
	WorkerStatus     string
	RuntimePlan      string
	PlannedExpiresAt *time.Time
}

// activeCommercialOpsActions returns pending/running actions indexed by the
// username the action creates. The username is derived in Go
// (types.CommercialActionUsername is a hash), so it cannot be joined in SQL.
// The index is keyed per operation on purpose: matching by owner would label
// every worker of that owner as provisioning.
func (a *Adapter) activeCommercialOpsActions() (map[string]commercialOpsActiveAction, error) {
	rows, err := a.db.Query(`
		SELECT uid, operation_id, status, worker_status,
		       COALESCE(preview->>'runtime_plan', ''),
		       COALESCE(preview->>'worker_expires_at', '')
		FROM commercial_ops_actions
		WHERE status IN ('pending','running')
		ORDER BY created_at, operation_id`)
	if err != nil {
		return nil, fmt.Errorf("list active commercial actions: %w", err)
	}
	defer rows.Close()
	active := map[string]commercialOpsActiveAction{}
	for rows.Next() {
		var uid int64
		var action commercialOpsActiveAction
		var planned string
		if err := rows.Scan(&uid, &action.OperationID, &action.OperationStatus, &action.WorkerStatus, &action.RuntimePlan, &planned); err != nil {
			return nil, fmt.Errorf("scan active commercial action: %w", err)
		}
		if planned != "" {
			if value, err := time.Parse(time.RFC3339Nano, planned); err == nil {
				utc := value.UTC()
				action.PlannedExpiresAt = &utc
			}
		}
		active[types.CommercialActionUsername(uid, action.OperationID)] = action
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active commercial actions: %w", err)
	}
	return active, nil
}

// ListCloudWorkerAdminRecords returns the safe, read-only projection consumed
// by the internal commercial-ops cloud-worker dashboard. Provider credentials
// and bot API keys are intentionally excluded.
func (a *Adapter) ListCloudWorkerAdminRecords() ([]types.CloudWorkerAdminRecord, error) {
	// Read in-flight operator actions first: a worker that an active action is
	// still provisioning has no container yet, and the caller needs that fact
	// attached to the row (matched by exact per-operation username) to avoid
	// reporting it as a missing worker.
	activeActions, err := a.activeCommercialOpsActions()
	if err != nil {
		return nil, err
	}
	rows, err := a.db.Query(`
		SELECT u.id,
		       COALESCE(b.owner_id, 0),
		       COALESCE(owner.username, ''),
		       COALESCE(owner.display_name, ''),
		       COALESCE(u.username, ''),
		       COALESCE(u.display_name, ''),
		       COALESCE(b.tenant_name, ''),
		       u.state,
		       COALESCE(b.enabled, TRUE),
		       COALESCE(b.visibility, 'public'),
		       l.state,
		       l.package_expires_at,
		       l.delete_after,
		       COALESCE(l.last_error, ''),
		       credit.state,
		       COALESCE(credit.source_ref, ''),
		       credit.expires_at,
		       COALESCE(credit.runtime_plan, '')
		FROM users u
		JOIN bot_config b ON b.user_id = u.id
		LEFT JOIN users owner ON owner.id = b.owner_id
		LEFT JOIN cloud_worker_lifecycles l ON l.worker_uid = u.id
		LEFT JOIN LATERAL (
			SELECT c.state, c.source_ref, c.expires_at, c.runtime_plan
			FROM cloud_worker_credits c
			WHERE c.worker_uid = u.id
			ORDER BY COALESCE(c.consumed_at, c.reserved_at, c.created_at) DESC, c.id DESC
			LIMIT 1
		) credit ON TRUE
		WHERE u.account_type = 'bot'
		  AND COALESCE(NULLIF(BTRIM(b.tenant_name), ''), '') <> ''
		ORDER BY u.id`)
	if err != nil {
		return nil, fmt.Errorf("list cloud worker admin records: %w", err)
	}
	defer rows.Close()

	var records []types.CloudWorkerAdminRecord
	for rows.Next() {
		var record types.CloudWorkerAdminRecord
		var lifecycleState, lifecycleLastError, creditState, creditSourceRef, creditRuntimePlan sql.NullString
		var packageExpiresAt, deleteAfter, creditExpiresAt sql.NullTime
		if err := rows.Scan(
			&record.WorkerUID,
			&record.OwnerUID,
			&record.OwnerUsername,
			&record.OwnerDisplayName,
			&record.Username,
			&record.DisplayName,
			&record.TenantName,
			&record.BotState,
			&record.BotEnabled,
			&record.Visibility,
			&lifecycleState,
			&packageExpiresAt,
			&deleteAfter,
			&lifecycleLastError,
			&creditState,
			&creditSourceRef,
			&creditExpiresAt,
			&creditRuntimePlan,
		); err != nil {
			return nil, fmt.Errorf("scan cloud worker admin record: %w", err)
		}
		record.LifecycleState = lifecycleState.String
		record.LifecycleLastError = lifecycleLastError.String
		record.CreditState = creditState.String
		record.CreditSourceRef = creditSourceRef.String
		if packageExpiresAt.Valid {
			value := packageExpiresAt.Time
			record.PackageExpiresAt = &value
		}
		if deleteAfter.Valid {
			value := deleteAfter.Time
			record.DeleteAfter = &value
		}
		if creditExpiresAt.Valid {
			value := creditExpiresAt.Time
			record.CreditExpiresAt = &value
		}
		// Daily-use machines get their tier from the credit that paid for them.
		// An in-flight action overrides it below: the credit may not exist yet.
		record.RuntimePlan = creditRuntimePlan.String
		if action, found := activeActions[record.Username]; found {
			record.OperationID = action.OperationID
			record.OperationStatus = action.OperationStatus
			record.WorkerStatus = action.WorkerStatus
			record.PlannedExpiresAt = action.PlannedExpiresAt
			if action.RuntimePlan != "" {
				record.RuntimePlan = action.RuntimePlan
			}
		}
		record.Provider = "ctyun"
		record.ManagementMode = "managed"
		record.LifecycleMode = "platform"
		record.BindingSource = "platform"
		record.BindingStatus = "managed"
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read cloud worker admin records: %w", err)
	}

	// External/manual instances have no bot row and therefore need a separate
	// inventory query. They are intentionally returned with no lifecycle or
	// credit state so callers cannot mistake them for platform-managed workers.
	// The owner identity is joined back in because the dashboard labels rows by
	// account; without it an imported host shows up as a bare "UID <n>" and is
	// impossible to recognise next to platform-managed workers.
	bindings, err := a.db.Query(`
		SELECT b.worker_uid, b.owner_uid, COALESCE(owner.username, ''), COALESCE(owner.display_name, ''),
		       b.tenant_name, b.provider, b.region_id, b.project_id,
		       b.az_name, b.instance_id, b.instance_name, b.public_ip, b.management_mode,
		       b.lifecycle_mode, b.source, b.status, b.last_verified_at
		FROM cloud_worker_bindings b
		LEFT JOIN users owner ON owner.id = b.owner_uid
		ORDER BY b.id`)
	if err != nil {
		return nil, fmt.Errorf("list external cloud worker bindings: %w", err)
	}
	defer bindings.Close()
	for bindings.Next() {
		var record types.CloudWorkerAdminRecord
		var workerUID, ownerUID sql.NullInt64
		var verifiedAt sql.NullTime
		if err := bindings.Scan(&workerUID, &ownerUID, &record.OwnerUsername, &record.OwnerDisplayName, &record.TenantName, &record.Provider, &record.RegionID, &record.ProjectID, &record.AZName, &record.InstanceID, &record.InstanceName, &record.PublicIP, &record.ManagementMode, &record.LifecycleMode, &record.BindingSource, &record.BindingStatus, &verifiedAt); err != nil {
			return nil, fmt.Errorf("scan external cloud worker binding: %w", err)
		}
		if workerUID.Valid {
			record.WorkerUID = workerUID.Int64
		}
		if ownerUID.Valid {
			record.OwnerUID = ownerUID.Int64
		}
		if verifiedAt.Valid {
			value := verifiedAt.Time
			record.LastVerifiedAt = &value
		}
		record.LifecycleState = "external"
		records = append(records, record)
	}
	if err := bindings.Err(); err != nil {
		return nil, fmt.Errorf("read external cloud worker bindings: %w", err)
	}
	return records, nil
}

// UpsertExternalCloudWorkerBinding is idempotent on provider + instance ID.
// It always restores the safety defaults and never grants platform lifecycle
// control as a side effect of import.
func (a *Adapter) UpsertExternalCloudWorkerBinding(record types.CloudWorkerBindingRecord) error {
	_, err := a.db.Exec(`
		INSERT INTO cloud_worker_bindings
		(worker_uid, owner_uid, tenant_name, provider, region_id, project_id, az_name,
		 instance_id, instance_name, public_ip, management_mode, lifecycle_mode, source, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'manual_import','external','manual',$11)
		ON CONFLICT (provider, instance_id) DO UPDATE SET
		 worker_uid=EXCLUDED.worker_uid, owner_uid=EXCLUDED.owner_uid,
		 tenant_name=EXCLUDED.tenant_name, region_id=EXCLUDED.region_id,
		 project_id=EXCLUDED.project_id, az_name=EXCLUDED.az_name,
		 instance_name=EXCLUDED.instance_name, public_ip=EXCLUDED.public_ip,
		 management_mode='manual_import', lifecycle_mode='external',
		 source='manual', status=EXCLUDED.status, updated_at=CURRENT_TIMESTAMP`,
		nullInt64(record.WorkerUID), nullInt64(record.OwnerUID), record.TenantName,
		record.Provider, record.RegionID, record.ProjectID, record.AZName,
		record.InstanceID, record.InstanceName, record.PublicIP, record.Status)
	return err
}

func nullInt64(value *int64) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

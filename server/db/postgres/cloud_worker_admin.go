package postgres

import (
	"database/sql"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// ListCloudWorkerAdminRecords returns the safe, read-only projection consumed
// by the internal commercial-ops cloud-worker dashboard. Provider credentials
// and bot API keys are intentionally excluded.
func (a *Adapter) ListCloudWorkerAdminRecords() ([]types.CloudWorkerAdminRecord, error) {
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
		       credit.expires_at
		FROM users u
		JOIN bot_config b ON b.user_id = u.id
		LEFT JOIN users owner ON owner.id = b.owner_id
		LEFT JOIN cloud_worker_lifecycles l ON l.worker_uid = u.id
		LEFT JOIN LATERAL (
			SELECT c.state, c.source_ref, c.expires_at
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
		var lifecycleState, lifecycleLastError, creditState, creditSourceRef sql.NullString
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
	bindings, err := a.db.Query(`
		SELECT worker_uid, owner_uid, tenant_name, provider, region_id, project_id,
		       az_name, instance_id, instance_name, public_ip, management_mode,
		       lifecycle_mode, source, status, last_verified_at
		FROM cloud_worker_bindings ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list external cloud worker bindings: %w", err)
	}
	defer bindings.Close()
	for bindings.Next() {
		var record types.CloudWorkerAdminRecord
		var workerUID, ownerUID sql.NullInt64
		var verifiedAt sql.NullTime
		if err := bindings.Scan(&workerUID, &ownerUID, &record.TenantName, &record.Provider, &record.RegionID, &record.ProjectID, &record.AZName, &record.InstanceID, &record.InstanceName, &record.PublicIP, &record.ManagementMode, &record.LifecycleMode, &record.BindingSource, &record.BindingStatus, &verifiedAt); err != nil {
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

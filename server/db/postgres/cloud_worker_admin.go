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
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read cloud worker admin records: %w", err)
	}
	return records, nil
}

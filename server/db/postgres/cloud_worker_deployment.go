package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

const migrateCloudWorkerDeploymentProfiles = `
ALTER TABLE cloud_worker_credits ADD COLUMN IF NOT EXISTS deployment_profile VARCHAR(24) NOT NULL DEFAULT 'private_nat'
    CHECK (deployment_profile IN ('private_nat','public_ip'));
ALTER TABLE commercial_invite_codes ADD COLUMN IF NOT EXISTS cloud_worker_profile VARCHAR(24) NOT NULL DEFAULT 'private_nat'
    CHECK (cloud_worker_profile IN ('private_nat','public_ip'));
ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS cloud_deployment JSONB NOT NULL DEFAULT '{}';
ALTER TABLE cloud_worker_credits ADD COLUMN IF NOT EXISTS billing_mode VARCHAR(16) NOT NULL DEFAULT 'month' CHECK (billing_mode IN ('month','ondemand'));
ALTER TABLE commercial_invite_codes ADD COLUMN IF NOT EXISTS cloud_worker_billing_mode VARCHAR(16) NOT NULL DEFAULT 'month' CHECK (cloud_worker_billing_mode IN ('month','ondemand'));
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS cloud_worker_billing_mode VARCHAR(16) NOT NULL DEFAULT 'month' CHECK (cloud_worker_billing_mode IN ('month','ondemand'));
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS billing_mode VARCHAR(16) NOT NULL DEFAULT 'month' CHECK (billing_mode IN ('month','ondemand'));
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS conversion_pending BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS billing_action VARCHAR(24) NOT NULL DEFAULT '';
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS billing_action_started_at TIMESTAMPTZ;
`

func (a *Adapter) CloudWorkerProfileCreditSummary(uid int64, profile string) (total, available int, err error) {
	return a.CloudWorkerConfiguredCreditSummary(uid, profile, "")
}

func (a *Adapter) CloudWorkerConfiguredCreditSummary(uid int64, profile, billing string) (total, available int, err error) {
	if billing != "" {
		if _, ok := types.NormalizeCloudWorkerBilling(billing); !ok {
			return 0, 0, fmt.Errorf("invalid billing mode")
		}
	}
	profile, valid := types.NormalizeCloudWorkerProfile(profile)
	if !valid || uid <= 0 {
		return 0, 0, fmt.Errorf("invalid deployment profile owner")
	}
	err = a.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE state IN ('available','reserved','consumed')),
		COUNT(*) FILTER (WHERE state='available') FROM cloud_worker_credits
		WHERE uid=$1 AND deployment_profile=$2 AND ($3='' OR billing_mode=$3) AND (expires_at IS NULL OR expires_at>CURRENT_TIMESTAMP)`, uid, profile, billing).Scan(&total, &available)
	return
}

func (a *Adapter) SetCloudWorkerDeployment(uid int64, deployment types.CloudWorkerDeployment) error {
	if _, ok := types.NormalizeCloudWorkerProfile(deployment.Profile); !ok || uid <= 0 {
		return fmt.Errorf("invalid cloud worker deployment")
	}
	raw, err := json.Marshal(deployment)
	if err != nil {
		return err
	}
	result, err := a.db.Exec(`UPDATE bot_config SET cloud_deployment=$2 WHERE user_id=$1`, uid, raw)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("cloud worker not found")
	}
	return nil
}

func (a *Adapter) GetCloudWorkerDeployment(tenant string) (*types.CloudWorkerDeployment, error) {
	var raw []byte
	err := a.db.QueryRow(`SELECT cloud_deployment FROM bot_config WHERE tenant_name=$1`, tenant).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value types.CloudWorkerDeployment
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value.Profile == "" {
		return nil, nil
	} // Existing NAT workers retain legacy configuration.
	return &value, nil
}

func (a *Adapter) ListCloudWorkerDeployments() (map[string]types.CloudWorkerDeployment, error) {
	rows, err := a.db.Query(`SELECT tenant_name, cloud_deployment FROM bot_config WHERE COALESCE(tenant_name,'')<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]types.CloudWorkerDeployment{}
	for rows.Next() {
		var tenant string
		var raw []byte
		if err := rows.Scan(&tenant, &raw); err != nil {
			return nil, err
		}
		var value types.CloudWorkerDeployment
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		result[tenant] = value
	}
	return result, rows.Err()
}

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
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS cloud_worker_deployment_profile VARCHAR(24) NOT NULL DEFAULT 'private_nat'
    CHECK (cloud_worker_deployment_profile IN ('private_nat','public_ip','packed_shared'));
-- How long a worker's data survives package expiry, per purchased plan.
-- 0 = derive from duration_days (≤ 7d → 3 days, longer → 7 days).
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS cloud_worker_retention_days INT NOT NULL DEFAULT 0
    CHECK (cloud_worker_retention_days BETWEEN 0 AND 90);
-- Whether the package buys a cloud worker at all.
--
-- This used to be inferred from the plan slug (catsco-personal / catsco-pro
-- were hard-coded in both the fulfilment transaction and the payment handler).
-- That made every new package either silently token-only or a code change, and
-- the two copies could drift apart. The decision is data now: a plan with this
-- flag set grants one provisioning credit per paid period.
--
-- The backfill must run exactly once, in the same transaction as the ADD COLUMN.
-- The column default is FALSE, so adding it alone would strip the machine from
-- the two packages that already sell one: every existing buyer's renewal would
-- stop provisioning. Guarding the UPDATE on "column did not exist" makes the
-- backfill a one-time migration, after which the flag is operator-controlled.
--
-- This guard is load-bearing. An unguarded UPDATE would re-assert the flag on
-- every boot, silently undoing an operator that deliberately turned the machine
-- off for a legacy plan — and the two official slugs are exactly the ones we
-- retire when a new package line takes over.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'commercial_plans'
          AND column_name = 'cloud_worker_included'
    ) THEN
        ALTER TABLE commercial_plans
            ADD COLUMN cloud_worker_included BOOLEAN NOT NULL DEFAULT FALSE;
        -- Backfill the two packages that already sold a machine. Dropping the
        -- slug check without this would strip the machine from every renewal of
        -- an existing buyer: the column default is FALSE, so "not configured
        -- yet" and "token-only" would be indistinguishable.
        UPDATE commercial_plans SET cloud_worker_included = TRUE
        WHERE slug IN ('catsco-personal', 'catsco-pro');
    END IF;
END $$;
ALTER TABLE cloud_worker_credits ADD COLUMN IF NOT EXISTS retention_days INT NOT NULL DEFAULT 0
    CHECK (retention_days BETWEEN 0 AND 90);
-- Which runtime tier the worker runs at on the shared container fleet.
--
-- The packed cluster allocates memory and CPU weight per slot (light/standard/
-- pro/heavy). Without this column every package bought the same slot, so a
-- package priced at three times another delivered identical machine resources —
-- the only difference was the token allowance, and the density plan the pricing
-- assumed (fewer, larger slots for the dearer packages) never reached the
-- scheduler. The empty default means "use the fleet default", which is what
-- every existing row should keep.
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS cloud_worker_runtime_plan VARCHAR(16) NOT NULL DEFAULT ''
    CHECK (cloud_worker_runtime_plan IN ('', 'light', 'standard', 'pro', 'heavy'));
-- The credit carries the tier so the create path does not have to re-read the
-- package: a credit can outlive the plan row it came from (the plan is archived
-- after a package line is retired, while paid credits stay spendable).
ALTER TABLE cloud_worker_credits ADD COLUMN IF NOT EXISTS runtime_plan VARCHAR(16) NOT NULL DEFAULT ''
    CHECK (runtime_plan IN ('', 'light', 'standard', 'pro', 'heavy'));
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS retention_days INT NOT NULL DEFAULT 0
    CHECK (retention_days BETWEEN 0 AND 90);
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS billing_mode VARCHAR(16) NOT NULL DEFAULT 'month' CHECK (billing_mode IN ('month','ondemand'));
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS conversion_pending BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS billing_action VARCHAR(24) NOT NULL DEFAULT '';
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS billing_action_started_at TIMESTAMPTZ;
`

// activateCloudWorkerPackedSharedProfile widens the two profile CHECK
// constraints so operators can hand out the shared-container backend next to
// the dedicated ECS ones. The original constraints are dropped by name and
// re-added with the same name, so this stays idempotent across restarts.
const activateCloudWorkerPackedSharedProfile = `
ALTER TABLE cloud_worker_credits DROP CONSTRAINT IF EXISTS cloud_worker_credits_deployment_profile_check;
ALTER TABLE cloud_worker_credits ADD CONSTRAINT cloud_worker_credits_deployment_profile_check
    CHECK (deployment_profile IN ('private_nat','public_ip','packed_shared'));
ALTER TABLE commercial_invite_codes DROP CONSTRAINT IF EXISTS commercial_invite_codes_cloud_worker_profile_check;
ALTER TABLE commercial_invite_codes ADD CONSTRAINT commercial_invite_codes_cloud_worker_profile_check
    CHECK (cloud_worker_profile IN ('private_nat','public_ip','packed_shared'));
`

// stripPackedSharedMachineSettings removes ECS machine parameters that older
// builds copied into shared-container deployment snapshots.
//
// A packed worker is placed by the cluster scheduler: it has no region, flavor,
// VPC, subnet, security group or jump host. Persisting those values described a
// machine that does not exist, and every reader had to special-case them back
// out. New deployments no longer carry them; this cleans the rows written by
// earlier builds so a stale CTYUN_WORKER_BILLING_MODE cannot be mistaken for a
// real billing decision.
//
// Idempotent: re-running it on an already-clean row is a no-op.
const stripPackedSharedMachineSettings = `
UPDATE bot_config
SET cloud_deployment = jsonb_set(
        cloud_deployment,
        '{env}',
        COALESCE((
            SELECT jsonb_object_agg(key, value)
            FROM jsonb_each(COALESCE(cloud_deployment->'env', '{}'::jsonb))
            WHERE key NOT LIKE 'CTYUN\_%' OR key = 'CTYUN_WORKER_EXT_IP'
        ), '{}'::jsonb)
    )
WHERE cloud_deployment->>'profile' = 'packed_shared'
  AND cloud_deployment->'env' ?| ARRAY[
        'CTYUN_WORKER_REGION_ID','CTYUN_WORKER_PROJECT_ID','CTYUN_IMAGE_PROJECT_ID',
        'CTYUN_WORKER_AZ_NAME','CTYUN_WORKER_FLAVOR_ID','CTYUN_WORKER_VPC_ID',
        'CTYUN_WORKER_SUBNET_ID','CTYUN_WORKER_SECURITY_GROUP_ID',
        'CTYUN_WORKER_BILLING_MODE','CTYUN_WORKER_CYCLE_COUNT',
        'CTYUN_JUMP_IP','CTYUN_JUMP_PORT','CTYUN_JUMP_USER','CTYUN_JUMP_KEY']
`

// allowCloudWorkerCreditQueuedState lets a paid credit wait for capacity.
//
// A pooled tier serves a fixed number of tenants per node (15/15/10/3), so a
// burst of purchases can arrive with every slot taken. Before this state the
// only options were to fail the purchase (the buyer paid and got nothing) or to
// hand the credit back as generic 'available' (the request is lost, because the
// buyer has no reason to click create again). 'queued' keeps the debt explicit
// and lets the capacity sweep retry it automatically.
const allowCloudWorkerCreditQueuedState = `
ALTER TABLE cloud_worker_credits DROP CONSTRAINT IF EXISTS chk_cloud_worker_credits_state;
ALTER TABLE cloud_worker_credits ADD CONSTRAINT chk_cloud_worker_credits_state
    CHECK (state IN ('available','reserved','consumed','revoked','queued'));
ALTER TABLE cloud_worker_credits ADD COLUMN IF NOT EXISTS queued_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_cloud_worker_credits_queued
    ON cloud_worker_credits (queued_at) WHERE state = 'queued';
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
	err = a.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE state IN ('available','reserved','consumed','queued')),
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

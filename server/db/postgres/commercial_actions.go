package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// Preview and commit use the same snapshot reader. The apply reader runs inside
// the serializable ledger transaction, never in an unguarded HTTP preflight.
func commercialActionSnapshot(tx *sql.Tx, uid int64, now time.Time) (*types.CommercialSummary, int, error) {
	s := &types.CommercialSummary{UID: uid}
	rows, err := tx.Query(`SELECT ` + commercialPlanColumns + ` FROM commercial_plans ORDER BY id`)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		p, e := scanCommercialPlan(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		s.Plans = append(s.Plans, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	rows, err = tx.Query(`SELECT e.id,e.uid,e.plan_id,p.slug,p.name,e.source,e.source_ref,e.state,e.starts_at,e.expires_at,e.created_at,e.updated_at
 FROM commercial_entitlements e JOIN commercial_plans p ON p.id=e.plan_id
 WHERE e.uid=$1 AND e.state='active' AND (e.expires_at IS NULL OR e.expires_at>$2) ORDER BY e.id`, uid, now)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		e, x := scanCommercialEntitlement(rows)
		if x != nil {
			rows.Close()
			return nil, 0, x
		}
		s.Entitlements = append(s.Entitlements, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	rows, err = tx.Query(`SELECT id,uid,COALESCE(plan_id,0),COALESCE(invite_code_id,0),grant_type,source_ref,model,amount_cny,
 reset_duration,effective_at,expires_at,revoked_at,note,COALESCE(operator_uid,0),created_at
 FROM commercial_quota_grants WHERE uid=$1 AND effective_at<=$2 AND revoked_at IS NULL
 AND (expires_at IS NULL OR expires_at>$2) ORDER BY id`, uid, now)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		g, e := scanCommercialQuotaGrant(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		s.Grants = append(s.Grants, g)
		s.TotalCNY += commercialQuotaGrantSignedAmount(g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	var count int
	err = tx.QueryRow(`SELECT COUNT(*) FROM bot_config WHERE owner_id=$1 AND COALESCE(tenant_name,'')<>''`, uid).Scan(&count)
	return s, count, err
}

func buildCommercialActionPreview(r types.CommercialActionRequest, s *types.CommercialSummary, count int, anchor, now time.Time) (*types.CommercialActionPreview, error) {
	p := &types.CommercialActionPreview{UID: r.UID, Intent: r.Intent, CanApply: true, Warnings: []string{}, WorkerAction: r.WorkerAction, WorkerCount: count, PackageEffect: "none", WorkerEffect: "none", CurrentTotalCNY: s.TotalCNY, NextTotalCNY: s.TotalCNY}
	warn := func(message string) { p.CanApply = false; p.Warnings = append(p.Warnings, message) }
	byID := map[int64]*types.CommercialPlan{}
	for _, v := range s.Plans {
		byID[v.ID] = v
	}
	primary := types.PrimaryCommercialEntitlement(s.Entitlements, now)
	if primary != nil {
		p.CurrentPlan = byID[primary.PlanID]
		p.ExpiresAt = primary.ExpiresAt
		p.PreviousExpiresAt = primary.ExpiresAt
	}
	plan := p.CurrentPlan
	switch r.Intent {
	case "grant_plan":
		plan = byID[r.PlanID]
		p.TargetPlan = plan
		p.PackageEffect = "change_plan"
		if plan == nil || plan.State != 0 || !commercialPlanHasQuota(plan) {
			warn("所选套餐不可用或没有额度")
			break
		}
		if primary != nil && primary.PlanID == plan.ID {
			warn("当前已经使用该套餐，请使用续期")
		}
		p.DurationDays = plan.DurationDays
		p.ExpiresAt = commercialOperatorPlanExpiry(plan, anchor)
		p.NextTotalCNY = commercialOperatorPlanQuota(plan)
		for _, g := range s.Grants {
			if g.PlanID == 0 || (byID[g.PlanID] != nil && byID[g.PlanID].Slug == commercialLegacyPlanSlug) {
				p.NextTotalCNY += commercialQuotaGrantSignedAmount(g)
			}
		}
	case "renew_plan":
		p.PackageEffect = "extend"
		p.TargetPlan = plan
		if primary == nil || plan == nil {
			warn("当前没有可续期的套餐")
			break
		}
		if r.PlanID != 0 && r.PlanID != plan.ID {
			warn("plan_id 与当前主套餐不一致")
		}
		if primary.ExpiresAt == nil {
			warn("永久权益不需要续期")
			break
		}
		latest := primary.ExpiresAt
		for _, e := range s.Entitlements {
			if e.PlanID == primary.PlanID && e.State == "active" && e.ExpiresAt != nil && e.ExpiresAt.After(*latest) {
				latest = e.ExpiresAt
			}
		}
		days := r.DurationDays
		if days == 0 {
			days = plan.DurationDays
		}
		if days <= 0 || days > 3650 {
			warn("有限期内部或旧权益需要明确 duration_days")
			break
		}
		p.DurationDays = days
		p.PreviousExpiresAt = latest
		v := latest.AddDate(0, 0, days)
		p.ExpiresAt = &v
		// Extension grants start at the previous expiry; currently effective quota
		// remains unchanged until that segment starts.
		p.NextTotalCNY = s.TotalCNY
	}
	if r.WorkerAction != "none" {
		p.WorkerEffect = "create_one"
		if r.WorkerAction == "ensure" {
			p.WorkerEffect = "ensure_one"
			if count > 0 {
				p.WorkerEffect = "keep_existing"
				p.Warnings = append(p.Warnings, "保留已有机器，不自动升降档或增加机器")
			}
		}
		p.DeploymentProfile = r.DeploymentProfile
		p.BillingMode = r.BillingMode
		p.RuntimePlan = r.RuntimePlan
		if r.Intent == "grant_plan" || r.Intent == "renew_plan" {
			if plan != nil {
				p.DeploymentProfile, _ = types.NormalizeCloudWorkerProfile(plan.CloudWorkerDeploymentProfile)
				p.BillingMode = types.CloudWorkerBillingForProfile(p.DeploymentProfile, plan.CloudWorkerBillingMode)
				p.RuntimePlan = plan.CloudWorkerRuntimePlan
			}
			p.WorkerExpiresAt = p.ExpiresAt
			if r.WorkerExpiresAt != nil {
				if p.ExpiresAt != nil {
					warn("有限套餐的机器期限由套餐推导")
				} else {
					p.WorkerExpiresAt = r.WorkerExpiresAt
				}
			}
		} else {
			p.DurationDays = r.DurationDays
			p.WorkerExpiresAt = r.WorkerExpiresAt
			if r.DurationDays > 0 {
				if r.WorkerExpiresAt != nil {
					warn("duration_days 与 worker_expires_at 只能指定一个")
				}
				v := anchor.AddDate(0, 0, r.DurationDays)
				p.WorkerExpiresAt = &v
			}
			if p.WorkerExpiresAt == nil {
				p.WorkerExpiresAt = p.ExpiresAt
			}
		}
		if p.DeploymentProfile == types.CloudWorkerPackedShared && p.RuntimePlan == "" {
			p.RuntimePlan = types.CloudWorkerRuntimeStandard
		}
		if p.WorkerExpiresAt == nil || !p.WorkerExpiresAt.After(now) {
			if p.WorkerEffect != "keep_existing" {
				warn("新机器必须有明确的未来期限；永久套餐请指定 worker_expires_at")
			}
		}
	} else if r.WorkerExpiresAt != nil {
		warn("worker_action=none 时不能指定机器期限")
	}
	// Bind complete effective entitlement windows and grants, plus both relevant
	// plan configurations (including model budgets). Unrelated plans are excluded.
	snapshot := struct {
		Fingerprint  string
		Preview      *types.CommercialActionPreview
		Entitlements []*types.CommercialEntitlement
		Grants       []*types.CommercialQuotaGrant
	}{r.Fingerprint(), p, s.Entitlements, s.Grants}
	p.PreviewToken = fmt.Sprintf("%d:%s", anchor.UnixMicro(), types.CommercialActionHash(snapshot))
	return p, nil
}

func (a *Adapter) PreviewCommercialAction(r types.CommercialActionRequest) (*types.CommercialActionPreview, error) {
	if err := r.Normalize(); err != nil {
		return nil, err
	}
	tx, err := a.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, r.UID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, commercialAdjustmentError("not_found", "commercial user not found")
	}
	now := time.Now().UTC()
	s, n, err := commercialActionSnapshot(tx, r.UID, now)
	if err != nil {
		return nil, err
	}
	return buildCommercialActionPreview(r, s, n, now.Truncate(time.Microsecond), now)
}

const commercialActionColumns = `uid,operation_id,intent,status,package_applied,relay_synced,worker_status,worker_uid,error,request,preview,result,lease`

func scanCommercialAction(row interface{ Scan(...interface{}) error }) (*types.CommercialAction, error) {
	v := &types.CommercialAction{}
	o := &v.Operation
	var request, preview, result []byte
	if err := row.Scan(&o.UID, &o.OperationID, &o.Intent, &o.Status, &o.PackageApplied, &o.RelaySynced, &o.WorkerStatus, &o.WorkerUID, &o.Error, &request, &preview, &result, &v.Lease); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(request, &v.Request); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(preview, &v.Preview); err != nil {
		return nil, err
	}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &v.Result); err != nil {
			return nil, err
		}
	}
	return v, nil
}
func (a *Adapter) GetCommercialAction(uid int64, id string) (*types.CommercialAction, error) {
	v, err := scanCommercialAction(a.db.QueryRow(`SELECT `+commercialActionColumns+` FROM commercial_ops_actions WHERE uid=$1 AND operation_id=$2`, uid, id))
	if err == sql.ErrNoRows {
		return nil, commercialAdjustmentError("not_found", "operation not found")
	}
	return v, err
}
func (a *Adapter) ApplyCommercialAction(r types.CommercialActionRequest) (*types.CommercialAction, error) {
	if err := r.Normalize(); err != nil {
		return nil, err
	}
	for i := 0; i < 5; i++ {
		v, err := a.applyCommercialAction(r)
		var state interface{ SQLState() string }
		if errors.As(err, &state) && (state.SQLState() == "40001" || state.SQLState() == "40P01") {
			continue
		}
		return v, err
	}
	return nil, commercialAdjustmentError("operation_busy", "concurrent commercial update; retry the same operation")
}
func (a *Adapter) applyCommercialAction(r types.CommercialActionRequest) (*types.CommercialAction, error) {
	tx, err := a.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var uid int64
	// A no-op owner update creates a row version as well as taking the gate.
	// Concurrent serializable applies must restart with a fresh snapshot after
	// waiting, so they observe the journal instead of a pre-commit snapshot.
	if err = tx.QueryRow(`UPDATE users SET id=id WHERE id=$1 RETURNING id`, r.UID).Scan(&uid); err == sql.ErrNoRows {
		return nil, commercialAdjustmentError("not_found", "commercial user not found")
	}
	if err != nil {
		return nil, err
	}
	// Replay MUST precede preview: a successful plan replacement changes exactly
	// the state that its old preview described.
	old, err := scanCommercialAction(tx.QueryRow(`SELECT `+commercialActionColumns+` FROM commercial_ops_actions WHERE uid=$1 AND operation_id=$2`, r.UID, r.OperationID))
	if err == nil {
		if old.Request.Fingerprint() != r.Fingerprint() {
			return nil, commercialAdjustmentError("operation_conflict", "operation_id already belongs to different parameters")
		}
		// Failed work waits for an explicit retry of the same request. Only the
		// unfinished background steps are requeued; the ledger is never replayed.
		if old.Operation.Status == "failed" {
			old, err = scanCommercialAction(tx.QueryRow(`UPDATE commercial_ops_actions SET status='pending',error='',
			worker_status=CASE WHEN worker_status='failed' THEN 'pending' ELSE worker_status END,
			retry_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE uid=$1 AND operation_id=$2 RETURNING `+commercialActionColumns, r.UID, r.OperationID))
			if err != nil {
				return nil, err
			}
			if err = tx.Commit(); err != nil {
				return nil, err
			}
		}
		return old, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	now := time.Now().UTC()
	parts := strings.Split(r.PreviewToken, ":")
	if len(parts) != 2 {
		return nil, commercialAdjustmentError("stale_preview", "invalid preview token")
	}
	micros, e := strconv.ParseInt(parts[0], 10, 64)
	anchor := time.UnixMicro(micros).UTC()
	if e != nil || anchor.After(now) || now.Sub(anchor) > 15*time.Minute {
		return nil, commercialAdjustmentError("stale_preview", "preview expired; refresh preview")
	}
	s, n, err := commercialActionSnapshot(tx, r.UID, now)
	if err != nil {
		return nil, err
	}
	p, err := buildCommercialActionPreview(r, s, n, anchor, now)
	if err != nil {
		return nil, err
	}
	if p.PreviewToken != r.PreviewToken || !p.CanApply {
		return nil, commercialAdjustmentError("stale_preview", "commercial state or parameters changed; refresh preview")
	}
	var result *types.CommercialAccountAdjustmentResult
	if r.Intent == "grant_plan" || r.Intent == "renew_plan" {
		planID := r.PlanID
		if r.Intent == "renew_plan" {
			planID = p.CurrentPlan.ID
		}
		result, err = a.applyCommercialAccountAdjustmentTx(tx, &types.CommercialAccountAdjustment{UID: r.UID, Action: p.PackageEffect, PlanID: planID, OperationsIntent: true, DurationDays: p.DurationDays, OperationID: types.CommercialActionSource(r.UID, r.OperationID), Note: r.Note, EffectiveAt: anchor, ExpectedTotalCNY: &p.CurrentTotalCNY, ExpectedExpiresAt: p.PreviousExpiresAt})
		if err != nil {
			return nil, err
		}
		// Report the effective total, rather than adding a future segment's quota.
		result.NextTotalCNY, err = commercialActiveQuotaTotal(tx, r.UID, now)
		if err != nil {
			return nil, err
		}
	}
	workerStatus := "none"
	if r.WorkerAction != "none" {
		workerStatus = "pending"
		retention := types.CloudWorkerRetentionDays(0, p.DurationDays)
		if p.TargetPlan != nil {
			retention = types.CloudWorkerRetentionDays(p.TargetPlan.CloudWorkerRetentionDays, p.DurationDays)
		}
		if _, err = tx.Exec(`INSERT INTO cloud_worker_credits(uid,source_ref,expires_at,deployment_profile,billing_mode,runtime_plan,retention_days) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.UID, types.CommercialActionSource(r.UID, r.OperationID), p.WorkerExpiresAt, p.DeploymentProfile, p.BillingMode, p.RuntimePlan, retention); err != nil {
			return nil, err
		}
	}
	requestJSON, _ := json.Marshal(r)
	previewJSON, _ := json.Marshal(p)
	resultJSON, _ := json.Marshal(result)
	v, err := scanCommercialAction(tx.QueryRow(`INSERT INTO commercial_ops_actions(uid,operation_id,fingerprint,request,preview,result,intent,package_applied,relay_synced,worker_status)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+commercialActionColumns, r.UID, r.OperationID, r.Fingerprint(), requestJSON, previewJSON, resultJSON, r.Intent, result != nil, result == nil, workerStatus))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return v, nil
}

// Claim a 35-minute lease: provision and cleanup can each take ten minutes.
// No second action of this owner is claimed while an unexpired lease exists.
func (a *Adapter) ClaimCommercialAction(lease string) (*types.CommercialAction, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var uid int64
	err = tx.QueryRow(`SELECT u.id FROM users u WHERE EXISTS(SELECT 1 FROM commercial_ops_actions j
 WHERE j.uid=u.id AND j.status IN ('pending','running') AND j.retry_at<=CURRENT_TIMESTAMP AND (j.lease_until IS NULL OR j.lease_until<CURRENT_TIMESTAMP))
 AND NOT EXISTS(SELECT 1 FROM commercial_ops_actions b WHERE b.uid=u.id AND b.lease_until>CURRENT_TIMESTAMP)
 ORDER BY u.id FOR UPDATE OF u SKIP LOCKED LIMIT 1`).Scan(&uid)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Recheck after acquiring the owner row lock. A competing transaction may
	// have committed its lease after the selection statement took its snapshot.
	v, err := scanCommercialAction(tx.QueryRow(`UPDATE commercial_ops_actions SET lease=$2,lease_until=CURRENT_TIMESTAMP+INTERVAL '35 minutes',status='running',
	worker_status=CASE WHEN worker_status IN ('pending','queued') THEN 'running' ELSE worker_status END,updated_at=CURRENT_TIMESTAMP
	WHERE (uid,operation_id)=(SELECT uid,operation_id FROM commercial_ops_actions WHERE uid=$1
	AND status IN ('pending','running') AND retry_at<=CURRENT_TIMESTAMP AND (lease_until IS NULL OR lease_until<CURRENT_TIMESTAMP)
	AND NOT EXISTS(SELECT 1 FROM commercial_ops_actions b WHERE b.uid=$1 AND b.lease_until>CURRENT_TIMESTAMP)
	ORDER BY retry_at,created_at,operation_id LIMIT 1) RETURNING `+commercialActionColumns, uid, lease))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return v, nil
}
func (a *Adapter) SaveCommercialAction(v *types.CommercialAction) error {
	o := v.Operation
	res, err := a.db.Exec(`UPDATE commercial_ops_actions SET status=$4,relay_synced=$5,worker_status=$6,worker_uid=$7,error=$8,lease='',lease_until=NULL,retry_at=CURRENT_TIMESTAMP+INTERVAL '30 seconds',updated_at=CURRENT_TIMESTAMP WHERE uid=$1 AND operation_id=$2 AND lease=$3`, o.UID, o.OperationID, v.Lease, o.Status, o.RelaySynced, o.WorkerStatus, o.WorkerUID, o.Error)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("commercial action lease lost")
	}
	return nil
}

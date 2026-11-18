package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Mirrors migration 000079. No historical rows are populated by the migration.
const createCommercialInviteMonthlyRepairs = `
CREATE TABLE IF NOT EXISTS commercial_invite_monthly_repairs (
    entitlement_id BIGINT PRIMARY KEY REFERENCES commercial_entitlements(id) ON DELETE RESTRICT,
    units BIGINT NOT NULL CHECK (units > 0),
    term_ends_at TIMESTAMPTZ NOT NULL,
    audit_ref TEXT NOT NULL CHECK (length(audit_ref) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

var inviteRepairAuditRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// InviteMonthlyRepair describes one separately reviewed redemption. The values
// must come from the audit, not from a user's current credit balance.
type InviteMonthlyRepair struct {
	EntitlementID int64
	PlanID        int64
	Units         int64
	GiftUnits     int64
	StartsAt      time.Time
	TermEndsAt    time.Time
	AuditRef      string
	Database      string
	Schema        string
	Commit        bool
}

// RepairInviteMonthly credits the existing first-term reconciler without
// modifying the original entitlement or the worker credit. A dry run performs
// the same checks and mutations in a transaction that is always rolled back.
func (a *Adapter) RepairInviteMonthly(ctx context.Context, in InviteMonthlyRepair) (bool, error) {
	if in.EntitlementID <= 0 || in.PlanID <= 0 || in.Units <= 0 || in.GiftUnits <= 0 || in.GiftUnits >= in.Units ||
		in.StartsAt.IsZero() || !in.TermEndsAt.After(in.StartsAt) || !inviteRepairAuditRef.MatchString(in.AuditRef) || in.Database == "" || in.Schema == "" {
		return false, store.ErrModelCreditInvalid
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin invite monthly repair: %w", err)
	}
	defer tx.Rollback()
	var dbName, schema string
	if err := tx.QueryRowContext(ctx, `SELECT current_database(), current_schema()`).Scan(&dbName, &schema); err != nil || dbName != in.Database || schema != in.Schema {
		return false, fmt.Errorf("invite repair database identity mismatch")
	}
	// Restrict every unqualified relation to the database identity checked above.
	if _, err := tx.ExecContext(ctx, `SELECT pg_catalog.set_config('search_path', pg_catalog.quote_ident($1) || ', pg_temp', true)`, schema); err != nil {
		return false, fmt.Errorf("pin invite repair schema: %w", err)
	}
	var uid int64
	if err := tx.QueryRowContext(ctx, `SELECT uid FROM commercial_entitlements WHERE id=$1 AND source='invite'`, in.EntitlementID).Scan(&uid); err != nil {
		return false, fmt.Errorf("load invite repair owner: %w", err)
	}
	if err := lockCommercialOfficialPlanTier(tx, uid); err != nil {
		return false, err
	}
	account, err := lockModelCreditAccount(ctx, tx, uid)
	if err != nil {
		return false, err
	}

	key := store.ModelCreditMonthlyGrantKey(uid, in.PlanID, in.StartsAt)
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT r.units=$2 AND r.term_ends_at=$3 AND r.audit_ref=$4
		AND EXISTS (SELECT 1 FROM model_credit_ledger_events WHERE event_key=$5 AND uid=$6 AND units=r.units AND event_type='grant' AND bucket='monthly')
		AND EXISTS (SELECT 1 FROM model_credit_monthly_lots WHERE grant_key=$5 AND uid=$6 AND entitlement_id=r.entitlement_id)
		FROM commercial_invite_monthly_repairs r WHERE entitlement_id=$1`, in.EntitlementID, in.Units, in.TermEndsAt.UTC(), in.AuditRef, key, uid).Scan(&matches)
	if err == nil {
		if matches {
			return false, nil
		}
		return false, fmt.Errorf("existing invite monthly repair conflicts: %w", store.ErrModelCreditConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read existing invite monthly repair: %w", err)
	}
	if account.MonthlyUnits != 0 || account.FrozenMonthlyUnits != 0 {
		return false, fmt.Errorf("unexplained existing monthly balance: %w", store.ErrModelCreditConflict)
	}

	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, err
	}
	if err := validateNoOpenCommercialOfficialOrder(tx, uid, now.UTC()); err != nil {
		return false, err
	}
	var eligible int64
	err = tx.QueryRowContext(ctx, `SELECT e.id
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id=e.plan_id
		JOIN commercial_invite_codes c ON c.plan_id=e.plan_id AND c.code=e.source_ref
		JOIN model_credit_ledger_events g ON g.event_key='grant:invite:'||e.uid||':'||e.source_ref
		WHERE e.id=$1 AND e.uid=$2 AND e.plan_id=$3 AND e.source='invite' AND e.state='active'
		  AND e.expires_at IS NULL AND e.invite_monthly_units IS NULL AND e.starts_at=$4
		  AND e.starts_at BETWEEN $7::timestamptz - interval '7 days' AND $7::timestamptz
		  AND $5::timestamptz>$7::timestamptz
		  AND p.state=0 AND p.archived_at IS NULL AND p.sale_state='public' AND p.plan_tier>0 AND p.duration_days=30
		  AND p.updated_at<=e.starts_at AND p.model_credit_units=$6
		  AND c.model_credit_units=$8 AND c.updated_at<=e.starts_at AND c.redeemed_count>0
		  AND g.uid=e.uid AND g.bucket='gift' AND g.event_type='grant' AND g.units=$8
		  AND $5::timestamptz=(e.starts_at AT TIME ZONE 'UTC' + make_interval(days=>p.duration_days)) AT TIME ZONE 'UTC'
		  AND EXISTS (SELECT 1 FROM cloud_worker_credits w WHERE w.entitlement_id=e.id AND w.uid=e.uid
		      AND w.source_ref LIKE 'invite:%' AND w.expires_at=$5 AND w.state IN ('available','reserved','consumed')
		      FOR SHARE OF w NOWAIT)
		  AND NOT EXISTS (SELECT 1 FROM commercial_entitlements x WHERE x.uid=e.uid AND x.source IN ('order','admin','internal')
		      AND (x.expires_at IS NULL OR x.expires_at>e.starts_at))
		  AND NOT EXISTS (SELECT 1 FROM commercial_orders o WHERE o.uid=e.uid AND o.kind='plan'
		      AND (o.paid_at>=e.starts_at OR o.fulfilled_at>=e.starts_at OR o.refunded_at>=e.starts_at))
		  AND NOT EXISTS (SELECT 1 FROM model_credit_ledger_events l WHERE l.uid=e.uid AND l.bucket='monthly' AND l.event_type='grant')
		  AND NOT EXISTS (SELECT 1 FROM model_credit_monthly_lots l WHERE l.uid=e.uid)
		FOR UPDATE OF e NOWAIT
		FOR SHARE OF p, c, g NOWAIT`, in.EntitlementID, uid, in.PlanID, in.StartsAt.UTC(), in.TermEndsAt.UTC(), in.Units, now.UTC(), in.GiftUnits).Scan(&eligible)
	if err != nil {
		return false, fmt.Errorf("invite monthly repair evidence not matched: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO commercial_invite_monthly_repairs (entitlement_id,units,term_ends_at,audit_ref) VALUES ($1,$2,$3,$4)`, eligible, in.Units, in.TermEndsAt.UTC(), in.AuditRef); err != nil {
		return false, fmt.Errorf("record invite repair approval: %w", err)
	}
	ent, err := loadModelCreditMonthlyEntitlement(ctx, tx, uid, now)
	if err != nil {
		return false, err
	}
	if ent == nil || ent.id != eligible || ent.units != in.Units || !ent.expiresAt.Valid || !ent.expiresAt.Time.Equal(in.TermEndsAt) {
		return false, fmt.Errorf("reviewed invite is not the selected first term: %w", store.ErrModelCreditConflict)
	}
	if _, changed, err := reconcileModelCreditMonthlyPeriod(ctx, tx, uid, now); err != nil {
		return false, err
	} else if !changed {
		return false, fmt.Errorf("first-term reconciliation did not issue points: %w", store.ErrModelCreditConflict)
	}
	var booked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM model_credit_ledger_events g
		JOIN model_credit_monthly_lots l ON l.grant_key=g.event_key AND l.uid=g.uid
		WHERE g.event_key=$1 AND g.uid=$2 AND g.bucket='monthly' AND g.event_type='grant'
		  AND g.units=$3 AND l.entitlement_id=$4)`, key, uid, in.Units, eligible).Scan(&booked); err != nil {
		return false, err
	}
	if !booked {
		return false, fmt.Errorf("first-term grant or lot missing: %w", store.ErrModelCreditConflict)
	}
	if in.Commit {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit invite monthly repair: %w", err)
		}
	} else if err := tx.Rollback(); err != nil {
		return false, fmt.Errorf("roll back invite monthly repair preview: %w", err)
	}
	return true, nil
}

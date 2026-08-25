package postgres

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	commercialPersonalPlanSlug = "catsco-personal"
	commercialProPlanSlug      = "catsco-pro"
	commercialFreePlanSlug     = "catsco-free"
	commercialLegacyPlanSlug   = "catsco-legacy-custom"
)

var commercialOfficialPaidModels = []string{
	"MiniMax-M2.7",
	"MiniMax-M3",
	"deepseek-v4-flash",
	"gpt-5.6-terra",
	"gpt-5.6-sol",
	"gpt-5.6-luna",
}

func validateCommercialOfficialPaidPlanModels(slug string, budgets map[string]float64) error {
	expectedTotal := 0.0
	switch strings.TrimSpace(slug) {
	case commercialPersonalPlanSlug:
		expectedTotal = 10500
	case commercialProPlanSlug:
		expectedTotal = 31500
	default:
		return nil
	}
	total := 0.0
	for _, model := range commercialOfficialPaidModels {
		if budgets[model] <= 0 {
			return fmt.Errorf("official paid plan must include model %s", model)
		}
		total += budgets[model]
	}
	if math.Abs(total-expectedTotal) > 0.000001 {
		return fmt.Errorf("official paid plan model budgets must total %.0f", expectedTotal)
	}
	return nil
}

func commercialOfficialPlanTier(slug string) int {
	switch strings.TrimSpace(slug) {
	case commercialPersonalPlanSlug:
		return 1
	case commercialProPlanSlug:
		return 2
	default:
		return 0
	}
}

func lockCommercialOfficialPlanTier(tx *sql.Tx, uid int64) error {
	_, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("commercial_plan_tier:%d", uid))
	if err != nil {
		return fmt.Errorf("lock commercial plan tier: %w", err)
	}
	return nil
}

func activeCommercialOfficialPlanTier(tx *sql.Tx, uid int64, now time.Time) (int, error) {
	rows, err := tx.Query(`
		SELECT p.slug
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		WHERE e.uid = $1 AND e.state = 'active'
		  AND e.starts_at <= $2
		  AND (e.expires_at IS NULL OR e.expires_at > $2)
		  AND p.slug IN ($3, $4)
		FOR UPDATE OF e`, uid, now, commercialPersonalPlanSlug, commercialProPlanSlug)
	if err != nil {
		return 0, fmt.Errorf("load active commercial plan tier: %w", err)
	}
	defer rows.Close()
	highest := 0
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return 0, fmt.Errorf("scan active commercial plan tier: %w", err)
		}
		if tier := commercialOfficialPlanTier(slug); tier > highest {
			highest = tier
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read active commercial plan tier: %w", err)
	}
	return highest, nil
}

func validateCommercialOfficialPlanPurchase(tx *sql.Tx, uid int64, targetSlug string, now time.Time) error {
	targetTier := commercialOfficialPlanTier(targetSlug)
	if targetTier == 0 {
		return nil
	}
	if err := lockCommercialOfficialPlanTier(tx, uid); err != nil {
		return err
	}
	activeTier, err := activeCommercialOfficialPlanTier(tx, uid, now)
	if err != nil {
		return err
	}
	switch {
	case activeTier > targetTier:
		return fmt.Errorf("commercial plan is below active plan")
	default:
		// Buying the currently active official tier is a renewal. The
		// fulfillment transaction appends a new period after the existing
		// expiry instead of creating an overlapping entitlement.
		return nil
	}
}

func validateCommercialOfficialOpenOrder(tx *sql.Tx, uid, targetPlanID int64, channel, targetSlug string, now time.Time) error {
	if commercialOfficialPlanTier(targetSlug) == 0 {
		return nil
	}
	var conflictExists bool
	if err := tx.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM commercial_orders o
			JOIN commercial_plans p ON p.id = o.plan_id
			WHERE o.uid = $1 AND o.status IN ('created','pending')
			  AND (o.expires_at IS NULL OR o.expires_at > $2)
			  AND p.slug IN ($3, $4)
			  AND (o.plan_id <> $5 OR o.channel <> $6)
		)`, uid, now, commercialPersonalPlanSlug, commercialProPlanSlug, targetPlanID, strings.TrimSpace(channel)).Scan(&conflictExists); err != nil {
		return fmt.Errorf("check open commercial plan orders: %w", err)
	}
	if conflictExists {
		return fmt.Errorf("another commercial plan order is already pending")
	}
	return nil
}

func validateNoOpenCommercialOfficialOrder(tx *sql.Tx, uid int64, now time.Time) error {
	if err := lockCommercialOfficialPlanTier(tx, uid); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM commercial_orders o
			JOIN commercial_plans p ON p.id = o.plan_id
			WHERE o.uid = $1 AND o.status IN ('created','pending')
			  AND (o.expires_at IS NULL OR o.expires_at > $2)
			  AND p.slug IN ($3, $4)
		)`, uid, now, commercialPersonalPlanSlug, commercialProPlanSlug).Scan(&exists); err != nil {
		return fmt.Errorf("check open commercial plan orders: %w", err)
	}
	if exists {
		return fmt.Errorf("another commercial plan order is already pending")
	}
	return nil
}

func activateCommercialOfficialPlan(tx *sql.Tx, uid int64, targetSlug string, now time.Time) error {
	targetTier := commercialOfficialPlanTier(targetSlug)
	if targetTier == 0 {
		return nil
	}
	if err := lockCommercialOfficialPlanTier(tx, uid); err != nil {
		return err
	}
	activeTier, err := activeCommercialOfficialPlanTier(tx, uid, now)
	if err != nil {
		return err
	}
	switch {
	case activeTier > targetTier:
		return fmt.Errorf("commercial plan is below active plan")
	case activeTier == targetTier:
		// Same-tier purchases are explicit renewals. Keep the current tier's
		// grants and entitlement intact; FulfillCommercialOrder appends the
		// paid period after its existing expiry.
		return nil
	}
	if err := revokeCommercialFreeBaseline(tx, uid, targetSlug, now); err != nil {
		return err
	}
	if activeTier > 0 {
		for _, slug := range commercialOfficialPlanSlugsBelow(targetTier) {
			if err := revokeCommercialPlanTier(tx, uid, slug, targetSlug, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func revokeCommercialFreeBaseline(tx *sql.Tx, uid int64, targetSlug string, now time.Time) error {
	rows, err := tx.Query(`
		SELECT g.id, g.model, g.amount_cny
		FROM commercial_quota_grants g
		JOIN commercial_plans p ON p.id = g.plan_id
		WHERE g.uid = $1 AND p.slug = $2 AND g.grant_type = 'free'
		  AND g.revoked_at IS NULL
		FOR UPDATE OF g`, uid, commercialFreePlanSlug)
	if err != nil {
		return fmt.Errorf("lock free commercial quota grants: %w", err)
	}
	type quotaGrant struct {
		id     int64
		model  string
		amount float64
	}
	var grants []quotaGrant
	for rows.Next() {
		var grant quotaGrant
		if err := rows.Scan(&grant.id, &grant.model, &grant.amount); err != nil {
			rows.Close()
			return fmt.Errorf("scan free commercial quota grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read free commercial quota grants: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close free commercial quota grants: %w", err)
	}
	for _, grant := range grants {
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'revoke', 'upgrade', $4, $5)`, uid, grant.model, -grant.amount, grant.id, "upgrade to "+targetSlug); err != nil {
			return fmt.Errorf("record free commercial quota reversal: %w", err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE commercial_quota_grants g
		SET revoked_at = $3, expires_at = LEAST(COALESCE(g.expires_at, $3), $3)
		FROM commercial_plans p
		WHERE g.plan_id = p.id AND g.uid = $1 AND p.slug = $2
		  AND g.grant_type = 'free' AND g.revoked_at IS NULL`, uid, commercialFreePlanSlug, now); err != nil {
		return fmt.Errorf("revoke free commercial quota grants: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE commercial_entitlements e
		SET state = 'revoked'
		FROM commercial_plans p
		WHERE e.plan_id = p.id AND e.uid = $1 AND e.state = 'active' AND p.slug = $2`, uid, commercialFreePlanSlug); err != nil {
		return fmt.Errorf("revoke free commercial entitlement: %w", err)
	}
	return nil
}

func commercialOfficialPlanSlugsBelow(targetTier int) []string {
	if targetTier > 1 {
		return []string{commercialPersonalPlanSlug}
	}
	return nil
}

func revokeCommercialPlanTier(tx *sql.Tx, uid int64, slug, targetSlug string, now time.Time) error {
	// A paid tier grants one cloud-worker creation credit per order. When a
	// lower tier is superseded by an immediate upgrade, its still-available
	// credit must not remain usable alongside the new tier's credit. Credits
	// already reserved by an in-flight create are left intact and will be
	// consumed by that operation; only unclaimed credits are revoked here.
	if _, err := tx.Exec(`
		UPDATE cloud_worker_credits c
		SET state = 'revoked', reservation_ref = '', reserved_at = NULL
		WHERE c.uid = $1 AND c.state = 'available'
		  AND c.source_ref IN (
			SELECT 'order:' || e.source_ref
			FROM commercial_entitlements e
			JOIN commercial_plans p ON p.id = e.plan_id
			WHERE e.uid = $1 AND e.source = 'order' AND e.state = 'active'
			  AND e.starts_at <= $2 AND (e.expires_at IS NULL OR e.expires_at > $2)
			  AND p.slug = $3
		  )`, uid, now, slug); err != nil {
		return fmt.Errorf("revoke superseded cloud worker credit: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE commercial_entitlements e
		SET state = 'revoked',
		    expires_at = LEAST(COALESCE(e.expires_at, $2), $2)
		FROM commercial_plans p
		WHERE e.plan_id = p.id AND e.uid = $1 AND e.state = 'active'
		  AND e.starts_at <= $2 AND (e.expires_at IS NULL OR e.expires_at > $2)
		  AND p.slug = $3`, uid, now, slug); err != nil {
		return fmt.Errorf("revoke superseded commercial entitlement: %w", err)
	}

	rows, err := tx.Query(`
		SELECT g.id, g.model, g.amount_cny
		FROM commercial_quota_grants g
		JOIN commercial_plans p ON p.id = g.plan_id
		WHERE g.uid = $1 AND g.revoked_at IS NULL AND p.slug = $2
		  AND g.grant_type IN ('order', 'invite')
		FOR UPDATE OF g`, uid, slug)
	if err != nil {
		return fmt.Errorf("lock superseded commercial quota grants: %w", err)
	}
	type quotaGrant struct {
		id     int64
		model  string
		amount float64
	}
	var grants []quotaGrant
	for rows.Next() {
		var grant quotaGrant
		if err := rows.Scan(&grant.id, &grant.model, &grant.amount); err != nil {
			rows.Close()
			return fmt.Errorf("scan superseded commercial quota grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read superseded commercial quota grants: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close superseded commercial quota grants: %w", err)
	}
	for _, grant := range grants {
		if _, err := tx.Exec(`
			INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
			VALUES ($1, $2, $3, 'revoke', 'upgrade', $4, $5)`, uid, grant.model, -grant.amount, grant.id, "upgrade to "+targetSlug); err != nil {
			return fmt.Errorf("record superseded commercial quota reversal: %w", err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE commercial_quota_grants g
		SET revoked_at = $3, expires_at = LEAST(COALESCE(g.expires_at, $3), $3)
		FROM commercial_plans p
		WHERE g.plan_id = p.id AND g.uid = $1 AND p.slug = $2 AND g.revoked_at IS NULL
		  AND g.grant_type IN ('order', 'invite')`, uid, slug, now); err != nil {
		return fmt.Errorf("revoke superseded commercial quota grants: %w", err)
	}
	return nil
}

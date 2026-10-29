package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	// Slugs that still carry behaviour of their own. The ladder no longer lives
	// here: `plan_tier` does. These remain because they name specific packages
	// the code treats specially (the free baseline, the legacy custom row, and
	// the two retired packages whose test fixtures still populate them).
	commercialPersonalPlanSlug = "catsco-personal"
	commercialProPlanSlug      = "catsco-pro"
	commercialFreePlanSlug     = "catsco-free"
	commercialLegacyPlanSlug   = "catsco-legacy-custom"
)

// commercialOfficialPaidModels and its validator used to live here as a second
// copy of the server-level whitelist. Both are gone: the relay catalog is the
// source of truth for which models a paid plan may sell, and duplicating that
// list in the storage layer is what let the two copies drift apart. The
// validation now happens once, in the server package, before the store is
// called.

// commercialOfficialPlanTiers used to live here as a hard-coded slug→rung table.
// It is gone: the rung is a column on the plan (`plan_tier`), so a package that
// replaces an old one joins the ladder by setting a number rather than by
// editing Go. The same slug-keyed shape had already caused trouble in three
// other places (machine inclusion, fleet, runtime tier); keeping an allow-list
// here meant a renamed package silently lost its upgrade/refuse-to-buy-down
// behaviour, and a new package could never join.
//
// The rung is ordinal and never displayed. 0 means "not on the ladder" — free,
// internal and legacy packages.
var errCommercialPlanTierUnknown = fmt.Errorf("commercial plan is not on the upgrade ladder")

func lockCommercialOfficialPlanTier(tx *sql.Tx, uid int64) error {
	_, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("commercial_plan_tier:%d", uid))
	if err != nil {
		return fmt.Errorf("lock commercial plan tier: %w", err)
	}
	return nil
}

func activeCommercialOfficialPlanTier(tx *sql.Tx, uid int64, now time.Time) (int, error) {
	rows, err := tx.Query(`
		SELECT p.plan_tier
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id
		WHERE e.uid = $1 AND e.state = 'active'
		  AND e.starts_at <= $2
		  AND (e.expires_at IS NULL OR e.expires_at > $2)
		  AND p.plan_tier > 0
		FOR UPDATE OF e`, uid, now)
	if err != nil {
		return 0, fmt.Errorf("load active commercial plan tier: %w", err)
	}
	defer rows.Close()
	highest := 0
	for rows.Next() {
		var tier int
		if err := rows.Scan(&tier); err != nil {
			return 0, fmt.Errorf("scan active commercial plan tier: %w", err)
		}
		if tier > highest {
			highest = tier
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read active commercial plan tier: %w", err)
	}
	return highest, nil
}

// planTierForPlanID reads the rung of one plan. A row that vanished, or a plan
// outside the ladder, reports 0 — which every caller treats as "no upgrade
// rules apply", the same answer the old slug table gave for an unknown slug.
func planTierForPlanID(tx *sql.Tx, planID int64) (int, error) {
	var tier int
	err := tx.QueryRow(`SELECT plan_tier FROM commercial_plans WHERE id = $1`, planID).Scan(&tier)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read commercial plan tier: %w", err)
	}
	return tier, nil
}

// planTierForSlug is the slug-addressed form, for entry points that arrive with
// a slug (a checkout request naming the package) rather than an id.
func planTierForSlug(tx *sql.Tx, slug string) (int, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return 0, nil
	}
	var tier int
	err := tx.QueryRow(`SELECT plan_tier FROM commercial_plans WHERE slug = $1`, slug).Scan(&tier)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read commercial plan tier: %w", err)
	}
	return tier, nil
}

func validateCommercialOfficialPlanPurchase(tx *sql.Tx, uid int64, targetSlug string, now time.Time) error {
	targetTier, err := planTierForSlug(tx, targetSlug)
	if err != nil {
		return err
	}
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
	targetTier, err := planTierForSlug(tx, targetSlug)
	if err != nil {
		return err
	}
	if targetTier == 0 {
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
			  AND p.plan_tier > 0
			  AND (o.plan_id <> $3 OR o.channel <> $4)
		)`, uid, now, targetPlanID, strings.TrimSpace(channel)).Scan(&conflictExists); err != nil {
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
			  AND p.plan_tier > 0
		)`, uid, now).Scan(&exists); err != nil {
		return fmt.Errorf("check open commercial plan orders: %w", err)
	}
	if exists {
		return fmt.Errorf("another commercial plan order is already pending")
	}
	return nil
}

func activateCommercialOfficialPlan(tx *sql.Tx, uid int64, targetSlug string, now time.Time) error {
	targetTier, err := planTierForSlug(tx, targetSlug)
	if err != nil {
		return err
	}
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
		below, err := commercialPlanIDsBelow(tx, targetTier)
		if err != nil {
			return err
		}
		for _, planID := range below {
			if err := revokeCommercialPlanTier(tx, uid, planID, targetSlug, now); err != nil {
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

// commercialPlanIDsBelow lists the plans a purchase supersedes.
//
// Derived from the `plan_tier` column rather than a Go table so a new entry
// package cannot keep handing out a credit that the upgrade was supposed to
// replace. It reads ids, not slugs: a slug is how a buyer addresses a package,
// not what the package is.
func commercialPlanIDsBelow(tx *sql.Tx, targetTier int) ([]int64, error) {
	if targetTier <= 0 {
		return nil, nil
	}
	rows, err := tx.Query(`
		SELECT id FROM commercial_plans
		WHERE plan_tier > 0 AND plan_tier < $1
		ORDER BY plan_tier, id`, targetTier)
	if err != nil {
		return nil, fmt.Errorf("list superseded commercial plans: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan superseded commercial plan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read superseded commercial plans: %w", err)
	}
	return ids, nil
}

func revokeCommercialPlanTier(tx *sql.Tx, uid int64, planID int64, targetSlug string, now time.Time) error {
	// A paid tier grants one cloud-worker creation credit per order. When a
	// lower tier is superseded by an immediate upgrade, its still-available
	// credit must not remain usable alongside the new tier's credit. Credits
	// already reserved by an in-flight create are left intact and will be
	// consumed by that operation; only unclaimed credits are revoked here.
	if _, err := tx.Exec(`
		UPDATE cloud_worker_credits c
		SET state = 'revoked', reservation_ref = '', reserved_at = NULL
		WHERE c.uid = $1 AND c.state = 'available'
		  AND (c.entitlement_id IN (
			SELECT e.id
			FROM commercial_entitlements e
			WHERE e.uid = $1 AND e.source IN ('order', 'invite') AND e.state = 'active'
			  AND e.starts_at <= $2 AND (e.expires_at IS NULL OR e.expires_at > $2)
			  AND e.plan_id = $3
		  ) OR c.source_ref IN (
			SELECT 'order:' || e.source_ref
			FROM commercial_entitlements e
			WHERE e.uid = $1 AND e.source = 'order' AND e.state = 'active'
			  AND e.starts_at <= $2 AND (e.expires_at IS NULL OR e.expires_at > $2)
			  AND e.plan_id = $3
		  ))`, uid, now, planID); err != nil {
		return fmt.Errorf("revoke superseded cloud worker credit: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE commercial_entitlements e
		SET state = 'revoked',
		    expires_at = LEAST(COALESCE(e.expires_at, $2), $2)
		WHERE e.uid = $1 AND e.state = 'active'
		  AND e.starts_at <= $2 AND (e.expires_at IS NULL OR e.expires_at > $2)
		  AND e.plan_id = $3`, uid, now, planID); err != nil {
		return fmt.Errorf("revoke superseded commercial entitlement: %w", err)
	}

	rows, err := tx.Query(`
		SELECT g.id, g.model, g.amount_cny
		FROM commercial_quota_grants g
		WHERE g.uid = $1 AND g.revoked_at IS NULL AND g.plan_id = $2
		  AND g.grant_type IN ('order', 'invite')
		FOR UPDATE OF g`, uid, planID)
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
		WHERE g.uid = $1 AND g.plan_id = $2 AND g.revoked_at IS NULL
		  AND g.grant_type IN ('order', 'invite')`, uid, planID, now); err != nil {
		return fmt.Errorf("revoke superseded commercial quota grants: %w", err)
	}
	return nil
}

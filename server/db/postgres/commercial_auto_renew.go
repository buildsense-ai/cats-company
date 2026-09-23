package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const commercialAutoRenewRunLimit = 50

// GetCommercialAutoRenewConfig returns the opt-in row for uid, or nil when
// the account has never been configured (callers treat that as disabled).
func (a *Adapter) GetCommercialAutoRenewConfig(uid int64) (*types.CommercialAutoRenewConfig, error) {
	if uid <= 0 {
		return nil, fmt.Errorf("uid is required")
	}
	var config types.CommercialAutoRenewConfig
	err := a.db.QueryRow(`
		SELECT uid, enabled, note, created_at, updated_at
		FROM commercial_auto_renew_configs
		WHERE uid = $1`, uid).Scan(&config.UID, &config.Enabled, &config.Note, &config.CreatedAt, &config.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load commercial auto renew config: %w", err)
	}
	return &config, nil
}

// SetCommercialAutoRenewConfig upserts the opt-in switch for uid.
func (a *Adapter) SetCommercialAutoRenewConfig(uid int64, enabled bool, note string) (*types.CommercialAutoRenewConfig, error) {
	if uid <= 0 {
		return nil, fmt.Errorf("uid is required")
	}
	var config types.CommercialAutoRenewConfig
	err := a.db.QueryRow(`
		INSERT INTO commercial_auto_renew_configs(uid, enabled, note)
		VALUES ($1, $2, $3)
		ON CONFLICT (uid) DO UPDATE
		SET enabled = EXCLUDED.enabled, note = EXCLUDED.note, updated_at = CURRENT_TIMESTAMP
		RETURNING uid, enabled, note, created_at, updated_at`, uid, enabled, strings.TrimSpace(note)).
		Scan(&config.UID, &config.Enabled, &config.Note, &config.CreatedAt, &config.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("save commercial auto renew config: %w", err)
	}
	return &config, nil
}

// ListDueCommercialAutoRenew returns enabled users whose latest active
// personal/pro window expires inside the lead time. The latest expiry across
// all active segments (including future-dated extension segments) is compared
// so an account renewed early is never extended twice.
func (a *Adapter) ListDueCommercialAutoRenew(now time.Time, within time.Duration) ([]*types.CommercialAutoRenewDue, error) {
	if within <= 0 {
		return nil, nil
	}
	rows, err := a.db.Query(`
		SELECT c.uid, MAX(e.expires_at)
		FROM commercial_auto_renew_configs c
		JOIN commercial_entitlements e ON e.uid = c.uid
		JOIN commercial_plans p ON p.id = e.plan_id
		WHERE c.enabled = TRUE
		  AND e.state = 'active'
		  AND p.slug IN ($3, $4)
		  AND e.expires_at IS NOT NULL
		  AND e.expires_at > $1
		GROUP BY c.uid
		HAVING MAX(e.expires_at) <= $2
		ORDER BY c.uid`, now, now.Add(within), commercialPersonalPlanSlug, commercialProPlanSlug)
	if err != nil {
		return nil, fmt.Errorf("list due commercial auto renew: %w", err)
	}
	defer rows.Close()
	var due []*types.CommercialAutoRenewDue
	for rows.Next() {
		item := &types.CommercialAutoRenewDue{}
		if err := rows.Scan(&item.UID, &item.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan due commercial auto renew: %w", err)
		}
		due = append(due, item)
	}
	return due, rows.Err()
}

// RecordCommercialAutoRenewRun appends one scheduler attempt to the run log.
func (a *Adapter) RecordCommercialAutoRenewRun(run *types.CommercialAutoRenewRun) error {
	if run == nil || run.UID <= 0 {
		return fmt.Errorf("run is required")
	}
	message := run.Message
	if len(message) > 500 {
		message = message[:500]
	}
	err := a.db.QueryRow(`
		INSERT INTO commercial_auto_renew_runs(uid, action, status, previous_expiry, new_expiry, message, operation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		run.UID, run.Action, run.Status, run.PreviousExpiry, run.NewExpiry, message, run.OperationID).
		Scan(&run.ID, &run.CreatedAt)
	if err != nil {
		return fmt.Errorf("record commercial auto renew run: %w", err)
	}
	return nil
}

// ListCommercialAutoRenewRuns returns the newest run-log entries for uid.
func (a *Adapter) ListCommercialAutoRenewRuns(uid int64, limit int) ([]*types.CommercialAutoRenewRun, error) {
	if uid <= 0 {
		return nil, fmt.Errorf("uid is required")
	}
	if limit <= 0 || limit > commercialAutoRenewRunLimit {
		limit = commercialAutoRenewRunLimit
	}
	rows, err := a.db.Query(`
		SELECT id, uid, action, status, previous_expiry, new_expiry, message, operation_id, created_at
		FROM commercial_auto_renew_runs
		WHERE uid = $1
		ORDER BY id DESC
		LIMIT $2`, uid, limit)
	if err != nil {
		return nil, fmt.Errorf("list commercial auto renew runs: %w", err)
	}
	defer rows.Close()
	var runs []*types.CommercialAutoRenewRun
	for rows.Next() {
		run := &types.CommercialAutoRenewRun{}
		var previous, next sql.NullTime
		if err := rows.Scan(&run.ID, &run.UID, &run.Action, &run.Status, &previous, &next, &run.Message, &run.OperationID, &run.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan commercial auto renew run: %w", err)
		}
		if previous.Valid {
			value := previous.Time
			run.PreviousExpiry = &value
		}
		if next.Valid {
			value := next.Time
			run.NewExpiry = &value
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

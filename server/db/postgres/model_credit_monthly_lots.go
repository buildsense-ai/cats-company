package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

type modelCreditMonthlyGrant struct {
	key                string
	period, recordedAt time.Time
	units              int64
}

func loadLastModelCreditMonthlyGrant(ctx context.Context, tx *sql.Tx, uid int64, before *time.Time) (*modelCreditMonthlyGrant, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT event_key, created_at, units FROM model_credit_ledger_events
		WHERE uid = $1 AND bucket = 'monthly' AND event_type = 'grant' AND event_key LIKE $2
		  AND ($3::timestamptz IS NULL OR created_at <= $3)
		ORDER BY split_part(event_key, ':', 5) DESC, created_at DESC, event_key DESC LIMIT 50`,
		uid, store.ModelCreditMonthlyGrantKeyPrefix(uid)+"%", before)
	if err != nil {
		return nil, fmt.Errorf("load monthly grant: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var grant modelCreditMonthlyGrant
		if err := rows.Scan(&grant.key, &grant.recordedAt, &grant.units); err != nil {
			return nil, err
		}
		if period, ok := store.ParseModelCreditMonthlyGrantKey(uid, grant.key); ok {
			grant.period = period
			return &grant, nil
		}
	}
	return nil, rows.Err()
}

func moveModelCreditMonthlyBalance(account *store.ModelCreditAccount, freeze bool) (bool, error) {
	if freeze {
		if account.MonthlyUnits == 0 {
			return false, nil
		}
		units := account.MonthlyUnits
		account.MonthlyUnits = 0
		return true, addModelCreditMonthlyBalance(account, units, true)
	}
	if account.FrozenMonthlyUnits == 0 {
		return false, nil
	}
	units := account.FrozenMonthlyUnits
	account.FrozenMonthlyUnits = 0
	return true, addModelCreditMonthlyBalance(account, units, false)
}

func addModelCreditMonthlyBalance(account *store.ModelCreditAccount, units int64, frozen bool) error {
	balance := &account.MonthlyUnits
	if frozen {
		balance = &account.FrozenMonthlyUnits
	}
	updated, ok := store.ModelCreditAddUnits(*balance, units)
	if !ok {
		return store.ErrModelCreditInvalid
	}
	*balance = updated
	return nil
}

func insertModelCreditMonthlyLot(ctx context.Context, tx *sql.Tx, uid int64, key string, entitlementID int64, period time.Time, units int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO model_credit_monthly_lots (grant_key, uid, entitlement_id, period_start, units)
		VALUES ($1, $2, NULLIF($3::bigint, 0), $4, $5)`, key, uid, entitlementID, period, units)
	return err
}

// Adopt only surviving legacy balances. For an older in-flight hold, create
// an empty source lot so the hold can return without restoring expired cash.
func adoptModelCreditMonthlyLot(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount, grant *modelCreditMonthlyGrant, keepBalance bool) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM model_credit_monthly_lots WHERE grant_key = $1)`, grant.key).Scan(&exists); err != nil || exists {
		return err
	}
	plan, _, _ := strings.Cut(strings.TrimPrefix(grant.key, store.ModelCreditMonthlyGrantKeyPrefix(account.UID)), ":")
	planID, err := strconv.ParseInt(plan, 10, 64)
	if err != nil {
		return err
	}
	var entitlementID int64
	var refunded bool
	// Match the business period, not the time a delayed payment was recorded.
	// Entitlement revocation alone does not imply a refund (e.g. tier changes).
	err = tx.QueryRowContext(ctx, `
		SELECT e.id, COALESCE(o.status = 'refunded', FALSE)
		FROM commercial_entitlements e`+modelCreditEntitlementOrderJoinSQL+`
		WHERE e.uid = $1 AND e.plan_id = $2 AND e.source IN `+modelCreditMonthlyGrantableSourcesSQL+`
		  AND e.starts_at <= $3
		  AND (e.expires_at IS NULL OR e.expires_at > $3)
		ORDER BY e.expires_at DESC NULLS LAST, e.starts_at DESC, e.id DESC LIMIT 1`,
		account.UID, planID, grant.period).Scan(&entitlementID, &refunded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var units int64
	if keepBalance && !refunded {
		expired, err := modelCreditLedgerEventExists(ctx, tx, store.ModelCreditMonthlyExpireKey(account.UID, grant.period))
		if err != nil {
			return err
		}
		if !expired {
			units, err = replayLegacyMonthlyLot(ctx, tx, account.UID, grant, "", time.Time{})
			if err != nil {
				return err
			}
			total, ok := store.ModelCreditAddUnits(account.MonthlyUnits, account.FrozenMonthlyUnits)
			if !ok {
				return store.ErrModelCreditInvalid
			}
			units = min(units, total)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO model_credit_monthly_lots
		(grant_key, uid, entitlement_id, period_start, units, revoked)
		VALUES ($1, $2, NULLIF($3::bigint, 0), $4, $5, $6)`,
		grant.key, account.UID, entitlementID, grant.period, units, refunded)
	return err
}

// The old account grouped monthly points. Replay debits from this issuance
// first, without treating other grants as part of its order. For a pre-rollover
// hold, stop at its reserve event and return only the share that came from it.
func replayLegacyMonthlyLot(ctx context.Context, tx *sql.Tx, uid int64, grant *modelCreditMonthlyGrant, reserveKey string, reserveAt time.Time) (int64, error) {
	plan, _, _ := strings.Cut(strings.TrimPrefix(grant.key, store.ModelCreditMonthlyGrantKeyPrefix(uid)), ":")
	debtKey := store.ModelCreditDebtRepaymentKey(fmt.Sprintf("monthly:%d:%s:%s", uid, plan, store.ModelCreditMonthlyPeriodKeyDate(grant.period)))
	var repaid int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT units FROM model_credit_ledger_events
		WHERE event_key = $1 AND uid = $2 AND event_type = 'debt_repaid'), 0)`, debtKey, uid).Scan(&repaid); err != nil {
		return 0, err
	}
	var until *time.Time
	if !reserveAt.IsZero() {
		until = &reserveAt
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT e.event_key, e.event_type, COALESCE(e.request_id, ''), e.units,
		       COALESCE(r.units, 0)
		FROM model_credit_ledger_events e
		LEFT JOIN model_credit_ledger_events r ON r.uid = e.uid AND r.request_id = e.request_id
		   AND e.event_type = 'settle' AND r.event_type = 'reserve' AND r.bucket = 'monthly'
		WHERE e.uid = $1 AND e.bucket = 'monthly' AND e.created_at >= $2
		  AND ($3::timestamptz IS NULL OR e.created_at <= $3)
		  AND (e.event_type IN ('reserve','settle','release') OR e.event_key = $4)
		ORDER BY e.created_at, CASE e.event_type WHEN 'expire' THEN 0 WHEN 'reserve' THEN 1 WHEN 'settle' THEN 2 ELSE 3 END, e.event_key`,
		uid, grant.recordedAt, until, store.ModelCreditMonthlyExpireKey(uid, grant.period))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	remaining := max(0, grant.units-repaid)
	held := make(map[string]int64)
	for rows.Next() {
		var key, kind, request string
		var units, reserved int64
		if err := rows.Scan(&key, &kind, &request, &units, &reserved); err != nil {
			return 0, err
		}
		switch kind {
		case store.ModelCreditEventExpire:
			remaining = 0
		case store.ModelCreditEventReserve:
			share := min(remaining, units)
			if key == reserveKey {
				return share, nil
			}
			held[request] = share
			remaining -= share
		case store.ModelCreditEventSettle:
			held[request] = max(0, held[request]-units)
			remaining -= min(remaining, max(0, units-reserved))
		case store.ModelCreditEventRelease:
			remaining += min(units, held[request])
			delete(held, request)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if reserveKey != "" {
		return 0, store.ErrModelCreditInvalid
	}
	return remaining, nil
}

type modelCreditMonthlyPart struct {
	key   string
	units int64
}

func readModelCreditMonthlyParts(rows *sql.Rows) ([]modelCreditMonthlyPart, error) {
	defer rows.Close()
	var parts []modelCreditMonthlyPart
	for rows.Next() {
		var part modelCreditMonthlyPart
		if err := rows.Scan(&part.key, &part.units); err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, rows.Err()
}

// Account lock must be held. Spend oldest present lots first; any remainder
// is an unscoped monthly grant. A request ID records exact hold provenance.
func debitModelCreditMonthlyLots(ctx context.Context, tx *sql.Tx, uid int64, requestID string, units int64) error {
	if units == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_key, units FROM model_credit_monthly_lots
		WHERE uid = $1 AND units > 0 AND NOT revoked ORDER BY period_start, grant_key`, uid)
	if err != nil {
		return err
	}
	parts, err := readModelCreditMonthlyParts(rows)
	if err != nil {
		return err
	}
	parts = append(parts, modelCreditMonthlyPart{units: units})
	for i, part := range parts {
		take := min(units, part.units)
		if take == 0 {
			break
		}
		if part.key != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE model_credit_monthly_lots SET units = units - $2 WHERE grant_key = $1`, part.key, take); err != nil {
				return err
			}
		}
		if requestID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO model_credit_monthly_hold_lots
				(uid, request_id, ordinal, grant_key, units) VALUES ($1, $2, $3, $4, $5)`, uid, requestID, i, part.key, take); err != nil {
				return err
			}
		}
		units -= take
	}
	return nil
}

func modelCreditMonthlyBalanceFrozen(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (bool, error) {
	ent, err := loadModelCreditMonthlyEntitlement(ctx, tx, uid, now)
	if err != nil || ent != nil {
		return false, err
	}
	grant, err := loadLastModelCreditMonthlyGrant(ctx, tx, uid, nil)
	return grant != nil, err
}

// Complete a hold in its original order. Refunded lots can cover existing
// usage, but their unused part is discarded instead of becoming credit again.
func returnModelCreditMonthlyHold(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount,
	requestID string, held, consumed int64, now time.Time) (int64, error) {
	if held == 0 {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx, `WITH removed AS (
		DELETE FROM model_credit_monthly_hold_lots WHERE uid = $1 AND request_id = $2
		RETURNING ordinal, grant_key, units
	) SELECT grant_key, units FROM removed ORDER BY ordinal`, account.UID, requestID)
	if err != nil {
		return 0, err
	}
	parts, err := readModelCreditMonthlyParts(rows)
	if err != nil {
		return 0, err
	}
	if len(parts) == 0 && held > consumed {
		reserveKey := store.ModelCreditEventKey(store.ModelCreditEventReserve, account.UID, requestID, store.ModelCreditBucketMonthly)
		var createdAt time.Time
		if err := tx.QueryRowContext(ctx, `SELECT created_at FROM model_credit_ledger_events WHERE event_key = $1`, reserveKey).Scan(&createdAt); err != nil {
			return 0, fmt.Errorf("load legacy monthly reserve event: %w", err)
		}
		previous, err := loadLastModelCreditMonthlyGrant(ctx, tx, account.UID, &createdAt)
		if err != nil {
			return 0, err
		}
		var share int64
		if previous != nil {
			share, err = replayLegacyMonthlyLot(ctx, tx, account.UID, previous, reserveKey, createdAt)
			if err != nil {
				return 0, err
			}
			share = min(held, share)
			if share > 0 {
				if err := adoptModelCreditMonthlyLot(ctx, tx, account, previous, false); err != nil {
					return 0, err
				}
				parts = append(parts, modelCreditMonthlyPart{previous.key, share})
			}
		}
		if held > share {
			parts = append(parts, modelCreditMonthlyPart{units: held - share})
		}
	}
	var returned int64
	for _, part := range parts {
		used := min(consumed, part.units)
		consumed -= used
		unused := part.units - used
		if unused == 0 {
			continue
		}
		if part.key != "" {
			result, err := tx.ExecContext(ctx, `UPDATE model_credit_monthly_lots SET units = units + $2
				WHERE grant_key = $1 AND uid = $3 AND NOT revoked`, part.key, unused, account.UID)
			if err != nil {
				return 0, err
			}
			added, err := result.RowsAffected()
			if err != nil {
				return 0, err
			}
			if added == 0 {
				continue // the source order was refunded
			}
		}
		returned += unused
	}
	if returned > 0 {
		frozen, err := modelCreditMonthlyBalanceFrozen(ctx, tx, account.UID, now)
		if err != nil {
			return 0, err
		}
		if err := addModelCreditMonthlyBalance(account, returned, frozen); err != nil {
			return 0, err
		}
	}
	return returned, nil
}

func revokeOrderModelCreditMonthlyLots(ctx context.Context, tx *sql.Tx, uid int64, orderNo string, now time.Time) error {
	account, exists, err := readModelCreditAccountForUpdate(ctx, tx, uid)
	if err != nil || !exists {
		return err
	}
	last, err := loadLastModelCreditMonthlyGrant(ctx, tx, uid, nil)
	if err != nil {
		return err
	}
	if last != nil {
		if err := adoptModelCreditMonthlyLot(ctx, tx, account, last, true); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `
		WITH affected AS (
			SELECT l.grant_key, l.units FROM model_credit_monthly_lots l
			JOIN commercial_entitlements e ON e.id = l.entitlement_id AND e.uid = l.uid
			WHERE l.uid = $1 AND e.source = 'order' AND e.source_ref = $2 AND NOT l.revoked
		), revoked AS (
			UPDATE model_credit_monthly_lots l SET units = 0, revoked = TRUE
			FROM affected a WHERE l.grant_key = a.grant_key RETURNING a.grant_key, a.units
		)
		SELECT grant_key, units FROM revoked`, uid, orderNo)
	if err != nil {
		return err
	}
	parts, err := readModelCreditMonthlyParts(rows)
	if err != nil {
		return err
	}
	for _, part := range parts {
		available := min(account.MonthlyUnits, part.units)
		account.MonthlyUnits -= available
		frozen := part.units - available
		if frozen > account.FrozenMonthlyUnits {
			return store.ErrModelCreditInvalid
		}
		account.FrozenMonthlyUnits -= frozen
	}
	if len(parts) > 0 {
		if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
			return err
		}
	}
	for _, part := range parts {
		if part.units == 0 {
			continue
		}
		if err := recordModelCreditEvent(ctx, tx, "refund:"+part.key, account, store.ModelCreditEventRefund,
			store.ModelCreditBucketMonthly, -part.units, nil); err != nil {
			return err
		}
	}
	return nil
}

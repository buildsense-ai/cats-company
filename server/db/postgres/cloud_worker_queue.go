package postgres

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// QueueCloudWorkerCredit parks a reservation that could not be provisioned
// because the package tier has no free slot. It must be called with the
// reservation still held, so the credit cannot be spent by a concurrent create
// in between.
func (a *Adapter) QueueCloudWorkerCredit(uid int64, reservation string) (bool, error) {
	if uid <= 0 || reservation == "" {
		return false, fmt.Errorf("invalid cloud worker queue request")
	}
	result, err := a.db.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'queued', reservation_ref = '', reserved_at = NULL,
		    queued_at = CURRENT_TIMESTAMP
		WHERE uid = $1 AND reservation_ref = $2 AND state = 'reserved'`, uid, reservation)
	if err != nil {
		return false, fmt.Errorf("queue cloud worker credit: %w", err)
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

// CloudWorkerQueuePosition reports how many older queued credits are ahead of
// this owner, so the console can say "前面还有 N 位" instead of a vague wait.
//
// It returns -1 when the owner has nothing queued. Using 0 for "not queued"
// would hide the only queued buyer in the fleet: they are first in line, so the
// count ahead of them is legitimately 0.
func (a *Adapter) CloudWorkerQueuePosition(uid int64) (int, error) {
	if uid <= 0 {
		return -1, fmt.Errorf("invalid cloud worker queue owner")
	}
	var queuedAt sql.NullTime
	err := a.db.QueryRow(`
		SELECT MIN(queued_at) FROM cloud_worker_credits
		WHERE uid = $1 AND state = 'queued' AND source_ref NOT LIKE 'ops:%'`, uid).Scan(&queuedAt)
	if err == sql.ErrNoRows {
		return -1, nil
	}
	if err != nil {
		return -1, fmt.Errorf("cloud worker queue position: %w", err)
	}
	if !queuedAt.Valid {
		return -1, nil
	}
	var ahead int
	if err := a.db.QueryRow(`
		SELECT COUNT(*) FROM cloud_worker_credits
		WHERE state = 'queued' AND source_ref NOT LIKE 'ops:%' AND queued_at < $1`, queuedAt.Time).Scan(&ahead); err != nil {
		return -1, fmt.Errorf("cloud worker queue position: %w", err)
	}
	return ahead, nil
}

// ListOrphanedCloudWorkerCredits returns owners who still hold an unspent paid
// credit that nobody has tried to spend for a while.
//
// Why this exists: a plan or invite redemption mints the credit inside the
// payment transaction, then starts provisioning on a bare goroutine
// (commercial_payment.go). If the process dies, is redeployed, or the goroutine
// panics between the commit and the create, the credit stays 'available'
// forever: the buyer paid, owns a machine on paper, and nothing ever retries.
// The only recovery was "the customer notices and clicks create".
//
// Scope is deliberately narrow:
//   - only paid sources ('order:%' / 'invite:%'), so operator grants and
//     manually issued credits keep their current "spend it when you want" meaning
//   - only 'available', so a create that is still running ('reserved') or one
//     that parked on a full pool ('queued', handled by retryQueuedCloudWorkerCredits)
//     is never double-spent
//   - only credits older than minAge, so the normal payment-path goroutine is
//     never raced
func (a *Adapter) ListOrphanedCloudWorkerCredits(minAge time.Duration, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 10
	}
	if minAge < 0 {
		minAge = 0
	}
	rows, err := a.db.Query(`
		SELECT uid FROM cloud_worker_credits
		WHERE state = 'available'
		  AND (source_ref LIKE 'order:%' OR source_ref LIKE 'invite:%')
		  AND created_at <= CURRENT_TIMESTAMP - $1::interval
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		GROUP BY uid
		ORDER BY MIN(created_at)
		LIMIT $2`, fmt.Sprintf("%d seconds", int(minAge.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("list orphaned cloud worker credits: %w", err)
	}
	defer rows.Close()
	var uids []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("scan orphaned cloud worker credit: %w", err)
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}

// ListQueuedCloudWorkerCredits returns queued credits old enough to retry.
// The minimum age keeps a retry loop from spinning against the same full pool
// within one sweep interval, and lets a burst of purchases queue up in order.
func (a *Adapter) ListQueuedCloudWorkerCredits(minAge time.Duration, limit int) ([]types.CloudWorkerQueuedCredit, error) {
	if limit <= 0 {
		limit = 20
	}
	if minAge < 0 {
		minAge = 0
	}
	rows, err := a.db.Query(`
		SELECT uid, runtime_plan, queued_at
		FROM cloud_worker_credits
		WHERE state = 'queued' AND source_ref NOT LIKE 'ops:%' AND queued_at <= CURRENT_TIMESTAMP - $1::interval
		ORDER BY queued_at
		LIMIT $2`, fmt.Sprintf("%d seconds", int(minAge.Seconds())), limit)
	if err != nil {
		return nil, fmt.Errorf("list queued cloud worker credits: %w", err)
	}
	defer rows.Close()
	var queued []types.CloudWorkerQueuedCredit
	for rows.Next() {
		var item types.CloudWorkerQueuedCredit
		if err := rows.Scan(&item.UID, &item.Tier, &item.QueuedAt); err != nil {
			return nil, fmt.Errorf("scan queued cloud worker credit: %w", err)
		}
		queued = append(queued, item)
	}
	return queued, rows.Err()
}

// ClaimQueuedCloudWorkerCredit turns one queued credit back into a spendable
// one so the normal create path can reserve it. It is intentionally narrow: a
// concurrent manual create, a refund, or an expiry can change the row first, in
// which case the claim reports false and the caller simply skips this round.
func (a *Adapter) ClaimQueuedCloudWorkerCredit(uid int64) (bool, error) {
	if uid <= 0 {
		return false, fmt.Errorf("invalid cloud worker queue claim")
	}
	result, err := a.db.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'available', queued_at = NULL
		WHERE uid = $1 AND state = 'queued' AND source_ref NOT LIKE 'ops:%'
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`, uid)
	if err != nil {
		return false, fmt.Errorf("claim queued cloud worker credit: %w", err)
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// ClaimQueuedCloudWorkerRuntimeCredit releases one credit of the queued tier.
// Releasing every tier for the owner would strand the remaining manual grants
// after a filtered retry, or release a tier that has just queued again.
func (a *Adapter) ClaimQueuedCloudWorkerRuntimeCredit(uid int64, runtimePlan string) (bool, error) {
	runtimePlan, valid := types.NormalizeCloudWorkerRuntimePlan(runtimePlan)
	if uid <= 0 || !valid || runtimePlan == "" {
		return false, fmt.Errorf("invalid cloud worker runtime queue claim")
	}
	result, err := a.db.Exec(`
		UPDATE cloud_worker_credits
		SET state = 'available', queued_at = NULL
		WHERE id = (
			SELECT id FROM cloud_worker_credits
			WHERE uid = $1 AND state = 'queued' AND source_ref NOT LIKE 'ops:%'
			  AND deployment_profile = 'packed_shared' AND billing_mode = 'month'
			  AND COALESCE(NULLIF(runtime_plan, ''), 'standard') = $2
			  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
			ORDER BY queued_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)`, uid, runtimePlan)
	if err != nil {
		return false, fmt.Errorf("claim queued cloud worker runtime credit: %w", err)
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

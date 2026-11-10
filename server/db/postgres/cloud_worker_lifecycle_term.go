package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// cloudWorkerLifecycleColumns is the shared lifecycle projection. The claim
// timestamp is coalesced so no reader has to special-case a NULL, and the two
// dates are read as nullable: a permanent worker has no deadline at all, so
// callers get the zero time instead of a notional far-future placeholder.
const cloudWorkerLifecycleColumns = `id,worker_uid,owner_uid,tenant_name,package_expires_at,delete_after,state,billing_mode,conversion_pending,billing_action,COALESCE(billing_action_started_at,'epoch'::timestamptz),retention_days,expiry_mode,sync_pending`

// cloudWorkerSharedProfileFilter matches the workers the shared container
// cluster owns. The profile lives in the deployment snapshot because that is
// what the provider path itself reads; a lifecycle row alone does not know
// which backend is behind it.
const cloudWorkerSharedProfileFilter = `EXISTS (SELECT 1 FROM bot_config b WHERE b.user_id = l.worker_uid AND b.cloud_deployment->>'profile' = 'packed_shared')`

// scanCloudWorkerLifecycle reads one row of cloudWorkerLifecycleColumns.
//
// It exists so every reader (billing loop, admin dashboard, sync queue) sees
// the same fields. The previous per-query scans had already drifted: the
// billing reader dropped RetentionDays, which made a renewal of a worker
// created with a 7-day window fall back to the legacy 15.
func scanCloudWorkerLifecycle(row interface{ Scan(...interface{}) error }) (*types.CloudWorkerLifecycle, error) {
	var item types.CloudWorkerLifecycle
	var packageExpiresAt, deleteAfter sql.NullTime
	if err := row.Scan(
		&item.ID,
		&item.WorkerUID,
		&item.OwnerUID,
		&item.TenantName,
		&packageExpiresAt,
		&deleteAfter,
		&item.State,
		&item.BillingMode,
		&item.ConversionPending,
		&item.BillingAction,
		&item.BillingActionStartedAt,
		&item.RetentionDays,
		&item.ExpiryMode,
		&item.SyncPending,
	); err != nil {
		return nil, err
	}
	if packageExpiresAt.Valid {
		item.PackageExpiresAt = packageExpiresAt.Time.UTC()
	}
	if deleteAfter.Valid {
		item.DeleteAfter = deleteAfter.Time.UTC()
	}
	return &item, nil
}

// SetSharedCloudWorkerTerm re-terms one existing shared-container worker in
// place: no new row, no new machine, no charge.
//
// The worker keeps its identity (and therefore its data and its bot row) while
// the expiry it follows changes. That is the whole point: an operator who sold
// a permanent grant, or who moved a customer onto a package that now governs
// the machine, must not have to destroy and reprovision a running container to
// make the database agree.
//
// The provider is not touched here (a database write cannot reach it), so the
// row is marked sync_pending: the background sync brings the provider in line,
// and the expiry sweeper refuses to destroy a worker whose database term is
// still unsynced.
//
// follow_plan is resolved by the caller from the actual primary package. This
// method deliberately does not read plans: a second opinion about "the active
// package" inside the store is exactly how the two would drift apart.
//
// The write itself is guarded against shortening. The caller resolves the term
// from a snapshot (the HTTP request, or a plan read taken a moment ago), and a
// payment can extend the very same row in between. A re-term must never undo
// that: a fixed term may not be moved earlier than the row's current paid
// window, and a finite follow_plan target keeps the later of the two. Without
// the guard the operator's stale snapshot silently truncates time the customer
// has already paid for.
func (a *Adapter) SetSharedCloudWorkerTerm(id int64, mode string, expiresAt *time.Time, retentionDays int) (*types.CloudWorkerLifecycle, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid shared cloud worker term target")
	}
	normalized, ok := types.NormalizeCloudWorkerExpiryMode(mode)
	if !ok || normalized == "" {
		return nil, fmt.Errorf("invalid shared cloud worker expiry mode")
	}
	if retentionDays < 0 || retentionDays > 90 {
		return nil, fmt.Errorf("invalid cloud worker retention window")
	}
	var term *time.Time
	switch normalized {
	case types.CloudWorkerExpiryPermanent:
		// permanent means no deadline; carrying one would be a contradiction the
		// sweeper would eventually act on.
		if expiresAt != nil {
			return nil, fmt.Errorf("a permanent shared cloud worker must not carry an expiry")
		}
	case types.CloudWorkerExpiryFixed:
		if expiresAt == nil || !expiresAt.After(time.Now()) {
			return nil, fmt.Errorf("a fixed shared cloud worker requires a future expiry")
		}
		utc := expiresAt.UTC()
		term = &utc
	case types.CloudWorkerExpiryFollowPlan:
		// A perpetual package is a real state, so no expiry is valid here; a
		// stale date is not.
		if expiresAt != nil {
			if !expiresAt.After(time.Now()) {
				return nil, fmt.Errorf("a follow_plan shared cloud worker requires a future expiry or none")
			}
			utc := expiresAt.UTC()
			term = &utc
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin shared cloud worker re-term: %w", err)
	}
	defer tx.Rollback()
	matched, err := scanCloudWorkerLifecycle(tx.QueryRow(`
		UPDATE cloud_worker_lifecycles l
		SET expiry_mode = $2,
		    package_expires_at = CASE
		        WHEN $3::timestamptz IS NULL THEN NULL
		        WHEN $2::text = 'follow_plan' AND l.package_expires_at IS NOT NULL
		            THEN GREATEST(l.package_expires_at, $3::timestamptz)
		        ELSE $3::timestamptz END,
		    retention_days = CASE WHEN $4::int > 0 THEN $4::int ELSE l.retention_days END,
		    sync_pending = TRUE,
		    conversion_pending = FALSE,
		    state = 'active', archived_at = NULL, delete_started_at = NULL,
		    last_error = '', updated_at = CURRENT_TIMESTAMP
		-- A destroy already claimed by the sweeper is an external operation in
		-- flight, and an in-flight billing action owns the row until it finishes
		-- (or its 30-minute claim goes stale). Re-terming either of those would
		-- fight the provider instead of describing it.
		WHERE l.id = $1
		  AND l.state IN ('active','delete_pending','delete_failed')
		  AND l.billing_action = ''
		  -- A fixed term is the operator's own number; refuse it when it would
		  -- cut into a window that is already paid for (a concurrent payment, or
		  -- simply a longer row). Movement to a later date is still allowed.
		  AND ($2::text <> 'fixed'
		       OR l.package_expires_at IS NULL
		       OR $3::timestamptz IS NULL
		       OR $3::timestamptz >= l.package_expires_at)
		  AND `+cloudWorkerSharedProfileFilter+`
		RETURNING `+cloudWorkerLifecycleColumns, id, normalized, term, retentionDays))
	if err == sql.ErrNoRows {
		return nil, a.sharedCloudWorkerTermRefusal(tx, id, normalized, term)
	}
	if err != nil {
		return nil, fmt.Errorf("re-term shared cloud worker: %w", err)
	}
	// delete_after follows whatever window the guarded update actually committed,
	// which is why it is computed in a second statement instead of from the
	// caller's (possibly older) target.
	item, err := scanCloudWorkerLifecycle(tx.QueryRow(`
		UPDATE cloud_worker_lifecycles
		SET delete_after = CASE WHEN package_expires_at IS NULL THEN NULL
		        ELSE package_expires_at + (CASE WHEN $2::int > 0 THEN $2::int WHEN retention_days > 0 THEN retention_days ELSE 15 END * INTERVAL '1 day') END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING `+cloudWorkerLifecycleColumns, matched.ID, retentionDays))
	if err != nil {
		return nil, fmt.Errorf("schedule shared cloud worker retention: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit shared cloud worker re-term: %w", err)
	}
	return item, nil
}

// sharedCloudWorkerTermRefusal explains why the guarded re-term matched nothing.
//
// The guard is a single UPDATE on purpose (atomicity), but a bare "not eligible"
// would leave an operator guessing whether the worker is gone, still owned by an
// action, or simply being asked to shorten a paid window.
func (a *Adapter) sharedCloudWorkerTermRefusal(tx *sql.Tx, id int64, mode string, term *time.Time) error {
	var state, action, currentMode, profile string
	var current sql.NullTime
	err := tx.QueryRow(`
		SELECT l.state, l.billing_action, l.expiry_mode, l.package_expires_at,
		       COALESCE((SELECT b.cloud_deployment->>'profile' FROM bot_config b WHERE b.user_id = l.worker_uid), '')
		FROM cloud_worker_lifecycles l WHERE l.id = $1`, id).Scan(&state, &action, &currentMode, &current, &profile)
	if err == sql.ErrNoRows {
		return fmt.Errorf("cloud worker %d not found", id)
	}
	if err != nil {
		return fmt.Errorf("inspect cloud worker %d before re-terming: %w", id, err)
	}
	switch {
	case profile != types.CloudWorkerPackedShared:
		return fmt.Errorf("cloud worker %d is not a packed_shared worker; only the shared cluster fleet can be re-termed", id)
	case state == "deleted" || state == "delete_running":
		return fmt.Errorf("cloud worker %d is %s; a destroy is already in flight", id, state)
	case action != "":
		return fmt.Errorf("cloud worker %d has an in-flight billing action %q", id, action)
	case mode == types.CloudWorkerExpiryFixed && current.Valid && term != nil && term.Before(current.Time):
		return fmt.Errorf("refusing to shorten cloud worker %d: a fixed term of %s is earlier than the paid window ending %s",
			id, term.UTC().Format(time.RFC3339), current.Time.UTC().Format(time.RFC3339))
	}
	return fmt.Errorf("cloud worker %d cannot be re-termed from %q", id, currentMode)
}

// ClaimSharedCloudWorkerSync atomically takes the sync claim for one shared
// worker. The claim is billing_action='sync' with a start stamp, reusing the
// same 30-minute staleness window as the other billing claims so a crashed
// process cannot pin the row.
//
// It marks sync_pending on the way in: a claim exists precisely because the
// provider and the database disagree, and the flag is what stops the expiry
// sweeper from destroying the container meanwhile. The term itself is never
// modified here.
func (a *Adapter) ClaimSharedCloudWorkerSync(id int64) (*types.CloudWorkerLifecycle, bool, error) {
	if id <= 0 {
		return nil, false, fmt.Errorf("invalid shared cloud worker sync claim")
	}
	item, err := scanCloudWorkerLifecycle(a.db.QueryRow(`
		UPDATE cloud_worker_lifecycles l
		SET billing_action = 'sync', billing_action_started_at = CURRENT_TIMESTAMP,
		    sync_pending = TRUE, updated_at = CURRENT_TIMESTAMP
		WHERE l.id = $1
		  AND l.state NOT IN ('delete_running','deleted')
		  -- Only a row that actually owes the provider a term can be claimed.
		  -- Without this, a legacy worker with no expiry at all (nothing to sync)
		  -- could be claimed, marked pending and then "completed", reporting a
		  -- reconciliation that never happened.
		  AND l.sync_pending
		  AND (l.billing_action = ''
		       OR (l.billing_action = 'sync' AND l.billing_action_started_at <= CURRENT_TIMESTAMP - INTERVAL '30 minutes'))
		  AND `+cloudWorkerSharedProfileFilter+`
		RETURNING `+cloudWorkerLifecycleColumns, id))
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim shared cloud worker sync: %w", err)
	}
	return item, true, nil
}

// CompleteSharedCloudWorkerSync releases the sync claim.
//
// A successful sync only clears sync_pending when the row still says what the
// sync actually applied. If a payment extended the expiry while the provider
// call was in flight, the row no longer matches expectedExpiry, so the flag
// stays set: the newer term is still unsynced and must not be reported as
// applied just because an older provider call succeeded.
//
// A failed sync keeps sync_pending set and records the error, so the next pass
// retries it instead of leaving the row looking reconciled.
func (a *Adapter) CompleteSharedCloudWorkerSync(id int64, expectedExpiry *time.Time, mode string, errorText string) error {
	if id <= 0 {
		return fmt.Errorf("invalid shared cloud worker sync completion")
	}
	expectedMode := types.CloudWorkerExpiryModeOrDefault(mode)
	var expected *time.Time
	if expectedExpiry != nil {
		utc := expectedExpiry.UTC()
		expected = &utc
	}
	if strings.TrimSpace(errorText) != "" {
		// A failed sync keeps sync_pending set (it is not cleared here) and
		// records why, so the next pass retries instead of declaring victory.
		result, err := a.db.Exec(`
			UPDATE cloud_worker_lifecycles
			SET billing_action = '', billing_action_started_at = NULL,
			    last_error = $2, updated_at = CURRENT_TIMESTAMP
			WHERE id = $1 AND billing_action = 'sync'`, id, strings.TrimSpace(errorText))
		return cloudWorkerClaimReleased(result, err, "shared cloud worker sync")
	}
	result, err := a.db.Exec(`
		UPDATE cloud_worker_lifecycles l
		SET billing_action = '', billing_action_started_at = NULL,
		    last_error = '',
		    sync_pending = CASE WHEN l.package_expires_at IS NOT DISTINCT FROM $2::timestamptz
		                         AND l.expiry_mode = $3
		                    THEN FALSE ELSE TRUE END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE l.id = $1 AND l.billing_action = 'sync'`, id, expected, expectedMode)
	return cloudWorkerClaimReleased(result, err, "shared cloud worker sync")
}

// ListSharedCloudWorkerSyncPending returns the shared workers whose provider
// term still has to be reconciled.
//
// A claim younger than a minute is skipped so a pass in flight is not handed
// out twice, and last_error is left untouched: the operator needs the reason
// the previous attempt failed until a later one succeeds.
func (a *Adapter) ListSharedCloudWorkerSyncPending(limit int) ([]types.CloudWorkerLifecycle, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := a.db.Query(`
		SELECT `+cloudWorkerLifecycleColumns+`
		FROM cloud_worker_lifecycles l
		WHERE l.sync_pending
		  AND l.state NOT IN ('delete_running','deleted')
		  AND (l.billing_action = ''
		       OR l.billing_action_started_at IS NULL
		       OR l.billing_action_started_at <= CURRENT_TIMESTAMP - INTERVAL '1 minute')
		  -- A fresh row (or one a payment has just extended) is handed out
		  -- immediately, but a row whose last attempt failed waits a minute: the
		  -- failure just released its claim, and retrying it in the same pass only
		  -- burns provider calls on a provider that is evidently unhappy.
		  AND (l.last_error = '' OR l.updated_at <= CURRENT_TIMESTAMP - INTERVAL '1 minute')
		  AND `+cloudWorkerSharedProfileFilter+`
		ORDER BY l.updated_at, l.id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list shared cloud worker sync queue: %w", err)
	}
	defer rows.Close()
	var records []types.CloudWorkerLifecycle
	for rows.Next() {
		item, err := scanCloudWorkerLifecycle(rows)
		if err != nil {
			return nil, fmt.Errorf("scan shared cloud worker sync queue: %w", err)
		}
		records = append(records, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read shared cloud worker sync queue: %w", err)
	}
	return records, nil
}

// ClaimSharedCloudWorkerExpiry atomically takes the expiry claim for one
// shared worker.
//
// The expiry predicate is re-evaluated under the row lock instead of trusting
// the sweeper's snapshot: a package payment can extend the worker between the
// due scan and this claim, and an old snapshot must not destroy a worker that
// has just been renewed. A worker that still has to be synced is never claimed.
func (a *Adapter) ClaimSharedCloudWorkerExpiry(id int64, now time.Time) (bool, error) {
	if id <= 0 {
		return false, fmt.Errorf("invalid shared cloud worker expiry claim")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := a.db.Exec(`
		UPDATE cloud_worker_lifecycles l
		SET billing_action = 'expire', billing_action_started_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE l.id = $1
		  AND l.state = 'active'
		  AND l.package_expires_at IS NOT NULL AND l.package_expires_at <= $2::timestamptz
		  AND NOT l.sync_pending
		  AND (l.billing_action = ''
		       OR l.billing_action_started_at <= CURRENT_TIMESTAMP - INTERVAL '30 minutes')
		  AND `+cloudWorkerSharedProfileFilter, id, now.UTC())
	if err != nil {
		return false, fmt.Errorf("claim shared cloud worker expiry: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// CompleteSharedCloudWorkerExpiry releases the expiry claim.
//
// The worker is only scheduled for deletion when the row still carries exactly
// the expiry that was acted on, is still due, and has nothing pending: a
// renewal or a re-term that landed meanwhile wins. delete_after is left alone
// on purpose — the existing window (expiry plus the row's own retention) is
// what the customer was promised, so this claim must not shorten it.
func (a *Adapter) CompleteSharedCloudWorkerExpiry(id int64, expectedExpiry time.Time, errorText string) error {
	if id <= 0 || expectedExpiry.IsZero() {
		return fmt.Errorf("invalid shared cloud worker expiry completion")
	}
	expected := expectedExpiry.UTC()
	if strings.TrimSpace(errorText) != "" {
		// A failed expiry keeps the row as it is (a retry is allowed) and only
		// releases the claim with the reason attached.
		result, err := a.db.Exec(`
			UPDATE cloud_worker_lifecycles
			SET billing_action = '', billing_action_started_at = NULL,
			    last_error = $2, updated_at = CURRENT_TIMESTAMP
			WHERE id = $1 AND billing_action = 'expire'`, id, strings.TrimSpace(errorText))
		return cloudWorkerClaimReleased(result, err, "shared cloud worker expiry")
	}
	result, err := a.db.Exec(`
		UPDATE cloud_worker_lifecycles
		SET state = CASE WHEN package_expires_at = $2::timestamptz
		                       AND package_expires_at <= CURRENT_TIMESTAMP
		                       AND NOT sync_pending AND state = 'active'
		                 THEN 'delete_pending' ELSE state END,
		    archived_at = CASE WHEN package_expires_at = $2::timestamptz
		                            AND package_expires_at <= CURRENT_TIMESTAMP
		                            AND NOT sync_pending AND state = 'active'
		                      THEN COALESCE(archived_at, CURRENT_TIMESTAMP) ELSE archived_at END,
		    billing_action = '', billing_action_started_at = NULL,
		    last_error = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND billing_action = 'expire'`, id, expected)
	return cloudWorkerClaimReleased(result, err, "shared cloud worker expiry")
}

// cloudWorkerClaimReleased turns a released claim into an error when the update
// matched nothing: the claim was taken over by someone else (its 30-minute
// window elapsed and another pass re-claimed it), so reporting success would
// hide that this process's work was not recorded.
func cloudWorkerClaimReleased(result sql.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("release %s claim: %w", what, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%s claim lost", what)
	}
	return nil
}

// cloudWorkerMachineTermRow is the raw row behind CloudWorkerPackageTerm, kept
// separate so the query can return the plan's configured inputs (retention,
// duration) and let Go derive the effective retention once.
type cloudWorkerMachineTermRow struct {
	EntitlementID int64
	PlanID        int64
	ExpiresAt     sql.NullTime
	RetentionDays int
}

func (r *cloudWorkerMachineTermRow) packageTerm() *types.CloudWorkerPackageTerm {
	term := &types.CloudWorkerPackageTerm{
		EntitlementID: r.EntitlementID,
		PlanID:        r.PlanID,
		RetentionDays: r.RetentionDays,
	}
	if r.ExpiresAt.Valid {
		value := r.ExpiresAt.Time.UTC()
		term.ExpiresAt = &value
	}
	return term
}

// cloudWorkerMachineTermQuery is the single definition of "which bought windows
// give this owner a machine".
//
// Everything that needs the answer goes through queryCloudWorkerMachineTerm: the
// commercial summary, the operator preview, and the settlement that re-terms
// follow_plan workers after a payment, an operator extension, a refund or a
// revocation. One statement is what stops the preview from approving a worker the
// settlement would immediately expire (or the other way round), and it is where
// the eligibility rule lives:
//
//   - a free or legacy baseline never grants a machine, even when its plan row is
//     (wrongly) flagged cloud_worker_included: it is a quota backfill written
//     with no expiry, so honouring it would hand out a container that nothing
//     ever collects;
//   - a package grants a machine when the plan says so, or when an invite handed
//     out machine credits against a token-only plan;
//   - only an active entitlement counts; a revoked or expired one is not a grant.
//
// The "not started yet" cut is deliberately absent. An early renewal - and the
// operator extension that mirrors it - appends a segment that starts at the
// previous period's expiry, i.e. in the future, so a query that dropped those
// rows answered with the *previous* segment and the settlement that follows a
// payment re-termed the worker to a window the buyer had already left behind.
// The rows are returned with their windows; cloudWorkerMachineCoverageEnd merges
// them into the coverage that starts at the machine in force today and stops at
// the first gap, so a package bought for next month still authorizes nothing
// today.
const cloudWorkerMachineTermQuery = `
	SELECT e.id, e.plan_id, e.starts_at, e.expires_at, p.cloud_worker_retention_days, p.duration_days
	FROM commercial_entitlements e
	JOIN commercial_plans p ON p.id = e.plan_id
	WHERE e.uid = $1 AND e.state = 'active'
	  AND e.source NOT IN ('free', 'legacy')
	  AND (p.cloud_worker_included
	       OR (e.source = 'invite' AND EXISTS (
	            SELECT 1 FROM commercial_invite_codes ic
	            WHERE ic.code = e.source_ref AND ic.cloud_worker_credits > 0)))
	ORDER BY e.starts_at, e.expires_at DESC NULLS LAST, e.id`

// rowQuerier is the slice of *sql.DB / *sql.Tx this query needs, so the read
// path and the transactional write path share one statement instead of two
// copies that can drift.
type rowQuerier interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
}

// queryCloudWorkerMachineTerm resolves the machine-granting package for one
// owner, or nil when the owner holds none.
//
// It answers two things from one read: which machine package is in force now
// (the identity and the retention the account is told it is on) and how far the
// contiguous coverage of bought windows reaches. The second is what the
// settlement writes into a follow_plan worker, so an early renewal - whose new
// segment starts at the previous expiry - keeps the time it paid for.
func queryCloudWorkerMachineTerm(q rowQuerier, ownerUID int64, now time.Time) (*cloudWorkerMachineTermRow, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	segments, err := loadCloudWorkerMachineSegments(q, ownerUID)
	if err != nil {
		return nil, err
	}
	primary, ok := selectCloudWorkerMachinePrimary(segments, now)
	if !ok {
		// Nothing is in force today. A window that only starts later is not a
		// grant now, so the caller is told there is no machine package at all.
		return nil, nil
	}
	return &cloudWorkerMachineTermRow{
		EntitlementID: primary.EntitlementID,
		PlanID:        primary.PlanID,
		ExpiresAt:     cloudWorkerMachineCoverageEnd(segments, primary, now),
		RetentionDays: primary.RetentionDays,
	}, nil
}

// GetSharedCloudWorkerPackageTerm reports the package that actually grants this
// owner a machine, or nil when it holds none.
//
// Callers must use this instead of the model-primary entitlement: an invite can
// hand out a machine against a token-only plan, and a token package can outrank
// the machine package in the primary ordering. Reading the primary package is
// what made the operator preview reject a machine the account really owns.
func (a *Adapter) GetSharedCloudWorkerPackageTerm(ownerUID int64) (*types.CloudWorkerPackageTerm, error) {
	if ownerUID <= 0 {
		return nil, fmt.Errorf("invalid cloud worker package term owner")
	}
	row, err := queryCloudWorkerMachineTerm(a.db, ownerUID, time.Now().UTC())
	if err != nil || row == nil {
		return nil, err
	}
	return row.packageTerm(), nil
}

// settleSharedCloudWorkerFollowPlanTerms re-derives the term of every shared
// worker that follows its package from the packages the owner actually still
// holds.
//
// It runs after every change to the set of paid windows - a payment, an operator
// extension, a refund, and the tier revocation an invite redemption or an
// upgrade performs - because those paths only know what is left, and "left" can
// be nothing at all. A worker that was opened by a perpetual package must not
// stay open-ended after that entitlement is revoked — that is a machine nobody
// pays for and nothing ever collects.
//
// The three surviving states map to the three honest outcomes:
//
//   - a perpetual MACHINE package remains: the worker stays open-ended (no dates);
//   - finite machine packages remain: the worker's term becomes the end of the
//     coverage they form (cloudWorkerMachineCoverageEnd), which is what lets an
//     early renewal keep the segment it appended while a window bought after a
//     gap still authorizes nothing today. The row's own date is NOT merged in
//     with GREATEST: the current date may be the very window that was just taken
//     away (a refunded 365-day order must not outlive the 30 days the buyer
//     still holds);
//   - nothing remains: the worker expires now, keeps its retention window, and
//     is queued for the provider sync so the expiry path can pause it. It stays
//     'active' rather than 'delete_pending' on purpose: the term has not reached
//     the provider yet, and sync_pending is what holds the destroy off until it
//     has.
//
// "Package" here means a package that actually grants a machine: a free or
// legacy baseline is a perpetual quota backfill and must never be read as an
// unlimited machine grant.
//
// Fixed terms and permanent grants are never touched: they were sold
// independently of any package.
func settleSharedCloudWorkerFollowPlanTerms(tx *sql.Tx, ownerUID int64, now time.Time) error {
	if tx == nil || ownerUID <= 0 {
		return fmt.Errorf("invalid cloud worker term settlement")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// The surviving machine package comes from the same query the commercial
	// summary and the operator preview read, so an operator can never be told one
	// thing while the settlement does another.
	term, err := queryCloudWorkerMachineTerm(tx, ownerUID, now)
	if err != nil {
		return err
	}
	if term == nil {
		if _, err := tx.Exec(`
			UPDATE cloud_worker_lifecycles l
			SET package_expires_at = $2::timestamptz,
			    delete_after = $2::timestamptz + (CASE WHEN l.retention_days > 0 THEN l.retention_days ELSE 15 END * INTERVAL '1 day'),
			    state = 'active', archived_at = NULL, delete_started_at = NULL,
			    sync_pending = TRUE, updated_at = CURRENT_TIMESTAMP
			WHERE l.owner_uid = $1 AND l.expiry_mode = 'follow_plan'
			  AND l.state IN ('active','delete_pending','delete_failed')
			  AND `+cloudWorkerSharedProfileFilter, ownerUID, now.UTC()); err != nil {
			return fmt.Errorf("expire cloud workers whose package was revoked: %w", err)
		}
		return nil
	}
	if !term.ExpiresAt.Valid {
		if _, err := tx.Exec(`
			UPDATE cloud_worker_lifecycles l
			SET package_expires_at = NULL, delete_after = NULL,
			    state = 'active', archived_at = NULL, delete_started_at = NULL,
			    sync_pending = TRUE, updated_at = CURRENT_TIMESTAMP
			WHERE l.owner_uid = $1 AND l.expiry_mode = 'follow_plan'
			  AND l.state IN ('active','delete_pending','delete_failed')
			  AND `+cloudWorkerSharedProfileFilter, ownerUID); err != nil {
			return fmt.Errorf("open cloud workers on the surviving perpetual package: %w", err)
		}
		return nil
	}
	// The surviving package's own window REPLACES the row's date instead of being
	// merged with GREATEST. This runs after a revocation, so the row's current
	// date may be exactly the window that was just taken away: a refunded
	// 365-day order must not outlive the 30 days the buyer still holds.
	if _, err := tx.Exec(`
		UPDATE cloud_worker_lifecycles l
		SET package_expires_at = $2::timestamptz,
		    delete_after = $2::timestamptz + (CASE WHEN l.retention_days > 0 THEN l.retention_days ELSE 15 END * INTERVAL '1 day'),
		    state = 'active', archived_at = NULL, delete_started_at = NULL,
		    sync_pending = TRUE, updated_at = CURRENT_TIMESTAMP
		WHERE l.owner_uid = $1 AND l.expiry_mode = 'follow_plan'
		  AND l.state IN ('active','delete_pending','delete_failed')
		  AND `+cloudWorkerSharedProfileFilter, ownerUID, term.ExpiresAt.Time.UTC()); err != nil {
		return fmt.Errorf("settle cloud worker terms on the surviving package: %w", err)
	}
	return nil
}

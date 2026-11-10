package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// TestPostgresCloudWorkerLifecycleTermContract pins the shared-cluster term
// rules against a real database.
//
// The unit tests cover the validation in types; what they cannot cover is the
// SQL behaviour that decides whether a paying customer's container survives:
// nullable dates for permanent workers, the sync queue that a re-term opens,
// the guards that stop a stale snapshot from shortening a paid window, the
// exclusion that keeps a fixed term out of package extensions, and the claim
// guards that have to lose against a newer term or a running sync.
//
// Every expected timestamp goes through pgMicro: PostgreSQL keeps microseconds,
// and comparing nanosecond Go values against stored ones fails for no reason
// that has anything to do with the behaviour under test.
func TestPostgresCloudWorkerLifecycleTermContract(t *testing.T) {
	db := commercialPolicyTestDB(t)
	owner := createGLM53MigrationUser(t, db, "lifecycle-term")

	t.Run("permanent term opens the row and queues a sync", func(t *testing.T) {
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-shared-permanent", "legacy", nil, nil, "active", "")

		item, err := db.SetSharedCloudWorkerTerm(id, types.CloudWorkerExpiryPermanent, nil, 7)
		if err != nil {
			t.Fatalf("re-term permanent: %v", err)
		}
		if !item.PackageExpiresAt.IsZero() || !item.DeleteAfter.IsZero() {
			t.Fatalf("a permanent worker must not carry dates: %+v", item)
		}
		if item.ExpiryMode != types.CloudWorkerExpiryPermanent || !item.SyncPending {
			t.Fatalf("term/sync not recorded: %+v", item)
		}

		pending, err := db.ListSharedCloudWorkerSyncPending(100)
		if err != nil {
			t.Fatal(err)
		}
		if !containsLifecycleID(pending, id) {
			t.Fatalf("the re-termed worker is missing from the sync queue: %+v", pending)
		}

		claimed, ok, err := db.ClaimSharedCloudWorkerSync(id)
		if err != nil || !ok || claimed == nil || claimed.BillingAction != "sync" {
			t.Fatalf("sync claim: %+v ok=%v err=%v", claimed, ok, err)
		}
		if claimed.ExpiryMode != types.CloudWorkerExpiryPermanent {
			t.Fatalf("the claim must not alter the term: %+v", claimed)
		}
		if err := db.CompleteSharedCloudWorkerSync(id, nil, types.CloudWorkerExpiryPermanent, ""); err != nil {
			t.Fatalf("complete sync: %v", err)
		}
		reloaded, err := db.GetCloudWorkerBillingLifecycle("bot-shared-permanent")
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.SyncPending {
			t.Fatalf("a completed sync must clear the flag: %+v", reloaded)
		}
		if reloaded.RetentionDays != 7 {
			t.Fatalf("the billing reader dropped the retention window: %+v", reloaded)
		}

		// A permanent worker is never due and never destroyed by an expiry claim.
		due, err := db.ListCloudWorkerLifecycleDue(time.Now().UTC(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if containsLifecycleID(due, id) {
			t.Fatal("a permanent worker must not appear in the expiry sweep")
		}
		if ok, err := db.ClaimSharedCloudWorkerExpiry(id, time.Now().UTC()); err != nil || ok {
			t.Fatalf("permanent expiry claim: ok=%v err=%v", ok, err)
		}
		if ok, err := db.ClaimCloudWorkerLifecycleDeletion(id); err != nil || ok {
			t.Fatalf("permanent delete claim: ok=%v err=%v", ok, err)
		}
	})

	t.Run("a package extension skips fixed terms and syncs the rest", func(t *testing.T) {
		ownWindow := pgMicro(time.Now().UTC().Add(20 * 24 * time.Hour))
		coupleDays := pgMicro(time.Now().UTC().Add(24 * time.Hour))
		_, fixedID := seedSharedLifecycleWorker(t, db, owner, "bot-shared-fixed", types.CloudWorkerExpiryFixed, &ownWindow, ptrTime(ownWindow.AddDate(0, 0, 7)), "active", "")
		_, followID := seedSharedLifecycleWorker(t, db, owner, "bot-shared-follow", types.CloudWorkerExpiryFollowPlan, &coupleDays, ptrTime(coupleDays.AddDate(0, 0, 7)), "active", "")
		_ = fixedID
		_ = followID

		paidUntil := pgMicro(time.Now().UTC().Add(60 * 24 * time.Hour))
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := extendCloudWorkerLifecyclesWithPaidPeriod(tx, owner, &paidUntil, 7); err != nil {
			tx.Rollback()
			t.Fatalf("extend lifecycles: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		fixed, err := db.GetCloudWorkerBillingLifecycle("bot-shared-fixed")
		if err != nil {
			t.Fatal(err)
		}
		if !fixed.PackageExpiresAt.Equal(ownWindow) || fixed.SyncPending {
			t.Fatalf("a fixed term was moved by a package: %+v", fixed)
		}
		follow, err := db.GetCloudWorkerBillingLifecycle("bot-shared-follow")
		if err != nil {
			t.Fatal(err)
		}
		if !follow.PackageExpiresAt.Equal(paidUntil) {
			t.Fatalf("follow_plan did not follow the package: %+v", follow)
		}
		if !follow.SyncPending {
			t.Fatalf("a package-driven extension must queue a provider sync: %+v", follow)
		}
	})

	t.Run("a perpetual package opens the following worker and revives a pending one", func(t *testing.T) {
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-shared-open", types.CloudWorkerExpiryFollowPlan, ptrTime(time.Now().UTC().Add(24*time.Hour)), ptrTime(time.Now().UTC().Add(30*24*time.Hour)), "active", "")
		_, queuedID := seedSharedLifecycleWorker(t, db, owner, "bot-shared-revived", types.CloudWorkerExpiryFollowPlan, ptrTime(time.Now().UTC().Add(-2*time.Hour)), ptrTime(time.Now().UTC().Add(24*time.Hour)), "delete_pending", "")

		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := extendCloudWorkerLifecyclesWithPaidPeriod(tx, owner, nil, 7); err != nil {
			tx.Rollback()
			t.Fatalf("open lifecycles: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		item, err := db.GetCloudWorkerBillingLifecycle("bot-shared-open")
		if err != nil {
			t.Fatal(err)
		}
		if !item.PackageExpiresAt.IsZero() || !item.DeleteAfter.IsZero() {
			t.Fatalf("a perpetual package must clear the term: %+v", item)
		}
		if !item.SyncPending {
			t.Fatalf("the cleared term still has to reach the provider: %+v", item)
		}
		if item.ID != id {
			t.Fatalf("unexpected row: %+v", item)
		}
		// A worker that was already queued for deletion follows the new package
		// instead: showing it as pending would claim a paid-for machine is going
		// away.
		revived, err := db.GetCloudWorkerBillingLifecycle("bot-shared-revived")
		if err != nil {
			t.Fatal(err)
		}
		if revived.ID != queuedID || revived.State != "active" || !revived.PackageExpiresAt.IsZero() {
			t.Fatalf("a queued worker was not revived by the perpetual package: %+v", revived)
		}
		var archived sql.NullTime
		if err := db.db.QueryRow(`SELECT archived_at FROM cloud_worker_lifecycles WHERE id = $1`, queuedID).Scan(&archived); err != nil {
			t.Fatal(err)
		}
		if archived.Valid {
			t.Fatalf("a revived worker must not stay archived: %v", archived.Time)
		}
	})

	t.Run("a failed sync backs off a minute and then retries", func(t *testing.T) {
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-shared-failing", types.CloudWorkerExpiryPermanent, nil, nil, "active", "")
		// A re-term is what puts a row in the queue; a permanent grant has nothing
		// to sync with the provider in this fixture, so flag it directly.
		if err := db.setSyncPendingForTest(id); err != nil {
			t.Fatal(err)
		}
		pending, err := db.ListSharedCloudWorkerSyncPending(100)
		if err != nil {
			t.Fatal(err)
		}
		if !containsLifecycleID(pending, id) {
			t.Fatal("a freshly pending worker must be handed out immediately")
		}
		if _, ok, err := db.ClaimSharedCloudWorkerSync(id); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		if err := db.CompleteSharedCloudWorkerSync(id, nil, types.CloudWorkerExpiryPermanent, "provider timeout"); err != nil {
			t.Fatalf("complete failed sync: %v", err)
		}
		item, err := db.GetCloudWorkerBillingLifecycle("bot-shared-failing")
		if err != nil {
			t.Fatal(err)
		}
		if !item.SyncPending {
			t.Fatalf("a failed sync must stay pending: %+v", item)
		}
		// The failure just released its claim; retrying it in the same pass only
		// burns provider calls.
		pending, err = db.ListSharedCloudWorkerSyncPending(100)
		if err != nil {
			t.Fatal(err)
		}
		if containsLifecycleID(pending, id) {
			t.Fatal("a just-failed worker must not be retried within a minute")
		}
		if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET updated_at = CURRENT_TIMESTAMP - INTERVAL '2 minutes' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		pending, err = db.ListSharedCloudWorkerSyncPending(100)
		if err != nil {
			t.Fatal(err)
		}
		if !containsLifecycleID(pending, id) {
			t.Fatal("a failed worker must be retryable once the backoff has passed")
		}
		var lastError string
		if err := db.db.QueryRow(`SELECT last_error FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&lastError); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(lastError, "provider timeout") {
			t.Fatalf("the failure reason was lost: %q", lastError)
		}
	})

	t.Run("a sync claim requires a pending sync", func(t *testing.T) {
		// A legacy worker with no expiry has nothing to reconcile; claiming it
		// would mark the row pending and then "complete" a sync that never
		// happened, which is how an empty term gets cleared by accident.
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-shared-nothing-to-sync", types.CloudWorkerExpiryLegacy, nil, nil, "active", "")
		if _, ok, err := db.ClaimSharedCloudWorkerSync(id); err != nil || ok {
			t.Fatalf("a row with nothing pending must not be claimed: ok=%v err=%v", ok, err)
		}
		if item, err := db.GetCloudWorkerBillingLifecycle("bot-shared-nothing-to-sync"); err != nil {
			t.Fatal(err)
		} else if item.SyncPending || !item.PackageExpiresAt.IsZero() {
			t.Fatalf("a refused claim must not touch the row: %+v", item)
		}
		// A crashed pass leaves its claim behind; once the 30-minute window has
		// passed another pass may take it over instead of pinning the row forever.
		if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET sync_pending = TRUE, billing_action = 'sync', billing_action_started_at = CURRENT_TIMESTAMP - INTERVAL '31 minutes' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if claimed, ok, err := db.ClaimSharedCloudWorkerSync(id); err != nil || !ok || claimed == nil {
			t.Fatalf("a stale sync claim must be recoverable: %+v ok=%v err=%v", claimed, ok, err)
		}
	})

	t.Run("a stale sync completion cannot clear a newer term", func(t *testing.T) {
		oldExpiry := pgMicro(time.Now().UTC().Add(24 * time.Hour))
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-shared-raced", types.CloudWorkerExpiryFollowPlan, &oldExpiry, ptrTime(oldExpiry.AddDate(0, 0, 7)), "active", "")
		if err := db.setSyncPendingForTest(id); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := db.ClaimSharedCloudWorkerSync(id); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		// A payment lands while the provider call is in flight.
		newExpiry := pgMicro(time.Now().UTC().Add(60 * 24 * time.Hour))
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := extendCloudWorkerLifecyclesWithPaidPeriod(tx, owner, &newExpiry, 7); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		// The old provider result must not be reported as applied.
		if err := db.CompleteSharedCloudWorkerSync(id, &oldExpiry, types.CloudWorkerExpiryFollowPlan, ""); err != nil {
			t.Fatalf("complete stale sync: %v", err)
		}
		item, err := db.GetCloudWorkerBillingLifecycle("bot-shared-raced")
		if err != nil {
			t.Fatal(err)
		}
		if !item.SyncPending || !item.PackageExpiresAt.Equal(newExpiry) {
			t.Fatalf("a stale completion overwrote the newer term: %+v", item)
		}
	})

	t.Run("expiry and deletion claims respect sync and renewal", func(t *testing.T) {
		expired := pgMicro(time.Now().UTC().Add(-2 * time.Hour))
		pastRetention := pgMicro(time.Now().UTC().Add(-time.Hour))
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-shared-expiring", types.CloudWorkerExpiryLegacy, &expired, &pastRetention, "active", "")

		if ok, err := db.ClaimSharedCloudWorkerExpiry(id, time.Now().UTC()); err != nil || !ok {
			t.Fatalf("expiry claim: ok=%v err=%v", ok, err)
		}
		before, err := db.GetCloudWorkerBillingLifecycle("bot-shared-expiring")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CompleteSharedCloudWorkerExpiry(id, expired, ""); err != nil {
			t.Fatalf("complete expiry: %v", err)
		}
		after, err := db.GetCloudWorkerBillingLifecycle("bot-shared-expiring")
		if err != nil {
			t.Fatal(err)
		}
		if after.State != "delete_pending" {
			t.Fatalf("an expired worker was not scheduled: %+v", after)
		}
		if !after.DeleteAfter.Equal(before.DeleteAfter) {
			t.Fatalf("the retention window was rewritten: before=%s after=%s", before.DeleteAfter, after.DeleteAfter)
		}

		// A worker that still has to be synced is never claimed for expiry...
		_, pendingID := seedSharedLifecycleWorker(t, db, owner, "bot-shared-unsynced", types.CloudWorkerExpiryFollowPlan, &expired, &pastRetention, "active", "")
		if err := db.setSyncPendingForTest(pendingID); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.ClaimSharedCloudWorkerExpiry(pendingID, time.Now().UTC()); err != nil || ok {
			t.Fatalf("a pending sync must block the expiry claim: ok=%v err=%v", ok, err)
		}
		// ...and the automatic delete claim is blocked too.
		if ok, err := db.ClaimCloudWorkerLifecycleDeletion(pendingID); err != nil || ok {
			t.Fatalf("a pending sync must block the delete claim: ok=%v err=%v", ok, err)
		}
		if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET state='delete_pending', sync_pending=FALSE WHERE id=$1`, pendingID); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.ClaimCloudWorkerLifecycleDeletion(pendingID); err != nil || !ok {
			t.Fatalf("a synced worker must be claimable: ok=%v err=%v", ok, err)
		}

		// A renewal between the expiry claim and its completion wins.
		_, renewedID := seedSharedLifecycleWorker(t, db, owner, "bot-shared-renewed", types.CloudWorkerExpiryLegacy, &expired, &pastRetention, "active", "")
		if ok, err := db.ClaimSharedCloudWorkerExpiry(renewedID, time.Now().UTC()); err != nil || !ok {
			t.Fatalf("expiry claim: ok=%v err=%v", ok, err)
		}
		newExpiry := pgMicro(time.Now().UTC().Add(30 * 24 * time.Hour))
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := extendCloudWorkerLifecyclesWithPaidPeriod(tx, owner, &newExpiry, 7); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := db.CompleteSharedCloudWorkerExpiry(renewedID, expired, ""); err != nil {
			t.Fatalf("complete expiry after renewal: %v", err)
		}
		renewed, err := db.GetCloudWorkerBillingLifecycle("bot-shared-renewed")
		if err != nil {
			t.Fatal(err)
		}
		if renewed.State != "active" || !renewed.PackageExpiresAt.Equal(newExpiry) {
			t.Fatalf("a stale expiry completion destroyed a renewed worker: %+v", renewed)
		}
	})

	t.Run("re-term refuses anything but a live shared worker", func(t *testing.T) {
		dedicated, err := db.CreateUser(&types.User{Username: "bot-dedicated-term", AccountType: types.AccountBot, PassHash: []byte("test-only")})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveBotConfigWithOwner(dedicated, owner, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetTenantName(dedicated, "bot-dedicated-term"); err != nil {
			t.Fatal(err)
		}
		if err := db.SetCloudWorkerDeployment(dedicated, types.CloudWorkerDeployment{Profile: types.CloudWorkerPublicIP}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`INSERT INTO cloud_worker_lifecycles(worker_uid, owner_uid, tenant_name, package_expires_at, delete_after, state, expiry_mode) VALUES($1,$2,'bot-dedicated-term',CURRENT_TIMESTAMP+INTERVAL '30 days',CURRENT_TIMESTAMP+INTERVAL '37 days','active','legacy')`, dedicated, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SetSharedCloudWorkerTerm(dedicated, types.CloudWorkerExpiryPermanent, nil, 7); err == nil {
			t.Fatal("a dedicated ECS worker must not be re-termed as a shared one")
		}

		_, busyID := seedSharedLifecycleWorker(t, db, owner, "bot-shared-busy", types.CloudWorkerExpiryPermanent, nil, nil, "active", "convert")
		if _, err := db.SetSharedCloudWorkerTerm(busyID, types.CloudWorkerExpiryFollowPlan, nil, 7); err == nil {
			t.Fatal("an in-flight billing action must block a re-term")
		}

		_, badTermID := seedSharedLifecycleWorker(t, db, owner, "bot-shared-badterm", types.CloudWorkerExpiryLegacy, ptrTime(time.Now().UTC().Add(24*time.Hour)), ptrTime(time.Now().UTC().Add(31*24*time.Hour)), "active", "")
		if _, err := db.SetSharedCloudWorkerTerm(badTermID, types.CloudWorkerExpiryFixed, nil, 7); err == nil {
			t.Fatal("a fixed term without a future expiry must be refused")
		}
		if _, err := db.SetSharedCloudWorkerTerm(badTermID, types.CloudWorkerExpiryPermanent, ptrTime(time.Now().UTC().Add(24*time.Hour)), 7); err == nil {
			t.Fatal("permanent with an expiry must be refused")
		}
		if _, err := db.SetSharedCloudWorkerTerm(badTermID, "", nil, 7); err == nil {
			t.Fatal("an empty mode must be refused: legacy is not a requestable term")
		}
	})

	t.Run("a re-term never shortens a paid window", func(t *testing.T) {
		paidUntil := pgMicro(time.Now().UTC().Add(40 * 24 * time.Hour))
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-shared-guarded", types.CloudWorkerExpiryLegacy, &paidUntil, ptrTime(paidUntil.AddDate(0, 0, 7)), "active", "")

		// A fixed term earlier than the paid window would cut into time the
		// customer has already paid for. The caller resolved its target from a
		// snapshot; a payment may legitimately have extended the row since.
		shorter := pgMicro(time.Now().UTC().Add(10 * 24 * time.Hour))
		if _, err := db.SetSharedCloudWorkerTerm(id, types.CloudWorkerExpiryFixed, &shorter, 7); err == nil {
			t.Fatal("a fixed term must not shorten the paid window")
		} else if !strings.Contains(err.Error(), "shorten") {
			t.Fatalf("the refusal should say why: %v", err)
		}
		unchanged, err := db.GetCloudWorkerBillingLifecycle("bot-shared-guarded")
		if err != nil {
			t.Fatal(err)
		}
		if !unchanged.PackageExpiresAt.Equal(paidUntil) || unchanged.ExpiryMode != types.CloudWorkerExpiryLegacy || unchanged.SyncPending {
			t.Fatalf("a refused re-term must leave the row untouched: %+v", unchanged)
		}

		// A finite follow_plan target keeps the later of the two.
		if _, err := db.SetSharedCloudWorkerTerm(id, types.CloudWorkerExpiryFollowPlan, &shorter, 7); err != nil {
			t.Fatalf("follow_plan re-term: %v", err)
		}
		kept, err := db.GetCloudWorkerBillingLifecycle("bot-shared-guarded")
		if err != nil {
			t.Fatal(err)
		}
		if !kept.PackageExpiresAt.Equal(paidUntil) {
			t.Fatalf("a follow_plan re-term shortened the paid window: %+v", kept)
		}
		if !kept.SyncPending || kept.ExpiryMode != types.CloudWorkerExpiryFollowPlan {
			t.Fatalf("the follow_plan re-term was not recorded: %+v", kept)
		}

		// Moving a fixed term later is still allowed.
		later := pgMicro(paidUntil.AddDate(0, 0, 5))
		extended, err := db.SetSharedCloudWorkerTerm(id, types.CloudWorkerExpiryFixed, &later, 7)
		if err != nil {
			t.Fatalf("extending fixed term: %v", err)
		}
		if !extended.PackageExpiresAt.Equal(later) || extended.ExpiryMode != types.CloudWorkerExpiryFixed {
			t.Fatalf("a later fixed term was refused: %+v", extended)
		}
		if !extended.DeleteAfter.Equal(later.AddDate(0, 0, 7)) {
			t.Fatalf("delete_after must follow the committed window: %+v", extended)
		}
	})

	t.Run("revoking a package settles follow_plan workers on what is left", func(t *testing.T) {
		planID := seedSharedMachinePlan(t, db, "term-settle-plan", "Term settle", 30)
		_, followID := seedSharedLifecycleWorker(t, db, owner, "bot-settle-follow", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		fixedWindow := pgMicro(time.Now().UTC().Add(15 * 24 * time.Hour))
		_, fixedID := seedSharedLifecycleWorker(t, db, owner, "bot-settle-fixed", types.CloudWorkerExpiryFixed, &fixedWindow, ptrTime(fixedWindow.AddDate(0, 0, 7)), "active", "")
		_, permanentID := seedSharedLifecycleWorker(t, db, owner, "bot-settle-permanent", types.CloudWorkerExpiryPermanent, nil, nil, "active", "")

		insertEntitlement := func(expires *time.Time) int64 {
			t.Helper()
			var id int64
			if err := db.db.QueryRow(`
				INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
				VALUES($1, $2, 'order', $3, 'active', CURRENT_TIMESTAMP - INTERVAL '1 hour', $4)
				RETURNING id`, owner, planID, fmt.Sprintf("settle-%d-%t", time.Now().UnixNano(), expires != nil), expires).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		settle := func() {
			t.Helper()
			tx, err := db.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := settleSharedCloudWorkerFollowPlanTerms(tx, owner, time.Now().UTC()); err != nil {
				tx.Rollback()
				t.Fatalf("settle: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		// A revocation is what a refund and the tier upgrade both write (state,
		// not deletion), so the settlement is exercised on the same shape.
		revoke := func(id int64) {
			t.Helper()
			if _, err := db.db.Exec(`UPDATE commercial_entitlements SET state = 'revoked' WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}

		// A surviving perpetual package keeps the worker open-ended.
		perpetualEntitlement := insertEntitlement(nil)
		settle()
		follow, err := db.GetCloudWorkerBillingLifecycle("bot-settle-follow")
		if err != nil {
			t.Fatal(err)
		}
		if !follow.PackageExpiresAt.IsZero() || !follow.DeleteAfter.IsZero() {
			t.Fatalf("a perpetual surviving package must keep the worker open: %+v", follow)
		}
		if !follow.SyncPending || follow.State != "active" {
			t.Fatalf("the settlement must queue a sync: %+v", follow)
		}

		// A finite surviving package becomes the worker's term.
		revoke(perpetualEntitlement)
		finite := pgMicro(time.Now().UTC().Add(45 * 24 * time.Hour))
		finiteEntitlement := insertEntitlement(&finite)
		settle()
		follow, err = db.GetCloudWorkerBillingLifecycle("bot-settle-follow")
		if err != nil {
			t.Fatal(err)
		}
		if !follow.PackageExpiresAt.Equal(finite) || !follow.SyncPending {
			t.Fatalf("the worker did not adopt the surviving package's term: %+v", follow)
		}

		// Nothing left: expiry now, retention preserved, sync queued, still active
		// so the expiry path can claim it after the sync.
		revoke(finiteEntitlement)
		now := time.Now().UTC()
		settle()
		follow, err = db.GetCloudWorkerBillingLifecycle("bot-settle-follow")
		if err != nil {
			t.Fatal(err)
		}
		if follow.PackageExpiresAt.IsZero() || follow.PackageExpiresAt.After(now.Add(2*time.Minute)) {
			t.Fatalf("a worker with no package must expire now, not stay open: %+v", follow)
		}
		if !follow.SyncPending || follow.State != "active" {
			t.Fatalf("the settlement must leave the row active with a pending sync: %+v", follow)
		}
		if follow.DeleteAfter.IsZero() || !follow.DeleteAfter.After(follow.PackageExpiresAt) {
			t.Fatalf("the retention window must survive the settlement: %+v", follow)
		}

		// A fixed term and a permanent grant are not part of any package.
		fixed, err := db.GetCloudWorkerBillingLifecycle("bot-settle-fixed")
		if err != nil {
			t.Fatal(err)
		}
		if !fixed.PackageExpiresAt.Equal(fixedWindow) || fixed.SyncPending {
			t.Fatalf("a fixed term was rescheduled by a revocation: %+v", fixed)
		}
		permanent, err := db.GetCloudWorkerBillingLifecycle("bot-settle-permanent")
		if err != nil {
			t.Fatal(err)
		}
		if !permanent.PackageExpiresAt.IsZero() || permanent.SyncPending {
			t.Fatalf("a permanent grant was touched by a revocation: %+v", permanent)
		}
		_ = followID
		_ = fixedID
		_ = permanentID
	})

	t.Run("a perpetual shared credit registers a lifecycle row", func(t *testing.T) {
		worker, err := db.CreateUser(&types.User{Username: "bot-shared-perpetual-credit", AccountType: types.AccountBot, PassHash: []byte("test-only")})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveBotConfigWithOwner(worker, owner, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetTenantName(worker, "bot-shared-perpetual-credit"); err != nil {
			t.Fatal(err)
		}
		if err := db.SetCloudWorkerDeployment(worker, types.CloudWorkerDeployment{Profile: types.CloudWorkerPackedShared}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`
			INSERT INTO cloud_worker_credits(uid, source_ref, expires_at, deployment_profile, retention_days, runtime_plan, expiry_mode, state, reservation_ref, reserved_at)
			VALUES($1,'order:PERPETUAL-TEST',NULL,'packed_shared',7,'standard','follow_plan','reserved','perpetual-reservation',CURRENT_TIMESTAMP)`, owner); err != nil {
			t.Fatal(err)
		}
		if err := db.CommitCloudWorkerCredit(owner, "perpetual-reservation", worker, "bot-shared-perpetual-credit", 7); err != nil {
			t.Fatalf("commit perpetual credit: %v", err)
		}
		item, err := db.GetCloudWorkerBillingLifecycle("bot-shared-perpetual-credit")
		if err != nil {
			t.Fatal(err)
		}
		if !item.PackageExpiresAt.IsZero() || !item.DeleteAfter.IsZero() {
			t.Fatalf("a perpetual package must register without dates: %+v", item)
		}
		if item.ExpiryMode != types.CloudWorkerExpiryFollowPlan || item.SyncPending {
			t.Fatalf("a fresh create needs no sync but must keep its term: %+v", item)
		}
	})

	t.Run("a legacy credit without an expiry still registers nothing", func(t *testing.T) {
		worker, err := db.CreateUser(&types.User{Username: "bot-legacy-perpetual-credit", AccountType: types.AccountBot, PassHash: []byte("test-only")})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveBotConfigWithOwner(worker, owner, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetTenantName(worker, "bot-legacy-perpetual-credit"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`
			INSERT INTO cloud_worker_credits(uid, source_ref, expires_at, deployment_profile, retention_days, expiry_mode, state, reservation_ref, reserved_at)
			VALUES($1,'manual:legacy-perpetual:1',NULL,'private_nat',0,'legacy','reserved','legacy-perpetual-reservation',CURRENT_TIMESTAMP)`, owner); err != nil {
			t.Fatal(err)
		}
		if err := db.CommitCloudWorkerCredit(owner, "legacy-perpetual-reservation", worker, "bot-legacy-perpetual-credit", 15); err != nil {
			t.Fatalf("commit legacy credit: %v", err)
		}
		var count int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("a legacy credit without an expiry must keep the historical behaviour (no lifecycle row)")
		}
	})
}

// seedSharedLifecycleWorker creates a packed-shared bot with one lifecycle row.
// state/billingAction simulate a row the sweeper or another worker already owns.
func seedSharedLifecycleWorker(t *testing.T, db *Adapter, owner int64, username, mode string, expiresAt, deleteAfter *time.Time, state, billingAction string) (int64, int64) {
	t.Helper()
	worker, err := db.CreateUser(&types.User{Username: username, AccountType: types.AccountBot, PassHash: []byte("test-only")})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveBotConfigWithOwner(worker, owner, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTenantName(worker, username); err != nil {
		t.Fatal(err)
	}
	if err := db.SetCloudWorkerDeployment(worker, types.CloudWorkerDeployment{Profile: types.CloudWorkerPackedShared}); err != nil {
		t.Fatal(err)
	}
	var id int64
	// A row the sweeper (or a crashed pass) already owns carries the archive and
	// delete stamps production would have written.
	var ownedAt *time.Time
	if state == "delete_pending" {
		now := pgMicro(time.Now().UTC())
		ownedAt = &now
	}
	if err := db.db.QueryRow(`
		INSERT INTO cloud_worker_lifecycles(worker_uid, owner_uid, tenant_name, package_expires_at, delete_after, state, billing_mode, retention_days, expiry_mode, sync_pending, billing_action, archived_at, delete_started_at)
		VALUES($1,$2,$3,$4,$5,$6,'month',7,$7,FALSE,$8,$9,$9)
		RETURNING id`,
		worker, owner, username, expiresAt, deleteAfter, state, mode, billingAction, ownedAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return worker, id
}

func (a *Adapter) setSyncPendingForTest(id int64) error {
	_, err := a.db.Exec(`UPDATE cloud_worker_lifecycles SET sync_pending = TRUE WHERE id = $1`, id)
	return err
}

func containsLifecycleID(items []types.CloudWorkerLifecycle, id int64) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

// pgMicro is the precision PostgreSQL actually stores. Comparing a nanosecond
// Go timestamp against a stored one would fail for reasons unrelated to the
// behaviour under test.
func pgMicro(value time.Time) time.Time { return value.UTC().Truncate(time.Microsecond) }

func ptrTime(value time.Time) *time.Time { return &value }

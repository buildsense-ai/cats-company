package postgres

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// P1-2 of the approved credit rules (docs/credit-plan-rules-20261109.md): an
// environment, its files and its history are kept with no cleanup deadline, and
// running out of credit or letting the package lapse must never delete existing
// data.
//
// The rule is enforced through one stored fact: cloud_worker_lifecycles.retain_data.
// It is snapshotted from the credit that funded the worker (a package that sells
// model credit, its order/entitlement snapshot, or an operator action on such a
// package) and never re-derived later. It is deliberately NOT expiry_mode:
// a legacy shared package also follows its plan, and those rows keep the
// retention window they were sold with.
//
// For a retained row the paid expiry is recorded and delete_after stays NULL.
// delete_after is the only column the expiry sweep and the cluster fallback
// reaper read as permission to purge the volume, the state directory and the
// backups, so leaving it NULL is what keeps the data alive while expiry is still
// free to pause the machine. These tests pin the observable contract against a
// real database:
//
//   - a retained row is still handed to the sweep (so it can be paused),
//   - archiving it keeps delete_after NULL and nothing ever claims it,
//   - a renewal, an operator extension, a re-term or a settlement does not hand
//     it a deadline later, even when the package it followed is gone,
//   - an explicit operator deletion (which stamps delete_after itself) still
//     deletes,
//   - and a legacy row — follow_plan or not — keeps its schedule unchanged.

// markLifecycleRetained sets the stored marker on a seeded row. Seeding writes
// the raw columns; production gets the same value from CommitCloudWorkerCredit.
func markLifecycleRetained(t *testing.T, db *Adapter, id int64) {
	t.Helper()
	if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET retain_data = TRUE WHERE id = $1`, id); err != nil {
		t.Fatalf("mark lifecycle %d retained: %v", id, err)
	}
}

func lifecycleDeadline(t *testing.T, db *Adapter, id int64) *time.Time {
	t.Helper()
	var deleteAfter *time.Time
	if err := db.db.QueryRow(`SELECT delete_after FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&deleteAfter); err != nil {
		t.Fatalf("read lifecycle %d deadline: %v", id, err)
	}
	return deleteAfter
}

func TestPostgresCreditEnvironmentKeepsDataOnExpiry(t *testing.T) {
	db := commercialPolicyTestDB(t)
	owner := createGLM53MigrationUser(t, db, "credit-retention")
	now := time.Now().UTC()
	expired := pgMicro(now.Add(-time.Minute))

	t.Run("an expired credit environment is paused, never destroyed", func(t *testing.T) {
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-retain", types.CloudWorkerExpiryFollowPlan, &expired, nil, "active", "")
		markLifecycleRetained(t, db, id)

		due, err := db.ListCloudWorkerLifecycleDue(now, 100)
		if err != nil {
			t.Fatalf("list due: %v", err)
		}
		if !containsLifecycleID(due, id) {
			t.Fatal("an expired credit environment must still reach the sweep so it can be paused")
		}

		// The sweep archives with the row's own (absent) deadline.
		if err := db.MarkCloudWorkerLifecyclePending(id, time.Time{}); err != nil {
			t.Fatalf("archive expired credit environment: %v", err)
		}
		var state string
		var deleteAfter *time.Time
		if err := db.db.QueryRow(`SELECT state, delete_after FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&state, &deleteAfter); err != nil {
			t.Fatal(err)
		}
		if state != "delete_pending" || deleteAfter != nil {
			t.Fatalf("state=%q delete_after=%v: a retained environment must be archived without a cleanup deadline", state, deleteAfter)
		}

		// Even with the clock far past every plausible grace window, nothing may
		// claim it for deletion.
		claimed, err := db.ClaimCloudWorkerLifecycleDeletion(id)
		if err != nil {
			t.Fatalf("claim deletion: %v", err)
		}
		if claimed {
			t.Fatal("a credit environment with no cleanup deadline must never be claimed for deletion")
		}
		due, err = db.ListCloudWorkerLifecycleDue(now.Add(10*365*24*time.Hour), 100)
		if err != nil {
			t.Fatalf("list due later: %v", err)
		}
		if containsLifecycleID(due, id) {
			t.Fatal("a retained environment must not become due for cleanup later either")
		}
	})

	t.Run("a lapsed or revoked credit package still keeps the data", func(t *testing.T) {
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-revoked", types.CloudWorkerExpiryFollowPlan, &expired, nil, "active", "")
		markLifecycleRetained(t, db, id)
		// The owner holds no machine package at all: settlement is the "nothing
		// survived" branch a refund or a tier revocation takes.
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := settleSharedCloudWorkerFollowPlanTerms(tx, owner, now); err != nil {
			tx.Rollback()
			t.Fatalf("settle: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var expiresAt time.Time
		var deleteAfter *time.Time
		if err := db.db.QueryRow(`SELECT package_expires_at, delete_after FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&expiresAt, &deleteAfter); err != nil {
			t.Fatal(err)
		}
		if deleteAfter != nil {
			t.Fatalf("delete_after=%v: a revoked package scheduled a data deletion", deleteAfter)
		}
		if expiresAt.After(now.Add(time.Minute)) {
			t.Fatalf("expires=%v: a revoked package must expire the machine now", expiresAt)
		}
	})

	t.Run("an explicit operator deletion still deletes", func(t *testing.T) {
		// Operators delete by stamping the deadline themselves (the admin paths
		// use ForceCloudWorkerLifecyclePending). That must keep working: the rule
		// forbids automatic expiry-driven deletion, not a deliberate one. The
		// deadline is a minute in the past so a small clock difference between
		// this process and the database cannot decide the outcome.
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-explicit", types.CloudWorkerExpiryFollowPlan, &expired, nil, "active", "")
		markLifecycleRetained(t, db, id)
		if err := db.ForceCloudWorkerLifecyclePending(id, now.Add(-time.Minute)); err != nil {
			t.Fatalf("explicit delete: %v", err)
		}
		if lifecycleDeadline(t, db, id) == nil {
			t.Fatal("the explicit deletion must stamp a deadline")
		}
		claimed, err := db.ClaimCloudWorkerLifecycleDeletion(id)
		if err != nil {
			t.Fatalf("claim explicit deletion: %v", err)
		}
		if !claimed {
			t.Fatal("an explicit deletion must still be claimable on a credit environment")
		}
	})

	t.Run("a renewal or re-term does not hand out a deadline later", func(t *testing.T) {
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-renew", types.CloudWorkerExpiryFollowPlan, &expired, nil, "active", "")
		markLifecycleRetained(t, db, id)
		renewed := pgMicro(now.Add(30 * 24 * time.Hour))
		if err := db.ExtendCloudWorkerLifecycle(id, renewed, 7); err != nil {
			t.Fatalf("extend credit environment: %v", err)
		}
		var expiresAt time.Time
		var deleteAfter *time.Time
		if err := db.db.QueryRow(`SELECT package_expires_at, delete_after FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&expiresAt, &deleteAfter); err != nil {
			t.Fatal(err)
		}
		if !expiresAt.UTC().Equal(renewed) || deleteAfter != nil {
			t.Fatalf("expires=%v delete_after=%v: a renewal must extend the paid window without scheduling a cleanup", expiresAt, deleteAfter)
		}

		// The owner-wide renewal used by the payment path must agree.
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		later := pgMicro(now.Add(60 * 24 * time.Hour))
		if err := extendCloudWorkerLifecyclesWithPaidPeriod(tx, owner, &later, 7); err != nil {
			tx.Rollback()
			t.Fatalf("extend paid period: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if deleteAfter := lifecycleDeadline(t, db, id); deleteAfter != nil {
			t.Fatalf("delete_after=%v: the paid-period extension scheduled a cleanup for a credit environment", deleteAfter)
		}

		// Re-terming the same machine (operator "set term") must not add one
		// either, while a fixed term remains an explicit operator decision.
		if _, err := db.SetSharedCloudWorkerTerm(id, types.CloudWorkerExpiryFollowPlan, &later, 7); err != nil {
			t.Fatalf("re-term follow_plan: %v", err)
		}
		if deleteAfter := lifecycleDeadline(t, db, id); deleteAfter != nil {
			t.Fatalf("delete_after=%v: re-terming a credit environment scheduled a cleanup", deleteAfter)
		}
	})

	t.Run("a stale sweep cannot schedule a cleanup or archive a renewed row", func(t *testing.T) {
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-stale", types.CloudWorkerExpiryFollowPlan, &expired, nil, "active", "")
		markLifecycleRetained(t, db, id)
		// A payment lands between ListCloudWorkerLifecycleDue and the archive
		// step of a sweep that already holds the old snapshot.
		renewed := pgMicro(now.Add(30 * 24 * time.Hour))
		if err := db.ExtendCloudWorkerLifecycle(id, renewed, 7); err != nil {
			t.Fatalf("renew during sweep: %v", err)
		}
		// MarkCloudWorkerLifecyclePending re-checks the expiry under the row
		// update, so the stale archive must be a no-op rather than a rewrite.
		if err := db.MarkCloudWorkerLifecyclePending(id, time.Time{}); err != nil {
			t.Fatalf("stale archive: %v", err)
		}
		var state string
		if err := db.db.QueryRow(`SELECT state FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "active" {
			t.Fatalf("state=%q: a stale sweep archived a renewed credit environment", state)
		}
		claimed, err := db.ClaimCloudWorkerLifecycleDeletion(id)
		if err != nil {
			t.Fatalf("claim after stale sweep: %v", err)
		}
		if claimed {
			t.Fatal("a stale sweep must not make a renewed credit environment claimable")
		}
	})

	t.Run("committing a credit snapshots the marker from the credit", func(t *testing.T) {
		worker, _ := seedSharedLifecycleWorker(t, db, owner, "bot-credit-commit", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		// Drop the seeded row so the commit path is the only writer.
		if _, err := db.db.Exec(`DELETE FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker); err != nil {
			t.Fatal(err)
		}
		paidUntil := pgMicro(now.Add(30 * 24 * time.Hour))
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := grantCloudWorkerCredit(tx, owner, "order:credit-retention-test", paidUntil, types.CloudWorkerPackedShared, 7, "", types.CloudWorkerExpiryFollowPlan, true); err != nil {
			tx.Rollback()
			t.Fatalf("grant credit: %v", err)
		}
		if _, err := tx.Exec(`UPDATE cloud_worker_credits SET state = 'reserved', reservation_ref = 'res-1', reserved_at = CURRENT_TIMESTAMP WHERE source_ref = 'order:credit-retention-test'`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := db.CommitCloudWorkerCredit(owner, "res-1", worker, "bot-credit-commit", 7); err != nil {
			t.Fatalf("commit credit: %v", err)
		}
		var expiresAt time.Time
		var deleteAfter *time.Time
		var retainData bool
		if err := db.db.QueryRow(`SELECT package_expires_at, delete_after, retain_data FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker).Scan(&expiresAt, &deleteAfter, &retainData); err != nil {
			t.Fatal(err)
		}
		if !expiresAt.UTC().Equal(paidUntil) {
			t.Fatalf("expires=%v want %v: the paid window must still be recorded", expiresAt, paidUntil)
		}
		if !retainData || deleteAfter != nil {
			t.Fatalf("retain_data=%v delete_after=%v: a credit purchase must keep the environment without scheduling a deletion", retainData, deleteAfter)
		}
	})

	t.Run("a legacy credit keeps the documented retention schedule", func(t *testing.T) {
		worker, _ := seedSharedLifecycleWorker(t, db, owner, "bot-legacy-commit", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
		if _, err := db.db.Exec(`DELETE FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker); err != nil {
			t.Fatal(err)
		}
		paidUntil := pgMicro(now.Add(30 * 24 * time.Hour))
		tx, err := db.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		// No marker: a legacy package's credit, including a shared one that
		// follows its plan (empty mode keeps the historical dedicated-ECS shape).
		if err := grantCloudWorkerCredit(tx, owner, "order:legacy-retention-test", paidUntil, types.CloudWorkerPrivateNAT, 7, "", "", false); err != nil {
			tx.Rollback()
			t.Fatalf("grant legacy credit: %v", err)
		}
		if _, err := tx.Exec(`UPDATE cloud_worker_credits SET state = 'reserved', reservation_ref = 'res-2', reserved_at = CURRENT_TIMESTAMP WHERE source_ref = 'order:legacy-retention-test'`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := db.CommitCloudWorkerCredit(owner, "res-2", worker, "bot-legacy-commit", 7); err != nil {
			t.Fatalf("commit legacy credit: %v", err)
		}
		var deleteAfter *time.Time
		var retainData bool
		if err := db.db.QueryRow(`SELECT delete_after, retain_data FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker).Scan(&deleteAfter, &retainData); err != nil {
			t.Fatal(err)
		}
		if retainData {
			t.Fatal("a legacy credit must not be marked as a retained credit environment")
		}
		if deleteAfter == nil || !deleteAfter.UTC().Equal(pgMicro(paidUntil.AddDate(0, 0, 7))) {
			t.Fatalf("delete_after=%v: the legacy retention schedule must be unchanged", deleteAfter)
		}
	})

	t.Run("a legacy follow_plan row keeps its retention schedule", func(t *testing.T) {
		// This is the regression the marker exists for: a shared worker that
		// follows an old shared package is not a credit environment, so it keeps
		// the window it was sold with instead of being retained forever.
		retention := pgMicro(expired.AddDate(0, 0, 7))
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-legacy-follow", types.CloudWorkerExpiryFollowPlan, &expired, &retention, "active", "")
		if err := db.MarkCloudWorkerLifecyclePending(id, retention); err != nil {
			t.Fatalf("archive legacy row: %v", err)
		}
		if deadline := lifecycleDeadline(t, db, id); deadline == nil {
			t.Fatal("a legacy follow_plan row must keep its cleanup deadline")
		}
		// Its deadline is still in the future, so it is not claimable yet.
		if claimed, err := db.ClaimCloudWorkerLifecycleDeletion(id); err != nil || claimed {
			t.Fatalf("claimed=%v err=%v: the legacy deadline has not passed yet", claimed, err)
		}
		if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET delete_after = CURRENT_TIMESTAMP - INTERVAL '1 minute' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if claimed, err := db.ClaimCloudWorkerLifecycleDeletion(id); err != nil || !claimed {
			t.Fatalf("claimed=%v err=%v: the legacy cleanup path must keep working", claimed, err)
		}
	})

	t.Run("a legacy dedicated lifecycle keeps its retention schedule", func(t *testing.T) {
		retention := pgMicro(now.Add(-time.Minute))
		_, id := seedSharedLifecycleWorker(t, db, owner, "bot-legacy-schedule", types.CloudWorkerExpiryLegacy, &expired, &retention, "delete_pending", "")
		claimed, err := db.ClaimCloudWorkerLifecycleDeletion(id)
		if err != nil {
			t.Fatalf("claim legacy deletion: %v", err)
		}
		if !claimed {
			t.Fatal("the legacy retention schedule must keep working unchanged")
		}
	})
}

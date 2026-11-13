package postgres

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// The two follow-ups to P1-2 that the retention marker has to survive: a later
// credit on the same machine must not withdraw it, and an operator operation that
// adopts an existing machine must carry it onto that machine.

// pgMicroPtr is the seeded-lifecycle form of pgMicro.
func pgMicroPtr(value time.Time) *time.Time {
	micro := pgMicro(value)
	return &micro
}

// reserveLifecycleCredit grants one credit and marks it reserved, so the commit
// path can consume it exactly as the create flow does.
func reserveLifecycleCredit(t *testing.T, db *Adapter, owner int64, sourceRef string, expiresAt time.Time, retain bool, reservation string) {
	t.Helper()
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := grantCloudWorkerCredit(tx, owner, sourceRef, expiresAt, types.CloudWorkerPackedShared, 7, "", types.CloudWorkerExpiryFollowPlan, retain); err != nil {
		tx.Rollback()
		t.Fatalf("grant credit %s: %v", sourceRef, err)
	}
	if _, err := tx.Exec(`UPDATE cloud_worker_credits SET state = 'reserved', reservation_ref = $2, reserved_at = CURRENT_TIMESTAMP WHERE source_ref = $1`, sourceRef, reservation); err != nil {
		tx.Rollback()
		t.Fatalf("reserve credit %s: %v", sourceRef, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The marker is a promise already made on a machine: a later credit that does not
// carry it (a legacy or operator-issued purchase) must neither withdraw it nor
// schedule the deletion it forbids. Explicit deletion still works.
func TestPostgresCreditEnvironmentMarkerIsStickyAcrossCredits(t *testing.T) {
	db := commercialPolicyTestDB(t)
	owner := createGLM53MigrationUser(t, db, "credit-sticky")
	now := time.Now().UTC()

	worker, _ := seedSharedLifecycleWorker(t, db, owner, "bot-credit-sticky", types.CloudWorkerExpiryFollowPlan, nil, nil, "active", "")
	if _, err := db.db.Exec(`DELETE FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker); err != nil {
		t.Fatal(err)
	}

	// The purchase that makes this machine a credit environment.
	first := pgMicro(now.Add(30 * 24 * time.Hour))
	reserveLifecycleCredit(t, db, owner, "order:sticky-credit", first, true, "res-sticky-1")
	if err := db.CommitCloudWorkerCredit(owner, "res-sticky-1", worker, "bot-credit-sticky", 7); err != nil {
		t.Fatalf("commit credit: %v", err)
	}
	var id int64
	var retainData bool
	if err := db.db.QueryRow(`SELECT id, retain_data FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker).Scan(&id, &retainData); err != nil {
		t.Fatal(err)
	}
	if !retainData || lifecycleDeadline(t, db, id) != nil {
		t.Fatalf("retain_data=%v: the credit purchase must keep the environment", retainData)
	}

	// A later legacy credit on the same machine: same machine, new window, and a
	// marker of its own that says "no". It must not win.
	second := pgMicro(now.Add(60 * 24 * time.Hour))
	reserveLifecycleCredit(t, db, owner, "order:sticky-legacy", second, false, "res-sticky-2")
	if err := db.CommitCloudWorkerCredit(owner, "res-sticky-2", worker, "bot-credit-sticky", 7); err != nil {
		t.Fatalf("commit legacy credit: %v", err)
	}
	var expiresAt time.Time
	if err := db.db.QueryRow(`SELECT package_expires_at, retain_data FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker).Scan(&expiresAt, &retainData); err != nil {
		t.Fatal(err)
	}
	if !expiresAt.UTC().Equal(second) {
		t.Fatalf("expires=%v want %v: the new paid window must still be recorded", expiresAt, second)
	}
	if !retainData {
		t.Fatal("a legacy credit must not withdraw the retention promise already made on this machine")
	}
	if deadline := lifecycleDeadline(t, db, id); deadline != nil {
		t.Fatalf("delete_after=%v: a legacy credit re-scheduled a deletion for a retained environment", deadline)
	}

	// An explicit operator deletion is still effective.
	if err := db.ForceCloudWorkerLifecyclePending(id, now.Add(-time.Minute)); err != nil {
		t.Fatalf("explicit delete: %v", err)
	}
	if claimed, err := db.ClaimCloudWorkerLifecycleDeletion(id); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v: an explicit deletion must still be claimable", claimed, err)
	}
}

// insertOperationCredit seeds the durable credit a commercial operation carries,
// exactly as commercial_actions.go writes it.
func insertOperationCredit(t *testing.T, db *Adapter, uid int64, source string, expiresAt time.Time, retain bool) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO cloud_worker_credits(uid, source_ref, expires_at, deployment_profile, billing_mode, retention_days, runtime_plan, expiry_mode, retain_data)
		VALUES($1, $2, $3, 'packed_shared', 'month', 7, '', 'follow_plan', $4)`, uid, source, expiresAt, retain); err != nil {
		t.Fatalf("insert operation credit %s: %v", source, err)
	}
}

// An operator operation that adopts an existing machine ("ensure") must carry the
// credit's retention intent onto that machine — and only onto that machine. A
// machine that is not this owner's, has no lifecycle row, or is already being
// destroyed makes the call fail, so the operation is never reported as kept.
func TestPostgresOperationKeepCarriesCreditRetention(t *testing.T) {
	db := commercialPolicyTestDB(t)
	owner := createGLM53MigrationUser(t, db, "credit-keep")
	stranger := createGLM53MigrationUser(t, db, "credit-keep-stranger")
	now := time.Now().UTC()

	t.Run("adoption carries the marker, clears the deadline and queues the sync", func(t *testing.T) {
		// An adopted machine whose automatic deadline is already set (the package
		// lapsed and the sweeper archived it): adopting it must clear that
		// deadline, not preserve it.
		worker, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-keep", types.CloudWorkerExpiryFollowPlan,
			pgMicroPtr(now.Add(-time.Minute)), pgMicroPtr(now.Add(24*time.Hour)), "delete_pending", "")
		source := types.CommercialActionSource(owner, "op-keep-1")
		insertOperationCredit(t, db, owner, source, pgMicro(now.Add(30*24*time.Hour)), true)

		if err := db.KeepCommercialOperationWorker(owner, source, worker); err != nil {
			t.Fatalf("keep adopted worker: %v", err)
		}
		var retainData, syncPending bool
		var state, creditState string
		var completed bool
		if err := db.db.QueryRow(`SELECT retain_data, sync_pending, state FROM cloud_worker_lifecycles WHERE id = $1`, id).
			Scan(&retainData, &syncPending, &state); err != nil {
			t.Fatal(err)
		}
		if !retainData || !syncPending || state != "active" {
			t.Fatalf("retain=%v sync=%v state=%q: the adopted machine must be retained, revived and queued for sync", retainData, syncPending, state)
		}
		if deadline := lifecycleDeadline(t, db, id); deadline != nil {
			t.Fatalf("delete_after=%v: the adopted credit environment still has an automatic deletion deadline", deadline)
		}
		if err := db.db.QueryRow(`SELECT state, operation_completed FROM cloud_worker_credits WHERE uid = $1 AND source_ref = $2`, owner, source).
			Scan(&creditState, &completed); err != nil {
			t.Fatal(err)
		}
		if creditState != "revoked" || !completed {
			t.Fatalf("state=%q completed=%v: the adopted credit must be closed out", creditState, completed)
		}
		// The pending sync is what pushes the marker to the cluster; the platform
		// reads RetainData when it builds that call.
		items, err := db.ListCloudWorkerLifecycles(owner)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range items {
			if item.ID == id {
				found = item.RetainData
			}
		}
		if !found {
			t.Fatal("the lifecycle reader must report the adopted machine as retained")
		}
	})

	t.Run("a machine that is not this owner's is never touched", func(t *testing.T) {
		worker, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-keep-stranger", types.CloudWorkerExpiryFollowPlan,
			pgMicroPtr(now.Add(-time.Minute)), pgMicroPtr(now.Add(24*time.Hour)), "active", "")
		source := types.CommercialActionSource(stranger, "op-keep-stranger")
		insertOperationCredit(t, db, stranger, source, pgMicro(now.Add(30*24*time.Hour)), true)

		if err := db.KeepCommercialOperationWorker(stranger, source, worker); err == nil {
			t.Fatal("adopting another owner's machine must fail")
		}
		// Nothing was written: the credit is still spendable and the machine still
		// carries the deadline it had.
		var creditState string
		var retainData bool
		if err := db.db.QueryRow(`SELECT state FROM cloud_worker_credits WHERE uid = $1 AND source_ref = $2`, stranger, source).Scan(&creditState); err != nil {
			t.Fatal(err)
		}
		if creditState != "available" {
			t.Fatalf("credit state=%q: a refused adoption must not consume the credit", creditState)
		}
		if err := db.db.QueryRow(`SELECT retain_data FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&retainData); err != nil {
			t.Fatal(err)
		}
		if retainData {
			t.Fatal("a refused adoption stamped the marker on the machine anyway")
		}
	})

	t.Run("a machine without a lifecycle row cannot be adopted as retained", func(t *testing.T) {
		worker, _ := seedSharedLifecycleWorker(t, db, owner, "bot-credit-keep-norow", types.CloudWorkerExpiryFollowPlan,
			pgMicroPtr(now.Add(-time.Minute)), pgMicroPtr(now.Add(24*time.Hour)), "active", "")
		if _, err := db.db.Exec(`DELETE FROM cloud_worker_lifecycles WHERE worker_uid = $1`, worker); err != nil {
			t.Fatal(err)
		}
		source := types.CommercialActionSource(owner, "op-keep-norow")
		insertOperationCredit(t, db, owner, source, pgMicro(now.Add(30*24*time.Hour)), true)
		if err := db.KeepCommercialOperationWorker(owner, source, worker); err == nil {
			t.Fatal("a machine without a lifecycle row cannot be recorded as retained")
		}
		var creditState string
		if err := db.db.QueryRow(`SELECT state FROM cloud_worker_credits WHERE uid = $1 AND source_ref = $2`, owner, source).Scan(&creditState); err != nil {
			t.Fatal(err)
		}
		if creditState != "available" {
			t.Fatalf("credit state=%q: a refused adoption must not consume the credit", creditState)
		}
	})

	t.Run("a destroy already in flight blocks the adoption", func(t *testing.T) {
		worker, id := seedSharedLifecycleWorker(t, db, owner, "bot-credit-keep-deleting", types.CloudWorkerExpiryFollowPlan,
			pgMicroPtr(now.Add(-time.Minute)), pgMicroPtr(now.Add(-time.Minute)), "delete_running", "")
		source := types.CommercialActionSource(owner, "op-keep-deleting")
		insertOperationCredit(t, db, owner, source, pgMicro(now.Add(30*24*time.Hour)), true)
		if err := db.KeepCommercialOperationWorker(owner, source, worker); err == nil {
			t.Fatal("adopting a machine whose destroy is in flight must fail")
		}
		var retainData bool
		if err := db.db.QueryRow(`SELECT retain_data FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&retainData); err != nil {
			t.Fatal(err)
		}
		if retainData {
			t.Fatal("a refused adoption stamped the marker on a machine that is being destroyed")
		}
	})

	t.Run("a legacy operation credit keeps the schedule it always had", func(t *testing.T) {
		deadline := pgMicro(now.Add(24 * time.Hour))
		worker, id := seedSharedLifecycleWorker(t, db, owner, "bot-legacy-keep", types.CloudWorkerExpiryFollowPlan,
			pgMicroPtr(now.Add(-time.Minute)), &deadline, "active", "")
		source := types.CommercialActionSource(owner, "op-keep-legacy")
		insertOperationCredit(t, db, owner, source, pgMicro(now.Add(30*24*time.Hour)), false)
		if err := db.KeepCommercialOperationWorker(owner, source, worker); err != nil {
			t.Fatalf("keep legacy worker: %v", err)
		}
		var retainData bool
		if err := db.db.QueryRow(`SELECT retain_data FROM cloud_worker_lifecycles WHERE id = $1`, id).Scan(&retainData); err != nil {
			t.Fatal(err)
		}
		if retainData {
			t.Fatal("a legacy operation must not turn an existing machine into a retained environment")
		}
		if got := lifecycleDeadline(t, db, id); got == nil || !got.UTC().Equal(deadline) {
			t.Fatalf("delete_after=%v want %v: a legacy operation must not change the schedule", got, deadline)
		}
	})
}

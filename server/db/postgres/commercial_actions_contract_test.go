package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// These contracts use the same isolated-schema fixture as the other commercial
// tests. They never run unless CATS_PG_TEST_DSN is explicitly supplied.
func TestPostgresCommercialActionsContract(t *testing.T) {
	db := commercialPolicyTestDB(t)
	plan := seedGLM53MigrationPlan(t, db, "ops-actions", "Operations", `{"model":12}`)
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if _, err := db.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE commercial_plans SET price_fen=0,cloud_worker_deployment_profile='packed_shared',cloud_worker_runtime_plan='pro' WHERE id=$1`, plan)
	count := func(query string, args ...interface{}) int {
		t.Helper()
		var n int
		if err := db.db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	preview := func(r types.CommercialActionRequest) types.CommercialActionRequest {
		t.Helper()
		r.Preview = true
		p, err := db.PreviewCommercialAction(r)
		if err != nil || !p.CanApply {
			t.Fatalf("preview: %+v %v", p, err)
		}
		r.Preview, r.PreviewToken = false, p.PreviewToken
		return r
	}
	apply := func(r types.CommercialActionRequest) *types.CommercialAction {
		t.Helper()
		v, err := db.ApplyCommercialAction(r)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	expectCode := func(err error, code string) {
		t.Helper()
		var e *types.CommercialAdjustmentError
		if !errors.As(err, &e) || e.Code != code {
			t.Fatalf("want %s, got %v", code, err)
		}
	}

	t.Run("atomic rollback and durable replay", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-rollback")
		r := preview(types.CommercialActionRequest{UID: uid, Intent: "grant_plan", PlanID: plan, WorkerAction: "additional", Note: "first package", OperationID: "rollback"})
		if count(`SELECT COUNT(*) FROM commercial_ops_actions WHERE uid=$1`, uid) != 0 || count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1`, uid) != 0 {
			t.Fatal("preview wrote state")
		}
		exec(`ALTER TABLE commercial_ops_actions ADD CONSTRAINT ops_test_rollback CHECK(operation_id<>'rollback')`)
		if _, err := db.ApplyCommercialAction(r); err == nil {
			t.Fatal("injected journal failure did not fail")
		}
		exec(`ALTER TABLE commercial_ops_actions DROP CONSTRAINT ops_test_rollback`)
		for _, table := range []string{"commercial_entitlements", "commercial_quota_grants", "commercial_quota_ledger", "cloud_worker_credits", "commercial_ops_actions"} {
			if count(`SELECT COUNT(*) FROM `+table+` WHERE uid=$1`, uid) != 0 {
				t.Fatalf("%s survived rollback", table)
			}
		}
		first := apply(r)
		if !first.Operation.PackageApplied || first.Result == nil || !first.Result.Applied || first.Operation.WorkerStatus != "pending" || first.Result.NextTotalCNY != 12 {
			t.Fatalf("first grant: %+v", first)
		}
		// A fresh adapter reads only persisted request/result state. Replay must
		// succeed even though the original preview is now invalid/expired.
		restarted := &Adapter{db: db.db}
		r.PreviewToken = "expired-token"
		replay, err := restarted.ApplyCommercialAction(r)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(first)
		b, _ := json.Marshal(replay)
		if string(a) != string(b) {
			t.Fatalf("unstable durable result: %s != %s", a, b)
		}
		for _, change := range []func(*types.CommercialActionRequest){
			func(v *types.CommercialActionRequest) { v.PlanID++ },
			func(v *types.CommercialActionRequest) { v.WorkerAction = "ensure" },
			func(v *types.CommercialActionRequest) { v.Note = "changed reason" },
			func(v *types.CommercialActionRequest) { v.Intent = "renew_plan"; v.DurationDays = 5 },
		} {
			changed := r
			change(&changed)
			_, err = restarted.ApplyCommercialAction(changed)
			expectCode(err, "operation_conflict")
		}
		if count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1`, uid) != 1 || count(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid=$1`, uid) != 1 || count(`SELECT COUNT(*) FROM commercial_ops_actions WHERE uid=$1`, uid) != 1 {
			t.Fatal("replay duplicated journal/package/credit")
		}
	})

	t.Run("concurrent identical apply", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-concurrent")
		r := preview(types.CommercialActionRequest{UID: uid, Intent: "grant_plan", PlanID: plan, WorkerAction: "ensure", Note: "concurrent", OperationID: "one"})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := db.ApplyCommercialAction(r); errs <- err }()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range []string{"commercial_ops_actions", "commercial_entitlements", "cloud_worker_credits"} {
			if count(`SELECT COUNT(*) FROM `+table+` WHERE uid=$1`, uid) != 1 {
				t.Fatalf("concurrent apply duplicated %s", table)
			}
		}
	})

	t.Run("finite internal primary renewal and legacy", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-renew")
		first := apply(preview(types.CommercialActionRequest{UID: uid, Intent: "grant_plan", PlanID: plan, Note: "internal", OperationID: "grant"}))
		old := *first.Result.ExpiresAt
		legacy := seedGLM53MigrationPlan(t, db, commercialLegacyPlanSlug, "Legacy", `{"model":20}`)
		exec(`UPDATE commercial_plans SET duration_days=-1,price_fen=0,sale_state='hidden' WHERE id=$1`, legacy)
		exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at) VALUES($1,$2,'legacy','ops-legacy','active',$3,$4)`, uid, legacy, time.Now().Add(-time.Hour), old.AddDate(0, 0, 300))
		r := preview(types.CommercialActionRequest{UID: uid, Intent: "renew_plan", DurationDays: 7, Note: "internal renewal", OperationID: "renew"})
		v := apply(r)
		if !v.Result.ExpiresAt.Equal(old.AddDate(0, 0, 7)) || v.Preview.CurrentPlan.ID != plan || v.Result.NextTotalCNY != 12 {
			t.Fatalf("renewed wrong entitlement: %+v", v)
		}
		changed := r
		changed.DurationDays = 8
		_, err := db.ApplyCommercialAction(changed)
		expectCode(err, "operation_conflict")
		v = apply(preview(types.CommercialActionRequest{UID: uid, Intent: "renew_plan", Note: "next period", OperationID: "renew-again"}))
		if !v.Result.ExpiresAt.Equal(old.AddDate(0, 0, 37)) {
			t.Fatal("did not chain latest segment of primary plan")
		}
		if count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1`, uid) != 0 {
			t.Fatal("package-only renewal minted worker credits")
		}
		_, err = db.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{UID: uid, Action: "extend", OperationID: "old-path"})
		expectCode(err, "unsupported_plan")
		legacyUID := createGLM53MigrationUser(t, db, "ops-finite-legacy")
		finite := time.Now().UTC().Truncate(time.Microsecond).AddDate(0, 0, 10)
		exec(`INSERT INTO commercial_entitlements(uid,plan_id,source,source_ref,state,starts_at,expires_at) VALUES($1,$2,'legacy','finite','active',$3,$4)`, legacyUID, legacy, time.Now().Add(-time.Hour), finite)
		v = apply(preview(types.CommercialActionRequest{UID: legacyUID, Intent: "renew_plan", DurationDays: 4, Note: "finite legacy", OperationID: "legacy"}))
		if !v.Result.ExpiresAt.Equal(finite.AddDate(0, 0, 4)) {
			t.Fatal("finite entitlement of permanent legacy plan did not renew")
		}
	})

	t.Run("snapshot guard rejects all preview changes", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-stale")
		apply(preview(types.CommercialActionRequest{UID: uid, Intent: "grant_plan", PlanID: plan, Note: "seed", OperationID: "seed"}))
		mutations := []func(){
			func() { exec(`UPDATE commercial_plans SET model_budgets='{"model":14}' WHERE id=$1`, plan) },
			func() {
				exec(`UPDATE commercial_entitlements SET expires_at=expires_at+INTERVAL '1 day' WHERE uid=$1`, uid)
			},
			func() { exec(`UPDATE commercial_quota_grants SET amount_cny=amount_cny+1 WHERE uid=$1`, uid) },
			func() { exec(`UPDATE commercial_entitlements SET source_ref='new-window-identity' WHERE uid=$1`, uid) },
			func() {
				worker := createGLM53MigrationUser(t, db, "ops-stale-worker")
				exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name) VALUES($1,$2,'existing')`, worker, uid)
			},
		}
		for i, mutate := range mutations {
			r := preview(types.CommercialActionRequest{UID: uid, Intent: "renew_plan", WorkerAction: "ensure", Note: "guard", OperationID: fmt.Sprintf("stale-%d", i)})
			mutate()
			_, err := db.ApplyCommercialAction(r)
			expectCode(err, "stale_preview")
		}
		if count(`SELECT COUNT(*) FROM commercial_ops_actions WHERE uid=$1`, uid) != 1 || count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1`, uid) != 0 {
			t.Fatal("stale preview partially committed")
		}
	})

	t.Run("operation credit expiry isolation queue and recovery", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-credit")
		r := preview(types.CommercialActionRequest{UID: uid, Intent: "test_worker", DurationDays: 2, DeploymentProfile: "packed_shared", RuntimePlan: "pro", Note: "machine only", OperationID: "credit"})
		v := apply(r)
		source := types.CommercialActionSource(uid, r.OperationID)
		if v.Operation.PackageApplied || count(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid=$1`, uid) != 0 || count(`SELECT COUNT(*) FROM commercial_quota_grants WHERE uid=$1`, uid) != 0 {
			t.Fatal("test_worker mutated package")
		}
		if total, available, err := db.CloudWorkerCreditSummary(uid); err != nil || total != 0 || available != 0 {
			t.Fatalf("operation credit visible to generic automation: %d %d %v", total, available, err)
		}
		if total, available, err := db.CloudWorkerRuntimeCreditSummary(uid, "packed_shared", "month", "pro"); err != nil || total != 0 || available != 0 {
			t.Fatalf("operation credit visible in configured summary: %d %d %v", total, available, err)
		}
		if ok, err := db.ReserveCloudWorkerCredit(uid, "generic"); err != nil || ok {
			t.Fatalf("generic reserve consumed operation credit: %v %v", ok, err)
		}
		if _, ok, err := db.ReserveCloudWorkerRuntimeCredit(uid, "generic-tier", "packed_shared", "month", "pro"); err != nil || ok {
			t.Fatalf("configured generic reserve consumed operation credit: %v %v", ok, err)
		}
		// An operation's private credit is not a paid renewal's existing worker.
		for _, state := range []string{"available", "reserved"} {
			exec(`UPDATE cloud_worker_credits SET state=$2 WHERE source_ref=$1`, source, state)
			tx, err := db.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			survives, err := cloudWorkerSurvivesRenewal(tx, uid, types.CloudWorkerPackedShared)
			_ = tx.Rollback()
			if err != nil || survives {
				t.Fatalf("private %s credit suppressed paid renewal credit: %v %v", state, survives, err)
			}
		}

		t.Run("dedicated and shared machine entitlements do not block each other", func(t *testing.T) {
			owner := createGLM53MigrationUser(t, db, "ops-profile-owner")
			dedicated := createGLM53MigrationUser(t, db, "ops-profile-dedicated")
			exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name,cloud_deployment) VALUES($1,$2,'dedicated-old','{"profile":"public_ip"}'::jsonb)`, dedicated, owner)
			exec(`INSERT INTO cloud_worker_lifecycles(worker_uid,owner_uid,tenant_name,package_expires_at,delete_after,state) VALUES($1,$2,'dedicated-old',CURRENT_TIMESTAMP+INTERVAL '30 days',CURRENT_TIMESTAMP+INTERVAL '37 days','active')`, dedicated, owner)
			check := func(profile string, want bool) {
				t.Helper()
				tx, err := db.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				got, err := cloudWorkerSurvivesRenewal(tx, owner, profile)
				_ = tx.Rollback()
				if err != nil || got != want {
					t.Fatalf("profile %s survives=%v want %v: %v", profile, got, want, err)
				}
			}
			check(types.CloudWorkerPackedShared, false)
			check(types.CloudWorkerPublicIP, true)
			exec(`UPDATE bot_config SET cloud_deployment='{"profile":"packed_shared"}'::jsonb WHERE user_id=$1`, dedicated)
			check(types.CloudWorkerPackedShared, true)
			check(types.CloudWorkerPublicIP, false)
		})
		t.Run("paid period extends only its deployment profile", func(t *testing.T) {
			owner := createGLM53MigrationUser(t, db, "ops-lifecycle-profile-owner")
			dedicated := createGLM53MigrationUser(t, db, "ops-lifecycle-dedicated")
			shared := createGLM53MigrationUser(t, db, "ops-lifecycle-shared")
			exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name,cloud_deployment) VALUES
				($1,$3,'dedicated-term','{"profile":"public_ip"}'::jsonb),
				($2,$3,'shared-term','{"profile":"packed_shared"}'::jsonb)`, dedicated, shared, owner)
			exec(`INSERT INTO cloud_worker_lifecycles(worker_uid,owner_uid,tenant_name,package_expires_at,delete_after,state,expiry_mode) VALUES
				($1,$3,'dedicated-term',CURRENT_TIMESTAMP+INTERVAL '30 days',CURRENT_TIMESTAMP+INTERVAL '37 days','active','legacy'),
				($2,$3,'shared-term',CURRENT_TIMESTAMP+INTERVAL '30 days',CURRENT_TIMESTAMP+INTERVAL '37 days','active','legacy')`, dedicated, shared, owner)
			var dedicatedBefore, sharedBefore time.Time
			if err := db.db.QueryRow(`SELECT package_expires_at FROM cloud_worker_lifecycles WHERE worker_uid=$1`, dedicated).Scan(&dedicatedBefore); err != nil {
				t.Fatal(err)
			}
			if err := db.db.QueryRow(`SELECT package_expires_at FROM cloud_worker_lifecycles WHERE worker_uid=$1`, shared).Scan(&sharedBefore); err != nil {
				t.Fatal(err)
			}
			tx, err := db.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			paidUntil := time.Now().UTC().AddDate(0, 0, 60)
			if err := extendCloudWorkerLifecyclesWithPaidPeriod(tx, owner, &paidUntil, 7, types.CloudWorkerPackedShared); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var dedicatedAfter, sharedAfter time.Time
			if err := db.db.QueryRow(`SELECT package_expires_at FROM cloud_worker_lifecycles WHERE worker_uid=$1`, dedicated).Scan(&dedicatedAfter); err != nil {
				t.Fatal(err)
			}
			if err := db.db.QueryRow(`SELECT package_expires_at FROM cloud_worker_lifecycles WHERE worker_uid=$1`, shared).Scan(&sharedAfter); err != nil {
				t.Fatal(err)
			}
			if !dedicatedAfter.Equal(dedicatedBefore) || !sharedAfter.After(sharedBefore) {
				t.Fatalf("wrong lifecycle rows extended: dedicated %s -> %s, shared %s -> %s", dedicatedBefore, dedicatedAfter, sharedBefore, sharedAfter)
			}
		})

		exec(`UPDATE cloud_worker_credits SET state='available' WHERE source_ref=$1`, source)
		wrong := v.Preview
		expires := wrong.WorkerExpiresAt.Add(time.Second)
		wrong.WorkerExpiresAt = &expires
		if _, ok, err := db.ReserveCommercialOperationCredit(uid, source, source, wrong); err != nil || ok {
			t.Fatalf("mismatched expiry reserved: %v %v", ok, err)
		}
		wrong = v.Preview
		wrong.RuntimePlan = "standard"
		if _, ok, err := db.ReserveCommercialOperationCredit(uid, source, source, wrong); err != nil || ok {
			t.Fatalf("mismatched tier reserved: %v %v", ok, err)
		}
		var wg sync.WaitGroup
		results := make(chan bool, 2)
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				selected, ok, err := db.ReserveCommercialOperationCredit(uid, source, source, v.Preview)
				if ok && (selected.ExpiresAt == nil || !selected.ExpiresAt.Equal(*v.Preview.WorkerExpiresAt) || selected.RuntimePlan != "pro") {
					err = fmt.Errorf("wrong operation selection: %+v", selected)
				}
				results <- ok
				errs <- err
			}()
		}
		wg.Wait()
		if n := boolInt(<-results) + boolInt(<-results); n != 1 {
			t.Fatalf("reserved %d times", n)
		}
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if ok, err := db.QueueCloudWorkerCredit(uid, source); err != nil || !ok {
			t.Fatalf("queue: %v %v", ok, err)
		}
		exec(`UPDATE cloud_worker_credits SET queued_at=CURRENT_TIMESTAMP-INTERVAL '1 hour' WHERE source_ref=$1`, source)
		if position, err := db.CloudWorkerQueuePosition(uid); err != nil || position != -1 {
			t.Fatalf("operation exposed as generic queue: %d %v", position, err)
		}
		buyer := createGLM53MigrationUser(t, db, "ops-queue-buyer")
		exec(`INSERT INTO cloud_worker_credits(uid,source_ref,state,queued_at) VALUES($1,'order:queue-buyer','queued',CURRENT_TIMESTAMP)`, buyer)
		if position, err := db.CloudWorkerQueuePosition(buyer); err != nil || position != 0 {
			t.Fatalf("private operation inflated buyer queue: %d %v", position, err)
		}
		exec(`DELETE FROM cloud_worker_credits WHERE uid=$1`, buyer)
		if list, err := db.ListQueuedCloudWorkerCredits(0, 100); err != nil || len(list) != 0 {
			t.Fatalf("generic queued sweep saw operation: %+v %v", list, err)
		}
		if ok, err := db.ClaimQueuedCloudWorkerCredit(uid); err != nil || ok {
			t.Fatalf("generic queue revived operation: %v %v", ok, err)
		}
		if ok, err := db.ClaimQueuedCloudWorkerRuntimeCredit(uid, "pro"); err != nil || ok {
			t.Fatalf("generic runtime queue revived operation: %v %v", ok, err)
		}
		if _, ok, err := db.ReserveCommercialOperationCredit(uid, source, source, v.Preview); err != nil || !ok {
			t.Fatalf("durable queue recovery: %v %v", ok, err)
		}
		exec(`UPDATE cloud_worker_credits SET reserved_at=CURRENT_TIMESTAMP-INTERVAL '36 minutes' WHERE source_ref=$1`, source)
		if ok, err := db.ReserveCloudWorkerCredit(uid, "stale-generic"); err != nil || ok {
			t.Fatalf("generic stale recovery stole operation: %v %v", ok, err)
		}
		if _, ok, err := db.ReserveCommercialOperationCredit(uid, source, source, v.Preview); err != nil || !ok {
			t.Fatalf("durable stale reservation recovery: %v %v", ok, err)
		}
		worker, err := db.CreateUser(&types.User{Username: types.CommercialActionUsername(uid, r.OperationID), AccountType: types.AccountBot, PassHash: []byte("test")})
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name) VALUES($1,$2,'ops-worker')`, worker, uid)
		if _, _, err = db.ReconcileCommercialOperationWorker(uid, r.OperationID); err == nil {
			t.Fatal("tenant record falsely confirmed provisioning")
		}
		if err = db.CommitCloudWorkerCredit(uid, source, worker, "ops-worker", 15); err != nil {
			t.Fatal(err)
		}
		if _, _, err = db.ReconcileCommercialOperationWorker(uid, r.OperationID); err == nil {
			t.Fatal("consumed credit falsely confirmed provisioning")
		}
		if err = db.CompleteCommercialOperationWorker(uid, source, worker); err != nil {
			t.Fatal(err)
		}
		if state, got, err := db.ReconcileCommercialOperationWorker(uid, r.OperationID); err != nil || state != "created" || got != worker {
			t.Fatalf("confirmed recovery: %s %d %v", state, got, err)
		}
		if _, ok, err := db.ReserveCommercialOperationCredit(uid, source, source, v.Preview); err != nil || ok {
			t.Fatalf("completed operation reserved again: %v %v", ok, err)
		}
		// A clean queue rollback retains a disabled username. It can be safely
		// retired; an enabled/ambiguous identity must remain blocked.
		q := apply(preview(types.CommercialActionRequest{UID: uid, Intent: "test_worker", DurationDays: 1, Note: "queued rollback", OperationID: "queued-identity"}))
		quser, err := db.CreateUser(&types.User{Username: types.CommercialActionUsername(uid, q.Operation.OperationID), AccountType: types.AccountBot, PassHash: []byte("test")})
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name) VALUES($1,$2,'queued')`, quser, uid)
		if err = db.DeleteBot(quser); err != nil {
			t.Fatal(err)
		}
		if state, _, err := db.ReconcileCommercialOperationWorker(uid, q.Operation.OperationID); err != nil || state != "pending" {
			t.Fatalf("clean identity not recoverable: %s %v", state, err)
		}
		if n := count(`SELECT COUNT(*) FROM users WHERE username=$1`, types.CommercialActionUsername(uid, q.Operation.OperationID)); n != 0 {
			t.Fatal("cleaned username still reserved")
		}
		qsource := types.CommercialActionSource(uid, q.Operation.OperationID)
		exec(`UPDATE cloud_worker_credits SET expires_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE source_ref=$1`, qsource)
		if _, ok, err := db.ReserveCommercialOperationCredit(uid, qsource, qsource, q.Preview); err != nil || ok {
			t.Fatalf("expired operation reserved: %v %v", ok, err)
		}
	})

	t.Run("failed work requires explicit same operation retry", func(t *testing.T) {
		exec(`UPDATE commercial_ops_actions SET status='completed',lease='',lease_until=NULL`)
		uid := createGLM53MigrationUser(t, db, "ops-failed")
		r := preview(types.CommercialActionRequest{UID: uid, Intent: "grant_plan", PlanID: plan, WorkerAction: "additional", Note: "failure recovery", OperationID: "failed"})
		apply(r)
		v, err := db.ClaimCommercialAction("failure-lease")
		if err != nil || v == nil || v.Operation.UID != uid {
			t.Fatalf("initial claim: %+v %v", v, err)
		}
		v.Operation.Status, v.Operation.WorkerStatus, v.Operation.Error = "failed", "failed", "provider failed"
		v.Operation.RelaySynced = true
		if err := db.SaveCommercialAction(v); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE commercial_ops_actions SET retry_at=CURRENT_TIMESTAMP-INTERVAL '1 hour' WHERE uid=$1`, uid)
		if got, err := db.ClaimCommercialAction("automatic-retry"); err != nil || got != nil {
			t.Fatalf("failed action automatically retried: %+v %v", got, err)
		}
		if got, err := db.GetCommercialAction(uid, r.OperationID); err != nil || got.Operation.Status != "failed" {
			t.Fatalf("status read restarted failed work: %+v %v", got, err)
		}
		changed := r
		changed.Note = "different request"
		_, err = db.ApplyCommercialAction(changed)
		expectCode(err, "operation_conflict")
		ledgerCount := count(`SELECT COUNT(*) FROM commercial_quota_ledger WHERE uid=$1`, uid)
		r.PreviewToken = "expired-original-preview"
		retry := apply(r)
		if retry.Operation.Status != "pending" || retry.Operation.WorkerStatus != "pending" || retry.Operation.Error != "" || !retry.Operation.RelaySynced || !retry.Operation.PackageApplied {
			t.Fatalf("explicit retry lost completed steps: %+v", retry)
		}
		if count(`SELECT COUNT(*) FROM commercial_quota_ledger WHERE uid=$1`, uid) != ledgerCount || count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1`, uid) != 1 || count(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid=$1`, uid) != 1 {
			t.Fatal("explicit retry duplicated ledger/package/credit")
		}
		next, err := db.ClaimCommercialAction("explicit-retry")
		if err != nil || next == nil || next.Operation.OperationID != r.OperationID || next.Operation.WorkerStatus != "running" {
			t.Fatalf("explicit retry not claimable: %+v %v", next, err)
		}
		next.Operation.Status = "completed"
		if err := db.SaveCommercialAction(next); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("owner lease fencing and restart claims", func(t *testing.T) {
		exec(`UPDATE commercial_ops_actions SET status='completed',lease='',lease_until=NULL`)
		uid := createGLM53MigrationUser(t, db, "ops-claims")
		for _, id := range []string{"claim-a", "claim-b"} {
			apply(preview(types.CommercialActionRequest{UID: uid, Intent: "test_worker", DurationDays: 1, Note: "lease", OperationID: id}))
		}
		var wg sync.WaitGroup
		claims := make(chan *types.CommercialAction, 2)
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				v, err := db.ClaimCommercialAction(fmt.Sprintf("lease-%d", i))
				claims <- v
				errs <- err
			}(i)
		}
		wg.Wait()
		var claimed *types.CommercialAction
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			if v := <-claims; v != nil {
				if claimed != nil {
					t.Fatal("two operations claimed for one owner")
				}
				claimed = v
			}
		}
		if claimed == nil || claimed.Operation.WorkerStatus != "running" {
			t.Fatal("no running claim")
		}
		exec(`UPDATE commercial_ops_actions SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE uid=$1 AND operation_id=$2`, uid, claimed.Operation.OperationID)
		restarted := &Adapter{db: db.db}
		next, err := restarted.ClaimCommercialAction("new-process")
		if err != nil || next == nil {
			t.Fatalf("expired claim not recovered: %+v %v", next, err)
		}
		if next.Operation.OperationID == claimed.Operation.OperationID {
			if err := db.SaveCommercialAction(claimed); err == nil {
				t.Fatal("stale lease overwrote new executor")
			}
		}
		next.Operation.Status = "completed"
		if err := db.SaveCommercialAction(next); err != nil {
			t.Fatal(err)
		}
		release, ok, err := db.AcquireCommercialWorkerOwnerGate(uid)
		if err != nil || !ok {
			t.Fatalf("owner gate: %v %v", ok, err)
		}
		if otherRelease, otherOK, err := db.AcquireCommercialWorkerOwnerGate(uid); err != nil || otherOK {
			if otherRelease != nil {
				otherRelease()
			}
			t.Fatalf("cross-process owner gate admitted two: %v %v", otherOK, err)
		}
		release()
		release, ok, err = db.AcquireCommercialWorkerOwnerGate(uid)
		if err != nil || !ok {
			t.Fatalf("owner gate was not released: %v %v", ok, err)
		}
		release()
	})

	t.Run("ensure binds machine identity and deployment class", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-class")
		machine := createGLM53MigrationUser(t, db, "ops-class-shared")
		exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name,cloud_deployment) VALUES($1,$2,'ops-class-shared','{"profile":"packed_shared"}'::jsonb)`, machine, uid)
		normalize := func(r types.CommercialActionRequest) types.CommercialActionRequest {
			t.Helper()
			if err := r.Normalize(); err != nil {
				t.Fatal(err)
			}
			return r
		}
		// A dedicated operation must not adopt the shared machine: it provisions
		// one of its own class and says so in the preview.
		dedicated := normalize(types.CommercialActionRequest{UID: uid, Intent: "ensure_worker", DeploymentProfile: types.CloudWorkerPrivateNAT, DurationDays: 3, Preview: true, Note: "class", OperationID: "class-dedicated"})
		p, err := db.PreviewCommercialAction(dedicated)
		if err != nil || !p.CanApply {
			t.Fatalf("dedicated preview: %+v %v", p, err)
		}
		if p.WorkerCount != 1 || p.WorkerMatchingCount != 0 || p.WorkerEffect != "ensure_one" {
			t.Fatalf("a shared machine was counted as adoptable by a dedicated operation: %+v", p)
		}
		dedicated.Preview, dedicated.PreviewToken = false, p.PreviewToken
		if v := apply(dedicated); v.Operation.WorkerStatus != "pending" {
			t.Fatalf("dedicated operation did not queue a machine: %+v", v.Operation)
		}
		if n := count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1 AND deployment_profile='private_nat'`, uid); n != 1 {
			t.Fatalf("dedicated operation minted %d dedicated credits", n)
		}
		if n := count(`SELECT COUNT(*) FROM bot_config WHERE owner_id=$1 AND cloud_deployment->>'profile'='packed_shared'`, uid); n != 1 {
			t.Fatalf("the cross-class operation changed the shared machine: %d", n)
		}
		// The same package class is adoptable, and the machine set is part of the
		// preview token: replacing the machine one-for-one invalidates it.
		shared := normalize(types.CommercialActionRequest{UID: uid, Intent: "ensure_worker", DeploymentProfile: types.CloudWorkerPackedShared, DurationDays: 3, Preview: true, Note: "class shared", OperationID: "class-shared"})
		p, err = db.PreviewCommercialAction(shared)
		if err != nil || !p.CanApply || p.WorkerMatchingCount != 1 || p.WorkerEffect != "keep_existing" {
			t.Fatalf("shared machine was not adoptable: %+v %v", p, err)
		}
		shared.Preview, shared.PreviewToken = false, p.PreviewToken
		replacement := createGLM53MigrationUser(t, db, "ops-class-shared-2")
		exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name,cloud_deployment) VALUES($1,$2,'ops-class-shared-2','{"profile":"packed_shared"}'::jsonb)`, replacement, uid)
		exec(`DELETE FROM bot_config WHERE user_id=$1`, machine)
		if _, err = db.ApplyCommercialAction(shared); err == nil {
			t.Fatal("a preview survived its machine being replaced by another one of the same count")
		}
		expectCode(err, "stale_preview")
		// Switching the machine's backend is the same kind of change.
		shared.Preview, shared.PreviewToken = true, ""
		fresh, err := db.PreviewCommercialAction(shared)
		if err != nil || !fresh.CanApply {
			t.Fatalf("fresh preview: %+v %v", fresh, err)
		}
		shared.Preview, shared.PreviewToken = false, fresh.PreviewToken
		exec(`UPDATE bot_config SET cloud_deployment='{"profile":"private_nat"}'::jsonb WHERE user_id=$1`, replacement)
		if _, err = db.ApplyCommercialAction(shared); err == nil {
			t.Fatal("a preview survived its machine changing deployment profile")
		}
		expectCode(err, "stale_preview")
		if n := count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1 AND deployment_profile='packed_shared'`, uid); n != 0 {
			t.Fatalf("a stale preview minted %d shared credits", n)
		}
	})

	// A finished deletion leaves the bot_config row behind (purging files and the
	// account row are separate steps), so the lifecycle row is what decides whether
	// a machine still exists. Such a machine must not be counted, adopted, or left
	// making an operation believe the account already has one.
	t.Run("a deleted machine is not a live machine", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-deleted-live")
		machine := createGLM53MigrationUser(t, db, "ops-deleted-machine")
		exec(`UPDATE users SET account_type='bot' WHERE id=$1`, machine)
		exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name,cloud_deployment) VALUES($1,$2,'ops-deleted-machine','{"profile":"packed_shared"}'::jsonb)`, machine, uid)
		exec(`INSERT INTO cloud_worker_lifecycles(worker_uid,owner_uid,tenant_name,package_expires_at,delete_after,state)
			VALUES($1,$2,'ops-deleted-machine',CURRENT_TIMESTAMP+INTERVAL '30 days',CURRENT_TIMESTAMP+INTERVAL '37 days','active')`, machine, uid)
		r := ensureWorkerRequest(t, uid, types.CloudWorkerPackedShared, "deleted machine", "deleted-machine")
		p, err := db.PreviewCommercialAction(r)
		if err != nil || !p.CanApply || p.WorkerCount != 1 || p.WorkerMatchingCount != 1 || p.WorkerEffect != "keep_existing" {
			t.Fatalf("live machine preview: %+v %v", p, err)
		}
		exec(`UPDATE cloud_worker_lifecycles SET state='deleted' WHERE worker_uid=$1`, machine)
		if bots, err := db.ListBotsByOwner(uid); err != nil || len(bots) != 0 {
			t.Fatalf("a deleted machine's residual bot was listed as live: %d %v", len(bots), err)
		}
		if n := count(`SELECT COUNT(*) FROM bot_config WHERE owner_id=$1`, uid); n != 1 {
			t.Fatalf("the residual bot row disappeared: %d", n)
		}
		p, err = db.PreviewCommercialAction(r)
		if err != nil || !p.CanApply || p.WorkerCount != 0 || p.WorkerMatchingCount != 0 || p.WorkerEffect != "ensure_one" {
			t.Fatalf("deleted machine still counted as adoptable: %+v %v", p, err)
		}
		r.Preview, r.PreviewToken = false, p.PreviewToken
		if v := apply(r); v.Operation.WorkerStatus != "pending" {
			t.Fatalf("the operation did not provision for an account without a machine: %+v", v.Operation)
		}
		if n := count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1 AND deployment_profile='packed_shared'`, uid); n != 1 {
			t.Fatalf("the operation minted %d shared credits", n)
		}
	})

	// The preview pins the machine set it saw. A count alone cannot tell the
	// previewed machine from a different one of the same class, and the lifecycle
	// state and billing claim are part of that identity: a machine being deleted or
	// re-termed is not the machine the preview promised to keep.
	t.Run("a machine's lifecycle state and billing claim invalidate the preview", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-machine-state")
		machine := createGLM53MigrationUser(t, db, "ops-machine-state-bot")
		exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name,cloud_deployment) VALUES($1,$2,'ops-machine-state-bot','{"profile":"packed_shared"}'::jsonb)`, machine, uid)
		exec(`INSERT INTO cloud_worker_lifecycles(worker_uid,owner_uid,tenant_name,package_expires_at,state)
			VALUES($1,$2,'ops-machine-state-bot',CURRENT_TIMESTAMP+INTERVAL '30 days','active')`, machine, uid)
		r := ensureWorkerRequest(t, uid, types.CloudWorkerPackedShared, "machine state", "machine-state")
		p, err := db.PreviewCommercialAction(r)
		if err != nil || !p.CanApply || p.WorkerEffect != "keep_existing" {
			t.Fatalf("active machine preview: %+v %v", p, err)
		}
		r.Preview, r.PreviewToken = false, p.PreviewToken
		exec(`UPDATE cloud_worker_lifecycles SET state='delete_pending' WHERE worker_uid=$1`, machine)
		if _, err = db.ApplyCommercialAction(r); err == nil {
			t.Fatal("a preview survived its machine's lifecycle state changing")
		}
		expectCode(err, "stale_preview")
		// A billing claim is the same kind of change: another worker already owns
		// this machine's next term.
		r.Preview, r.PreviewToken = true, ""
		if p, err = db.PreviewCommercialAction(r); err != nil || !p.CanApply {
			t.Fatalf("delete_pending machine preview: %+v %v", p, err)
		}
		r.Preview, r.PreviewToken = false, p.PreviewToken
		exec(`UPDATE cloud_worker_lifecycles SET billing_action='expire' WHERE worker_uid=$1`, machine)
		if _, err = db.ApplyCommercialAction(r); err == nil {
			t.Fatal("a preview survived a billing claim being taken on its machine")
		}
		expectCode(err, "stale_preview")
		// A destroy in flight is not adoptable at all: the preview says so and no
		// token issued before it can be applied.
		exec(`UPDATE cloud_worker_lifecycles SET state='delete_running', billing_action='' WHERE worker_uid=$1`, machine)
		r.Preview, r.PreviewToken = true, ""
		blocked, err := db.PreviewCommercialAction(r)
		if err != nil || blocked.CanApply || len(blocked.Warnings) == 0 {
			t.Fatalf("a machine being destroyed was still adoptable: %+v %v", blocked, err)
		}
		r.Preview, r.PreviewToken = false, blocked.PreviewToken
		if _, err = db.ApplyCommercialAction(r); err == nil {
			t.Fatal("an operation applied against a machine that is not adoptable")
		}
		expectCode(err, "stale_preview")
		if n := count(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid=$1`, uid); n != 0 {
			t.Fatalf("a refused machine operation minted %d credits", n)
		}
	})

	// An explicit profile this build does not know is not guessed: the machine
	// cannot be classified, so the preview fails instead of quietly treating the
	// account as machine-less and buying another one.
	t.Run("an unknown recorded machine profile fails the preview", func(t *testing.T) {
		uid := createGLM53MigrationUser(t, db, "ops-unknown-profile")
		machine := createGLM53MigrationUser(t, db, "ops-unknown-profile-bot")
		exec(`INSERT INTO bot_config(user_id,owner_id,tenant_name,cloud_deployment) VALUES($1,$2,'ops-unknown-profile-bot','{"profile":"mystery_fleet"}'::jsonb)`, machine, uid)
		r := ensureWorkerRequest(t, uid, types.CloudWorkerPackedShared, "unknown profile", "unknown-profile")
		p, err := db.PreviewCommercialAction(r)
		if err == nil {
			t.Fatalf("an unclassifiable machine was accepted: %+v", p)
		}
		expectCode(err, "invalid_request")
	})
}

// ensureWorkerRequest is the operator "keep the machine this account already has"
// request the machine-state contracts act on: the machine set is re-read from the
// database, so the request itself only carries the deployment class.
func ensureWorkerRequest(t *testing.T, uid int64, profile, note, operationID string) types.CommercialActionRequest {
	t.Helper()
	r := types.CommercialActionRequest{UID: uid, Intent: "ensure_worker", DeploymentProfile: profile, DurationDays: 3, Preview: true, Note: note, OperationID: operationID}
	if err := r.Normalize(); err != nil {
		t.Fatalf("normalize %s: %v", operationID, err)
	}
	return r
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

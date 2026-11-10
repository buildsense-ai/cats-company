package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// machinePlanFixture describes the only knobs these contracts need: whether the
// package sells a machine, how long it lasts and what retention it promises.
type machinePlanFixture struct {
	Slug          string
	DurationDays  int
	RetentionDays int
	Included      bool
	PriceFen      int64
	SaleState     string
}

// seedMachinePlan inserts the package shape directly. -1 is only allowed for a
// hidden, zero-priced plan (chk_commercial_plans_duration), which is how a
// perpetual package is expressed.
func seedMachinePlan(t *testing.T, db *Adapter, f machinePlanFixture) int64 {
	t.Helper()
	if f.SaleState == "" {
		f.SaleState = "test"
	}
	if f.DurationDays == 0 {
		f.DurationDays = 30
	}
	var id int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_plans(
			slug, name, price_fen, currency, sale_state, purchase_limit,
			monthly_budget_cny, model_budgets, duration_days, state, sort_order,
			cloud_worker_included, cloud_worker_deployment_profile, cloud_worker_billing_mode,
			cloud_worker_retention_days
		) VALUES ($1, $1, $2, 'CNY', $3, 0, 0, '{"MiniMax-M3":10}'::jsonb, $4, 0, 100,
		          $5, 'packed_shared', 'month', $6)
		RETURNING id`,
		f.Slug, f.PriceFen, f.SaleState, f.DurationDays, f.Included, f.RetentionDays).Scan(&id); err != nil {
		t.Fatalf("seed machine plan %s: %v", f.Slug, err)
	}
	return id
}

func seedPerpetualMachinePlanWithRetention(t *testing.T, db *Adapter, slug string, retentionDays int) int64 {
	t.Helper()
	return seedMachinePlan(t, db, machinePlanFixture{
		Slug: slug, DurationDays: -1, RetentionDays: retentionDays,
		Included: true, SaleState: "hidden",
	})
}

// seedMachineEntitlement assigns one package directly, so a test can express
// combinations the purchase flows never produce on demand: a free baseline, a
// revoked row, a window that has not started yet.
func seedMachineEntitlement(t *testing.T, db *Adapter, uid, planID int64, source, sourceRef, state string, startsAt time.Time, expiresAt *time.Time) int64 {
	t.Helper()
	var id int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		uid, planID, source, sourceRef, state, startsAt, expiresAt).Scan(&id); err != nil {
		t.Fatalf("seed %s entitlement: %v", source, err)
	}
	return id
}

// seedMachineInviteCode registers an invite that hands out machine credits
// against whatever plan it is bound to - the way a token-only package can still
// deliver a machine.
func seedMachineInviteCode(t *testing.T, db *Adapter, code string, planID int64, credits int) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_invite_codes(code, plan_id, max_redemptions, cloud_worker_credits, cloud_worker_profile)
		VALUES ($1, $2, 100, $3, 'packed_shared')`, code, planID, credits); err != nil {
		t.Fatalf("seed invite code %s: %v", code, err)
	}
}

// TestPostgresCloudWorkerPackageTermContract pins the single read that decides
// which package grants an owner a machine.
//
// Every consumer (commercial summary, operator preview, refund settlement) reads
// this one query, so the cases below are the shared contract: baselines never
// count, token-only packages never count unless an invite handed out machine
// credits, and the term is the machine package's own window - which is not the
// window of the package that governs models.
func TestPostgresCloudWorkerPackageTermContract(t *testing.T) {
	db := commercialPolicyTestDB(t)
	now := time.Now().UTC()

	t.Run("a free or legacy baseline never grants a machine", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-free-baseline")
		// The plan is flagged as selling a machine on purpose: the exclusion has to
		// come from the entitlement source, not from a lucky plan flag.
		included := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-baseline-included", Included: true, DurationDays: 30})
		seedMachineEntitlement(t, db, owner, included, "free", "baseline-free", "active", now.Add(-time.Hour), nil)

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term != nil {
			t.Fatalf("a free baseline was read as a machine package: %+v", term)
		}

		seedMachineEntitlement(t, db, owner, included, "legacy", "baseline-legacy", "active", now.Add(-time.Hour), nil)
		term, err = db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term != nil {
			t.Fatalf("a legacy baseline was read as a machine package: %+v", term)
		}
	})

	t.Run("a model-only package is not a machine package", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-model-only")
		tokenOnly := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-model-only-plan", Included: false, DurationDays: 30})
		expiry := pgMicro(now.Add(30 * 24 * time.Hour))
		seedMachineEntitlement(t, db, owner, tokenOnly, "order", "TERM-MODEL-ONLY", "active", now.Add(-time.Hour), &expiry)

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term != nil {
			t.Fatalf("a token-only package was read as a machine package: %+v", term)
		}
	})

	t.Run("an invite can grant a machine on a token-only plan", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-invite-machine")
		tokenOnly := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-invite-token-plan", Included: false, DurationDays: 365, RetentionDays: 4})
		seedMachineInviteCode(t, db, "TERM-INVITE-MACHINE", tokenOnly, 1)
		expiry := pgMicro(now.Add(365 * 24 * time.Hour))
		entitlementID := seedMachineEntitlement(t, db, owner, tokenOnly, "invite", "TERM-INVITE-MACHINE", "active", now.Add(-time.Hour), &expiry)

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil {
			t.Fatal("an invite with machine credits must grant a machine even on a token-only plan")
		}
		if term.EntitlementID != entitlementID || term.PlanID != tokenOnly {
			t.Fatalf("wrong machine package: %+v", term)
		}
		if term.ExpiresAt == nil || !term.ExpiresAt.Equal(expiry) {
			t.Fatalf("machine window = %v, want %s", term.ExpiresAt, expiry)
		}
		if term.RetentionDays != 4 {
			t.Fatalf("retention = %d, want the package's configured 4", term.RetentionDays)
		}
	})

	t.Run("an invite with no machine credits does not grant a machine", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-invite-tokens")
		tokenOnly := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-invite-token-plan-0", Included: false, DurationDays: 30})
		seedMachineInviteCode(t, db, "TERM-INVITE-TOKENS", tokenOnly, 0)
		expiry := pgMicro(now.Add(30 * 24 * time.Hour))
		seedMachineEntitlement(t, db, owner, tokenOnly, "invite", "TERM-INVITE-TOKENS", "active", now.Add(-time.Hour), &expiry)

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term != nil {
			t.Fatalf("a token-only invite was read as a machine package: %+v", term)
		}
	})

	t.Run("retention follows the configured value and the package length", func(t *testing.T) {
		cases := []struct {
			name          string
			durationDays  int
			retentionDays int
			want          int
		}{
			{name: "short package derives three days", durationDays: 7, want: 3},
			{name: "monthly package derives seven days", durationDays: 30, want: 7},
			{name: "configured value wins", durationDays: 30, retentionDays: 5, want: 5},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				owner := createGLM53MigrationUser(t, db, "term-retention-"+tc.name[:4])
				plan := seedMachinePlan(t, db, machinePlanFixture{
					Slug:          "term-retention-" + strings.ReplaceAll(tc.name, " ", "-"),
					Included:      true,
					DurationDays:  tc.durationDays,
					RetentionDays: tc.retentionDays,
				})
				expiry := pgMicro(now.Add(time.Duration(tc.durationDays) * 24 * time.Hour))
				seedMachineEntitlement(t, db, owner, plan, "order", "TERM-RETENTION", "active", now.Add(-time.Hour), &expiry)

				term, err := db.GetSharedCloudWorkerPackageTerm(owner)
				if err != nil {
					t.Fatal(err)
				}
				if term == nil || term.RetentionDays != tc.want {
					t.Fatalf("retention = %+v, want %d", term, tc.want)
				}
			})
		}
	})

	t.Run("a perpetual machine package has no deadline", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-perpetual-machine")
		plan := seedPerpetualMachinePlanWithRetention(t, db, "term-perpetual-machine-plan", 7)
		seedMachineEntitlement(t, db, owner, plan, "operator", "TERM-PERPETUAL", "active", now.Add(-time.Hour), nil)

		term, err := db.GetSharedCloudWorkerPackageTerm(owner)
		if err != nil {
			t.Fatal(err)
		}
		if term == nil {
			t.Fatal("a perpetual machine package must be reported")
		}
		if term.ExpiresAt != nil {
			t.Fatalf("a perpetual machine package must not carry a deadline: %+v", term)
		}
		if term.RetentionDays != 7 {
			t.Fatalf("retention = %d, want the configured 7", term.RetentionDays)
		}
	})

	t.Run("the machine package is not the model primary", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-mixed")
		tokenPlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-mixed-tokens", Included: false, DurationDays: 400})
		machinePlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-mixed-machine", Included: true, DurationDays: 30, RetentionDays: 7})
		modelExpiry := pgMicro(now.Add(400 * 24 * time.Hour))
		machineExpiry := pgMicro(now.Add(30 * 24 * time.Hour))
		seedMachineEntitlement(t, db, owner, tokenPlan, "order", "TERM-MIXED-TOKENS", "active", now.Add(-2*time.Hour), &modelExpiry)
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "TERM-MIXED-MACHINE", "active", now.Add(-time.Hour), &machineExpiry)

		summary, err := db.GetCommercialSummary(owner)
		if err != nil {
			t.Fatal(err)
		}
		primary := types.PrimaryCommercialEntitlement(summary.Entitlements, time.Now().UTC())
		if primary == nil || primary.PlanID != tokenPlan {
			t.Fatalf("fixture invalid: the token package should be primary, got %+v", primary)
		}
		term := summary.CloudWorkerPackageTerm
		if term == nil || term.PlanID != machinePlan {
			t.Fatalf("summary machine package = %+v, want the machine plan %d", term, machinePlan)
		}
		if term.ExpiresAt == nil || !term.ExpiresAt.Equal(machineExpiry) {
			t.Fatalf("summary machine window = %v, want %s (not the model window %s)", term.ExpiresAt, machineExpiry, modelExpiry)
		}
		if primary.ExpiresAt == nil || !primary.ExpiresAt.Equal(modelExpiry) {
			t.Fatalf("fixture invalid: model primary window = %v", primary.ExpiresAt)
		}
	})

	t.Run("expired, revoked and not-yet-started windows are not a machine package", func(t *testing.T) {
		plan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-ineligible-machine", Included: true, DurationDays: 30})
		expired := pgMicro(now.Add(-time.Hour))
		future := pgMicro(now.Add(30 * 24 * time.Hour))
		futureStart := now.Add(time.Hour)

		expiredOwner := createGLM53MigrationUser(t, db, "term-expired")
		seedMachineEntitlement(t, db, expiredOwner, plan, "order", "TERM-EXPIRED", "active", now.Add(-48*time.Hour), &expired)
		if term, err := db.GetSharedCloudWorkerPackageTerm(expiredOwner); err != nil || term != nil {
			t.Fatalf("an expired machine package must not count: %+v err=%v", term, err)
		}

		revokedOwner := createGLM53MigrationUser(t, db, "term-revoked")
		seedMachineEntitlement(t, db, revokedOwner, plan, "order", "TERM-REVOKED", "revoked", now.Add(-48*time.Hour), &future)
		if term, err := db.GetSharedCloudWorkerPackageTerm(revokedOwner); err != nil || term != nil {
			t.Fatalf("a revoked machine package must not count: %+v err=%v", term, err)
		}

		notStartedOwner := createGLM53MigrationUser(t, db, "term-not-started")
		seedMachineEntitlement(t, db, notStartedOwner, plan, "order", "TERM-NOT-STARTED", "active", futureStart, &future)
		if term, err := db.GetSharedCloudWorkerPackageTerm(notStartedOwner); err != nil || term != nil {
			t.Fatalf("a machine package that has not started must not count: %+v err=%v", term, err)
		}
	})

	t.Run("the summary exposes the machine package and its absence", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-summary-null")
		summary, err := db.GetCommercialSummary(owner)
		if err != nil {
			t.Fatal(err)
		}
		if summary.CloudWorkerPackageTerm != nil {
			t.Fatalf("an account without a machine package must report nil: %+v", summary.CloudWorkerPackageTerm)
		}
		encoded, err := json.Marshal(summary)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"cloud_worker_package_term":null`) {
			t.Fatalf("the field must serialise as an explicit null: %s", encoded)
		}
	})
}

// TestCommercialSummaryCloudWorkerPackageTermJSON pins the wire contract: the
// field is always present, and a perpetual machine term is an explicit null
// deadline rather than an omitted field.
func TestCommercialSummaryCloudWorkerPackageTermJSON(t *testing.T) {
	encoded, err := json.Marshal(&types.CommercialSummary{UID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"cloud_worker_package_term":null`) {
		t.Fatalf("a summary without a machine package must serialise null: %s", encoded)
	}

	encoded, err = json.Marshal(&types.CommercialSummary{
		UID:                    1,
		CloudWorkerPackageTerm: &types.CloudWorkerPackageTerm{EntitlementID: 3, PlanID: 4, RetentionDays: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"entitlement_id":3`, `"plan_id":4`, `"expires_at":null`, `"retention_days":7`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s in %s", want, encoded)
		}
	}
}

// TestPostgresCommercialActionMachineTermPreview pins the operator preview to
// the machine package instead of the model package.
//
// The bug this covers: add_worker/follow_plan read the *model* primary package,
// so an account whose machine came from an invite on a token-only plan was told
// it had nothing to follow, while an account with only a free baseline was told
// it could follow a term that the settlement would never honour.
func TestPostgresCommercialActionMachineTermPreview(t *testing.T) {
	db := commercialPolicyTestDB(t)
	now := time.Now().UTC()

	followPlanRequest := func(uid int64, intent string, planID int64) types.CommercialActionRequest {
		request := types.CommercialActionRequest{
			UID:              uid,
			Intent:           intent,
			WorkerAction:     "additional",
			WorkerExpiryMode: types.CloudWorkerExpiryFollowPlan,
			Preview:          true,
		}
		// A package intent derives the fleet from the plan it assigns, so it must
		// not carry one of its own; a worker intent names the fleet directly.
		if intent == "grant_plan" {
			request.PlanID = planID
		} else if intent != "renew_plan" {
			request.DeploymentProfile = types.CloudWorkerPackedShared
			request.BillingMode = types.CloudWorkerMonthly
		}
		return request
	}

	t.Run("add_worker rejects a free baseline even when the plan is flagged included", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-preview-baseline")
		included := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-preview-baseline-plan", Included: true, DurationDays: 30})
		seedMachineEntitlement(t, db, owner, included, "free", "baseline-free", "active", now.Add(-time.Hour), nil)

		preview, err := db.PreviewCommercialAction(followPlanRequest(owner, "add_worker", 0))
		if err != nil {
			t.Fatal(err)
		}
		if preview.CanApply {
			t.Fatalf("a free baseline must not be followed: %+v", preview)
		}
		if preview.WorkerExpiresAt != nil {
			t.Fatalf("a rejected follow_plan must not carry a term: %+v", preview)
		}
	})

	t.Run("add_worker follows the machine package, not the model package", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-preview-mixed")
		tokenPlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-preview-tokens", Included: false, DurationDays: 400})
		machinePlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-preview-machine", Included: true, DurationDays: 30, RetentionDays: 7})
		modelExpiry := pgMicro(now.Add(400 * 24 * time.Hour))
		machineExpiry := pgMicro(now.Add(30 * 24 * time.Hour))
		seedMachineEntitlement(t, db, owner, tokenPlan, "order", "TERM-PREVIEW-TOKENS", "active", now.Add(-2*time.Hour), &modelExpiry)
		seedMachineEntitlement(t, db, owner, machinePlan, "order", "TERM-PREVIEW-MACHINE", "active", now.Add(-time.Hour), &machineExpiry)

		preview, err := db.PreviewCommercialAction(followPlanRequest(owner, "add_worker", 0))
		if err != nil {
			t.Fatal(err)
		}
		if !preview.CanApply {
			t.Fatalf("the account holds a machine package: %+v", preview)
		}
		if preview.WorkerExpiresAt == nil || !preview.WorkerExpiresAt.Equal(machineExpiry) {
			t.Fatalf("follow_plan window = %v, want the machine package's %s (not the model package's %s)",
				preview.WorkerExpiresAt, machineExpiry, modelExpiry)
		}
	})

	t.Run("add_worker accepts an invite machine on a token-only plan", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-preview-invite")
		tokenPlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-preview-invite-plan", Included: false, DurationDays: 30})
		seedMachineInviteCode(t, db, "TERM-PREVIEW-INVITE", tokenPlan, 1)
		inviteExpiry := pgMicro(now.Add(90 * 24 * time.Hour))
		seedMachineEntitlement(t, db, owner, tokenPlan, "invite", "TERM-PREVIEW-INVITE", "active", now.Add(-time.Hour), &inviteExpiry)

		preview, err := db.PreviewCommercialAction(followPlanRequest(owner, "add_worker", 0))
		if err != nil {
			t.Fatal(err)
		}
		if !preview.CanApply {
			t.Fatalf("an invite-granted machine must be followable: %+v", preview)
		}
		if preview.WorkerExpiresAt == nil || !preview.WorkerExpiresAt.Equal(inviteExpiry) {
			t.Fatalf("follow_plan window = %v, want the invite's %s", preview.WorkerExpiresAt, inviteExpiry)
		}
	})

	t.Run("add_worker follows a perpetual machine package", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-preview-perpetual")
		plan := seedPerpetualMachinePlanWithRetention(t, db, "term-preview-perpetual-plan", 7)
		seedMachineEntitlement(t, db, owner, plan, "operator", "TERM-PREVIEW-PERPETUAL", "active", now.Add(-time.Hour), nil)

		preview, err := db.PreviewCommercialAction(followPlanRequest(owner, "add_worker", 0))
		if err != nil {
			t.Fatal(err)
		}
		if !preview.CanApply || preview.WorkerExpiresAt != nil {
			t.Fatalf("a perpetual machine package must be followable without a deadline: %+v", preview)
		}
	})

	t.Run("grant_plan requires the target package to sell a machine", func(t *testing.T) {
		owner := createGLM53MigrationUser(t, db, "term-preview-grant")
		tokenPlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-preview-grant-tokens", Included: false, DurationDays: 30})
		machinePlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-preview-grant-machine", Included: true, DurationDays: 30, RetentionDays: 7})

		rejected, err := db.PreviewCommercialAction(followPlanRequest(owner, "grant_plan", tokenPlan))
		if err != nil {
			t.Fatal(err)
		}
		if rejected.CanApply {
			t.Fatalf("a token-only target package cannot be followed: %+v", rejected)
		}

		accepted, err := db.PreviewCommercialAction(followPlanRequest(owner, "grant_plan", machinePlan))
		if err != nil {
			t.Fatal(err)
		}
		if !accepted.CanApply {
			t.Fatalf("a machine target package must be followable: %+v", accepted)
		}
		if accepted.WorkerExpiresAt == nil || accepted.ExpiresAt == nil || !accepted.WorkerExpiresAt.Equal(*accepted.ExpiresAt) {
			t.Fatalf("follow_plan must use the granted window: worker=%v package=%v", accepted.WorkerExpiresAt, accepted.ExpiresAt)
		}
	})

	t.Run("renew_plan keeps an invite machine and uses the new window", func(t *testing.T) {
		withMachine := createGLM53MigrationUser(t, db, "term-preview-renew")
		tokenPlan := seedMachinePlan(t, db, machinePlanFixture{Slug: "term-preview-renew-plan", Included: false, DurationDays: 30})
		modelExpiry := pgMicro(now.Add(30 * 24 * time.Hour))
		seedMachineEntitlement(t, db, withMachine, tokenPlan, "order", "TERM-PREVIEW-RENEW", "active", now.Add(-time.Hour), &modelExpiry)
		seedMachineInviteCode(t, db, "TERM-PREVIEW-RENEW-INVITE", tokenPlan, 1)
		inviteExpiry := pgMicro(now.Add(30 * 24 * time.Hour))
		seedMachineEntitlement(t, db, withMachine, tokenPlan, "invite", "TERM-PREVIEW-RENEW-INVITE", "active", now.Add(-time.Hour), &inviteExpiry)

		accepted, err := db.PreviewCommercialAction(followPlanRequest(withMachine, "renew_plan", 0))
		if err != nil {
			t.Fatal(err)
		}
		if !accepted.CanApply {
			t.Fatalf("a renewal of an invite-granted machine must be followable: %+v", accepted)
		}
		if accepted.PreviousExpiresAt == nil || accepted.WorkerExpiresAt == nil {
			t.Fatalf("renewal preview lost its windows: %+v", accepted)
		}
		if !accepted.WorkerExpiresAt.After(*accepted.PreviousExpiresAt) {
			t.Fatalf("follow_plan must use the new renewal window: worker=%v previous=%v",
				accepted.WorkerExpiresAt, accepted.PreviousExpiresAt)
		}

		withoutMachine := createGLM53MigrationUser(t, db, "term-preview-renew-none")
		seedMachineEntitlement(t, db, withoutMachine, tokenPlan, "order", "TERM-PREVIEW-RENEW-NONE", "active", now.Add(-time.Hour), &modelExpiry)
		rejected, err := db.PreviewCommercialAction(followPlanRequest(withoutMachine, "renew_plan", 0))
		if err != nil {
			t.Fatal(err)
		}
		if rejected.CanApply {
			t.Fatalf("renewing a token-only account must not follow a machine term: %+v", rejected)
		}
	})
}

// TestCommercialActionPreviewTokenIgnoresMachineTerm keeps the new summary field
// out of the durable preview token: a request that predates worker_expiry_mode
// must hash exactly as it did before, and the extra read must not change it.
func TestCommercialActionPreviewTokenIgnoresMachineTerm(t *testing.T) {
	now := time.Now().UTC()
	anchor := now.Truncate(time.Microsecond)
	request := types.CommercialActionRequest{
		UID:               9,
		Intent:            "ensure_worker",
		WorkerAction:      "ensure",
		DeploymentProfile: types.CloudWorkerPackedShared,
		BillingMode:       types.CloudWorkerMonthly,
		Preview:           true,
	}
	expiry := now.Add(30 * 24 * time.Hour)
	base := &types.CommercialSummary{
		UID:           request.UID,
		Entitlements:  []*types.CommercialEntitlement{{ID: 1, UID: request.UID, PlanID: 2, Source: "order", State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &expiry}},
		TotalsByModel: map[string]float64{},
	}
	withoutTerm, err := buildCommercialActionPreview(request, base, 0, anchor, now)
	if err != nil {
		t.Fatal(err)
	}
	withTerm := *base
	withTerm.CloudWorkerPackageTerm = &types.CloudWorkerPackageTerm{EntitlementID: 1, PlanID: 2, RetentionDays: 7}
	withMachine, err := buildCommercialActionPreview(request, &withTerm, 0, anchor, now)
	if err != nil {
		t.Fatal(err)
	}
	if withMachine.PreviewToken != withoutTerm.PreviewToken {
		t.Fatalf("the machine package leaked into the preview token: %s != %s",
			withMachine.PreviewToken, withoutTerm.PreviewToken)
	}
	if withoutTerm.WorkerExpiryMode != "" {
		t.Fatalf("a legacy request must keep its historical preview shape: %+v", withoutTerm)
	}
}

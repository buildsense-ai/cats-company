package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestCommercialActionPreviewFirstGrantAndWorkerPurposes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := &types.CommercialPlan{ID: 10, Slug: "ops-internal", DurationDays: 30, MonthlyBudget: 10, ModelBudgets: map[string]float64{"model": 4}, CloudWorkerDeploymentProfile: "packed_shared", CloudWorkerRuntimePlan: "pro"}
	s := &types.CommercialSummary{Plans: []*types.CommercialPlan{plan}}
	for _, purpose := range []string{"none", "ensure", "additional"} {
		r := types.CommercialActionRequest{UID: 7, Intent: "grant_plan", PlanID: 10, WorkerAction: purpose, Preview: true, Note: "first grant"}
		if err := r.Normalize(); err != nil {
			t.Fatal(err)
		}
		p, err := buildCommercialActionPreview(r, s, []types.CommercialActionWorker{{UID: 1, Profile: "packed_shared"}, {UID: 2, Profile: "private_nat"}}, now, now)
		if err != nil {
			t.Fatal(err)
		}
		if !p.CanApply || p.CurrentPlan != nil || p.TargetPlan.ID != 10 || p.NextTotalCNY != 14 {
			t.Fatalf("first grant: %+v", p)
		}
		if purpose == "ensure" && p.WorkerEffect != "keep_existing" {
			t.Fatalf("ensure: %+v", p)
		}
		if purpose == "additional" && (p.WorkerEffect != "create_one" || p.RuntimePlan != "pro" || p.WorkerExpiresAt == nil) {
			t.Fatalf("additional: %+v", p)
		}
		if purpose == "none" && p.WorkerEffect != "none" {
			t.Fatal("package-only minted worker intent")
		}
	}
}
func TestCommercialActionRenewPrimaryFiniteInternalAndLegacy(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.AddDate(0, 0, 10)
	segment := expiry.AddDate(0, 0, 30)
	legacyExpiry := expiry.AddDate(0, 0, 300)
	internal := &types.CommercialPlan{ID: 1, Slug: "internal", PriceFen: 0, DurationDays: 30, MonthlyBudget: 5}
	legacy := &types.CommercialPlan{ID: 2, Slug: commercialLegacyPlanSlug, DurationDays: -1, MonthlyBudget: 20}
	s := &types.CommercialSummary{Plans: []*types.CommercialPlan{internal, legacy}, TotalCNY: 25, Entitlements: []*types.CommercialEntitlement{
		{ID: 1, PlanID: 1, State: "active", Source: "operator", StartsAt: now.Add(-time.Hour), ExpiresAt: &expiry},
		{ID: 2, PlanID: 1, State: "active", Source: "operator", StartsAt: expiry, ExpiresAt: &segment},
		{ID: 3, PlanID: 2, State: "active", Source: "legacy", StartsAt: now.Add(-time.Hour), ExpiresAt: &legacyExpiry},
	}}
	r := types.CommercialActionRequest{UID: 7, Intent: "renew_plan", Preview: true, Note: "renew"}
	_ = r.Normalize()
	p, err := buildCommercialActionPreview(r, s, nil, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if !p.CanApply || p.CurrentPlan.ID != 1 || !p.PreviousExpiresAt.Equal(segment) || !p.ExpiresAt.Equal(segment.AddDate(0, 0, 30)) || p.NextTotalCNY != 25 {
		t.Fatalf("wrong primary/period: %+v", p)
	}
	s.Entitlements = s.Entitlements[2:]
	r.DurationDays = 12
	p, _ = buildCommercialActionPreview(r, s, nil, now, now)
	if !p.CanApply || p.CurrentPlan.ID != 2 || !p.ExpiresAt.Equal(legacyExpiry.AddDate(0, 0, 12)) {
		t.Fatalf("finite legacy: %+v", p)
	}
	s.Entitlements[0].ExpiresAt = nil
	p, _ = buildCommercialActionPreview(r, s, nil, now, now)
	if p.CanApply {
		t.Fatal("permanent entitlement renewed")
	}
}
func TestCommercialActionPreviewTokenBindsBusinessAndState(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.AddDate(0, 0, 20)
	plan := &types.CommercialPlan{ID: 1, DurationDays: 30, MonthlyBudget: 10, ModelBudgets: map[string]float64{"model": 2}}
	s := &types.CommercialSummary{Plans: []*types.CommercialPlan{plan}, TotalCNY: 10, Entitlements: []*types.CommercialEntitlement{{ID: 1, PlanID: 1, Source: "operator", State: "active", StartsAt: now.Add(-time.Hour), ExpiresAt: &expiry}}}
	r := types.CommercialActionRequest{UID: 7, Intent: "renew_plan", Preview: true, Note: "why"}
	_ = r.Normalize()
	base, _ := buildCommercialActionPreview(r, s, nil, now, now)
	for _, change := range []func(){func() { r.Note = "other" }, func() { r.DurationDays = 8 }, func() { r.WorkerAction = "additional" }, func() { plan.ModelBudgets["model"] = 3 }, func() { s.Entitlements[0].ID = 9 }, func() { s.TotalCNY = 11 }} {
		change()
		p, _ := buildCommercialActionPreview(r, s, nil, now, now)
		if p.PreviewToken == base.PreviewToken {
			t.Fatal("preview failed to bind changed state")
		}
		base = p
	}
}
func TestCommercialActionWorkerOnlyPreviewDoesNotChangePackage(t *testing.T) {
	now := time.Now().UTC()
	r := types.CommercialActionRequest{UID: 7, Intent: "test_worker", DurationDays: 2, DeploymentProfile: "packed_shared", Preview: true, Note: "test"}
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	p, _ := buildCommercialActionPreview(r, &types.CommercialSummary{TotalCNY: 15}, nil, now, now)
	if !p.CanApply || p.PackageEffect != "none" || p.NextTotalCNY != 15 || !p.WorkerExpiresAt.Equal(now.AddDate(0, 0, 2)) {
		t.Fatalf("test worker: %+v", p)
	}
	r.Intent = "ensure_worker"
	r.DurationDays = 0
	r.WorkerAction = ""
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	p, _ = buildCommercialActionPreview(r, &types.CommercialSummary{}, nil, now, now)
	if p.CanApply {
		t.Fatal("unbounded machine permitted")
	}
	p, _ = buildCommercialActionPreview(r, &types.CommercialSummary{}, []types.CommercialActionWorker{{UID: 1, Profile: "packed_shared"}}, now, now)
	if !p.CanApply || p.WorkerEffect != "keep_existing" {
		t.Fatal("existing worker must be kept")
	}
}

func TestCommercialActionCreditOnlyPreview(t *testing.T) {
	anchor := time.Date(2027, time.January, 31, 10, 0, 0, 123000, time.UTC)
	plan := &types.CommercialPlan{ID: 846, Slug: "hidden-credit-v3", SaleState: "hidden", DurationDays: 30, PlanTier: 3, ModelCreditUnits: 22_500_000_000}
	s := &types.CommercialSummary{Plans: []*types.CommercialPlan{plan}}
	r := types.CommercialActionRequest{UID: 1677, Intent: "grant_plan", PlanID: plan.ID, WorkerAction: "none", Preview: true, Note: "credit only"}
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	p, err := buildCommercialActionPreview(r, s, nil, anchor, anchor)
	february := time.Date(2027, time.February, 28, 10, 0, 0, 123000, time.UTC)
	if err != nil || !p.CanApply || !p.ExpiresAt.Equal(february) || p.NextTotalCNY != 0 || p.WorkerEffect != "none" {
		t.Fatalf("credit-only grant: %+v %v", p, err)
	}
	if commercialPlanHasQuota(plan) {
		t.Fatal("credit eligibility leaked into legacy quota validation")
	}
	baseToken := p.PreviewToken
	plan.ModelCreditUnits++
	p, _ = buildCommercialActionPreview(r, s, nil, anchor, anchor)
	if p.PreviewToken == baseToken {
		t.Fatal("preview token did not bind credit allowance")
	}
	plan.ModelCreditUnits--
	for _, mutate := range []func(){func() { plan.State = 1 }, func() { plan.State = 0; plan.ModelCreditUnits = 0 }} {
		mutate()
		p, _ = buildCommercialActionPreview(r, s, nil, anchor, anchor)
		if p.CanApply {
			t.Fatal("disabled or empty plan was accepted")
		}
	}
	plan.ModelCreditUnits = 22_500_000_000
	s.Entitlements = []*types.CommercialEntitlement{{ID: 1, PlanID: plan.ID, Source: "admin", State: "active", StartsAt: anchor, ExpiresAt: &february, PeriodAnchorAt: &anchor}}
	r.Intent = "renew_plan"
	p, _ = buildCommercialActionPreview(r, s, nil, anchor.Add(time.Hour), anchor.Add(time.Hour))
	march := time.Date(2027, time.March, 31, 10, 0, 0, 123000, time.UTC)
	if !p.CanApply || !p.ExpiresAt.Equal(march) || p.NextTotalCNY != 0 || p.WorkerEffect != "none" {
		t.Fatalf("anchored renewal: %+v", p)
	}
	// The original segment is now expired and absent from the snapshot; the
	// surviving segment must carry January's anchor through February's clamp.
	s.Entitlements[0].StartsAt = february
	s.Entitlements[0].ExpiresAt = &march
	p, _ = buildCommercialActionPreview(r, s, nil, february.Add(time.Hour), february.Add(time.Hour))
	if !p.CanApply || !p.ExpiresAt.Equal(time.Date(2027, time.April, 30, 10, 0, 0, 123000, time.UTC)) {
		t.Fatalf("renewal lost original anchor: %+v", p)
	}
	r.DurationDays = 30
	p, _ = buildCommercialActionPreview(r, s, nil, february.Add(time.Hour), february.Add(time.Hour))
	if p.CanApply {
		t.Fatal("credit renewal accepted a fixed-day override")
	}
}

// An ensure operation may only keep a machine of the deployment class the
// package (or the operator request) is for. The preview has to say so in a way
// the operator can read - total machines and matching machines are different
// numbers - and the token has to bind which machine was seen, not how many.
func TestCommercialActionPreviewClassifiesMachinesOfBothDeployments(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := &types.CommercialPlan{ID: 10, Slug: "ops-shared", DurationDays: 30, MonthlyBudget: 10,
		ModelBudgets: map[string]float64{"model": 4}, CloudWorkerIncluded: true,
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared, CloudWorkerRuntimePlan: "pro"}
	s := &types.CommercialSummary{Plans: []*types.CommercialPlan{plan}}
	ecs := types.CommercialActionWorker{UID: 1, Profile: types.CloudWorkerPrivateNAT}
	publicIP := types.CommercialActionWorker{UID: 2, Profile: types.CloudWorkerPublicIP}
	shared := types.CommercialActionWorker{UID: 3, Profile: types.CloudWorkerPackedShared}
	request := func(profile string) types.CommercialActionRequest {
		r := types.CommercialActionRequest{UID: 7, Intent: "ensure_worker", DeploymentProfile: profile, DurationDays: 3, Preview: true, Note: "ensure"}
		if err := r.Normalize(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// An ECS-only account: a shared operation provisions one machine and says so.
	p, err := buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, []types.CommercialActionWorker{ecs}, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if !p.CanApply || p.WorkerCount != 1 || p.WorkerMatchingCount != 0 || p.WorkerEffect != "ensure_one" {
		t.Fatalf("ECS-only account was not reported as needing a machine: %+v", p)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "新增") {
		t.Fatalf("no operator-visible warning about the other-class machine: %v", p.Warnings)
	}
	// A shared-only account keeps its machine; the mixed account counts both and
	// keeps only the one that matches the deployment.
	for _, tc := range []struct {
		name     string
		machines []types.CommercialActionWorker
		matching int
	}{
		{"shared only", []types.CommercialActionWorker{shared}, 1},
		{"mixed", []types.CommercialActionWorker{ecs, publicIP, shared}, 1},
	} {
		p, err = buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, tc.machines, now, now)
		if err != nil {
			t.Fatal(err)
		}
		if !p.CanApply || p.WorkerEffect != "keep_existing" || p.WorkerMatchingCount != tc.matching || p.WorkerCount != len(tc.machines) {
			t.Fatalf("%s: total=%d matching=%d effect=%s", tc.name, p.WorkerCount, p.WorkerMatchingCount, p.WorkerEffect)
		}
	}
	// Both ECS profiles are the dedicated class, so a private-NAT request keeps a
	// public-IP machine (the same rule the paid renewal path uses).
	p, err = buildCommercialActionPreview(request(types.CloudWorkerPrivateNAT), s, []types.CommercialActionWorker{publicIP, shared}, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.WorkerEffect != "keep_existing" || p.WorkerMatchingCount != 1 {
		t.Fatalf("dedicated-class machine was not adoptable: %+v", p)
	}
	// The token binds machine identity, so replacing a machine one-for-one or
	// switching its backend invalidates the preview instead of being accepted.
	base, err := buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, []types.CommercialActionWorker{shared}, now, now)
	if err != nil {
		t.Fatal(err)
	}
	replaced, _ := buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, []types.CommercialActionWorker{{UID: 99, Profile: types.CloudWorkerPackedShared}}, now, now)
	if replaced.PreviewToken == base.PreviewToken {
		t.Fatal("preview token survived a machine being replaced by another one")
	}
	switched, _ := buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, []types.CommercialActionWorker{{UID: shared.UID, Profile: types.CloudWorkerPublicIP}}, now, now)
	if switched.PreviewToken == base.PreviewToken {
		t.Fatal("preview token survived the machine changing deployment profile")
	}
	// The lifecycle state and an in-flight billing claim belong to the same
	// identity: a machine being deleted, or one another worker is already
	// re-terming, is not the machine the preview promised to keep.
	stateChanged, _ := buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, []types.CommercialActionWorker{{UID: shared.UID, Profile: shared.Profile, LifecycleState: "delete_pending"}}, now, now)
	if stateChanged.PreviewToken == base.PreviewToken {
		t.Fatal("preview token survived the machine's lifecycle state changing")
	}
	claimed, _ := buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, []types.CommercialActionWorker{{UID: shared.UID, Profile: shared.Profile, LifecycleState: "active", BillingAction: "expire"}}, now, now)
	if claimed.PreviewToken == base.PreviewToken {
		t.Fatal("preview token survived a billing claim being taken on the machine")
	}
	// An account with no machine keeps the historical (pre-field) preview shape so
	// already-journaled requests keep their durable token.
	empty, err := buildCommercialActionPreview(request(types.CloudWorkerPackedShared), s, nil, now, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(empty)
	if strings.Contains(string(raw), "worker_machines") || strings.Contains(string(raw), "worker_matching_count") {
		t.Fatalf("machine fields changed the shape of an empty preview: %s", raw)
	}
}

// An ensure may only keep a machine it is allowed to adopt. A same-class machine
// that is being destroyed, or that another worker is already re-terming, is not
// adoptable - and an adoptable machine of a *different* class must not make the
// account look like it has one to keep.
func TestCommercialActionPreviewRefusesUnadoptableMachines(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := func() types.CommercialActionRequest {
		r := types.CommercialActionRequest{UID: 7, Intent: "ensure_worker", DeploymentProfile: types.CloudWorkerPackedShared, DurationDays: 3, Preview: true, Note: "ensure"}
		if err := r.Normalize(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	shared := func(state, billing string) types.CommercialActionWorker {
		return types.CommercialActionWorker{UID: 3, Profile: types.CloudWorkerPackedShared, LifecycleState: state, BillingAction: billing}
	}
	ecs := types.CommercialActionWorker{UID: 1, Profile: types.CloudWorkerPrivateNAT}
	for _, tc := range []struct {
		name     string
		machines []types.CommercialActionWorker
	}{
		{"destroy in flight", []types.CommercialActionWorker{shared("delete_running", "")}},
		{"billing claim", []types.CommercialActionWorker{shared("active", "expire")}},
		// An adoptable machine of another class provisioned next to it does not
		// help: the operation is still about the shared machine this account owns.
		{"unadoptable next to an adoptable other class", []types.CommercialActionWorker{ecs, shared("delete_running", "")}},
	} {
		p, err := buildCommercialActionPreview(request(), &types.CommercialSummary{}, tc.machines, now, now)
		if err != nil {
			t.Fatal(err)
		}
		if p.CanApply || p.WorkerMatchingCount != 1 || p.WorkerEffect != "ensure_one" {
			t.Fatalf("%s: an unadoptable machine was accepted: apply=%v matching=%d effect=%s", tc.name, p.CanApply, p.WorkerMatchingCount, p.WorkerEffect)
		}
		if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[len(p.Warnings)-1], "核对机器状态") {
			t.Fatalf("%s: no operator-visible reconciliation warning: %v", tc.name, p.Warnings)
		}
	}
	// delete_pending and delete_failed are still adoptable: the sweeper paused, it
	// did not destroy, so the machine may be kept and revived.
	for _, state := range []string{"delete_pending", "delete_failed"} {
		p, err := buildCommercialActionPreview(request(), &types.CommercialSummary{}, []types.CommercialActionWorker{shared(state, "")}, now, now)
		if err != nil {
			t.Fatal(err)
		}
		if !p.CanApply || p.WorkerEffect != "keep_existing" {
			t.Fatalf("%s machine was not adoptable: %+v", state, p)
		}
	}
}

// A package whose recorded deployment profile this build does not know cannot be
// provisioned safely. The preview must refuse it instead of defaulting it to one
// of the known products, because the default would buy the buyer the wrong
// machine.
func TestCommercialActionPreviewRefusesUnknownPackageDeployment(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := &types.CommercialPlan{ID: 11, Slug: "ops-unknown", DurationDays: 30, MonthlyBudget: 10,
		ModelBudgets: map[string]float64{"model": 4}, CloudWorkerIncluded: true,
		CloudWorkerDeploymentProfile: "mystery_fleet"}
	r := types.CommercialActionRequest{UID: 7, Intent: "grant_plan", PlanID: 11, WorkerAction: "ensure", Preview: true, Note: "unknown fleet"}
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	p, err := buildCommercialActionPreview(r, &types.CommercialSummary{Plans: []*types.CommercialPlan{plan}}, nil, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.CanApply {
		t.Fatalf("a package with an unknown deployment profile was accepted: %+v", p)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "部署类型未知") {
		t.Fatalf("no operator-visible warning about the unknown deployment: %v", p.Warnings)
	}
}

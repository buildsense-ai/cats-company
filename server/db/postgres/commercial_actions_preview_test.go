package postgres

import (
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
		p, err := buildCommercialActionPreview(r, s, 2, now, now)
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
	p, err := buildCommercialActionPreview(r, s, 0, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if !p.CanApply || p.CurrentPlan.ID != 1 || !p.PreviousExpiresAt.Equal(segment) || !p.ExpiresAt.Equal(segment.AddDate(0, 0, 30)) || p.NextTotalCNY != 25 {
		t.Fatalf("wrong primary/period: %+v", p)
	}
	s.Entitlements = s.Entitlements[2:]
	r.DurationDays = 12
	p, _ = buildCommercialActionPreview(r, s, 0, now, now)
	if !p.CanApply || p.CurrentPlan.ID != 2 || !p.ExpiresAt.Equal(legacyExpiry.AddDate(0, 0, 12)) {
		t.Fatalf("finite legacy: %+v", p)
	}
	s.Entitlements[0].ExpiresAt = nil
	p, _ = buildCommercialActionPreview(r, s, 0, now, now)
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
	base, _ := buildCommercialActionPreview(r, s, 0, now, now)
	for _, change := range []func(){func() { r.Note = "other" }, func() { r.DurationDays = 8 }, func() { r.WorkerAction = "additional" }, func() { plan.ModelBudgets["model"] = 3 }, func() { s.Entitlements[0].ID = 9 }, func() { s.TotalCNY = 11 }} {
		change()
		p, _ := buildCommercialActionPreview(r, s, 0, now, now)
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
	p, _ := buildCommercialActionPreview(r, &types.CommercialSummary{TotalCNY: 15}, 0, now, now)
	if !p.CanApply || p.PackageEffect != "none" || p.NextTotalCNY != 15 || !p.WorkerExpiresAt.Equal(now.AddDate(0, 0, 2)) {
		t.Fatalf("test worker: %+v", p)
	}
	r.Intent = "ensure_worker"
	r.DurationDays = 0
	r.WorkerAction = ""
	if err := r.Normalize(); err != nil {
		t.Fatal(err)
	}
	p, _ = buildCommercialActionPreview(r, &types.CommercialSummary{}, 0, now, now)
	if p.CanApply {
		t.Fatal("unbounded machine permitted")
	}
	p, _ = buildCommercialActionPreview(r, &types.CommercialSummary{}, 1, now, now)
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
	p, err := buildCommercialActionPreview(r, s, 0, anchor, anchor)
	february := time.Date(2027, time.February, 28, 10, 0, 0, 123000, time.UTC)
	if err != nil || !p.CanApply || !p.ExpiresAt.Equal(february) || p.NextTotalCNY != 0 || p.WorkerEffect != "none" {
		t.Fatalf("credit-only grant: %+v %v", p, err)
	}
	if commercialPlanHasQuota(plan) {
		t.Fatal("credit eligibility leaked into legacy quota validation")
	}
	baseToken := p.PreviewToken
	plan.ModelCreditUnits++
	p, _ = buildCommercialActionPreview(r, s, 0, anchor, anchor)
	if p.PreviewToken == baseToken {
		t.Fatal("preview token did not bind credit allowance")
	}
	plan.ModelCreditUnits--
	for _, mutate := range []func(){func() { plan.State = 1 }, func() { plan.State = 0; plan.ModelCreditUnits = 0 }} {
		mutate()
		p, _ = buildCommercialActionPreview(r, s, 0, anchor, anchor)
		if p.CanApply {
			t.Fatal("disabled or empty plan was accepted")
		}
	}
	plan.ModelCreditUnits = 22_500_000_000
	s.Entitlements = []*types.CommercialEntitlement{{ID: 1, PlanID: plan.ID, Source: "admin", State: "active", StartsAt: anchor, ExpiresAt: &february, PeriodAnchorAt: &anchor}}
	r.Intent = "renew_plan"
	p, _ = buildCommercialActionPreview(r, s, 0, anchor.Add(time.Hour), anchor.Add(time.Hour))
	march := time.Date(2027, time.March, 31, 10, 0, 0, 123000, time.UTC)
	if !p.CanApply || !p.ExpiresAt.Equal(march) || p.NextTotalCNY != 0 || p.WorkerEffect != "none" {
		t.Fatalf("anchored renewal: %+v", p)
	}
	// The original segment is now expired and absent from the snapshot; the
	// surviving segment must carry January's anchor through February's clamp.
	s.Entitlements[0].StartsAt = february
	s.Entitlements[0].ExpiresAt = &march
	p, _ = buildCommercialActionPreview(r, s, 0, february.Add(time.Hour), february.Add(time.Hour))
	if !p.CanApply || !p.ExpiresAt.Equal(time.Date(2027, time.April, 30, 10, 0, 0, 123000, time.UTC)) {
		t.Fatalf("renewal lost original anchor: %+v", p)
	}
	r.DurationDays = 30
	p, _ = buildCommercialActionPreview(r, s, 0, february.Add(time.Hour), february.Add(time.Hour))
	if p.CanApply {
		t.Fatal("credit renewal accepted a fixed-day override")
	}
}

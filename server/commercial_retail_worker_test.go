package server

import (
	"fmt"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestCommercialRetailCreditWorkerContract(t *testing.T) {
	enableCreditPlanSale(t)
	handler := &CommercialPaymentHandler{publicEnabled: true}
	for tier := 1; tier <= 3; tier++ {
		slug := fmt.Sprintf("catsco-credit-v%d-monthly", tier)
		t.Run(slug, func(t *testing.T) {
			plan := creditOnlyTestPlan()
			plan.Slug = slug
			plan.PlanTier = tier
			plan.CloudWorkerIncluded = true
			plan.CloudWorkerDeploymentProfile = "packed_shared"
			for _, machine := range []string{"light", "standard", "pro", "heavy", ""} {
				plan.CloudWorkerRuntimePlan = machine
				want := machine == "light"
				if got := handler.planVisibleFor(1, plan); got != want {
					t.Fatalf("machine=%q visible=%v want=%v", machine, got, want)
				}
				if got := commercialRetailWorkerConfigurationValid(plan.Slug, plan.CloudWorkerIncluded, plan.CloudWorkerDeploymentProfile, machine); got != want {
					t.Fatalf("machine=%q configuration valid=%v want=%v", machine, got, want)
				}
				if got := planRuntimeTier([]*types.CommercialPlan{plan}, plan.ID); got != "" {
					t.Fatalf("model purchase would resize existing machines to %q", got)
				}
			}
			plan.CloudWorkerRuntimePlan = "light"
			plan.CloudWorkerIncluded = false
			if handler.planVisibleFor(1, plan) {
				t.Fatal("retail package missing included worker is sellable")
			}
			plan.CloudWorkerIncluded = true
			plan.CloudWorkerDeploymentProfile = "private_nat"
			if handler.planVisibleFor(1, plan) {
				t.Fatal("retail package with dedicated worker is sellable")
			}
		})
	}
}

func TestCommercialTeamWorkerContractUnchanged(t *testing.T) {
	plan := legacyBudgetTestPlan()
	plan.Slug = "catsco-team"
	plan.CloudWorkerIncluded = true
	plan.CloudWorkerDeploymentProfile = "packed_shared"
	plan.CloudWorkerRuntimePlan = "heavy"
	if !commercialRetailWorkerConfigurationValid(plan.Slug, true, "packed_shared", "heavy") {
		t.Fatal("team worker configuration rejected")
	}
	if got := planRuntimeTier([]*types.CommercialPlan{plan}, plan.ID); got != "heavy" {
		t.Fatalf("team machine tier=%q want heavy", got)
	}
	if !(&CommercialPaymentHandler{publicEnabled: true}).planVisibleFor(1, plan) {
		t.Fatal("team sale unexpectedly changed")
	}
}

func TestCommercialOrderWorkerTierDoesNotBorrowCurrentPlan(t *testing.T) {
	team := &types.CommercialPlan{ID: 806, Slug: "catsco-team", CloudWorkerRuntimePlan: "heavy"}
	retail := &types.CommercialPlan{ID: 846, Slug: "catsco-credit-v3-monthly", CloudWorkerRuntimePlan: "light"}
	storage := &retailPaymentSummaryStore{commercialPaymentTestStore: newCommercialPaymentTestStore()}
	storage.summary = &types.CommercialSummary{
		Plans:              []*types.CommercialPlan{team, retail},
		CurrentEntitlement: &types.CommercialEntitlement{PlanID: team.ID},
	}
	handler := &CommercialPaymentHandler{store: storage}
	for _, planID := range []int64{retail.ID, 9999} {
		if got := handler.cloudWorkerTierForOrder(&types.CommercialOrder{UID: 38, PlanID: planID}); got != "" {
			t.Fatalf("order plan %d borrowed machine tier %q", planID, got)
		}
	}
	if got := handler.cloudWorkerTierForOrder(&types.CommercialOrder{UID: 38, PlanID: team.ID}); got != "heavy" {
		t.Fatalf("team order machine tier = %q, want heavy", got)
	}
}

type retailPaymentSummaryStore struct {
	*commercialPaymentTestStore
	summary *types.CommercialSummary
}

func (s *retailPaymentSummaryStore) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	return s.summary, nil
}

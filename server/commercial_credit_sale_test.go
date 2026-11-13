package server

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func creditOnlyTestPlan() *types.CommercialPlan {
	return &types.CommercialPlan{
		ID:               9,
		Slug:             "v1-entry",
		Name:             "V1 入门",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "public",
		State:            0,
		DurationDays:     30,
		PlanTier:         1,
		ModelCreditUnits: 4_000 * 1_000_000,
	}
}

func legacyBudgetTestPlan() *types.CommercialPlan {
	return &types.CommercialPlan{
		ID:            10,
		Slug:          "legacy-budget",
		Name:          "Legacy Budget",
		PriceFen:      5900,
		Currency:      "CNY",
		SaleState:     "public",
		State:         0,
		DurationDays:  30,
		ModelBudgets:  map[string]float64{"deepseek-flash": 20},
	}
}

func TestCommercialCreditOnlyPlanDetection(t *testing.T) {
	if !commercialCreditOnlyPlan(creditOnlyTestPlan()) {
		t.Fatal("a package that only sells model credit must be recognised as credit-only")
	}
	if commercialCreditOnlyPlan(legacyBudgetTestPlan()) {
		t.Fatal("a CNY budget package is not credit-only")
	}
	mixed := creditOnlyTestPlan()
	mixed.MonthlyBudget = 5
	if commercialCreditOnlyPlan(mixed) {
		t.Fatal("credit and CNY budget are mutually exclusive")
	}
	if !commercialPlanHasBenefits(creditOnlyTestPlan()) {
		t.Fatal("credit units are a benefit and must keep the plan sellable")
	}
}

// credit-only 套餐默认不可售：没有运营开关、没有 relay 结算通道时，它既不能出现在
// 目录里，也不能下单，否则会卖成「付了钱却没有模型权限」的商品。
func TestCommercialCreditOnlyPlanRequiresBothGates(t *testing.T) {
	handler := &CommercialPaymentHandler{publicEnabled: true}
	plan := creditOnlyTestPlan()

	t.Setenv("CATSCO_CREDIT_PLAN_SALE_ENABLED", "")
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", "")
	if handler.planVisibleFor(1, plan) {
		t.Fatal("credit-only plans must stay hidden while the sale gate is off")
	}

	t.Setenv("CATSCO_CREDIT_PLAN_SALE_ENABLED", "true")
	if handler.planVisibleFor(1, plan) {
		t.Fatal("the sale flag alone must not sell credit before relay credit settlement is configured")
	}

	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if !handler.planVisibleFor(1, plan) {
		t.Fatal("an enabled credit-only plan must be visible once both gates are configured")
	}
}

func TestCommercialLegacyPlanUnaffectedByCreditGates(t *testing.T) {
	handler := &CommercialPaymentHandler{publicEnabled: true}
	t.Setenv("CATSCO_CREDIT_PLAN_SALE_ENABLED", "")
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", "")
	if !handler.planVisibleFor(1, legacyBudgetTestPlan()) {
		t.Fatal("the credit sale gate must not change how a legacy CNY budget package sells")
	}
	hidden := legacyBudgetTestPlan()
	hidden.SaleState = "hidden"
	if handler.planVisibleFor(1, hidden) {
		t.Fatal("sale state still decides visibility for legacy packages")
	}
}

// 邀请码的赠送额度是邀请自己的面额：默认 20 credit，与套餐的月额度无关（不是
// 22500/12000/4000），也不是套餐月额度的别名。
func TestCommercialInviteDefaultCreditIsIndependentOfPlan(t *testing.T) {
	if types.CommercialInviteDefaultCreditUnits != 20*1_000_000 {
		t.Fatalf("invite default gift = %d units, want 20 credit", types.CommercialInviteDefaultCreditUnits)
	}
	plan := creditOnlyTestPlan()
	plan.PlanTier = 3
	plan.ModelCreditUnits = 22_500 * 1_000_000
	invite := &types.CommercialInviteCode{
		PlanID:                  plan.ID,
		ModelCreditUnits:        types.CommercialInviteDefaultCreditUnits,
		ModelCreditUnitsInherit: false,
	}
	if invite.ModelCreditUnits == plan.ModelCreditUnits {
		t.Fatal("a V3 invite must not inherit the package's 22,500-credit monthly allowance")
	}
}

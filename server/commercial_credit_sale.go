package server

import (
	"os"
	"regexp"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

// credit-only 套餐（只卖 model credit、不配 CNY/token 预算）能不能公开售卖，
// 由两个开关共同决定，默认关闭：
//
//  1. CATSCO_CREDIT_PLAN_SALE_ENABLED 显式开启（部署/env 配置商品时才打开）。
//  2. relay 侧 credit 结算已经接好：CATSCO_RELAY_CREDIT_PEER_SECRET 是合法的
//     64 位十六进制密钥，说明 eligibility / reserve / settle 通道已部署。缺了它，
//     credit-only 套餐会卖成「付了钱却没有模型权限」的商品。
//
// 旧 CNY/token 预算套餐与 credit 套餐严格互斥：一个套餐要么配 model_credit_units，
// 要么配 monthly_budget/model_budgets，不能同时配置（服务端保存时就拒绝）。
//
// 这个门禁只影响「卖」，不影响已经卖出去的订阅：账本、月额度周期对账和邀请
// 赠送额度都不看这个开关。
var commercialCreditPeerSecretPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// commercialCreditSettlementConfigured 报告 relay 侧 credit 结算通道是否已配置。
func commercialCreditSettlementConfigured() bool {
	return commercialCreditPeerSecretPattern.MatchString(strings.TrimSpace(os.Getenv("CATSCO_RELAY_CREDIT_PEER_SECRET")))
}

// commercialCreditPlanSaleEnabled 报告运营是否显式开启 credit-only 销售。
func commercialCreditPlanSaleEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_CREDIT_PLAN_SALE_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// commercialCreditPlanSaleGatesOpen 是两个开关的合并判断，保存套餐与下单共用。
func commercialCreditPlanSaleGatesOpen() bool {
	return commercialCreditPlanSaleEnabled() && commercialCreditSettlementConfigured()
}

// commercialCreditOnlyPlan 判断套餐是否只卖 credit：有 model credit、且没有旧
// CNY/token 预算。
func commercialCreditOnlyPlan(plan *types.CommercialPlan) bool {
	return plan != nil && plan.ModelCreditUnits > 0 && plan.MonthlyBudget <= 0 &&
		!commercialModelBudgetsConfigured(plan.ModelBudgets)
}

// commercialCreditPlanSellable 是 credit-only 套餐的销售门禁：产品本身合法，
// 并且运营开关与 relay 结算通道都已就绪。
func commercialCreditPlanSellable(plan *types.CommercialPlan) bool {
	return commercialCreditOnlyPlan(plan) && commercialCreditPlanSaleGatesOpen() &&
		commercialRetailWorkerConfigurationValid(plan.Slug, plan.CloudWorkerIncluded, plan.CloudWorkerDeploymentProfile, plan.CloudWorkerRuntimePlan)
}

// The current retail model-credit ladder includes the same 1/20 shared worker.
// Larger machine tiers are separate future purchases; team and custom plans keep
// their own resource configuration.
func commercialRetailCreditPlan(slug string) bool {
	switch strings.TrimSpace(slug) {
	case "catsco-credit-v1-monthly", "catsco-credit-v2-monthly", "catsco-credit-v3-monthly":
		return true
	default:
		return false
	}
}

func commercialRetailWorkerConfigurationValid(slug string, included bool, profile, runtimePlan string) bool {
	if !commercialRetailCreditPlan(slug) {
		return true
	}
	return included && strings.TrimSpace(profile) == "packed_shared" && strings.TrimSpace(runtimePlan) == "light"
}

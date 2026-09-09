package server

import (
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const commercialTerraTrialModel = "gpt-5.6-terra"

type commercialRelayTerraTrial struct {
	Enabled       bool    `json:"enabled"`
	MaxLimit      float64 `json:"max_limit"`
	CurrentUsage  float64 `json:"current_usage"`
	ResetDuration string  `json:"reset_duration"`
}

// A paid/internal entitlement wins over the Free trial even when both remain
// in the ledger. Future renewals do not change the current access policy.
func commercialFreeTerraTrialEnabled(summary *types.CommercialSummary, now time.Time) bool {
	if summary == nil {
		return false
	}
	free := false
	for _, entitlement := range summary.Entitlements {
		if entitlement == nil || entitlement.State != "active" || entitlement.StartsAt.After(now) ||
			(entitlement.ExpiresAt != nil && !entitlement.ExpiresAt.After(now)) {
			continue
		}
		if entitlement.PlanSlug == "catsco-free" || entitlement.Source == "free" {
			free = true
		} else {
			return false
		}
	}
	// Explicit operator/model grants must also restore ordinary Terra access.
	for _, grant := range summary.Grants {
		if grant != nil && grant.AmountCNY > 0 && grant.GrantType != "free" && grant.RevokedAt == nil &&
			!grant.EffectiveAt.After(now) && (grant.ExpiresAt == nil || grant.ExpiresAt.After(now)) &&
			(strings.EqualFold(grant.Model, commercialTerraTrialModel) || grant.Model == "*") {
			return false
		}
	}
	return free
}

// Trial access is a separate wallet, not a recurring commercial quota grant.
// Only the reconciliation view receives this model; the shared total is kept.
func commercialSummaryWithTerraTrial(summary *types.CommercialSummary) *types.CommercialSummary {
	copySummary := *summary
	copySummary.TotalCNY = commercialRelaySharedLimit(summary)
	copySummary.TotalsByModel = make(map[string]float64, len(summary.TotalsByModel)+1)
	for model, amount := range summary.TotalsByModel {
		copySummary.TotalsByModel[model] = amount
	}
	copySummary.TotalsByModel[commercialTerraTrialModel] = 100
	copySummary.Grants = append(append([]*types.CommercialQuotaGrant{}, summary.Grants...),
		&types.CommercialQuotaGrant{Model: commercialTerraTrialModel, AmountCNY: 100, GrantType: "free_trial"})
	return &copySummary
}

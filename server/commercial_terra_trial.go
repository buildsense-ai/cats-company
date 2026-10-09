package server

import (
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// commercialTerraTrialModel is the retired trial model id. It stays so
// historical ledger rows and relay payloads remain decodable.
const commercialTerraTrialModel = "gpt-5.6-terra"

type commercialRelayTerraTrial struct {
	Enabled       bool    `json:"enabled"`
	MaxLimit      float64 `json:"max_limit"`
	CurrentUsage  float64 `json:"current_usage"`
	ResetDuration string  `json:"reset_duration"`
}

// A paid/internal entitlement wins over the Free trial even when both remain
// in the ledger. Future renewals do not change the current access policy.
//
// Retired 2026-10-08: the trial's model (gpt-5.6-terra) left the relay, so no
// account is eligible any more. The function stays as the single gate every
// caller already uses; it now returns false unconditionally, which also makes
// the next reconcile sync `free_terra_trial: false` to the relay for every
// account that still carried the historical flag.
func commercialFreeTerraTrialEnabled(summary *types.CommercialSummary, now time.Time) bool {
	return false
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

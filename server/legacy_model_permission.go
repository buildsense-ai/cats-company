package server

import (
	"github.com/openchat/openchat/server/store/types"
)

// legacyModelSummaryReader supplies only currently effective, unrevoked grants
// and token entitlements. Never infer legacy access from credit identity or from
// the mere presence of an active (possibly credit-only) package.
type legacyModelSummaryReader interface {
	GetCommercialSummary(int64) (*types.CommercialSummary, error)
}

func legacyQuotaModelAllowed(summary *types.CommercialSummary, modelID string) bool {
	if summary == nil || isRetiredCatalogModel(modelID) {
		return false
	}
	target := normalizeRelayModelName(resolveLegacyCatalogModelID(modelID))
	if target == "" {
		return false
	}
	// Free signup baselines do not turn a credit account into a legacy account.
	// Keep purchased, migrated and independently granted legacy quotas intact.
	totals := summary.TotalsByModel
	if len(summary.Grants) > 0 {
		totals = make(map[string]float64)
		for _, grant := range summary.Grants {
			if grant != nil && grant.GrantType != "free" {
				amount := grant.AmountCNY
				if grant.GrantType == "adjustment_debit" {
					amount = -amount
				}
				totals[grant.Model] += amount
			}
		}
	}
	hasQuota := false
	for _, amount := range totals {
		hasQuota = hasQuota || amount > 0
	}
	if target == normalizeRelayModelName(deepSeekPublicModelID) && (hasQuota || summary.TokenQuota > 0) {
		return true
	}
	for candidate, amount := range totals {
		if amount <= 0 || isRetiredCatalogModel(candidate) {
			continue
		}
		if candidate == "*" || normalizeRelayModelName(resolveLegacyCatalogModelID(candidate)) == target {
			return true
		}
	}
	return false
}

// legacyModelsForSummary is shared by the peer protocol, owner catalog and
// selection validation. Enumerate the website catalog so aliases and retired
// models follow exactly the same public model names. Always return [] not null.
func legacyModelsForSummary(summary *types.CommercialSummary) []string {
	models := make([]string, 0)
	for _, item := range botModelCatalog {
		if legacyQuotaModelAllowed(summary, item.ID) {
			models = append(models, item.ID)
		}
	}
	return models
}

func readLegacyModelSummary(source interface{}, uid int64) (*types.CommercialSummary, error) {
	if reader, ok := source.(legacyModelSummaryReader); ok && reader != nil {
		return reader.GetCommercialSummary(uid)
	}
	return nil, nil
}

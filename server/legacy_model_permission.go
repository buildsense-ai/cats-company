package server

import (
	"context"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// legacyModelSummaryReader supplies only currently effective, unrevoked grants
// and token entitlements. Never infer legacy access from credit identity or from
// the mere presence of an active (possibly credit-only) package.
type legacyModelSummaryReader interface {
	GetCommercialSummary(int64) (*types.CommercialSummary, error)
}

func legacyQuotaModelAllowed(summary *types.CommercialSummary, modelID string) bool {
	if types.IsGPT62Sol(modelID) {
		return gpt62LegacyQuota(summary, time.Now()) > 0
	}
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

// gpt62LegacyQuota must come from a live package and its own explicit grant.
// Neither a wildcard nor a manually granted model can authorize this route.
func gpt62LegacyQuota(summary *types.CommercialSummary, now time.Time) float64 {
	if summary == nil || summary.TotalsByModel[types.GPT62Sol] <= 0 {
		return 0
	}
	plans := make(map[int64]string)
	for _, plan := range summary.Plans {
		if plan != nil && plan.State == 0 && plan.ModelCreditUnits == 0 && types.GPT62SolLegacyPlan(plan.Slug) && plan.ModelBudgets[types.GPT62Sol] > 0 {
			plans[plan.ID] = plan.Slug
		}
	}
	var amount float64
	for _, grant := range summary.Grants {
		if grant == nil || grant.Model != types.GPT62Sol || grant.AmountCNY <= 0 || grant.RevokedAt != nil ||
			grant.EffectiveAt.After(now) || grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) || plans[grant.PlanID] == "" {
			continue
		}
		source := grant.GrantType
		switch source {
		case "order", "invite":
		case "operator_plan":
			source = "operator"
		default:
			continue
		}
		for _, ent := range summary.Entitlements {
			if ent != nil && ent.PlanID == grant.PlanID && ent.PlanSlug == plans[grant.PlanID] && ent.Source == source &&
				ent.SourceRef == grant.SourceRef && ent.State == "active" && !ent.StartsAt.After(now) &&
				(ent.ExpiresAt == nil || ent.ExpiresAt.After(now)) {
				amount += grant.AmountCNY
				break
			}
		}
	}
	return min(amount, summary.TotalsByModel[types.GPT62Sol])
}

func gpt62CatalogEnabled(ctx context.Context, catalog *CommercialModelCatalog) bool {
	return catalog.GPT62SolEnabled(ctx)
}

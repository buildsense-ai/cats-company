package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type commercialGrantModelOption struct {
	ID        string     `json:"id"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type commercialGrantOptions struct {
	Models         []commercialGrantModelOption `json:"models"`
	RequiresExpiry bool                         `json:"requires_expiry"`
	SharedPool     bool                         `json:"shared_pool"`
	Error          string                       `json:"error,omitempty"`
}

// Only the effective legacy package may expand its model set. A retained legacy
// baseline must not bypass the restrictions of a newer paid package.
func commercialLegacyGrantEligible(summary *types.CommercialSummary, now time.Time) bool {
	if summary == nil {
		return false
	}
	primary := types.PrimaryCommercialEntitlement(summary.Entitlements, now)
	return primary != nil && primary.Source == "legacy" && primary.PlanSlug == "catsco-legacy-custom"
}

func commercialGrantModels(summary *types.CommercialSummary, relayUser *commercialRelayUsageUser, now time.Time) []commercialGrantModelOption {
	options := []commercialGrantModelOption{}
	// Do not fall back to historical per-user limits: they may include retired models.
	if relayUser == nil || !relayUser.Configured {
		return options
	}
	legacy := commercialLegacyGrantEligible(summary, now)
	seen := map[string]bool{}
	for _, limit := range relayUser.Limits.AvailableModelLimits {
		model := strings.TrimSpace(limit.Model)
		key := strings.ToLower(model)
		if model == "" || model == "*" || key == "gpt-5.6-luna" || seen[key] || strings.TrimSpace(limit.Provider) == "" || len(limit.AllowedModels) == 0 {
			continue
		}
		option := commercialGrantModelOption{ID: model}
		if !legacy {
			canonical, expiry, err := resolveCommercialBonusGrant(summary, model, "", now)
			if err != nil {
				continue
			}
			option.ID, option.ExpiresAt = canonical, &expiry
		}
		seen[key] = true
		options = append(options, option)
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options
}

func (h *AccountAdminHandler) commercialGrantOptions(ctx context.Context, uid int64, summary *types.CommercialSummary) commercialGrantOptions {
	result := commercialGrantOptions{Models: []commercialGrantModelOption{}, SharedPool: h.commercialRelaySyncer.EnforcedFor(uid)}
	result.RequiresExpiry = commercialLegacyGrantEligible(summary, time.Now().UTC())
	if h.relayAdmin == nil {
		result.Error = "模型目录暂不可用，请稍后重新加载账户。"
		return result
	}
	relayUser, err := fetchRelayLimitsForUID(ctx, h.relayAdmin, uid)
	if err != nil || relayUser == nil || !relayUser.Configured || len(relayUser.Limits.AvailableModelLimits) == 0 {
		result.Error = "无法读取该用户的可用模型目录，请检查执行层后重新加载账户。"
		return result
	}
	if result.RequiresExpiry && !result.SharedPool {
		result.Error = "内部保留套餐需要先启用共享额度执行，才能通过此入口增加模型。"
		return result
	}
	result.Models = commercialGrantModels(summary, relayUser, time.Now().UTC())
	if len(result.Models) == 0 {
		result.Error = "当前套餐没有可补额的模型；普通套餐需有有效到期时间且已包含该模型。"
	}
	return result
}

func resolveCommercialLegacyGrant(options commercialGrantOptions, model, rawExpiry string, now time.Time) (string, time.Time, error) {
	if options.Error != "" {
		return "", time.Time{}, fmt.Errorf("%s", options.Error)
	}
	if !options.RequiresExpiry || !options.SharedPool {
		return "", time.Time{}, fmt.Errorf("legacy shared package is required")
	}
	canonical := ""
	for _, option := range options.Models {
		if strings.EqualFold(option.ID, strings.TrimSpace(model)) {
			canonical = option.ID
			break
		}
	}
	if canonical == "" {
		return "", time.Time{}, fmt.Errorf("model is not available for this user")
	}
	expiry, err := time.Parse(time.RFC3339, strings.TrimSpace(rawExpiry))
	if err != nil || !expiry.After(now) {
		return "", time.Time{}, fmt.Errorf("内部保留套餐补额必须指定未来的到期时间。")
	}
	return canonical, expiry.UTC(), nil
}

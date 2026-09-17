package postgres

import (
	"strings"
	"testing"
)

// The postgres copy of the official paid-plan validator mirrors the server
// copy; this test pins the same ten-model set and totals so the two copies
// cannot drift apart unnoticed.
func TestValidateCommercialOfficialPaidPlanModelsTracksImageAddOns(t *testing.T) {
	personal := map[string]float64{
		"MiniMax-M2.7": 1750, "MiniMax-M3": 1750, "deepseek-v4-flash": 1750, "deepseek-flash": 1750,
		"glm-5.3-flash": 1750, "gpt-5.6-terra": 1750,
		"gpt-image-2": 100, "gpt-image-2.5": 100, "gpt-image-2.5-flare": 100, "gpt-image-2.5-sunburst": 100,
		"chatgpt-image-latest": 100,
	}
	if err := validateCommercialOfficialPaidPlanModels(commercialPersonalPlanSlug, personal); err != nil {
		t.Fatalf("complete personal plan rejected: %v", err)
	}
	personal["gpt-image-2"] = 99
	if err := validateCommercialOfficialPaidPlanModels(commercialPersonalPlanSlug, personal); err == nil {
		t.Fatal("mismatched total was accepted")
	}
	personal["gpt-image-2"] = 100

	pro := map[string]float64{
		"MiniMax-M2.7": 5250, "MiniMax-M3": 5250, "deepseek-v4-flash": 5250, "deepseek-flash": 5250,
		"glm-5.3-flash": 5250, "gpt-5.6-terra": 5250,
		"gpt-image-2": 300, "gpt-image-2.5": 300, "gpt-image-2.5-flare": 300, "gpt-image-2.5-sunburst": 300,
		"chatgpt-image-latest": 300,
	}
	if err := validateCommercialOfficialPaidPlanModels(commercialProPlanSlug, pro); err != nil {
		t.Fatalf("complete pro plan rejected: %v", err)
	}
	delete(pro, "chatgpt-image-latest")
	pro["chatgpt-image-9"] = 300
	if err := validateCommercialOfficialPaidPlanModels(commercialProPlanSlug, pro); err == nil || !strings.Contains(err.Error(), "chatgpt-image-latest") {
		t.Fatalf("missing image model was accepted: %v", err)
	}
	if err := validateCommercialOfficialPaidPlanModels(commercialFreePlanSlug, map[string]float64{}); err != nil {
		t.Fatalf("free plan must stay unconstrained: %v", err)
	}
}

package server

import "strings"

// DeepSeek's public catalog identity is the native Flash model. The retired
// V4 ids stay accepted as legacy aliases so stored selections keep resolving
// to the current entry and are rewritten on the next save.
const deepSeekPublicModelID = "deepseek-flash"

// deepSeekLegacyModelIDs are retired public ids that must keep working while
// the relay still serves existing sessions.
var deepSeekLegacyModelIDs = []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp"}

// resolveLegacyCatalogModelID maps retired catalog ids onto the current public
// model. Unknown ids are returned unchanged.
func resolveLegacyCatalogModelID(modelID string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	for _, legacy := range deepSeekLegacyModelIDs {
		if normalized == legacy {
			return deepSeekPublicModelID
		}
	}
	return modelID
}

// catalogModelIDMatches compares catalog selections while accepting retired
// aliases, so an applied legacy id does not look like an unapplied change.
func catalogModelIDMatches(left, right string) bool {
	return resolveLegacyCatalogModelID(left) == resolveLegacyCatalogModelID(right)
}

// deepSeekModelCatalogItem is the CatsCompany boundary for DeepSeek product
// metadata. Future protocol, capability, or version changes belong here
// instead of adding DeepSeek branches to the generic bot catalog handlers.
func deepSeekModelCatalogItem() botModelCatalogItem {
	return botModelCatalogItem{
		ID: deepSeekPublicModelID, Label: "DeepSeek Flash", Description: "原生多模态，支持图片、工具调用与推理强度",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 1000000,
		ReasoningEfforts: []string{"low", "high", "max", "disabled"}, DefaultReasoningEffort: "high",
		Vision: true, RuntimeModel: deepSeekPublicModelID,
	}
}

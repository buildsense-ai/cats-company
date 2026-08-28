package server

// DeepSeek's public catalog identity is deliberately stable. XiaoBa and
// commercial quota accounting use this ID while Relay owns the mapping to the
// current upstream DeepSeek version.
const deepSeekPublicModelID = "deepseek-v4-flash"

// deepSeekModelCatalogItem is the CatsCompany boundary for DeepSeek product
// metadata. Future protocol, capability, or version changes belong here
// instead of adding DeepSeek branches to the generic bot catalog handlers.
func deepSeekModelCatalogItem() botModelCatalogItem {
	return botModelCatalogItem{
		ID: deepSeekPublicModelID, Label: "DeepSeek V4 Flash", Description: "低额度 Flash，支持推理强度与视觉理解",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 1000000,
		ReasoningEfforts: []string{"low", "high", "max", "disabled"}, DefaultReasoningEffort: "high",
		Vision: true, RuntimeModel: deepSeekPublicModelID,
	}
}

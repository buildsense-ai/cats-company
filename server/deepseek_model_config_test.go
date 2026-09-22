package server

import (
	"reflect"
	"testing"
)

func TestDeepSeekModelConfigOwnsStableResponsesContract(t *testing.T) {
	item := deepSeekModelCatalogItem()
	if item.ID != "deepseek-flash" || item.RuntimeModel != item.ID {
		t.Fatalf("DeepSeek public identity changed: id=%q runtime=%q", item.ID, item.RuntimeModel)
	}
	if item.Provider != "openai" || item.Protocol != "OpenAI Responses" {
		t.Fatalf("DeepSeek protocol changed: provider=%q protocol=%q", item.Provider, item.Protocol)
	}
	if !item.Vision || item.ContextWindowTokens != 1000000 {
		t.Fatalf("DeepSeek capabilities changed: vision=%v context=%d", item.Vision, item.ContextWindowTokens)
	}
	wantEfforts := []string{"low", "high", "max", "disabled"}
	if !reflect.DeepEqual(item.ReasoningEfforts, wantEfforts) || item.DefaultReasoningEffort != "high" {
		t.Fatalf("DeepSeek reasoning changed: efforts=%v default=%q", item.ReasoningEfforts, item.DefaultReasoningEffort)
	}
}

func TestDeepSeekModelConfigKeepsCatalogPosition(t *testing.T) {
	if len(botModelCatalog) < 3 || botModelCatalog[2].ID != deepSeekPublicModelID {
		t.Fatalf("DeepSeek catalog position changed: %#v", botModelCatalog)
	}
}

func TestDeepSeekModelConfigConvertsRetiredAliases(t *testing.T) {
	for _, legacy := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp", " DeepSeek-V4-Flash "} {
		if got := resolveLegacyCatalogModelID(legacy); got != deepSeekPublicModelID {
			t.Fatalf("alias %q resolved to %q", legacy, got)
		}
	}
	if got := resolveLegacyCatalogModelID("MiniMax-M3"); got != "MiniMax-M3" {
		t.Fatalf("unrelated id was rewritten: %q", got)
	}
	if !catalogModelIDMatches("deepseek-v4-flash", "deepseek-flash") {
		t.Fatal("applied legacy id must match the current catalog model")
	}
	if tokens, ok := catalogContextWindowTokens("deepseek-v4-flash"); !ok || tokens != 1000000 {
		t.Fatalf("legacy id lost its catalog context window: tokens=%d ok=%v", tokens, ok)
	}
	if descriptor := catalogRuntimeDescriptorForModel("deepseek-v4-flash"); descriptor == nil || descriptor.CatalogModelID != deepSeekPublicModelID {
		t.Fatalf("legacy id lost its runtime descriptor: %#v", descriptor)
	}
	count := 0
	for _, model := range botModelCatalog {
		if model.ID == "deepseek-flash" || model.ID == "deepseek-v4-flash" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("DeepSeek must be offered exactly once: count=%d", count)
	}
}

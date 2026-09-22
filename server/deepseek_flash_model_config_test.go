package server

import "testing"

func TestDeepSeekFlashIndependentResponsesSelection(t *testing.T) {
	item, effort, ok := normalizeBotModelSelection("deepseek-flash", "high")
	if !ok || item.ID != "deepseek-flash" || effort != "high" {
		t.Fatalf("native Flash selection unavailable: %#v %q %v", item, effort, ok)
	}
	runtime := catalogRuntimeDescriptor(item)
	if runtime == nil || runtime.Model != "deepseek-flash" || runtime.OpenAIAPIMode != "responses" || !runtime.Vision {
		t.Fatalf("incorrect native Flash runtime: %#v", runtime)
	}
	legacy, _, legacyOK := normalizeBotModelSelection("deepseek-v4-flash", "high")
	if !legacyOK || legacy.ID != item.ID {
		t.Fatal("retired V4 selection must resolve to the current Flash model")
	}
}

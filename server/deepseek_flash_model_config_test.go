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
	old, _, oldOK := normalizeBotModelSelection("deepseek-v4-flash", "high")
	if !oldOK || old.ID == item.ID {
		t.Fatal("old selection must remain independent until retirement")
	}
}

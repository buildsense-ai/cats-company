package server

import (
	"reflect"
	"testing"
)

func TestDeepSeekModelConfigOwnsStableResponsesContract(t *testing.T) {
	item := deepSeekModelCatalogItem()
	if item.ID != "deepseek-v4-flash" || item.RuntimeModel != item.ID {
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

package mysql

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMySQLServiceAgentDefinitionHello(t *testing.T) {
	db := mysqlTestAdapter(t)
	seedHelloFixture(t, db)
	ctx := context.Background()
	id, _, _, _, err := db.GetServiceAgentBySlug(ctx, "hello")
	if err != nil {
		t.Fatalf("get hello: %v", err)
	}
	slug, threshold, definition, err := db.GetServiceAgentDefinition(ctx, id, 0)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if slug != "hello" || threshold != 60 {
		t.Fatalf("slug=%q threshold=%d, want hello/60", slug, threshold)
	}
	var parsed map[string]any
	if err := json.Unmarshal(definition, &parsed); err != nil {
		t.Fatalf("definition json: %v", err)
	}
	models, _ := parsed["models"].([]any)
	if len(models) != 1 || models[0] != "gpt-6.1-sol" {
		t.Fatalf("models = %+v", parsed["models"])
	}
	// 指定不存在的版本 → 回退最新（会话钉定兑底语义）
	if _, _, _, err := db.GetServiceAgentDefinition(ctx, id, 9999); err != nil {
		t.Fatalf("missing version should fall back to latest: %v", err)
	}
	if _, _, _, err := db.GetServiceAgentDefinition(ctx, 999999, 0); err == nil {
		t.Fatal("missing agent should error")
	}
}

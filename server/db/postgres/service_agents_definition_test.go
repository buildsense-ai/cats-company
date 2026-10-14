package postgres

import (
	"context"
	"encoding/json"
	"testing"
)

func TestServiceAgentDefinitionHello(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	ctx := context.Background()
	id, _, _, _, err := db.GetServiceAgentBySlug(ctx, "hello")
	if err != nil {
		t.Fatalf("get hello: %v", err)
	}
	slug, threshold, definition, err := db.GetServiceAgentDefinition(ctx, id)
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
	if _, _, _, err := db.GetServiceAgentDefinition(ctx, 999999); err == nil {
		t.Fatal("missing agent should error")
	}
}

package postgres

import (
	"context"
	"testing"
)

func TestServiceListAgentsIncludesHello(t *testing.T) {
	db := pgTestDB(t)
	seedHelloAgent(t, db)
	agents, err := db.ListServiceAgents(context.Background())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %+v, want only hello", agents)
	}
	if agents[0].Slug != "hello" || agents[0].Version != 1 || agents[0].AsyncThresholdSeconds != 60 {
		t.Fatalf("hello agent = %+v", agents[0])
	}
}

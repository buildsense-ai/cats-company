package mysql

import (
	"context"
	"testing"
)

func TestMySQLServiceListAgentsIncludesHello(t *testing.T) {
	db := mysqlTestAdapter(t)
	seedHelloFixture(t, db)
	agents, err := db.ListServiceAgents(context.Background())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	found := false
	for _, agent := range agents {
		if agent.Slug == "hello" {
			found = true
			if agent.Version != 1 || agent.AsyncThresholdSeconds != 60 {
				t.Fatalf("hello agent = %+v", agent)
			}
		}
	}
	if !found {
		t.Fatalf("hello agent missing from %+v", agents)
	}
}

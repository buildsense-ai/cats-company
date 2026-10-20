package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// 归属与可见性（000045 / 设计 §4.3 P2）。

func TestServiceAgentOwnershipSchemaConstants(t *testing.T) {
	for _, want := range []string{"owner_kind", "owner_uid", "visibility"} {
		if !strings.Contains(migrateServiceAgentsAddOwnership, want) {
			t.Fatalf("ownership migration missing %q", want)
		}
	}
	if !strings.Contains(createServiceAgentsOwnerIndex, "idx_service_agents_owner") {
		t.Fatal("missing owner index")
	}
}

func TestServiceAgentOwnershipMigrationFilesReversible(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000045_service_agents_ownership.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"owner_kind", "owner_uid", "visibility", "idx_service_agents_owner"} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("up migration missing %q", want)
		}
	}
	down, err := os.ReadFile("../migrations/postgres/000045_service_agents_ownership.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DROP COLUMN IF EXISTS owner_uid", "DROP INDEX IF EXISTS idx_service_agents_owner"} {
		if !strings.Contains(string(down), want) {
			t.Fatalf("down migration missing %q: %s", want, down)
		}
	}
}

// Claim 语义：新占位 → 同 owner 幂等刷新 → 他人被拒 → 平台内部 slug 保留。
func TestServiceAgentOwnershipClaim(t *testing.T) {
	db := pgTestDB(t)
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	adapter := db
	ctx := context.Background()

	slug := "own-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	slug = strings.ToLower(slug)

	if err := adapter.ClaimServiceAgentOwnership(ctx, slug, "归属测试", store.ServiceAgentOwnerUser, 441, store.ServiceAgentVisibilityPrivate); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	agents, err := adapter.ListServiceAgents(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, agent := range agents {
		if agent.Slug != slug {
			continue
		}
		found = true
		if agent.OwnerKind != store.ServiceAgentOwnerUser || agent.OwnerUID != 441 {
			t.Fatalf("agent ownership = %s/%d", agent.OwnerKind, agent.OwnerUID)
		}
		if agent.Visibility != store.ServiceAgentVisibilityPrivate {
			t.Fatalf("visibility = %s", agent.Visibility)
		}
	}
	if !found {
		t.Fatalf("claimed agent not listed: %+v", agents)
	}

	// 同 owner 幂等 + 刷新可见性。
	if err := adapter.ClaimServiceAgentOwnership(ctx, slug, "改名后", store.ServiceAgentOwnerUser, 441, store.ServiceAgentVisibilityPublic); err != nil {
		t.Fatalf("same owner reclaim: %v", err)
	}

	// 他人抢占 → 明确错误。
	err = adapter.ClaimServiceAgentOwnership(ctx, slug, "hijack", store.ServiceAgentOwnerUser, 442, store.ServiceAgentVisibilityPublic)
	if !errors.Is(err, store.ErrServiceAgentOwnedByOther) {
		t.Fatalf("hijack err = %v, want ErrServiceAgentOwnedByOther", err)
	}

	// 平台内部 slug（hello 由 seed 建立，owner_kind=internal）不可被用户抢。
	err = adapter.ClaimServiceAgentOwnership(ctx, "hello", "假 hello", store.ServiceAgentOwnerUser, 441, store.ServiceAgentVisibilityPublic)
	if !errors.Is(err, store.ErrServiceAgentInternalReserved) {
		t.Fatalf("internal claim err = %v, want ErrServiceAgentInternalReserved", err)
	}
}

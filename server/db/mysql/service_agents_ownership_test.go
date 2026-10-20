package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// 归属与可见性（000045；与 PG 同语义镜像）。

func TestMySQLServiceAgentOwnershipSchemaConstants(t *testing.T) {
	for _, stmt := range []string{
		migrateServiceAgentsAddOwnership,
		migrateServiceAgentsAddOwnerUID,
		migrateServiceAgentsAddVisibility,
		createServiceAgentsOwnerIndex,
	} {
		if strings.TrimSpace(stmt) == "" {
			t.Fatalf("empty migration constant")
		}
	}
	for _, want := range []string{"owner_kind", "owner_uid", "visibility"} {
		joined := migrateServiceAgentsAddOwnership + migrateServiceAgentsAddOwnerUID + migrateServiceAgentsAddVisibility
		if !strings.Contains(joined, want) {
			t.Fatalf("migrations missing column %q", want)
		}
	}
	if !strings.Contains(createServiceAgentsOwnerIndex, "idx_service_agents_owner") {
		t.Fatal("missing owner index")
	}
}

// Claim 语义：占位 → 同 owner 幂等 → 他人被拒 → 平台内部 slug 保留。
func TestMySQLServiceAgentOwnershipClaim(t *testing.T) {
	db := mysqlTestAdapter(t)
	ctx := context.Background()

	slug := strings.ReplaceAll(strings.ToLower(t.Name()), "_", "-")
	if err := db.ClaimServiceAgentOwnership(ctx, slug, "归属测试", store.ServiceAgentOwnerUser, 441, store.ServiceAgentVisibilityPrivate); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	agents, err := db.ListServiceAgents(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got *store.ServiceAgentDescriptor
	for i := range agents {
		if agents[i].Slug == slug {
			got = &agents[i]
		}
	}
	if got == nil {
		t.Fatalf("claimed agent not listed: %+v", agents)
	}
	if got.OwnerKind != store.ServiceAgentOwnerUser || got.OwnerUID != 441 || got.Visibility != store.ServiceAgentVisibilityPrivate {
		t.Fatalf("ownership = %s/%d/%s", got.OwnerKind, got.OwnerUID, got.Visibility)
	}

	if err := db.ClaimServiceAgentOwnership(ctx, slug, "改名", store.ServiceAgentOwnerUser, 441, store.ServiceAgentVisibilityPublic); err != nil {
		t.Fatalf("same owner reclaim: %v", err)
	}

	err = db.ClaimServiceAgentOwnership(ctx, slug, "hijack", store.ServiceAgentOwnerUser, 442, store.ServiceAgentVisibilityPublic)
	if !errors.Is(err, store.ErrServiceAgentOwnedByOther) {
		t.Fatalf("hijack err = %v, want ErrServiceAgentOwnedByOther", err)
	}

	err = db.ClaimServiceAgentOwnership(ctx, "hello", "假 hello", store.ServiceAgentOwnerUser, 441, store.ServiceAgentVisibilityPublic)
	if !errors.Is(err, store.ErrServiceAgentInternalReserved) {
		t.Fatalf("internal claim err = %v, want ErrServiceAgentInternalReserved", err)
	}
}

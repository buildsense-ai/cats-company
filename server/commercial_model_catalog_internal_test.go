package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestCommercialModelCatalogKeepsInternalOnlySeparate guards the two-list
// contract. The reconcile needs the difference: a model missing from the
// sellable list is one it removes from a plan, which is right for a retired
// model and wrong for gpt-5.6-sol, which the internal all-models package grants.
// If the two lists ever merge into one, the reconcile silently starts stripping
// that model again - the defect this shape exists to prevent.
func TestCommercialModelCatalogKeepsInternalOnlySeparate(t *testing.T) {
	server, _ := catalogTestServerWithInternal(t,
		[]string{"MiniMax-M2.7", "gpt-6-sol"},
		[]string{"gpt-5.6-sol"},
		http.StatusOK,
	)
	catalog := newTestCatalog(t, server)

	models, internalOnly, source, err := catalog.Catalog(context.Background())
	if err != nil {
		t.Fatalf("catalog read failed: %v", err)
	}
	if source != "live" {
		t.Fatalf("source=%q, want live", source)
	}
	if strings.Join(models, ",") != "MiniMax-M2.7,gpt-6-sol" {
		t.Fatalf("sellable models = %v, want the two sellable names", models)
	}
	if strings.Join(internalOnly, ",") != "gpt-5.6-sol" {
		t.Fatalf("internal-only = %v, want gpt-5.6-sol kept out of the sellable list", internalOnly)
	}
	// The compatibility accessor must keep answering the sellable list: callers
	// that only save or validate a plan do not need the internal names.
	legacyModels, legacySource, err := catalog.Models(context.Background())
	if err != nil {
		t.Fatalf("Models failed: %v", err)
	}
	if strings.Join(legacyModels, ",") != "MiniMax-M2.7,gpt-6-sol" || legacySource != "memory" {
		t.Fatalf("Models() = %v/%q, want the sellable list from the cache", legacyModels, legacySource)
	}
}

// TestCommercialModelCatalogSnapshotCarriesInternalOnly is the outage case. A
// relay failure must not lose the internal-only list: a snapshot without it
// would make the reconcile read gpt-5.6-sol as retired and strip it from the
// plan, which is exactly the failure the list exists to prevent - and an outage
// is when an operator is least able to notice.
func TestCommercialModelCatalogSnapshotCarriesInternalOnly(t *testing.T) {
	dir := t.TempDir()
	warm, _ := catalogTestServerWithInternal(t,
		[]string{"MiniMax-M2.7", "gpt-6-sol"},
		[]string{"gpt-5.6-sol"},
		http.StatusOK,
	)
	cold := newTestCatalog(t, warm)
	cold.snapshotPath = dir + "/catalog.json"
	if _, _, _, err := cold.Catalog(context.Background()); err != nil {
		t.Fatalf("warm read failed: %v", err)
	}

	// A second instance sharing the same snapshot path sees a dead relay.
	dead, _ := catalogTestServer(t, nil, http.StatusServiceUnavailable)
	restored := newTestCatalog(t, dead)
	restored.snapshotPath = dir + "/catalog.json"

	models, internalOnly, source, err := restored.Catalog(context.Background())
	if err != nil {
		t.Fatalf("snapshot fallback failed: %v", err)
	}
	if source != "snapshot" {
		t.Fatalf("source=%q, want snapshot", source)
	}
	if strings.Join(models, ",") != "MiniMax-M2.7,gpt-6-sol" {
		t.Fatalf("sellable models = %v, want the snapshot's list", models)
	}
	if strings.Join(internalOnly, ",") != "gpt-5.6-sol" {
		t.Fatalf("internal-only = %v, want the snapshot to carry it too", internalOnly)
	}
}

// TestCommercialModelCatalogReportsAnEmptyInternalList keeps the common case
// honest: a relay that sells everything it routes reports no internal names, and
// that must not be confused with a failed read.
func TestCommercialModelCatalogReportsAnEmptyInternalList(t *testing.T) {
	server, _ := catalogTestServer(t, []string{"MiniMax-M2.7", "gpt-6-sol"}, http.StatusOK)
	catalog := newTestCatalog(t, server)

	models, internalOnly, _, err := catalog.Catalog(context.Background())
	if err != nil {
		t.Fatalf("catalog read failed: %v", err)
	}
	if len(models) != 2 || len(internalOnly) != 0 {
		t.Fatalf("models=%v internal=%v, want two sellable and no internal names", models, internalOnly)
	}
}

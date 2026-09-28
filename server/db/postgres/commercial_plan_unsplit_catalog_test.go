package postgres

import (
	"context"
	"errors"
	"testing"
)

// TestReconcileRefusesAnUnsplitRelayCatalog is the deployment-order guard for
// the reconcile.
//
// A relay older than the internal-only split answers without that list, so the
// pass cannot tell a model it deliberately keeps off the shelf from one it
// retired. Running anyway would delete gpt-5.6-sol from the internal all-models
// package that grants it - so the pass has to refuse instead of guessing.
func TestReconcileRefusesAnUnsplitRelayCatalog(t *testing.T) {
	// A nil adapter is fine: the guard must fire before any database work, so a
	// panic or nil dereference here would mean the check sits too late.
	adapter := &Adapter{}
	err := adapter.ReconcileCommercialPlanModelsWithInternal(context.Background(), []string{"MiniMax-M2.7"}, nil, false)
	if err == nil {
		t.Fatal("an unknown internal-only list must be refused, not treated as empty")
	}
	if !errors.Is(err, errReconcileCatalogUnknown) {
		t.Fatalf("error = %v, want errReconcileCatalogUnknown so the caller logs a skip", err)
	}
}

// TestReconcileNarrowEntryRefusesToo makes sure the compatibility entry point
// cannot be the way back to the unsafe behaviour: it supplies no internal-only
// list, so it must refuse for the same reason.
func TestReconcileNarrowEntryRefusesToo(t *testing.T) {
	adapter := &Adapter{}
	err := adapter.ReconcileCommercialPlanModels(context.Background(), []string{"MiniMax-M2.7"})
	if err == nil {
		t.Fatal("the narrow entry point must not run an unclassified reconcile")
	}
	if !errors.Is(err, errReconcileCatalogUnknown) {
		t.Fatalf("error = %v, want errReconcileCatalogUnknown", err)
	}
}

// TestReconcileRejectsAnEmptyCatalog keeps the pre-existing guard reachable: an
// empty sellable list is a relay failure, not "this relay sells nothing", and it
// must not be mistaken for the unknown-list case.
func TestReconcileRejectsAnEmptyCatalog(t *testing.T) {
	adapter := &Adapter{}
	err := adapter.ReconcileCommercialPlanModelsWithInternal(context.Background(), nil, nil, true)
	if err == nil {
		t.Fatal("an empty catalog must be refused")
	}
	if errors.Is(err, errReconcileCatalogUnknown) {
		t.Fatalf("error = %v, want the empty-catalog failure rather than the unknown-list one", err)
	}
}

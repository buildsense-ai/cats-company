package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// The cluster reaper (packing-cluster packed-retention.sh) only knows a tenant
// expired because the platform told it. If provisioning drops this argument a
// shared-fleet tenant stays "never expiring" until its first renewal.
func TestCloudWorkerProvisionExpiryArgs(t *testing.T) {
	paidUntil := time.Date(2026, 12, 1, 8, 30, 0, 0, time.UTC)

	got := cloudWorkerProvisionExpiryArgs(types.CloudWorkerPackedShared, paidUntil)
	if len(got) != 2 || got[0] != "--expires-at" || got[1] != "2026-12-01T08:30:00Z" {
		t.Fatalf("packed shared should carry the expiry, got %v", got)
	}

	if got := cloudWorkerProvisionExpiryArgs(types.CloudWorkerPackedShared, time.Time{}); got != nil {
		t.Fatalf("no paid window must send no flag, got %v", got)
	}

	// Dedicated ECS workers are reaped by the provider subscription; a package
	// expiry there would be a second, disagreeing source of truth.
	if got := cloudWorkerProvisionExpiryArgs(types.CloudWorkerPublicIP, paidUntil); got != nil {
		t.Fatalf("non-shared profile must not send the flag, got %v", got)
	}
}

type provisionExpirySummaryStore struct {
	CommercialStore
	summary *types.CommercialSummary
}

func (s provisionExpirySummaryStore) GetCommercialSummary(int64) (*types.CommercialSummary, error) {
	return s.summary, nil
}

func TestCloudWorkerProvisionExpiryFollowsCurrentPackage(t *testing.T) {
	now := time.Now().UTC()
	currentExpiry, legacyExpiry, futureExpiry := now.Add(10*24*time.Hour), now.Add(90*24*time.Hour), now.Add(120*24*time.Hour)
	current := &types.CommercialEntitlement{State: "active", Source: "invite", StartsAt: now.Add(-time.Hour), ExpiresAt: &currentExpiry}
	legacy := &types.CommercialEntitlement{State: "active", Source: "legacy", StartsAt: now.Add(-time.Hour), ExpiresAt: &legacyExpiry}
	future := &types.CommercialEntitlement{State: "active", Source: "invite", StartsAt: now.Add(24 * time.Hour), ExpiresAt: &futureExpiry}
	s := provisionExpirySummaryStore{summary: &types.CommercialSummary{Entitlements: []*types.CommercialEntitlement{legacy, future, current}}}
	got, err := cloudWorkerProvisionExpiry(s, 7, "")
	if err != nil || got == nil || !got.Equal(currentExpiry) {
		t.Fatalf("must follow current package: expiry=%v err=%v", got, err)
	}
	current.ExpiresAt = nil
	if got, err := cloudWorkerProvisionExpiry(s, 7, ""); err == nil || got != nil {
		t.Fatalf("permanent current package must require explicit test term: expiry=%v err=%v", got, err)
	}
	if got, err := cloudWorkerProvisionExpiry(s, 7, currentExpiry.Format(time.RFC3339)); err != nil || got == nil || got.Unix() != currentExpiry.Unix() {
		t.Fatalf("explicit test term must still work: expiry=%v err=%v", got, err)
	}
}

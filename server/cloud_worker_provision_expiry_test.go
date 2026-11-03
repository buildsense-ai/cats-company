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

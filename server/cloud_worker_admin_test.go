package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestCloudWorkerAdminOverviewUsesSafeProjectionAndProviderSnapshot(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	store.adminRecords = []types.CloudWorkerAdminRecord{{
		WorkerUID:        42,
		OwnerUID:         7,
		OwnerUsername:    "owner",
		Username:         "bot-tenant-a",
		DisplayName:      "云员工",
		TenantName:       "bot-tenant-a",
		BotEnabled:       true,
		LifecycleState:   "active",
		CreditState:      "consumed",
		CreditSourceRef:  "order:CC-TEST",
		PackageExpiresAt: ptrTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
	}}
	h.statusSnapshot = map[string]cloudInstanceInfo{
		"bot-tenant-a": {Status: "running", ImageID: "img-1", Version: "v1.5.0", AppVersion: "1.5.0"},
	}
	h.statusLoaded = true
	h.statusUpdatedAt = time.Now()

	overview, err := h.CloudWorkerAdminOverview(time.Now())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.WorkerCount != 1 || overview.StatusCounts["running"] != 1 || overview.CreditCounts["consumed"] != 1 {
		t.Fatalf("unexpected overview: %+v", overview)
	}
	if overview.Workers[0].AppVersion != "1.5.0" || overview.Workers[0].ImageID != "img-1" {
		t.Fatalf("provider facts missing: %+v", overview.Workers[0])
	}
}

// The admin roster is the only place where "platform says shared container,
// cluster disagrees" can be seen, so it has to be both visible and safe:
// it must appear after repeated *snapshots*, and it must never rewrite the
// record (a container being recreated looks exactly like a missing one).
func TestCloudWorkerAdminOverviewFlagsPackedContainerMissingRepeatedly(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	h.db = &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{
		"bot-shared-a": {Profile: types.CloudWorkerPackedShared},
	}}
	store.adminRecords = []types.CloudWorkerAdminRecord{{
		WorkerUID: 61, OwnerUID: 7, Username: "bot-shared-a", TenantName: "bot-shared-a",
	}}
	h.statusLoaded = true
	snapshot := time.Now()
	h.statusUpdatedAt = snapshot
	h.statusSnapshot = map[string]cloudInstanceInfo{"bot-shared-a": {Status: "missing"}}

	overview := func() cloudWorkerAdminItem {
		t.Helper()
		got, err := h.CloudWorkerAdminOverview(time.Now())
		if err != nil {
			t.Fatalf("overview: %v", err)
		}
		if len(got.Workers) != 1 {
			t.Fatalf("workers: %d", len(got.Workers))
		}
		return got.Workers[0]
	}

	// Polling the same snapshot five times is still one observation: the panel
	// refreshes far more often than the cluster is queried.
	for i := 0; i < 5; i++ {
		if drift := overview().DeploymentDrift; drift != "" {
			t.Fatalf("poll %d: drift reported from a single snapshot: %q", i, drift)
		}
	}

	for attempt := 2; attempt <= packedAbsenceThreshold; attempt++ {
		h.statusUpdatedAt = snapshot.Add(time.Duration(attempt) * time.Minute)
		item := overview()
		if attempt < packedAbsenceThreshold && item.DeploymentDrift != "" {
			t.Fatalf("attempt %d: drift reported too early: %q", attempt, item.DeploymentDrift)
		}
		if attempt == packedAbsenceThreshold && item.DeploymentDrift == "" {
			t.Fatalf("attempt %d: expected drift signal", attempt)
		}
		if item.DeploymentProfile != types.CloudWorkerPackedShared {
			t.Fatalf("drift must not rewrite the recorded profile: %q", item.DeploymentProfile)
		}
	}
	if got := h.packedMissingStreak["bot-shared-a"]; got != packedAbsenceThreshold {
		t.Fatalf("streak %d", got)
	}

	// A container that comes back clears the signal immediately.
	h.statusSnapshot = map[string]cloudInstanceInfo{"bot-shared-a": {Status: "running", Backend: "packed"}}
	h.statusUpdatedAt = snapshot.Add(time.Hour)
	if drift := overview().DeploymentDrift; drift != "" {
		t.Fatalf("recovered container kept drift: %q", drift)
	}
	if got := h.packedMissingStreak["bot-shared-a"]; got != 0 {
		t.Fatalf("streak after recovery %d", got)
	}
}

func TestCloudWorkerAdminOverviewKeepsExternalBindingUnverified(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	store.adminRecords = []types.CloudWorkerAdminRecord{{
		InstanceID: "i-fs7", InstanceName: "dh1987", Provider: "ctyun",
		RegionID: "200000004421", ManagementMode: "manual_import",
		LifecycleMode: "external", BindingStatus: "unverified", TenantName: "dh1987",
	}}
	h.statusLoaded = true
	h.statusSnapshot = map[string]cloudInstanceInfo{"dh1987": {Status: "running"}}
	overview, err := h.CloudWorkerAdminOverview(time.Now())
	if err != nil || overview.StatusCounts["unverified"] != 1 {
		t.Fatalf("external binding must not use platform snapshot: overview=%+v err=%v", overview, err)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestCloudWorkerAdminImportForcesExternalLifecycle(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	req := httptest.NewRequest(http.MethodPost, "/api/account/commercial-ops/cloud-workers/import", strings.NewReader(`{
		"owner_uid":404,"worker_uid":405,"provider":"ctyun",
		"region_id":"200000002530","instance_id":"i-1","instance_name":"worker1"
	}`))
	rec := httptest.NewRecorder()
	h.HandleAdminImport(rec, req)
	if rec.Code != http.StatusOK || store.importedBinding == nil {
		t.Fatalf("status=%d body=%s binding=%+v", rec.Code, rec.Body.String(), store.importedBinding)
	}
	if got := store.importedBinding; got.ManagementMode != "manual_import" || got.LifecycleMode != "external" || got.TenantName != "worker1" {
		t.Fatalf("unsafe import defaults: %+v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/account/commercial-ops/cloud-workers/import", strings.NewReader(`{"region_id":"r","instance_id":"i","instance_name":"n"}`))
	rec = httptest.NewRecorder()
	h.HandleAdminImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing owner status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type cloudWorkerAdminEndpointStub struct{}

func (cloudWorkerAdminEndpointStub) HandleAdminOverview(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"worker_count":1,"workers":[]}`))
}

func TestCommercialOpsCloudWorkersIsInternalReadOnlyAndAudited(t *testing.T) {
	store := newCommercialOpsTestStore()
	admin := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, store)
	h := NewCommercialOpsHandler(admin, commercialOpsTestVerifier{service: AccountService{
		Slug:   "dashboard",
		Scopes: []string{commercialOpsReadScope},
	}}, store)
	h.SetCloudWorkerAdmin(cloudWorkerAdminEndpointStub{})

	req := commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/cloud-workers", "")
	rec := httptest.NewRecorder()
	h.HandleCloudWorkers(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "worker_count") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 || store.events[0].Action != "cloud_workers.read" || store.events[0].TargetType != "cloud_worker_roster" {
		t.Fatalf("unexpected audit events: %+v", store.events)
	}

	req = commercialOpsRequest(http.MethodPost, "/api/account/commercial-ops/cloud-workers", "{}")
	rec = httptest.NewRecorder()
	h.HandleCloudWorkers(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d want %d", rec.Code, http.StatusMethodNotAllowed)
	}

	req = commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/cloud-workers", "")
	req.RemoteAddr = "203.0.113.8:443"
	rec = httptest.NewRecorder()
	h.HandleCloudWorkers(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("external status=%d want %d", rec.Code, http.StatusForbidden)
	}
}

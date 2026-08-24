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

func ptrTime(value time.Time) *time.Time { return &value }

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

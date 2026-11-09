package server

import (
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// 实测故障（owner 116 / worker 1671 / action 484efdad）：durable action 还在
// provisioning 时，worker 已经写进 users/bot_config，而集群里还没有它的容器。
// 总览把它显示成 missing，随后记录消失，运维无法区分"正在创建"和"机器丢了"。
//
// 契约：只有按 operation 精确匹配到的 pending/running action 才算 provisioning；
// 失败的历史行和无 action 的真 missing 都保持原有判定。
func TestCloudWorkerAdminOverviewTreatsActiveActionAsProvisioning(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	const operationID = "484efdad-787e-4b2f-bc99-fcf5b22474cd"
	tenant := types.CommercialActionUsername(116, operationID)
	h.db = &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{
		tenant:       {Profile: types.CloudWorkerPackedShared},
		"bot-lost":   {Profile: types.CloudWorkerPackedShared},
		"bot-failed": {Profile: types.CloudWorkerPackedShared},
	}}
	planned := time.Date(2026, 12, 19, 6, 0, 0, 0, time.UTC)
	store.adminRecords = []types.CloudWorkerAdminRecord{
		{
			WorkerUID: 1671, OwnerUID: 116, Username: tenant, TenantName: tenant,
			OperationID: operationID, OperationStatus: "running", WorkerStatus: "running",
			RuntimePlan: "standard", PlannedExpiresAt: &planned,
		},
		{WorkerUID: 1600, OwnerUID: 116, Username: "bot-lost", TenantName: "bot-lost"},
		{
			WorkerUID: 1700, OwnerUID: 116, Username: "bot-failed", TenantName: "bot-failed",
			OperationID: "finished-op", OperationStatus: "failed", WorkerStatus: "failed",
		},
	}
	h.statusLoaded = true
	snapshot := time.Now()
	h.statusSnapshot = map[string]cloudInstanceInfo{
		tenant:       {Status: "missing"},
		"bot-lost":   {Status: "missing"},
		"bot-failed": {Status: "missing"},
	}

	var last *cloudWorkerAdminOverview
	for attempt := 1; attempt <= packedAbsenceThreshold+1; attempt++ {
		h.statusUpdatedAt = snapshot.Add(time.Duration(attempt) * time.Minute)
		overview, err := h.CloudWorkerAdminOverview(time.Now())
		if err != nil {
			t.Fatalf("overview: %v", err)
		}
		last = overview
		if overview.WorkerCount != len(store.adminRecords) {
			t.Fatalf("worker_count=%d want %d", overview.WorkerCount, len(store.adminRecords))
		}
		items := map[string]cloudWorkerAdminItem{}
		for _, item := range overview.Workers {
			items[item.Username] = item
		}
		active := items[tenant]
		if active.ProviderStatus != "provisioning" {
			t.Fatalf("attempt %d: in-flight worker reported as %q", attempt, active.ProviderStatus)
		}
		if active.LifecycleState != "provisioning" {
			t.Fatalf("attempt %d: lifecycle_state=%q", attempt, active.LifecycleState)
		}
		if active.DeploymentDrift != "" {
			t.Fatalf("attempt %d: in-flight worker flagged as drift: %q", attempt, active.DeploymentDrift)
		}
		if active.OperationID != operationID || active.OperationStatus != "running" || active.WorkerStatus != "running" {
			t.Fatalf("attempt %d: operation identity lost: %+v", attempt, active)
		}
		if active.RuntimePlan != "standard" || active.PlannedExpiresAt == nil || !active.PlannedExpiresAt.Equal(planned) {
			t.Fatalf("attempt %d: planned facts lost: %+v", attempt, active)
		}
		// A worker without any active action keeps the original "missing"
		// verdict, and still accrues the packed-absence drift signal.
		lost := items["bot-lost"]
		if lost.ProviderStatus != "missing" {
			t.Fatalf("attempt %d: real missing worker reported as %q", attempt, lost.ProviderStatus)
		}
		if lost.OperationID != "" {
			t.Fatalf("attempt %d: unrelated worker carried an operation: %+v", attempt, lost)
		}
		if attempt < packedAbsenceThreshold && lost.DeploymentDrift != "" {
			t.Fatalf("attempt %d: drift signalled too early: %q", attempt, lost.DeploymentDrift)
		}
		if attempt == packedAbsenceThreshold && lost.DeploymentDrift == "" {
			t.Fatalf("attempt %d: real missing worker lost its drift signal", attempt)
		}
		// A failed action is history, not provisioning.
		if failed := items["bot-failed"]; failed.ProviderStatus != "missing" {
			t.Fatalf("attempt %d: failed action reported as %q", attempt, failed.ProviderStatus)
		}
	}

	if got := h.packedMissingStreak[tenant]; got != 0 {
		t.Fatalf("in-flight worker accumulated a packed absence streak: %d", got)
	}
	if got := h.packedMissingStreak["bot-lost"]; got < packedAbsenceThreshold {
		t.Fatalf("real missing worker lost its streak: %d", got)
	}
	if last == nil || last.StatusCounts["provisioning"] != 1 || last.StatusCounts["missing"] != 2 {
		t.Fatalf("status counts: %+v", last)
	}
}

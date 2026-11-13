package server

import (
	"net/http"

	"github.com/openchat/openchat/server/store/types"
)

// A durable adoption marker is only intent until the provider has confirmed it.
// Retry reconciles that same worker instead of returning kept or provisioning again.
func (h *CloudWorkerHandler) confirmCommercialKeptRetention(w http.ResponseWriter, owner, worker int64, action *types.CommercialAction) bool {
	if action.Preview.TargetPlan == nil || action.Preview.TargetPlan.ModelCreditUnits <= 0 {
		return false
	}
	fail := func() bool {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "existing worker retention is not confirmed; retry synchronization",
			"code":  "cloud_worker_retention_sync_required", "uid": worker,
		})
		return true
	}
	bots, err := h.db.ListBotsByOwner(owner)
	if err != nil {
		return fail()
	}
	tenant := ""
	for _, bot := range bots {
		id, _ := bot["id"].(int64)
		if id == worker {
			tenant, _ = bot["tenant_name"].(string)
			break
		}
	}
	if tenant == "" {
		return fail()
	}
	store, ok := h.credits.(interface {
		GetCloudWorkerBillingLifecycle(string) (*types.CloudWorkerLifecycle, error)
	})
	if !ok {
		return fail()
	}
	item, err := store.GetCloudWorkerBillingLifecycle(tenant)
	valid := func(row *types.CloudWorkerLifecycle) bool {
		return row != nil && row.ID > 0 && row.OwnerUID == owner && row.WorkerUID == worker &&
			row.TenantName == tenant && row.State == "active" && row.RetainData && row.DeleteAfter.IsZero() && row.BillingAction == ""
	}
	if err != nil || !valid(item) {
		return fail()
	}
	if item.SyncPending {
		if h.syncSharedCloudWorkerTerm(item.ID).Status != types.CloudWorkerRenewApplied {
			return fail()
		}
	}
	current, err := store.GetCloudWorkerBillingLifecycle(tenant)
	if err != nil || !valid(current) || current.ID != item.ID || current.SyncPending {
		return fail()
	}
	return false
}

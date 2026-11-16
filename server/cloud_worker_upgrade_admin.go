package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// HandleAdminMachineUpgradeRetry is registered behind adminAuthWithDB. It
// authorizes one further attempt after investigation, never a forced refund or
// settlement. The existing adapter receipt remains the authority for money.
func (h *CloudWorkerHandler) HandleAdminMachineUpgradeRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	actor := UIDFromContext(r.Context())
	if actor <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !isAdminUsername(UsernameFromContext(r.Context())) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}
	store, ok := h.machineUpgradeStoreFor()
	review, reviewOK := h.db.(interface {
		RetryMachineUpgradeReview(string) (*types.MachineUpgradeOperation, bool, error)
	})
	if !ok || !reviewOK || h.changePlanScript == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "machine upgrade review unavailable"})
		return
	}
	var input struct {
		UID         int64  `json:"uid"`
		OperationID string `json:"operation_id"`
		Reason      string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.UID <= 0 || strings.TrimSpace(input.OperationID) == "" || len(input.OperationID) > 128 || len(strings.TrimSpace(input.Reason)) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "uid, operation_id and an investigation reason are required"})
		return
	}
	lock := h.ownerOperationLock(input.UID)
	if !lock.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "owner operation busy"})
		return
	}
	defer lock.Unlock()
	if gate, ok := h.credits.(commercialWorkerOwnerGate); ok {
		release, acquired, err := gate.AcquireCommercialWorkerOwnerGate(input.UID)
		if err != nil || !acquired {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "owner operation busy"})
			return
		}
		defer release()
	}
	op, err := store.GetMachineUpgrade(input.UID, input.OperationID)
	if err != nil || op == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "machine upgrade not found"})
		return
	}
	// Never replay an old purchase against a deleted/reused tenant.
	lifecycle, err := h.lifecycleByName(input.UID, op.TenantName)
	if err != nil || lifecycle == nil || lifecycle.ID != op.LifecycleID || lifecycle.State != "active" || !lifecycle.PackageExpiresAt.After(time.Now()) || !h.tenantRunsOnPackedShared(op.TenantName) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "machine lifecycle changed; investigate the held operation without replaying it"})
		return
	}
	log.Printf("[cloud-worker] machine upgrade MANUAL_REVIEW actor=%d uid=%d op=%s reason=%q", actor, input.UID, input.OperationID, strings.TrimSpace(input.Reason))
	claimed, proceed, err := review.RetryMachineUpgradeReview(op.OperationID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to claim machine upgrade review"})
		return
	}
	if proceed {
		h.runMachineUpgradeScript(appliedMachineUpgrade{store: store, op: claimed}, true)
	}
	latest, err := store.GetMachineUpgrade(input.UID, op.OperationID)
	if err != nil || latest == nil {
		latest = claimed
	}
	writeJSON(w, http.StatusOK, machineUpgradeOperationView(latest))
}

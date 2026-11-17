package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

// 复核理由的长度按「字符」算，不按字节：中文理由 8 个字符是 24 字节，按字节
// 校验等于把中文的门槛降到 3 个字，反而放行了敷衍的理由；1000 个字符的上限挡住
// 把整段日志粘进来。数据库的 CHECK 用同一组数字，这里是它友好的那一半。
const (
	machineUpgradeReviewReasonMinChars = 8
	machineUpgradeReviewReasonMaxChars = 1000
)

// normalizeMachineUpgradeReviewReason trims the operator's investigation reason and
// bounds it by Unicode characters. It reports false instead of guessing, so the
// handler can answer 400 before anything is locked or charged.
func normalizeMachineUpgradeReviewReason(reason string) (string, bool) {
	trimmed := strings.TrimSpace(reason)
	length := utf8.RuneCountInString(trimmed)
	if length < machineUpgradeReviewReasonMinChars || length > machineUpgradeReviewReasonMaxChars {
		return "", false
	}
	return trimmed, true
}

// machineUpgradeReviewAuditor is the only claim this handler is allowed to use:
// one that records who authorized the retry. The audit-free helper still exists on
// the adapter for compatibility tests, but an adapter that cannot persist the
// authorization must not be reachable from HTTP — hence a dedicated interface that
// fails closed instead of falling back.
type machineUpgradeReviewAuditor interface {
	RetryMachineUpgradeReviewAudited(operationID string, actorUID int64, reason string) (*types.MachineUpgradeOperation, bool, error)
}

// HandleAdminMachineUpgradeRetry is registered behind adminAuthWithDB. It
// authorizes one further attempt after investigation, never a forced refund or
// settlement. The existing adapter receipt remains the authority for money.
//
// 授权必须落库：actor + operation + reason 由适配器在认领的同一个事务里写进
// cloud_worker_machine_upgrade_review_audits，审计写不进去就连认领一起回滚。
// 这里打的日志只是运维串联用的镜像，不是授权依据；没有审计能力的适配器直接
// 503，不做「只写日志」的降级。
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
	review, reviewOK := h.db.(machineUpgradeReviewAuditor)
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
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.UID <= 0 || strings.TrimSpace(input.OperationID) == "" || len(input.OperationID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "uid, operation_id and an investigation reason are required"})
		return
	}
	reason, reasonOK := normalizeMachineUpgradeReviewReason(input.Reason)
	if !reasonOK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an investigation reason of 8 to 1000 characters is required"})
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
	// The audited claim is atomic: the caller either gets a leased attempt with a
	// durable authorization row, or nothing at all.
	claimed, proceed, err := review.RetryMachineUpgradeReviewAudited(op.OperationID, actor, reason)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to claim machine upgrade review"})
		return
	}
	if proceed {
		h.runMachineUpgradeScript(appliedMachineUpgrade{store: store, op: claimed}, true)
	}
	if claimed != nil {
		// 只是串联用的镜像日志；真正的事实是数据库里那行审计。
		log.Printf("[cloud-worker] machine upgrade manual review actor=%d uid=%d op=%s claimed=%t attempt=%d reason=%q",
			actor, input.UID, input.OperationID, proceed, claimed.Attempts, reason)
	}
	latest, err := store.GetMachineUpgrade(input.UID, op.OperationID)
	if err != nil || latest == nil {
		latest = claimed
	}
	writeJSON(w, http.StatusOK, machineUpgradeOperationView(latest))
}

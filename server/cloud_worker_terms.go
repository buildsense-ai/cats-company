package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// Desired terms are durable before a remote operation. Retries use the same
// deadline and original container, without buying another cycle.
type sharedCloudWorkerTermStore interface {
	SetSharedCloudWorkerTerm(int64, string, *time.Time, int) (*types.CloudWorkerLifecycle, error)
	ClaimSharedCloudWorkerSync(int64) (*types.CloudWorkerLifecycle, bool, error)
	CompleteSharedCloudWorkerSync(int64, *time.Time, string, string) error
	ListSharedCloudWorkerSyncPending(int) ([]types.CloudWorkerLifecycle, error)
	ClaimSharedCloudWorkerExpiry(int64, time.Time) (bool, error)
	CompleteSharedCloudWorkerExpiry(int64, time.Time, string) error
}

type sharedCloudWorkerTermInput struct {
	Tenant    string     `json:"tenant_name"`
	Action    string     `json:"action"`
	Mode      string     `json:"expiry_mode"`
	ExpiresAt *time.Time `json:"expires_at"`
	Note      string     `json:"note"`
}

func (h *CloudWorkerHandler) sharedCloudWorkerPackageTerm(ownerUID int64) (*time.Time, int, error) {
	store, ok := h.db.(interface {
		GetSharedCloudWorkerPackageTerm(int64) (*types.CloudWorkerPackageTerm, error)
	})
	if !ok {
		return nil, 0, fmt.Errorf("package lookup unavailable")
	}
	term, err := store.GetSharedCloudWorkerPackageTerm(ownerUID)
	if err != nil {
		return nil, 0, fmt.Errorf("读取机器套餐权益失败，请稍后重试")
	}
	if term == nil {
		return nil, 0, fmt.Errorf("当前没有包含机器权益的有效套餐，请先续费套餐，或明确选择固定期限/长期使用")
	}
	if term.ExpiresAt == nil {
		return nil, term.RetentionDays, nil
	}
	expires := term.ExpiresAt.UTC()
	return &expires, term.RetentionDays, nil
}

// Reached only through the protected operator billing endpoint. A term grant
// is an operator entitlement decision, not an external purchase.
func (h *CloudWorkerHandler) handleSharedCloudWorkerTerm(w http.ResponseWriter, r *http.Request, item *types.CloudWorkerLifecycle, input sharedCloudWorkerTermInput) {
	store, ok := h.credits.(sharedCloudWorkerTermStore)
	if !ok || !h.tenantRunsOnPackedShared(item.TenantName) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "此入口仅支持已登记的共享集群机器", "code": "cloud_worker_term_unavailable"})
		return
	}
	if item.State == "deleted" || item.State == "delete_running" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "机器已删除或正在删除，不能恢复原数据", "code": "cloud_worker_term_unavailable"})
		return
	}
	if strings.TrimSpace(input.Note) == "" || len(input.Note) > 4096 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请填写续期或恢复原因"})
		return
	}
	if input.Action == "set_term" {
		mode := strings.TrimSpace(input.Mode)
		var expires *time.Time
		retention := item.RetentionDays
		if retention <= 0 {
			retention = 7
		}
		switch mode {
		case "fixed":
			if input.ExpiresAt == nil || !input.ExpiresAt.After(time.Now().UTC()) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "固定期限必须指定未来的截止时间"})
				return
			}
			expires = input.ExpiresAt
			if !item.PackageExpiresAt.IsZero() && expires.Before(item.PackageExpiresAt) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "续期不能缩短机器已有的使用期限"})
				return
			}
		case "follow_plan":
			if input.ExpiresAt != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "跟随套餐时不接受手填截止时间"})
				return
			}
			var err error
			expires, retention, err = h.sharedCloudWorkerPackageTerm(item.OwnerUID)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "cloud_worker_package_required"})
				return
			}
			// Preserve the remaining time of an independently paid fixed term.
			if expires != nil && item.PackageExpiresAt.After(*expires) {
				value := item.PackageExpiresAt.UTC()
				expires = &value
			}
		case "permanent":
			if input.ExpiresAt != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "长期使用不能同时指定截止时间"})
				return
			}
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请选择跟随套餐、固定期限或长期使用"})
			return
		}
		updated, err := store.SetSharedCloudWorkerTerm(item.ID, mode, expires, retention)
		if err != nil || updated == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "期限更新失败，机器可能正在执行其他操作", "code": "cloud_worker_term_busy"})
			return
		}
		item = updated
		log.Printf("[cloud-worker] operator term owner=%d tenant=%s mode=%s note=%q", item.OwnerUID, item.TenantName, mode, strings.TrimSpace(input.Note))
	} else if input.Action != "sync_term" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shared worker term action"})
		return
	}
	var outcome types.CloudWorkerRenewOutcome
	if input.Action == "sync_term" && !item.SyncPending && item.BillingAction == "" {
		outcome = types.CloudWorkerRenewOutcome{Status: types.CloudWorkerRenewApplied, ExpiresAt: sharedCloudWorkerDeadline(*item), Message: "原机器期限已同步，无需重复操作"}
	} else {
		outcome = h.syncSharedCloudWorkerTerm(item.ID)
	}
	if outcome.Status != types.CloudWorkerRenewApplied {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error": "期限已保存，恢复或同步尚未完成，请查看状态后重试同步；无需再开一台",
			"code":  "cloud_worker_term_sync_pending", "detail": outcome.Message, "sync_pending": true,
		})
		return
	}
	h.requestCloudStatusRefresh(true)
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "completed", "action": input.Action, "tenant_name": item.TenantName, "expiry_mode": item.ExpiryMode, "expires_at": outcome.ExpiresAt, "message": outcome.Message, "sync_pending": false})
}

func sharedCloudWorkerDeadline(item types.CloudWorkerLifecycle) *time.Time {
	if item.PackageExpiresAt.IsZero() {
		return nil
	}
	value := item.PackageExpiresAt.UTC()
	return &value
}

// sharedCloudWorkerSyncArgs is the platform -> cluster renew invocation for one
// synced term.
//
// A retained credit environment (RetainData, approved credit rules 2026-11-09
// P1-2) is handed the marker instead of a grace window: a grace window is exactly
// the deletion deadline the rule forbids, and the cluster's own renew/retention
// path turns it into a purge date. The marker comes from the durable row, so a
// retry or a later renewal re-applies it rather than dropping it. A legacy row
// keeps the exact invocation it always had.
func sharedCloudWorkerSyncArgs(item types.CloudWorkerLifecycle, expected *time.Time) []string {
	window := "none"
	if expected != nil {
		window = expected.Format(time.RFC3339)
	}
	args := []string{"--name", item.TenantName, "--expires-at", window}
	if item.RetainData {
		// A retained credit environment is handed the marker instead of a grace
		// window: the window is exactly the deletion deadline the rule forbids.
		args = append(args, "--retain")
	} else {
		graceDays := item.RetentionDays
		if graceDays <= 0 {
			graceDays = 7
		}
		args = append(args, "--grace-days", strconv.Itoa(graceDays))
	}
	if expected != nil && !expected.After(time.Now().UTC()) {
		// Revoked or naturally expired package targets must be persisted and
		// suspended in the same tenant lock, never renewed for a fallback cycle.
		args = append(args, "--expire-if-due")
	}
	return args
}

func (h *CloudWorkerHandler) syncSharedCloudWorkerTerm(id int64) types.CloudWorkerRenewOutcome {
	outcome := types.CloudWorkerRenewOutcome{Status: types.CloudWorkerRenewFailed}
	store, ok := h.credits.(sharedCloudWorkerTermStore)
	if !ok || h.renewScript == "" {
		outcome.Message = "共享机器期限同步不可用"
		return outcome
	}
	item, claimed, err := store.ClaimSharedCloudWorkerSync(id)
	if err != nil || !claimed || item == nil {
		outcome.Message = "机器正在操作或期限无需同步，请刷新状态"
		return outcome
	}
	outcome.TenantName = item.TenantName
	expected := sharedCloudWorkerDeadline(*item)
	expiring := expected != nil && !expected.After(time.Now().UTC())
	args := sharedCloudWorkerSyncArgs(*item, expected)
	out, runErr := h.runScript(h.renewScript, args...)
	var result struct {
		Status    string     `json:"status"`
		ExpiresAt *time.Time `json:"expires_at"`
		Permanent bool       `json:"permanent"`
		Resumed   bool       `json:"resumed"`
		Message   string     `json:"message"`
		Reason    string     `json:"reason"`
	}
	if runErr == nil {
		if err := json.Unmarshal([]byte(lastCloudWorkerJSONLine(out)), &result); err != nil {
			runErr = fmt.Errorf("期限同步未返回有效结果: %w", err)
		} else if (result.Status != "renewed" && !(expiring && result.Status == "expired")) || (expiring && result.Status != "expired") || (expected == nil && (!result.Permanent || result.ExpiresAt != nil)) ||
			// Cluster billing persists whole UTC seconds; keep the durable database
			// target unchanged while requiring the exact corresponding second.
			(expected != nil && (result.ExpiresAt == nil || !result.ExpiresAt.Equal(expected.Truncate(time.Second)))) {
			runErr = fmt.Errorf("容器未确认所请求的使用期限")
		}
	}
	errorText := ""
	if runErr != nil {
		errorText = truncateWorkerOutput(destroyErrorDetail(runErr, out))
		outcome.Message = errorText
	}
	if completeErr := store.CompleteSharedCloudWorkerSync(item.ID, expected, item.ExpiryMode, errorText); completeErr != nil {
		outcome.Message = "容器操作已执行，平台记录待核对：" + completeErr.Error()
		return outcome
	}
	if runErr != nil {
		return outcome
	}
	// A payment may have changed the desired term during the remote operation.
	// Completing an older target must not claim that the newer one was synced.
	billingStore, ok := h.credits.(cloudWorkerBillingStore)
	if !ok {
		outcome.Message = "容器操作已执行，平台记录待核对"
		return outcome
	}
	current, readErr := billingStore.GetCloudWorkerBillingLifecycle(item.TenantName)
	if readErr != nil || current == nil || current.SyncPending || current.ExpiryMode != item.ExpiryMode || !current.PackageExpiresAt.Equal(item.PackageExpiresAt) {
		outcome.Message = "机器期限目标已变化或平台记录待核对，请刷新后重试同步"
		return outcome
	}
	outcome.Status = types.CloudWorkerRenewApplied
	outcome.ExpiresAt = expected
	outcome.Message = "原机器期限已同步"
	if result.Reason == "manual-suspend" {
		outcome.Message = "原机器期限已同步；人工暂停或安全隔离仍保留，未自动启动"
	} else if expiring {
		outcome.Message = "原机器已按到期暂停，数据在保留期内可恢复"
	} else if result.Resumed {
		outcome.Message = "原机器已恢复，原数据保留"
	} else if result.Message != "" {
		outcome.Message = result.Message
	}
	return outcome
}

func lastCloudWorkerJSONLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") {
			return line
		}
	}
	return ""
}

func cloudWorkerHookSkipped(output string) bool {
	var result struct {
		Status string `json:"status"`
	}
	return json.Unmarshal([]byte(lastCloudWorkerJSONLine(output)), &result) == nil && result.Status == "skipped"
}

func (h *CloudWorkerHandler) retrySharedCloudWorkerTerms() {
	store, ok := h.credits.(sharedCloudWorkerTermStore)
	if !ok || h.renewScript == "" {
		return
	}
	items, err := store.ListSharedCloudWorkerSyncPending(10)
	if err != nil {
		log.Printf("[cloud-worker] pending shared term lookup failed: %v", err)
		return
	}
	for _, item := range items {
		lock := h.lockOwnerOperation(item.OwnerUID)
		outcome := h.syncSharedCloudWorkerTerm(item.ID)
		lock.Unlock()
		if outcome.Status != types.CloudWorkerRenewApplied {
			log.Printf("[cloud-worker] pending shared term tenant=%s failed: %s", item.TenantName, outcome.Message)
		}
	}
}

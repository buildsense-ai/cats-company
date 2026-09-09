package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type cloudWorkerBillingStore interface {
	GetCloudWorkerBillingLifecycle(string) (*types.CloudWorkerLifecycle, error)
	RequestCloudWorkerConversion(int64) (bool, error)
	ClaimCloudWorkerBillingAction(int64, string, bool) (bool, error)
	CompleteCloudWorkerBillingAction(int64, string, time.Time, string) error
	ClaimCloudWorkerTrialRelease(int64) (bool, error)
	MarkCloudWorkerLifecycleDeleted(int64, string) error
}

type CloudWorkerAdminBillingHandler interface {
	HandleAdminBilling(http.ResponseWriter, *http.Request)
}

// Caller holds opMu. The database claim protects against overlapping servers,
// payment fulfillment and expiry work already read from an older snapshot.
func (h *CloudWorkerHandler) runBillingAction(item types.CloudWorkerLifecycle, action string, automatic bool) error {
	store, ok := h.credits.(cloudWorkerBillingStore)
	if !ok || h.renewScript == "" {
		return fmt.Errorf("billing operations unavailable")
	}
	claimed, err := store.ClaimCloudWorkerBillingAction(item.ID, action, automatic)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("billing action already running or lifecycle changed")
	}
	script := filepath.Join(filepath.Dir(h.renewScript), "billing-worker.sh")
	out, err := h.runScript(script, "--name", item.TenantName, "--action", action)
	if err != nil && strings.TrimSpace(out) != "" {
		err = fmt.Errorf("%w: %s", err, truncateWorkerOutput(out))
	}
	var expiry time.Time
	if err == nil && action == "convert" {
		var result cloudWorkerRenewalResult
		result, err = parseCloudWorkerRenewalResult(out)
		if err == nil && (result.AutoRenewDisabled == nil || !*result.AutoRenewDisabled) {
			err = fmt.Errorf("provider automatic renewal disable was not confirmed")
		}
		expiry = result.ExpiresAt
	}
	if err != nil {
		_ = store.CompleteCloudWorkerBillingAction(item.ID, action, time.Time{}, truncateWorkerOutput(err.Error()))
		return err
	}
	if err = store.CompleteCloudWorkerBillingAction(item.ID, action, expiry, ""); err != nil {
		return fmt.Errorf("provider completed; lifecycle reconciliation required: %w", err)
	}
	h.requestCloudStatusRefresh(true)
	return nil
}

func (h *CloudWorkerHandler) HandleAdminBilling(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	store, ok := h.credits.(cloudWorkerBillingStore)
	if !ok || h.renewScript == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "billing operations unavailable"})
		return
	}
	var input struct {
		Tenant string `json:"tenant_name"`
		Action string `json:"action"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if json.NewDecoder(r.Body).Decode(&input) != nil || !workerUsernameRe.MatchString(input.Tenant) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid billing request"})
		return
	}
	if input.Action != "convert" && input.Action != "suspend" && input.Action != "start" && input.Action != "release" && input.Action != "cancel" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid billing action"})
		return
	}
	if !h.tryBeginOperation(w) {
		return
	}
	defer h.opMu.Unlock()
	item, err := store.GetCloudWorkerBillingLifecycle(input.Tenant)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "managed lifecycle not found"})
		return
	}
	if input.Action == "convert" && item.BillingMode == types.CloudWorkerMonthly {
		writeJSON(w, http.StatusOK, map[string]string{"status": "monthly"})
		return
	}
	if input.Action == "convert" {
		var accepted bool
		accepted, err = store.RequestCloudWorkerConversion(item.ID)
		if err != nil || !accepted {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "instance is being released or cannot be converted"})
			return
		}
	}
	if input.Action == "release" {
		if h.destroyScript == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "release unavailable"})
			return
		}
		claimed, claimErr := store.ClaimCloudWorkerTrialRelease(item.ID)
		if claimErr != nil || !claimed {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "only idle, unconverted trial instances may be released"})
			return
		}
		_, err = h.runScript(h.destroyScript, "--name", item.TenantName)
		if err == nil {
			err = h.db.DeleteBot(item.WorkerUID)
		}
		message := ""
		if err != nil {
			message = truncateWorkerOutput(err.Error())
		}
		if markErr := store.MarkCloudWorkerLifecycleDeleted(item.ID, message); err == nil {
			err = markErr
		}
	} else {
		err = h.runBillingAction(*item, input.Action, false)
	}
	if err != nil {
		log.Printf("[cloud-worker] billing action=%s tenant=%s failed: %v", input.Action, item.TenantName, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "cloud billing action failed; inspect the lifecycle error before retrying", "code": "cloud_worker_billing_failed"})
		return
	}
	h.requestCloudStatusRefresh(true)
	writeJSON(w, http.StatusOK, map[string]string{"status": "completed", "action": input.Action, "tenant_name": input.Tenant})
}

func (h *CloudWorkerHandler) renewTrial(item types.CloudWorkerLifecycle) bool {
	if item.BillingMode != types.CloudWorkerOnDemand {
		return false
	}
	store, ok := h.credits.(cloudWorkerBillingStore)
	if !ok {
		return true
	}
	accepted, err := store.RequestCloudWorkerConversion(item.ID)
	if err == nil && accepted {
		err = h.runBillingAction(item, "convert", false)
	}
	if err != nil {
		log.Printf("[cloud-worker] paid trial conversion tenant=%s failed: %v", item.TenantName, err)
	}
	return true
}

func trialNotice(item types.CloudWorkerLifecycle, now time.Time) string {
	if item.BillingMode != types.CloudWorkerOnDemand {
		return ""
	}
	if item.ConversionPending {
		return "付费开通处理中，正在确认云员工转换结果。"
	}
	if now.Before(item.PackageExpiresAt) {
		return "试用至 " + item.PackageExpiresAt.UTC().Format("2006-01-02 15:04 UTC") + "，到期后暂停；付费可继续使用原云员工。"
	}
	return strings.Join([]string{"试用已到期，请在套餐页付费继续使用。数据预计保留至", item.DeleteAfter.UTC().Format("2006-01-02 15:04 UTC"), "，之后将释放实例并删除数据。"}, " ")
}

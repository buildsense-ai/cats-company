package server

import (
	"encoding/json"
	"errors"
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

// cloudWorkerBillingClaimWindow must match the stale-claim window in
// ClaimCloudWorkerBillingAction: after it, the store lets a new action take
// over from a crashed one, so readers stop treating the old marker as busy.
const cloudWorkerBillingClaimWindow = 30 * time.Minute

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
	// renew is the operator-driven counterpart of the payment renewal path: it
	// buys one more provider subscription cycle for exactly one lifecycle and
	// writes the provider's own expiry back. It deliberately reuses
	// renewOneCloudWorker so a manual renewal can never drift from what a paid
	// package renewal does, including the expiry/retention reconciliation.
	if input.Action != "convert" && input.Action != "suspend" && input.Action != "start" && input.Action != "release" && input.Action != "cancel" && input.Action != "renew" {
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
	if input.Action == "renew" {
		h.handleAdminRenew(w, item)
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
		var destroyOut string
		destroyOut, err = h.runScript(h.destroyScript, "--name", item.TenantName)
		if err == nil {
			err = h.db.DeleteBot(item.WorkerUID)
		}
		message := ""
		if err != nil {
			// Keep the provider's own refusal with the error; a bare exit code
			// leaves operators guessing why the release failed.
			message = truncateWorkerOutput(destroyErrorDetail(err, destroyOut))
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

// handleAdminRenew extends one paid worker's provider subscription by a single
// cycle on operator request. Caller holds opMu, which serialises it against
// every other provider operation in this process; the DB claim is not reused
// here because it is drawn around the on-demand billing machine only.
func (h *CloudWorkerHandler) handleAdminRenew(w http.ResponseWriter, item *types.CloudWorkerLifecycle) {
	switch {
	case item.BillingMode != types.CloudWorkerMonthly:
		// A trial buys its month through convert; renewing it here would skip
		// the conversion claim and leave the lifecycle on the trial billing
		// path with a paid machine behind it.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "only monthly instances can be renewed; convert the trial first",
			"code":  "cloud_worker_renew_requires_monthly",
		})
		return
	case item.State == "delete_running" || item.State == "deleted":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "instance is being released",
			"code":  "cloud_worker_renew_unavailable",
		})
		return
	case item.BillingAction != "" && time.Since(item.BillingActionStartedAt) < cloudWorkerBillingClaimWindow:
		// A conversion or shutdown is mid-flight at the provider; renewing now
		// would race with it. The window matches the store's stale claim rule, so
		// a marker left behind by a crashed process cannot block a paid renewal
		// forever.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "another billing action is already running for this instance",
			"code":  "cloud_worker_billing_busy",
		})
		return
	}
	outcome := h.renewOneCloudWorker(item.OwnerUID, *item)
	if outcome.Status != types.CloudWorkerRenewApplied {
		log.Printf("[cloud-worker] admin renew tenant=%s uid=%d failed: %s", item.TenantName, item.OwnerUID, outcome.Message)
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error":  "provider renewal failed; inspect the lifecycle error before retrying",
			"code":   "cloud_worker_billing_failed",
			"detail": outcome.Message,
		})
		return
	}
	expiresAt := ""
	if outcome.ExpiresAt != nil {
		expiresAt = outcome.ExpiresAt.UTC().Format(time.RFC3339)
	}
	log.Printf("[cloud-worker] admin renew tenant=%s uid=%d completed expires_at=%s %s", item.TenantName, item.OwnerUID, expiresAt, outcome.Message)
	h.requestCloudStatusRefresh(true)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "completed",
		"action":      "renew",
		"tenant_name": item.TenantName,
		"expires_at":  expiresAt,
		"message":     outcome.Message,
	})
}

// renewTrial converts an on-demand trial instance into the paid lifecycle.
// handled reports whether the trial path owned the event (non-trial
// lifecycles return false); err reports a rejected or failed conversion so
// the caller can record it instead of claiming success.
func (h *CloudWorkerHandler) renewTrial(item types.CloudWorkerLifecycle) (handled bool, err error) {
	if item.BillingMode != types.CloudWorkerOnDemand {
		return false, nil
	}
	store, ok := h.credits.(cloudWorkerBillingStore)
	if !ok {
		return true, errors.New("cloud worker billing store unavailable")
	}
	accepted, err := store.RequestCloudWorkerConversion(item.ID)
	if err == nil && accepted {
		err = h.runBillingAction(item, "convert", false)
	} else if err == nil {
		err = errors.New("trial conversion was rejected: instance is being released or cannot be converted")
	}
	if err != nil {
		log.Printf("[cloud-worker] paid trial conversion tenant=%s failed: %v", item.TenantName, err)
	}
	return true, err
}

// trialNotice renders the lifecycle hint shown next to a trial cloud worker.
// The sweep runs once a minute, so a fulfilled payment is normally resolved
// inside that window; the copy states that expectation instead of leaving the
// user on an open-ended "processing" message. Close to expiry it turns into a
// countdown so the pause never arrives unannounced.
func trialNotice(item types.CloudWorkerLifecycle, now time.Time) string {
	if item.BillingMode != types.CloudWorkerOnDemand {
		return ""
	}
	if item.ConversionPending {
		return "已收到付费，正在转为包月并启动云员工，通常 1 分钟内完成。"
	}
	if now.Before(item.PackageExpiresAt) {
		expires := item.PackageExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
		if remaining := item.PackageExpiresAt.Sub(now); remaining <= trialNoticeCountdownWindow {
			return "试用剩余 " + trialNoticeRemaining(remaining) + "（至 " + expires + "），到期后云员工将自动暂停；付费可继续使用原云员工。"
		}
		return "试用至 " + expires + "，到期后暂停；付费可继续使用原云员工。"
	}
	return strings.Join([]string{
		"试用已到期：云员工已暂停，付费可立即恢复。数据保留至",
		item.DeleteAfter.UTC().Format("2006-01-02 15:04 UTC"),
		"，之后将释放实例并删除数据。",
	}, " ")
}

// trialNoticeCountdownWindow is how long before expiry the hint switches from
// a plain date to a countdown. Three days gives a user time to renew on a
// weekday instead of discovering the pause on the weekend.
const trialNoticeCountdownWindow = 72 * time.Hour

// trialNoticeRemaining renders a coarse countdown: hours below two days, whole
// days above. Sub-hour remainders stay explicit so a near-expiry worker never
// reads as "0 天".
func trialNoticeRemaining(d time.Duration) string {
	if d <= 0 {
		return "不足 1 小时"
	}
	if d < 48*time.Hour {
		hours := int(d / time.Hour)
		if hours < 1 {
			return "不足 1 小时"
		}
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
}

package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

var manualCloudWorkerCreditRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)

// HandleCloudWorkerCredits grants one-time cloud-worker creation credits from
// the local account-admin surface. It is deliberately separate from model
// quota grants and from CATSCO_WORKER_CREATE_QUOTA so paid users cannot gain a
// second entitlement through an operator rollout override.
func (h *AccountAdminHandler) HandleCloudWorkerCredits(w http.ResponseWriter, r *http.Request) {
	if !h.requireLocal(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	admin, ok := h.cloudWorkerCredits.(CloudWorkerCreditAdminStore)
	if !ok || admin == nil {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker credit store unavailable"})
		return
	}
	var req struct {
		UID               int64  `json:"uid"`
		Count             int    `json:"count"`
		SourceRef         string `json:"source_ref"`
		ExpiresAt         string `json:"expires_at"`
		DeploymentProfile string `json:"deployment_profile"`
		BillingMode       string `json:"billing_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid credit grant request"})
		return
	}
	if req.UID <= 0 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "uid is required"})
		return
	}
	if req.Count <= 0 || req.Count > 100 {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "count must be between 1 and 100"})
		return
	}
	req.SourceRef = strings.TrimSpace(req.SourceRef)
	if !manualCloudWorkerCreditRefRe.MatchString(req.SourceRef) {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "source_ref must be a safe idempotency key"})
		return
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(req.ExpiresAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil || !parsed.After(time.Now().UTC()) {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_at must be a future RFC3339 timestamp"})
			return
		}
		parsed = parsed.UTC()
		expiresAt = &parsed
	}
	profile, valid := types.NormalizeCloudWorkerProfile(req.DeploymentProfile)
	if !valid {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid deployment profile"})
		return
	}
	var granted int
	var err error
	billing, valid := types.NormalizeCloudWorkerBilling(req.BillingMode)
	if !valid || (billing == types.CloudWorkerOnDemand && expiresAt == nil) {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "on-demand trial requires a valid billing mode and future expiry"})
		return
	}
	if configured, ok := h.cloudWorkerCredits.(cloudWorkerBillingCredits); ok {
		granted, err = configured.GrantCloudWorkerConfiguredCredits(req.UID, req.Count, req.SourceRef, expiresAt, profile, billing)
	} else if billing != types.CloudWorkerMonthly {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "billing configuration unavailable"})
		return
	} else if profiled, ok := h.cloudWorkerCredits.(cloudWorkerProfileCredits); ok {
		granted, err = profiled.GrantCloudWorkerProfileCredits(req.UID, req.Count, req.SourceRef, expiresAt, profile)
	} else if profile == types.CloudWorkerPrivateNAT {
		granted, err = admin.GrantCloudWorkerCredits(req.UID, req.Count, req.SourceRef, expiresAt)
	} else {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "deployment profile store unavailable"})
		return
	}
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to grant cloud worker credits"})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"ok":                 true,
		"uid":                req.UID,
		"requested":          req.Count,
		"granted":            granted,
		"source_ref":         req.SourceRef,
		"deployment_profile": profile,
		"billing_mode":       billing,
	})
}

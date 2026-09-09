package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// HandleAdminProvision is the protected commercial-operations action used by
// the internal "补权益并自动部署" button.  It grants at most one missing
// creation credit (idempotently) and then delegates to HandleCreate so the
// normal tenant, credential, provider cleanup, lifecycle, and friendship
// logic remains the single source of truth.
func (h *CloudWorkerHandler) HandleAdminProvision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h == nil || h.credits == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker credit store unavailable"})
		return
	}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var input struct {
		UID               int64  `json:"uid"`
		Username          string `json:"username"`
		DisplayName       string `json:"display_name"`
		SourceRef         string `json:"source_ref"`
		ExpiresAt         string `json:"expires_at"`
		DeploymentProfile string `json:"deployment_profile"`
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provision request"})
		return
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provision request"})
		return
	}
	if input.UID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "uid is required"})
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if input.Username == "" && manualCloudWorkerCreditRefRe.MatchString(input.SourceRef) {
		suffix := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", input.UID, input.SourceRef)))
		input.Username = fmt.Sprintf("bot-%d-%x", input.UID, suffix[:5])
	}
	if !workerUsernameRe.MatchString(input.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid username"})
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" {
		input.DisplayName = input.Username
	}
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	if !manualCloudWorkerCreditRefRe.MatchString(input.SourceRef) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source_ref must be a safe idempotency key"})
		return
	}

	profile, valid := types.NormalizeCloudWorkerProfile(input.DeploymentProfile)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid deployment profile"})
		return
	}
	if _, err := h.deploymentForProfile(profile); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "selected deployment profile is not configured"})
		return
	}
	_, available, err := h.credits.CloudWorkerCreditSummary(input.UID)
	if profiled, ok := h.credits.(cloudWorkerProfileCredits); ok {
		_, available, err = profiled.CloudWorkerProfileCreditSummary(input.UID, profile)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read cloud worker credits"})
		return
	}
	// Credits of the other profile cannot authorize the requested deployment.
	if available == 0 {
		// A package expiry is the default for the button.  Requiring a bounded
		// expiry when a new credit must be issued prevents an operator typo from
		// creating a perpetual paid cloud entitlement.
		expiresAt, expiryErr := cloudWorkerProvisionExpiry(h.db, input.UID, input.ExpiresAt)
		if expiryErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": expiryErr.Error()})
			return
		}
		admin, ok := h.credits.(CloudWorkerCreditAdminStore)
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker credit admin unavailable"})
			return
		}
		var grantErr error
		if profiled, ok := h.credits.(cloudWorkerProfileCredits); ok {
			_, grantErr = profiled.GrantCloudWorkerProfileCredits(input.UID, 1, input.SourceRef, expiresAt, profile)
		} else if profile == types.CloudWorkerPrivateNAT {
			_, grantErr = admin.GrantCloudWorkerCredits(input.UID, 1, input.SourceRef, expiresAt)
		} else {
			grantErr = fmt.Errorf("deployment profile store unavailable")
		}
		if grantErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to grant cloud worker credit"})
			return
		}
	}

	createBody, _ := json.Marshal(BotRegisterRequest{Username: input.Username, DisplayName: input.DisplayName, Role: "general"})
	createReq := r.Clone(context.WithValue(r.Context(), uidKey, input.UID))
	createReq = createReq.WithContext(context.WithValue(createReq.Context(), cloudWorkerProfileContextKey{}, profile))
	createReq.Body = io.NopCloser(bytes.NewReader(createBody))
	h.HandleCreate(w, createReq)
}

func cloudWorkerProvisionExpiry(db interface{}, uid int64, raw string) (*time.Time, error) {
	if value := strings.TrimSpace(raw); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil || !parsed.After(time.Now().UTC()) {
			return nil, errInvalidCloudWorkerProvisionExpiry
		}
		parsed = parsed.UTC()
		return &parsed, nil
	}
	if store, ok := db.(CommercialStore); ok {
		summary, err := store.GetCommercialSummary(uid)
		if err != nil {
			return nil, errInvalidCloudWorkerProvisionExpiry
		}
		var latest *time.Time
		if summary != nil {
			for _, entitlement := range summary.Entitlements {
				if entitlement == nil || strings.ToLower(strings.TrimSpace(entitlement.State)) != "active" || entitlement.ExpiresAt == nil {
					continue
				}
				if latest == nil || entitlement.ExpiresAt.After(*latest) {
					value := entitlement.ExpiresAt.UTC()
					latest = &value
				}
			}
		}
		if latest != nil && latest.After(time.Now().UTC()) {
			return latest, nil
		}
	}
	return nil, errInvalidCloudWorkerProvisionExpiry
}

var errInvalidCloudWorkerProvisionExpiry = provisionExpiryError{}

type provisionExpiryError struct{}

func (provisionExpiryError) Error() string {
	return "expires_at must be a future RFC3339 timestamp or the user must have an active package"
}

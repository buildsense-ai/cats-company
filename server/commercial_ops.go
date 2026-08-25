package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	commercialOpsReadScope  = "commercial.ops.read"
	commercialOpsWriteScope = "commercial.ops.write"
	commercialOpsBodyLimit  = 64 * 1024
)

type CommercialOperationsStore interface {
	GetCommercialOperationsOverview(now time.Time) (*types.CommercialOperationsOverview, error)
	RecordCommercialOperatorEvent(event *types.CommercialOperatorEvent) error
}

type commercialOpsServiceContextKey struct{}

type CommercialOpsHandler struct {
	admin                  *AccountAdminHandler
	services               AccountServiceVerifier
	store                  CommercialOperationsStore
	cloudWorkers           CloudWorkerAdminOverviewHandler
	cloudWorkerProvisioner interface {
		HandleAdminProvision(http.ResponseWriter, *http.Request)
	}
}

func NewCommercialOpsHandler(admin *AccountAdminHandler, services AccountServiceVerifier, store CommercialOperationsStore) *CommercialOpsHandler {
	return &CommercialOpsHandler{admin: admin, services: services, store: store}
}

// SetCloudWorkerAdmin wires the read-only platform roster. It is deliberately
// optional so deployments without the cloud-worker tables remain compatible;
// the endpoint returns 503 instead of exposing a partial or public fallback.
func (h *CommercialOpsHandler) SetCloudWorkerAdmin(handler CloudWorkerAdminOverviewHandler) {
	if h != nil {
		h.cloudWorkers = handler
		if provisioner, ok := handler.(interface {
			HandleAdminProvision(http.ResponseWriter, *http.Request)
		}); ok {
			h.cloudWorkerProvisioner = provisioner
		}
	}
}

func commercialOpsServiceFromRequest(r *http.Request) (AccountService, bool) {
	if r == nil {
		return AccountService{}, false
	}
	service, ok := r.Context().Value(commercialOpsServiceContextKey{}).(AccountService)
	return service, ok && strings.TrimSpace(service.Slug) != ""
}

func withCommercialOpsService(r *http.Request, service AccountService) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), commercialOpsServiceContextKey{}, service))
}

func commercialOpsAllows(service AccountService, required string) bool {
	// Commercial operations are intentionally stricter than legacy account
	// endpoints: an unscoped service token never receives operator access.
	for _, scope := range service.Scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == required || (required == commercialOpsReadScope && scope == commercialOpsWriteScope) {
			return true
		}
	}
	return false
}

func (h *CommercialOpsHandler) requireService(w http.ResponseWriter, r *http.Request, write bool) (AccountService, bool) {
	if !isLocalAdminRequest(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "commercial operations are only available from an internal source"})
		return AccountService{}, false
	}
	if h == nil || h.services == nil || !h.services.Configured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial operations service auth is not configured"})
		return AccountService{}, false
	}
	service, ok := h.services.Verify(extractServiceToken(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid service token"})
		return AccountService{}, false
	}
	required := commercialOpsReadScope
	if write {
		required = commercialOpsWriteScope
	}
	if !commercialOpsAllows(service, required) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "service scope denied"})
		return AccountService{}, false
	}
	return service, true
}

func (h *CommercialOpsHandler) HandleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if _, ok := h.requireService(w, r, false); !ok {
		return
	}
	if h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial operations store unavailable"})
		return
	}
	if h.admin != nil {
		if paymentStore, ok := h.admin.commercial.(CommercialPaymentStore); ok {
			_, _ = paymentStore.CloseExpiredCommercialOrders(100)
		}
	}
	overview, err := h.store.GetCommercialOperationsOverview(time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load commercial operations overview"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"overview": overview})
}

func (h *CommercialOpsHandler) HandlePlans(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "plans.upsert", "plan", h.admin.HandleCommercialPlans)
}

func (h *CommercialOpsHandler) HandleInvites(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "invites.upsert", "invite", h.admin.HandleCommercialInvites)
}

func (h *CommercialOpsHandler) HandleGrants(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "grants.create", "user", h.admin.HandleCommercialGrant)
}

func (h *CommercialOpsHandler) HandleCloudWorkerCredits(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "cloud_worker_credits.create", "user", h.admin.HandleCloudWorkerCredits)
}

// HandleCloudWorkerProvision is an internal-only write action.  The service
// scope and local-source checks happen here; the cloud-worker handler then
// grants a missing credit and reuses the normal owner create path.
func (h *CommercialOpsHandler) HandleCloudWorkerProvision(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.cloudWorkerProvisioner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker provisioner unavailable"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	service, ok := h.requireService(w, r, true)
	if !ok {
		return
	}
	targetRef := commercialOpsTargetRef(r)
	tracked := &commercialOpsResponseWriter{ResponseWriter: w}
	h.cloudWorkerProvisioner.HandleAdminProvision(tracked, withCommercialOpsService(r, service))
	if h.store != nil {
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		if err := h.store.RecordCommercialOperatorEvent(&types.CommercialOperatorEvent{
			Service: service.Slug, Action: "cloud_worker.provision", TargetType: "cloud_worker", TargetRef: targetRef, StatusCode: status,
		}); err != nil {
			log.Printf("failed to record commercial operator event service=%s action=cloud_worker.provision: %v", service.Slug, err)
		}
	}
}

func (h *CommercialOpsHandler) HandleCloudWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial operations handler unavailable"})
		return
	}
	service, ok := h.requireService(w, r, false)
	if !ok {
		return
	}
	if h.cloudWorkers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker admin unavailable"})
		return
	}
	tracked := &commercialOpsResponseWriter{ResponseWriter: w}
	h.cloudWorkers.HandleAdminOverview(tracked, withCommercialOpsService(r, service))
	status := tracked.status
	if status == 0 {
		status = http.StatusOK
	}
	if h.store != nil {
		if err := h.store.RecordCommercialOperatorEvent(&types.CommercialOperatorEvent{
			Service: service.Slug, Action: "cloud_workers.read", TargetType: "cloud_worker_roster", StatusCode: status,
		}); err != nil {
			log.Printf("failed to record commercial operator event service=%s action=cloud_workers.read: %v", service.Slug, err)
		}
	}
}

func (h *CommercialOpsHandler) HandleAdjustments(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "adjustments.apply", "user", h.admin.HandleCommercialAdjustment)
}

func (h *CommercialOpsHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "users.read", "user", h.admin.HandleCommercialUserSummary)
}

func (h *CommercialOpsHandler) HandleOrders(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "orders.read", "order", h.admin.HandleCommercialOrders)
}

func (h *CommercialOpsHandler) HandleOrderRefund(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "orders.refund", "order", h.admin.HandleCommercialOrderRefund)
}

func (h *CommercialOpsHandler) HandleRelayDryRun(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "relay.dry-run", "user", h.admin.HandleCommercialRelayDryRun)
}

func (h *CommercialOpsHandler) HandleRelaySync(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r, "relay.sync", "user", h.admin.HandleCommercialRelaySync)
}

func (h *CommercialOpsHandler) forward(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	targetType string,
	handler http.HandlerFunc,
) {
	if h == nil || h.admin == nil || handler == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial operations handler unavailable"})
		return
	}
	writeOperation := r.Method != http.MethodGet && r.Method != http.MethodHead
	service, ok := h.requireService(w, r, writeOperation)
	if !ok {
		return
	}
	targetRef := commercialOpsTargetRef(r)
	tracked := &commercialOpsResponseWriter{ResponseWriter: w}
	handler(tracked, withCommercialOpsService(r, service))
	if !writeOperation || h.store == nil {
		return
	}
	status := tracked.status
	if status == 0 {
		status = http.StatusOK
	}
	if err := h.store.RecordCommercialOperatorEvent(&types.CommercialOperatorEvent{
		Service:    service.Slug,
		Action:     action,
		TargetType: targetType,
		TargetRef:  targetRef,
		StatusCode: status,
	}); err != nil {
		log.Printf("failed to record commercial operator event service=%s action=%s: %v", service.Slug, action, err)
	}
}

func commercialOpsTargetRef(r *http.Request) string {
	if r == nil {
		return ""
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return strings.TrimSpace(r.URL.Query().Get("uid"))
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, commercialOpsBodyLimit+1))
	if err != nil {
		return ""
	}
	// Preserve the complete body for the real handler even when the small
	// audit preview reaches its limit.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
	if len(raw) > commercialOpsBodyLimit {
		return "oversized"
	}
	var body map[string]interface{}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	for _, key := range []string{"order_no", "code", "slug", "uid"} {
		if value, exists := body[key]; exists {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(toCommercialOpsString(value), "\""), "\""))
		}
	}
	return ""
}

func toCommercialOpsString(value interface{}) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

type commercialOpsResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *commercialOpsResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *commercialOpsResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

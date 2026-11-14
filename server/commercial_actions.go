package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type CommercialActionStore interface {
	PreviewCommercialAction(types.CommercialActionRequest) (*types.CommercialActionPreview, error)
	ApplyCommercialAction(types.CommercialActionRequest) (*types.CommercialAction, error)
	GetCommercialAction(int64, string) (*types.CommercialAction, error)
	ClaimCommercialAction(string) (*types.CommercialAction, error)
	SaveCommercialAction(*types.CommercialAction) error
}

func (h *CommercialOpsHandler) HandleActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var req types.CommercialActionRequest
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, commercialOpsBodyLimit))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid action request"})
			return
		}
		var extra interface{}
		if decoder.Decode(&extra) != io.EOF {
			writeJSON(w, 400, map[string]string{"error": "invalid action request"})
			return
		}
	}
	service, ok := h.requireService(w, r, r.Method == http.MethodPost && !req.Preview)
	if !ok {
		return
	}
	if h.admin == nil {
		writeJSON(w, 503, map[string]string{"error": "commercial actions unavailable"})
		return
	}
	store, ok := h.admin.commercial.(CommercialActionStore)
	if !ok {
		writeJSON(w, 503, map[string]string{"error": "commercial actions store unavailable"})
		return
	}
	status := http.StatusOK
	// Preview and status remain strictly read-only. Apply uses the existing
	// commercial operations audit sink and scope.
	if r.Method == http.MethodPost && !req.Preview && h.store != nil {
		defer func() {
			if err := h.store.RecordCommercialOperatorEvent(&types.CommercialOperatorEvent{Service: service.Slug, Action: "actions." + req.Intent, TargetType: "user", TargetRef: strconv.FormatInt(req.UID, 10), StatusCode: status}); err != nil {
				log.Printf("commercial action audit: %v", err)
			}
		}()
	}
	fail := func(err error) { status = writeCommercialActionError(w, err) }
	if r.Method == http.MethodGet {
		uid, err := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
		id := r.URL.Query().Get("operation_id")
		if err != nil || uid <= 0 || !types.ValidCommercialActionID(id) {
			status = 400
			writeJSON(w, status, map[string]string{"error": "uid and safe operation_id required"})
			return
		}
		v, err := store.GetCommercialAction(uid, id)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, status, v)
		return
	}
	if err := req.Normalize(); err != nil {
		fail(err)
		return
	}
	if req.Preview {
		p, err := store.PreviewCommercialAction(req)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, status, map[string]interface{}{"preview": p})
		return
	}
	v, err := store.ApplyCommercialAction(req)
	if err != nil {
		fail(err)
		return
	}
	status = http.StatusAccepted
	if v.Operation.Status == "completed" {
		status = http.StatusOK
	}
	writeJSON(w, status, v)
}
func writeCommercialActionError(w http.ResponseWriter, err error) int {
	status := 500
	code := "internal_error"
	message := "commercial action failed"
	var e *types.CommercialAdjustmentError
	if errors.As(err, &e) {
		code = e.Code
		message = e.Message
		switch code {
		case "invalid_request":
			status = 400
		case "not_found":
			status = 404
		default:
			status = 409
		}
	} else {
		log.Printf("commercial action: %v", err)
	}
	writeJSON(w, status, map[string]string{"error": message, "code": code})
	return status
}

// StartActions owns durable work independently of any request or browser. All
// servers may run it; claims and owner gates are enforced by PostgreSQL.
func (h *CommercialOpsHandler) StartActions(ctx context.Context) {
	if h == nil || h.admin == nil {
		return
	}
	store, ok := h.admin.commercial.(CommercialActionStore)
	if !ok {
		return
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			for i := 0; i < 20; i++ {
				if ctx.Err() != nil {
					return
				}
				v, err := store.ClaimCommercialAction(types.CommercialActionHash(fmt.Sprintf("%d", time.Now().UnixNano())))
				if err != nil {
					log.Printf("claim commercial action: %v", err)
					break
				}
				if v == nil {
					break
				}
				h.runCommercialAction(ctx, v)
				if err = store.SaveCommercialAction(v); err != nil {
					log.Printf("save commercial action: %v", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (h *CommercialOpsHandler) runCommercialAction(ctx context.Context, v *types.CommercialAction) {
	o := &v.Operation
	o.Error = ""
	if !o.RelaySynced {
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := h.admin.applyCommercialAdjustmentToRelay(syncCtx, o.UID, v.Preview.PackageEffect, v.Result)
		cancel()
		if err != nil {
			o.Error = "账本已生效，Relay 同步待重试：" + err.Error()
			if h.admin.commercialRelaySyncer != nil {
				h.admin.commercialRelaySyncer.Enqueue(o.UID)
			}
		} else {
			o.RelaySynced = true
		}
	}
	if o.WorkerStatus != "none" && o.WorkerStatus != "created" && o.WorkerStatus != "kept" {
		worker, ok := h.cloudWorkers.(*CloudWorkerHandler)
		if !ok {
			o.WorkerStatus = "failed"
			o.Error = joinCommercialActionError(o.Error, "worker handler unavailable")
		} else {
			status, uid, err := worker.executeCommercialActionWorker(ctx, v)
			o.WorkerStatus = status
			o.WorkerUID = uid
			if err != nil {
				o.Error = joinCommercialActionError(o.Error, err.Error())
			}
		}
	}
	o.Status = "pending"
	if o.Error != "" || o.WorkerStatus == "failed" {
		o.Status = "failed"
	} else if o.RelaySynced && (o.WorkerStatus == "none" || o.WorkerStatus == "kept" || o.WorkerStatus == "created") {
		o.Status = "completed"
	}
}
func joinCommercialActionError(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

type commercialWorkerActionContextKey struct{}
type commercialWorkerOwnerGate interface {
	AcquireCommercialWorkerOwnerGate(int64) (func(), bool, error)
}
type commercialWorkerActionStore interface {
	ReserveCommercialOperationCredit(int64, string, string, types.CommercialActionPreview) (types.CloudWorkerCreditSelection, bool, error)
	ReconcileCommercialOperationWorker(int64, string) (string, int64, error)
	CompleteCommercialOperationWorker(int64, string, int64) error
	KeepCommercialOperationWorker(int64, string, int64) error
}

// This runs only after both HandleCreate owner gates have been acquired.
func (h *CloudWorkerHandler) prepareCommercialActionWorker(w http.ResponseWriter, uid int64, v *types.CommercialAction) bool {
	store, ok := h.credits.(commercialWorkerActionStore)
	if !ok {
		writeJSON(w, 503, map[string]string{"error": "operation credits unavailable"})
		return true
	}
	state, worker, err := store.ReconcileCommercialOperationWorker(uid, v.Operation.OperationID)
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error(), "code": "cloud_worker_reconciliation_required"})
		return true
	}
	if state == "created" || state == "kept" {
		if state == "kept" && h.confirmCommercialKeptRetention(w, uid, worker, v) {
			return true
		}
		writeJSON(w, 200, map[string]interface{}{"worker_status": state, "uid": worker})
		return true
	}
	if v.Request.WorkerAction == "ensure" {
		// The preview is not trusted here: the machine set is re-read and
		// re-classified at execution time, so a stale preview cannot make this
		// operation adopt a machine of another deployment class.
		target, found, err := h.commercialActionEnsureTarget(uid, v.Preview.DeploymentProfile)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "cloud_worker_reconciliation_required"})
			return true
		}
		if found {
			if err = store.KeepCommercialOperationWorker(uid, types.CommercialActionSource(uid, v.Operation.OperationID), target); err != nil {
				log.Printf("[cloud-worker] operation keep for uid %d worker %d rejected: %v", uid, target, err)
				writeJSON(w, http.StatusConflict, map[string]string{"error": "failed to retain existing worker", "code": "cloud_worker_keep_rejected"})
				return true
			}
			if h.confirmCommercialKeptRetention(w, uid, target, v) {
				return true
			}
			writeJSON(w, 200, map[string]interface{}{"worker_status": "kept", "uid": target})
			return true
		}
	}
	indefinite := v.Preview.DeploymentProfile == types.CloudWorkerPackedShared &&
		(v.Preview.WorkerExpiryMode == "permanent" || v.Preview.WorkerExpiryMode == "follow_plan") && v.Preview.WorkerExpiresAt == nil
	if !indefinite && (v.Preview.WorkerExpiresAt == nil || !v.Preview.WorkerExpiresAt.After(time.Now().UTC())) {
		writeJSON(w, 409, map[string]string{"error": "operation worker expiry has passed", "code": "cloud_worker_operation_expired"})
		return true
	}
	return false
}

// commercialActionEnsureTarget answers the only question an ensure may ask:
// which of this owner's machines, if any, is an instance of the requested
// deployment class. A shared-container worker and a dedicated ECS instance are
// different products, so the first machine in the roster is only adoptable when
// its class matches; otherwise the operation has to provision one.
//
// Two rules keep that safe:
//   - every machine's recorded profile is resolved and an unresolvable one is an
//     error, never a silent "this account has no machine" (which would buy a
//     second machine behind a failed lookup);
//   - a machine whose lifecycle is already shutting down is not silently skipped
//     either. It is not adoptable (the destroy is a decision already made), and
//     creating a second one next to it would bill twice for the same product, so
//     the operation stops for a human instead.
func (h *CloudWorkerHandler) commercialActionEnsureTarget(uid int64, requested string) (int64, bool, error) {
	class, valid := types.CloudWorkerDeploymentClass(requested)
	if !valid {
		return 0, false, fmt.Errorf("unsupported operation deployment profile %q", requested)
	}
	workers, err := h.cloudWorkersOfOwner(uid)
	if err != nil {
		return 0, false, fmt.Errorf("failed to read owner workers: %w", err)
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].UID < workers[j].UID })
	var unavailable int64
	for _, worker := range workers {
		if worker.LifecycleState == "deleted" {
			continue
		}
		profile := worker.DeploymentProfile
		workerClass, valid := types.CloudWorkerDeploymentClass(profile)
		if !valid {
			return 0, false, fmt.Errorf("worker %d has an unsupported deployment profile %q", worker.UID, profile)
		}
		if workerClass != class {
			continue
		}
		if !types.CloudWorkerCanBeAdopted(worker.LifecycleState, worker.BillingAction) {
			if unavailable == 0 {
				unavailable = worker.UID
			}
			continue
		}
		// The roster order is the existing deterministic one: the first machine
		// of this class is the one adopted.
		return worker.UID, true, nil
	}
	if unavailable != 0 {
		return 0, false, fmt.Errorf("worker %d is a %s worker that is unavailable for adoption; confirm its lifecycle and billing state before provisioning another one", unavailable, class)
	}
	return 0, false, nil
}

// A small in-memory response sink lets durable work reuse all of HandleCreate:
// credentials, official identity, provider cleanup, credit commit and queueing.
type commercialActionResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *commercialActionResponse) Header() http.Header { return w.header }
func (w *commercialActionResponse) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
}
func (w *commercialActionResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(p)
}
func (h *CloudWorkerHandler) executeCommercialActionWorker(ctx context.Context, v *types.CommercialAction) (string, int64, error) {
	raw, _ := json.Marshal(BotRegisterRequest{Username: types.CommercialActionUsername(v.Operation.UID, v.Operation.OperationID), DisplayName: "云员工"})
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/internal/commercial-action-worker", bytes.NewReader(raw))
	r = r.WithContext(context.WithValue(r.Context(), uidKey, v.Operation.UID))
	r = r.WithContext(context.WithValue(r.Context(), commercialWorkerActionContextKey{}, v))
	r = r.WithContext(context.WithValue(r.Context(), cloudWorkerProfileContextKey{}, v.Preview.DeploymentProfile))
	r = r.WithContext(context.WithValue(r.Context(), cloudWorkerBillingContextKey{}, v.Preview.BillingMode))
	r = r.WithContext(context.WithValue(r.Context(), cloudWorkerRuntimeContextKey{}, v.Preview.RuntimePlan))
	response := &commercialActionResponse{header: make(http.Header)}
	h.HandleCreate(response, r)
	var body struct {
		UID    int64  `json:"uid"`
		Status string `json:"worker_status"`
		Code   string `json:"code"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(response.body.Bytes(), &body)
	if response.status == 201 {
		return "created", body.UID, nil
	}
	if response.status == 200 && (body.Status == "created" || body.Status == "kept") {
		return body.Status, body.UID, nil
	}
	if body.Code == "cloud_worker_capacity_queued" {
		return "queued", 0, nil
	}
	if body.Code == "cloud_worker_operation_busy" {
		return "pending", 0, nil
	}
	if body.Error == "" {
		body.Error = "worker execution failed"
	}
	return "failed", body.UID, fmt.Errorf("%s", body.Error)
}

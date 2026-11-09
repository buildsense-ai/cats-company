package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type commercialActionsTestStore struct {
	*commercialOpsTestStore
	previews, applies, gets int
	applyError              error
	action                  *types.CommercialAction
	claim                   func() (*types.CommercialAction, error)
	save                    func(*types.CommercialAction) error
}

func (s *commercialActionsTestStore) PreviewCommercialAction(r types.CommercialActionRequest) (*types.CommercialActionPreview, error) {
	s.previews++
	return &types.CommercialActionPreview{UID: r.UID, Intent: r.Intent, CanApply: true, PreviewToken: "token", Warnings: []string{}}, nil
}
func (s *commercialActionsTestStore) ApplyCommercialAction(r types.CommercialActionRequest) (*types.CommercialAction, error) {
	s.applies++
	if s.applyError != nil {
		return nil, s.applyError
	}
	return s.action, nil
}
func (s *commercialActionsTestStore) GetCommercialAction(int64, string) (*types.CommercialAction, error) {
	s.gets++
	return s.action, nil
}
func (s *commercialActionsTestStore) ClaimCommercialAction(string) (*types.CommercialAction, error) {
	if s.claim != nil {
		return s.claim()
	}
	return nil, nil
}
func (s *commercialActionsTestStore) SaveCommercialAction(v *types.CommercialAction) error {
	if s.save != nil {
		return s.save(v)
	}
	return nil
}
func commercialActionsTestHandler(scope string) (*CommercialOpsHandler, *commercialActionsTestStore) {
	s := &commercialActionsTestStore{commercialOpsTestStore: newCommercialOpsTestStore(), action: &types.CommercialAction{Operation: types.CommercialActionOperation{UID: 7, OperationID: "op1", Intent: "grant_plan", Status: "pending", PackageApplied: true, WorkerStatus: "none"}}}
	admin := NewAccountAdminHandler(accountTestUserLookup{}, nil, nil, s)
	return NewCommercialOpsHandler(admin, commercialOpsTestVerifier{service: AccountService{Slug: "ops", Scopes: []string{scope}}}, s), s
}
func TestCommercialActionsHandlerReadOnlyPreviewAndOneApply(t *testing.T) {
	h, s := commercialActionsTestHandler(commercialOpsWriteScope)
	body := `{"uid":7,"intent":"grant_plan","plan_id":12,"note":"first grant","operation_id":"op1","preview":true}`
	rec := httptest.NewRecorder()
	h.HandleActions(rec, commercialOpsRequest(http.MethodPost, "/api/account/commercial-ops/actions", body))
	if rec.Code != 200 || s.previews != 1 || s.applies != 0 || len(s.events) != 0 {
		t.Fatalf("preview wrote or failed: %d %s %+v", rec.Code, rec.Body, s)
	}
	body = `{"uid":7,"intent":"grant_plan","plan_id":12,"note":"first grant","operation_id":"op1","preview_token":"token"}`
	rec = httptest.NewRecorder()
	h.HandleActions(rec, commercialOpsRequest(http.MethodPost, "/api/account/commercial-ops/actions", body))
	if rec.Code != 202 || s.applies != 1 || s.previews != 1 || len(s.events) != 1 {
		t.Fatalf("apply did not call atomic store once: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.HandleActions(rec, commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/actions?uid=7&operation_id=op1", ""))
	if rec.Code != 200 || s.gets != 1 || s.applies != 1 || len(s.events) != 1 {
		t.Fatalf("GET mutated: %d", rec.Code)
	}
	var response types.CommercialAction
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Operation.OperationID != "op1" {
		t.Fatalf("status contract: %s", rec.Body)
	}
}
func TestCommercialActionsHandlerScopesValidationAndConflict(t *testing.T) {
	h, s := commercialActionsTestHandler(commercialOpsReadScope)
	body := `{"uid":7,"intent":"test_worker","duration_days":2,"preview":true}`
	rec := httptest.NewRecorder()
	h.HandleActions(rec, commercialOpsRequest("POST", "/actions", body))
	if rec.Code != 200 {
		t.Fatalf("read scope preview: %d", rec.Code)
	}
	body = `{"uid":7,"intent":"test_worker","duration_days":2,"note":"test","operation_id":"op1","preview_token":"token"}`
	rec = httptest.NewRecorder()
	h.HandleActions(rec, commercialOpsRequest("POST", "/actions", body))
	if rec.Code != 403 || s.applies != 0 {
		t.Fatal("read scope applied")
	}
	h, s = commercialActionsTestHandler(commercialOpsWriteScope)
	for _, code := range []string{"operation_conflict", "stale_preview"} {
		s.applyError = &types.CommercialAdjustmentError{Code: code, Message: code}
		rec = httptest.NewRecorder()
		h.HandleActions(rec, commercialOpsRequest("POST", "/actions", body))
		if rec.Code != 409 {
			t.Fatalf("%s: %d", code, rec.Code)
		}
	}
	for _, invalid := range []string{`{"uid":7,"intent":"grant_plan","plan_id":1}`, `{"uid":7,"intent":"test_worker","duration_days":91,"preview":true}`, `{"uid":7,"intent":"renew_plan","note":"x","operation_id":"../bad","preview_token":"token"}`} {
		rec = httptest.NewRecorder()
		h.HandleActions(rec, commercialOpsRequest("POST", "/actions", invalid))
		if rec.Code != 400 {
			t.Fatalf("invalid request: %d %s", rec.Code, rec.Body)
		}
	}
}

type commercialWorkerActionTestCredits struct {
	countingCreditStub
	kept, reserved    int
	completed, queued int
	workerUID         int64
	reserveOK         bool
	reconcileError    error
}

func (s *commercialWorkerActionTestCredits) AcquireCommercialWorkerOwnerGate(int64) (func(), bool, error) {
	return func() {}, true, nil
}
func (s *commercialWorkerActionTestCredits) ReserveCommercialOperationCredit(_ int64, _, _ string, p types.CommercialActionPreview) (types.CloudWorkerCreditSelection, bool, error) {
	s.reserved++
	return types.CloudWorkerCreditSelection{Profile: p.DeploymentProfile, BillingMode: p.BillingMode, RuntimePlan: p.RuntimePlan, ExpiresAt: p.WorkerExpiresAt}, s.reserveOK, nil
}
func (s *commercialWorkerActionTestCredits) ReconcileCommercialOperationWorker(int64, string) (string, int64, error) {
	if s.completed > 0 {
		return "created", s.workerUID, nil
	}
	return "pending", 0, s.reconcileError
}
func (s *commercialWorkerActionTestCredits) CompleteCommercialOperationWorker(_ int64, _ string, worker int64) error {
	s.completed++
	s.workerUID = worker
	return nil
}
func (s *commercialWorkerActionTestCredits) KeepCommercialOperationWorker(int64, string, int64) error {
	s.kept++
	return nil
}
func TestCommercialActionWorkerEnsureKeptAndAdditionalExactCredit(t *testing.T) {
	h, s := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{CreateQuota: "42=99"})
	s.ownerBots = []map[string]interface{}{{"id": int64(1001), "tenant_name": "existing"}}
	credits := &commercialWorkerActionTestCredits{}
	h.credits = credits
	expiry := time.Now().Add(time.Hour)
	v := &types.CommercialAction{Operation: types.CommercialActionOperation{UID: 42, OperationID: "worker1"}, Request: types.CommercialActionRequest{WorkerAction: "ensure"}, Preview: types.CommercialActionPreview{DeploymentProfile: "private_nat", BillingMode: "month", WorkerExpiresAt: &expiry}}
	status, uid, err := h.executeCommercialActionWorker(context.Background(), v)
	if err != nil || status != "kept" || uid != 1001 || credits.kept != 1 || credits.reserved != 0 || len(s.tenantNames) != 0 {
		t.Fatalf("ensure: %s %d %v %+v", status, uid, err, credits)
	}
	v.Request.WorkerAction = "additional"
	status, _, err = h.executeCommercialActionWorker(context.Background(), v)
	if err == nil || status != "failed" || credits.reserved != 1 || len(s.tenantNames) != 0 {
		t.Fatalf("static quota bypassed operation credit: %s %v", status, err)
	}
	credits.reconcileError = errors.New("manual reconciliation required")
	status, _, err = h.executeCommercialActionWorker(context.Background(), v)
	if status != "failed" || err == nil || credits.reserved != 1 {
		t.Fatal("ambiguous bot retried creation")
	}
}
func TestCommercialActionWorkerExpirationAndOwnerGate(t *testing.T) {
	h, _ := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{CreateQuota: "42=99"})
	credits := &commercialWorkerActionTestCredits{}
	h.credits = credits
	expiry := time.Now().Add(-time.Hour)
	v := &types.CommercialAction{Operation: types.CommercialActionOperation{UID: 42, OperationID: "expired"}, Request: types.CommercialActionRequest{WorkerAction: "additional"}, Preview: types.CommercialActionPreview{WorkerExpiresAt: &expiry}}
	status, _, err := h.executeCommercialActionWorker(context.Background(), v)
	if status != "failed" || err == nil || credits.reserved != 0 {
		t.Fatal("expired action created worker")
	}
	lock := h.lockOwnerOperation(42)
	defer lock.Unlock()
	status, _, err = h.executeCommercialActionWorker(context.Background(), v)
	if status != "pending" || err != nil || credits.reserved != 0 {
		t.Fatal("owner gate not respected")
	}
}

func (s *commercialWorkerActionTestCredits) QueueCloudWorkerCredit(int64, string) (bool, error) {
	s.queued++
	return true, nil
}
func (s *commercialWorkerActionTestCredits) CloudWorkerQueuePosition(int64) (int, error) {
	return 0, nil
}
func (s *commercialWorkerActionTestCredits) ListQueuedCloudWorkerCredits(time.Duration, int) ([]types.CloudWorkerQueuedCredit, error) {
	return nil, nil
}
func (s *commercialWorkerActionTestCredits) ClaimQueuedCloudWorkerCredit(int64) (bool, error) {
	return false, nil
}

func TestCommercialActionWorkerCreateReplayAndCapacityRetry(t *testing.T) {
	for _, behavior := range []string{"ok", "capacity-full"} {
		t.Run(behavior, func(t *testing.T) {
			cfg := workerScriptCfg(t, "", map[string]string{"provision": writeWorkerOpScript(t, behavior)})
			if cfg.ProvisionScript == "" {
				t.Skip("no POSIX shell")
			}
			h, db := newCloudWorkerTestHandlerCfg(cfg)
			credits := &commercialWorkerActionTestCredits{reserveOK: true}
			h.credits = credits
			expiry := time.Now().Add(time.Hour)
			v := &types.CommercialAction{Operation: types.CommercialActionOperation{UID: 42, OperationID: "once"}, Request: types.CommercialActionRequest{WorkerAction: "additional"}, Preview: types.CommercialActionPreview{DeploymentProfile: "private_nat", BillingMode: "month", WorkerExpiresAt: &expiry}}
			status, uid, err := h.executeCommercialActionWorker(context.Background(), v)
			if err != nil {
				t.Fatal(err)
			}
			if behavior == "capacity-full" {
				if status != "queued" || credits.queued != 1 || credits.completed != 0 || len(db.deletedBots) != 1 {
					t.Fatalf("capacity handling: %s %+v", status, credits)
				}
				return
			}
			if status != "created" || uid == 0 || credits.completed != 1 || credits.reserved != 1 {
				t.Fatalf("create: %s %d %+v", status, uid, credits)
			}
			status, replayUID, err := h.executeCommercialActionWorker(context.Background(), v)
			if err != nil || status != "created" || replayUID != uid || credits.reserved != 1 || credits.completed != 1 {
				t.Fatalf("replay created another worker: %s %d %v %+v", status, replayUID, err, credits)
			}
		})
	}
}

func TestCommercialActionsRecoverAtStartupWithoutHTTP(t *testing.T) {
	h, store := commercialActionsTestHandler(commercialOpsWriteScope)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	claimed := false
	store.action.Operation.RelaySynced = true
	store.claim = func() (*types.CommercialAction, error) {
		if claimed {
			return nil, nil
		}
		claimed = true
		return store.action, nil
	}
	saved := make(chan types.CommercialActionOperation, 1)
	store.save = func(v *types.CommercialAction) error { cancel(); saved <- v.Operation; return nil }
	h.StartActions(ctx)
	select {
	case v := <-saved:
		if v.Status != "completed" || !v.PackageApplied || !v.RelaySynced || store.applies != 0 || store.previews != 0 {
			t.Fatalf("startup recovery: %+v", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup did not recover durable operation")
	}
}

func TestCommercialActionsRelayFailurePreservesAppliedLedger(t *testing.T) {
	h, _ := commercialActionsTestHandler(commercialOpsWriteScope)
	v := &types.CommercialAction{Operation: types.CommercialActionOperation{UID: 7, PackageApplied: true, WorkerStatus: "none"}, Preview: types.CommercialActionPreview{PackageEffect: "change_plan"}}
	h.runCommercialAction(context.Background(), v)
	if v.Operation.Status != "failed" || !v.Operation.PackageApplied || v.Operation.RelaySynced || v.Operation.Error == "" {
		t.Fatalf("ledger confused with Relay failure: %+v", v.Operation)
	}
}

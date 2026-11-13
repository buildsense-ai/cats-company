package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type commercialKeptRetentionCredits struct {
	sharedTermStub
	action      commercialWorkerActionTestCredits
	reconciled  bool
	lookupError error
}

func (s *commercialKeptRetentionCredits) AcquireCommercialWorkerOwnerGate(int64) (func(), bool, error) {
	return func() {}, true, nil
}
func (s *commercialKeptRetentionCredits) ReserveCommercialOperationCredit(uid int64, source, reservation string, p types.CommercialActionPreview) (types.CloudWorkerCreditSelection, bool, error) {
	return s.action.ReserveCommercialOperationCredit(uid, source, reservation, p)
}
func (s *commercialKeptRetentionCredits) ReconcileCommercialOperationWorker(int64, string) (string, int64, error) {
	if s.reconciled {
		return "kept", 1001, nil
	}
	return "pending", 0, nil
}
func (s *commercialKeptRetentionCredits) CompleteCommercialOperationWorker(uid int64, source string, worker int64) error {
	return s.action.CompleteCommercialOperationWorker(uid, source, worker)
}
func (s *commercialKeptRetentionCredits) KeepCommercialOperationWorker(int64, string, int64) error {
	s.action.kept++
	s.reconciled = true
	s.row.RetainData = true
	s.row.DeleteAfter = time.Time{}
	s.row.SyncPending = true
	return nil
}
func (s *commercialKeptRetentionCredits) GetCloudWorkerBillingLifecycle(string) (*types.CloudWorkerLifecycle, error) {
	if s.lookupError != nil {
		return nil, s.lookupError
	}
	value := s.row
	return &value, nil
}

func commercialKeptRetentionFixture(t *testing.T) (*CloudWorkerHandler, *commercialKeptRetentionCredits, *types.CommercialAction) {
	t.Helper()
	h, db := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{CreateQuota: "42=99"})
	db.ownerBots = []map[string]interface{}{{"id": int64(1001), "tenant_name": "bot-kept"}}
	h.db = &deploymentTestStore{cloudWorkerTestStore: db, deployments: map[string]types.CloudWorkerDeployment{
		"bot-kept": {Profile: types.CloudWorkerPackedShared, Env: map[string]string{}},
	}}
	credits := &commercialKeptRetentionCredits{sharedTermStub: sharedTermStub{row: types.CloudWorkerLifecycle{
		ID: 71, OwnerUID: 42, WorkerUID: 1001, TenantName: "bot-kept", State: "active", ExpiryMode: "permanent", RetainData: true,
	}}}
	h.credits = credits
	v := &types.CommercialAction{
		Operation: types.CommercialActionOperation{UID: 42, OperationID: "kept-retention"},
		Request:   types.CommercialActionRequest{WorkerAction: "ensure"},
		Preview:   types.CommercialActionPreview{DeploymentProfile: types.CloudWorkerPackedShared, BillingMode: "month", WorkerExpiryMode: "permanent", TargetPlan: &types.CommercialPlan{ModelCreditUnits: 4000000000}},
	}
	return h, credits, v
}

func TestCommercialCreditKeptWaitsForRetentionSyncAndRetriesSameWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX provider script; run Linux test binary")
	}
	h, credits, v := commercialKeptRetentionFixture(t)
	h.renewScript = filepath.Join(t.TempDir(), "renew.sh")
	writeSharedTermScript(t, h.renewScript, `{"status":"failed"}`, 1)
	status, uid, err := h.executeCommercialActionWorker(context.Background(), v)
	if err == nil || status != "failed" || uid != 1001 || credits.action.kept != 1 || credits.claims != 1 || !credits.row.SyncPending {
		t.Fatalf("unconfirmed adoption returned success: status=%s uid=%d err=%v credits=%+v", status, uid, err, credits)
	}
	// The failed provider operation keeps its durable marker. Replay must sync
	// that worker; it must not adopt, reserve or create another machine.
	script := "#!/bin/sh\ncase \" $* \" in *\" --retain \"*) ;; *) exit 7;; esac\nprintf '%s\\n' '{\"status\":\"renewed\",\"permanent\":true}'\n"
	if err := os.WriteFile(h.renewScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	status, uid, err = h.executeCommercialActionWorker(context.Background(), v)
	if err != nil || status != "kept" || uid != 1001 || credits.action.kept != 1 || credits.claims != 2 || credits.row.SyncPending || credits.action.reserved != 0 || credits.action.completed != 0 {
		t.Fatalf("retry did not confirm the same worker: status=%s uid=%d err=%v credits=%+v", status, uid, err, credits)
	}
	status, uid, err = h.executeCommercialActionWorker(context.Background(), v)
	if err != nil || status != "kept" || uid != 1001 || credits.claims != 2 {
		t.Fatal("confirmed replay reran provider sync")
	}
}

func TestCommercialCreditReconciledKeptFailsClosed(t *testing.T) {
	for _, condition := range []string{"missing-marker", "deadline", "wrong-owner", "wrong-worker", "wrong-tenant", "deleted", "billing-busy", "pending", "lookup-error"} {
		t.Run(condition, func(t *testing.T) {
			h, credits, v := commercialKeptRetentionFixture(t)
			credits.reconciled = true
			switch condition {
			case "missing-marker":
				credits.row.RetainData = false
			case "deadline":
				credits.row.DeleteAfter = time.Now().Add(time.Hour)
			case "wrong-owner":
				credits.row.OwnerUID = 99
			case "wrong-worker":
				credits.row.WorkerUID = 99
			case "wrong-tenant":
				credits.row.TenantName = "another"
			case "deleted":
				credits.row.State = "deleted"
			case "billing-busy":
				credits.row.BillingAction = "sync"
			case "pending":
				credits.row.SyncPending = true
			case "lookup-error":
				credits.lookupError = errors.New("read unavailable")
			}
			w := httptest.NewRecorder()
			if !h.prepareCommercialActionWorker(w, 42, v) || w.Code != 409 || !strings.Contains(w.Body.String(), "cloud_worker_retention_sync_required") || strings.Contains(w.Body.String(), `"worker_status":"kept"`) || credits.action.kept != 0 || credits.action.reserved != 0 {
				t.Fatalf("unconfirmed replay succeeded: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestCommercialCreditKeptRejectsRetentionChangedDuringSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX provider script; run Linux test binary")
	}
	h, credits, v := commercialKeptRetentionFixture(t)
	credits.reconciled = true
	credits.row.SyncPending = true
	credits.completeHook = func() { credits.row.RetainData = false }
	h.renewScript = filepath.Join(t.TempDir(), "renew.sh")
	writeSharedTermScript(t, h.renewScript, `{"status":"renewed","permanent":true}`, 0)
	status, _, err := h.executeCommercialActionWorker(context.Background(), v)
	if status != "failed" || err == nil {
		t.Fatal("changed retention accepted after provider success")
	}
}

func TestCommercialLegacyKeptDoesNotRequireCreditRetention(t *testing.T) {
	h, credits, v := commercialKeptRetentionFixture(t)
	credits.reconciled = true
	credits.row.RetainData = false
	credits.row.DeleteAfter = time.Now().Add(time.Hour)
	v.Preview.TargetPlan.ModelCreditUnits = 0
	status, uid, err := h.executeCommercialActionWorker(context.Background(), v)
	if status != "kept" || uid != 1001 || err != nil || credits.claims != 0 {
		t.Fatalf("legacy adoption changed: %s %d %v", status, uid, err)
	}
}

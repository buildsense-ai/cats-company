package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type machineUpgradeReviewTestDB struct {
	*machineUpgradeTestDB
	reviewCalls int
}

func (d *machineUpgradeReviewTestDB) RetryMachineUpgradeReview(id string) (*types.MachineUpgradeOperation, bool, error) {
	d.reviewCalls++
	return d.operations[id], false, nil
}

func TestMachineUpgradeAdminReviewRequiresAdminAndOriginalLifecycle(t *testing.T) {
	t.Setenv("OC_ADMIN_USERNAMES", "upgrade-operator")
	h, base := machineUpgradeTestHandler(t, "unused-adapter")
	db := &machineUpgradeReviewTestDB{machineUpgradeTestDB: base}
	h.db = db
	db.operations["mu-review"] = &types.MachineUpgradeOperation{
		OperationID: "mu-review", UID: 77, TenantName: "bot-worker", LifecycleID: 8,
		Status: types.MachineUpgradeStatusReconciling, Held: true, Attempts: 5, CreatedAt: time.Now(),
	}
	body := `{"uid":77,"operation_id":"mu-review","reason":"adapter connectivity investigated and restored"}`
	for _, tc := range []struct {
		uid      int64
		username string
		want     int
	}{
		{0, "", http.StatusUnauthorized},
		{88, "ordinary-user", http.StatusForbidden},
		{88, "upgrade-operator", http.StatusConflict},
	} {
		req := machineUpgradeRequest(http.MethodPost, "/api/admin/cloud-workers/upgrade/retry", body, tc.uid)
		req = req.WithContext(context.WithValue(req.Context(), usernameKey, tc.username))
		rec := httptest.NewRecorder()
		h.HandleAdminMachineUpgradeRetry(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("user=%s got=%d body=%s want=%d", tc.username, rec.Code, rec.Body.String(), tc.want)
		}
	}
	if db.reviewCalls != 0 {
		t.Fatal("unauthorized or reused lifecycle reached review claim")
	}
	db.operations["mu-review"].LifecycleID = 9
	req := machineUpgradeRequest(http.MethodPost, "/api/admin/cloud-workers/upgrade/retry", body, 88)
	req = req.WithContext(context.WithValue(req.Context(), usernameKey, "upgrade-operator"))
	rec := httptest.NewRecorder()
	h.HandleAdminMachineUpgradeRetry(rec, req)
	if rec.Code != http.StatusOK || db.reviewCalls != 1 {
		t.Fatalf("operator review not reachable: status=%d calls=%d body=%s", rec.Code, db.reviewCalls, rec.Body.String())
	}
}

func TestMachineUpgradeReconcileNeverReplaysAgainstReusedLifecycle(t *testing.T) {
	h, db := machineUpgradeTestHandler(t, "must-not-execute")
	op := &types.MachineUpgradeOperation{
		OperationID: "mu-old-lifecycle", UID: 77, TenantName: "bot-worker", LifecycleID: 8,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanPro,
		Status: types.MachineUpgradeStatusRunning, Held: true,
	}
	db.operations[op.OperationID] = op
	h.runMachineUpgradeScript(appliedMachineUpgrade{store: db, op: op}, true)
	got := db.operations[op.OperationID]
	if got.Status != types.MachineUpgradeStatusReconciling || !got.Held || got.Settled || got.Message != "机器生命周期或档位已变化，预占保留，待人工核实" {
		t.Fatalf("old lifecycle must be held for review without invoking adapter: %#v", got)
	}
}

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// machineUpgradeReviewCall 记录一次管理端复核认领带来了谁、什么理由。
type machineUpgradeReviewCall struct {
	operationID string
	actorUID    int64
	reason      string
}

// machineUpgradeReviewTestDB 只实现「带审计」的复核认领。管理端路径如果还去调
// 不带审计的兼容方法，这个假持久层就满足不了接口，测试会直接 503——这正是要
// 守住的行为：HTTP 入口不允许在没有授权记录的情况下认领。
type machineUpgradeReviewTestDB struct {
	*machineUpgradeTestDB
	calls []machineUpgradeReviewCall
}

func (d *machineUpgradeReviewTestDB) RetryMachineUpgradeReviewAudited(operationID string, actorUID int64, reason string) (*types.MachineUpgradeOperation, bool, error) {
	d.calls = append(d.calls, machineUpgradeReviewCall{operationID: operationID, actorUID: actorUID, reason: reason})
	return d.operations[operationID], false, nil
}

// machineUpgradeReviewableOperation 是一笔「等人复核」的升配：running/reconciling、
// 预占还在、尝试次数已经用满自动上限，绑在测试 fixture 里那条 active lifecycle 上。
func machineUpgradeReviewableOperation(operationID string) *types.MachineUpgradeOperation {
	now := time.Now()
	return &types.MachineUpgradeOperation{
		OperationID: operationID, UID: 77, TenantName: "bot-worker", LifecycleID: 9,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanPro, CostUnits: 100,
		Status: types.MachineUpgradeStatusReconciling, Held: true, Attempts: 5,
		Message: "adapter timeout", CreatedAt: now, UpdatedAt: now.Add(-10 * time.Minute),
	}
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
	if len(db.calls) != 0 {
		t.Fatalf("unauthorized or reused lifecycle reached review claim: %#v", db.calls)
	}
	db.operations["mu-review"].LifecycleID = 9
	req := machineUpgradeRequest(http.MethodPost, "/api/admin/cloud-workers/upgrade/retry", body, 88)
	req = req.WithContext(context.WithValue(req.Context(), usernameKey, "upgrade-operator"))
	rec := httptest.NewRecorder()
	h.HandleAdminMachineUpgradeRetry(rec, req)
	if rec.Code != http.StatusOK || len(db.calls) != 1 {
		t.Fatalf("operator review not reachable: status=%d calls=%d body=%s", rec.Code, len(db.calls), rec.Body.String())
	}
	// 授权人必须来自登录态，而不是请求体里那个 uid。
	if got := db.calls[0]; got.actorUID != 88 || got.operationID != "mu-review" ||
		got.reason != "adapter connectivity investigated and restored" {
		t.Fatalf("audited claim got actor=%d op=%q reason=%q", got.actorUID, got.operationID, got.reason)
	}
}

// machineUpgradeLegacyReviewTestDB 是「没有审计能力」的适配器：只有旧的兼容方法。
type machineUpgradeLegacyReviewTestDB struct {
	*machineUpgradeTestDB
	legacyCalls int
}

func (d *machineUpgradeLegacyReviewTestDB) RetryMachineUpgradeReview(operationID string) (*types.MachineUpgradeOperation, bool, error) {
	d.legacyCalls++
	return d.operations[operationID], true, nil
}

func TestMachineUpgradeAdminReviewFailsClosedWithoutAuditedClaim(t *testing.T) {
	t.Setenv("OC_ADMIN_USERNAMES", "upgrade-operator")
	h, base := machineUpgradeTestHandler(t, "must-not-execute")
	legacy := &machineUpgradeLegacyReviewTestDB{machineUpgradeTestDB: base}
	h.db = legacy
	base.operations["mu-no-audit"] = machineUpgradeReviewableOperation("mu-no-audit")
	body := `{"uid":77,"operation_id":"mu-no-audit","reason":"adapter connectivity investigated and restored"}`
	req := machineUpgradeRequest(http.MethodPost, "/api/admin/cloud-workers/upgrade/retry", body, 88)
	req = req.WithContext(context.WithValue(req.Context(), usernameKey, "upgrade-operator"))
	rec := httptest.NewRecorder()
	h.HandleAdminMachineUpgradeRetry(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("adapter without audit support must fail closed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if legacy.legacyCalls != 0 {
		t.Fatal("handler fell back to the audit-free claim")
	}
}

func TestMachineUpgradeAdminReviewBoundsActorAndReason(t *testing.T) {
	t.Setenv("OC_ADMIN_USERNAMES", "upgrade-operator")
	padded := "   " + strings.Repeat("理", 8) + "   "
	for _, tc := range []struct {
		name      string
		reason    string
		want      int
		wantCalls int
	}{
		{"eight Chinese characters are enough", "排查适配器并恢复连接", http.StatusOK, 1},
		{"seven characters are too short", "abcdefg", http.StatusBadRequest, 0},
		{"seven Chinese characters are too short", "排查适配器并恢", http.StatusBadRequest, 0},
		{"blank padding is not a reason", "            ", http.StatusBadRequest, 0},
		{"one thousand characters is the limit", strings.Repeat("理", 1000), http.StatusOK, 1},
		{"one thousand and one characters is too long", strings.Repeat("理", 1001), http.StatusBadRequest, 0},
		{"reason is trimmed before it is measured", padded, http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, base := machineUpgradeTestHandler(t, "unused-adapter")
			db := &machineUpgradeReviewTestDB{machineUpgradeTestDB: base}
			h.db = db
			base.operations["mu-bounds"] = machineUpgradeReviewableOperation("mu-bounds")
			body, err := json.Marshal(map[string]any{"uid": 77, "operation_id": "mu-bounds", "reason": tc.reason})
			if err != nil {
				t.Fatal(err)
			}
			req := machineUpgradeRequest(http.MethodPost, "/api/admin/cloud-workers/upgrade/retry", string(body), 88)
			req = req.WithContext(context.WithValue(req.Context(), usernameKey, "upgrade-operator"))
			rec := httptest.NewRecorder()
			h.HandleAdminMachineUpgradeRetry(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", rec.Code, rec.Body.String(), tc.want)
			}
			if len(db.calls) != tc.wantCalls {
				t.Fatalf("claim calls=%d want=%d", len(db.calls), tc.wantCalls)
			}
			if tc.wantCalls != 1 {
				return
			}
			call := db.calls[0]
			// actor 来自登录态（88），请求体里的 uid（77）只是被操作的账户。
			if call.actorUID != 88 || call.operationID != "mu-bounds" || call.reason != strings.TrimSpace(tc.reason) {
				t.Fatalf("audited claim got actor=%d op=%q reason=%q", call.actorUID, call.operationID, call.reason)
			}
		})
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

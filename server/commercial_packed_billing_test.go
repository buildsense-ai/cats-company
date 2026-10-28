package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// 共享机跑在集群已有的容器里：没有云订阅可买，也没有"试用转包月"这回事。
// 计费口径只有套餐期限，所以 month/ondemand 这一维必须在写入时就收敛掉。
//
// 这一改不是为了好看：ondemand 的共享机生命周期会被到期清扫送进 ECS 的
// billing-worker.sh（转换/节省关机），而共享机部署路径上没有这个脚本，
// 清扫每次都失败并跳过正常的到期归档 —— 共享机会永远不过期。
func TestPackedSharedPlanCollapsesMachineBilling(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)

	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/plans", strings.NewReader(`{
		"slug":"shared-trial","name":"共享试用","duration_days":7,"price_fen":0,
		"cloud_worker_deployment_profile":"packed_shared",
		"cloud_worker_billing_mode":"ondemand"
	}`))
	req.RemoteAddr = "127.0.0.1:32000"
	rec := httptest.NewRecorder()
	handler.HandleCommercialPlans(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
	if len(store.plans) != 1 {
		t.Fatalf("plans=%d", len(store.plans))
	}
	plan := store.plans[0]
	if plan.CloudWorkerDeploymentProfile != types.CloudWorkerPackedShared {
		t.Fatalf("profile=%q", plan.CloudWorkerDeploymentProfile)
	}
	if plan.CloudWorkerBillingMode != types.CloudWorkerMonthly {
		t.Fatalf("shared plan kept billing=%q, want month: there is no subscription to convert",
			plan.CloudWorkerBillingMode)
	}
}

func TestPackedSharedInviteCollapsesMachineBilling(t *testing.T) {
	store := newCommercialTestStore()
	store.plans = append(store.plans, &types.CommercialPlan{
		ID: 1, Slug: "shared-7d", Name: "共享 7 天",
		CloudWorkerDeploymentProfile: types.CloudWorkerPackedShared,
		CloudWorkerBillingMode:       types.CloudWorkerMonthly,
	})
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)

	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(
		`{"plan_id":1,"max_redemptions":1,"cloud_worker_credits":1,`+
			`"cloud_worker_profile":"packed_shared","cloud_worker_billing_mode":"ondemand"}`))
	req.RemoteAddr = "127.0.0.1:32000"
	rec := httptest.NewRecorder()
	handler.HandleCommercialInvites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
	if len(store.invites) != 1 {
		t.Fatalf("invites=%d", len(store.invites))
	}
	invite := store.invites[0]
	if invite.CloudWorkerBillingMode != types.CloudWorkerMonthly {
		t.Fatalf("shared invite kept billing=%q, want month", invite.CloudWorkerBillingMode)
	}
	if invite.CloudWorkerCredits != 1 {
		t.Fatalf("credits=%d want 1 (共享机仍是一份权益一台上限)", invite.CloudWorkerCredits)
	}

	var body struct {
		Invites []struct {
			CloudWorkerBillingMode string `json:"cloud_worker_billing_mode"`
		} `json:"invites"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Invites) != 1 || body.Invites[0].CloudWorkerBillingMode != types.CloudWorkerMonthly {
		t.Fatalf("response kept billing: %+v", body.Invites)
	}
}

// 独享实例仍然保留按量试用：这个分支是给 ECS 用的，不能被收敛顺手删掉。
func TestDedicatedInviteKeepsOnDemandBilling(t *testing.T) {
	store := newCommercialTestStore()
	store.plans = append(store.plans, &types.CommercialPlan{
		ID: 1, Slug: "ecs-trial", Name: "ECS 试用",
		CloudWorkerDeploymentProfile: types.CloudWorkerPublicIP,
		CloudWorkerBillingMode:       types.CloudWorkerOnDemand,
	})
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)

	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(
		`{"plan_id":1,"max_redemptions":1,"cloud_worker_credits":1,`+
			`"cloud_worker_profile":"public_ip","cloud_worker_billing_mode":"ondemand"}`))
	req.RemoteAddr = "127.0.0.1:32000"
	rec := httptest.NewRecorder()
	handler.HandleCommercialInvites(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
	}
	if got := store.invites[0].CloudWorkerBillingMode; got != types.CloudWorkerOnDemand {
		t.Fatalf("dedicated invite billing=%q, want ondemand", got)
	}
}

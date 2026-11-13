package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 真实 PostgreSQL + 真实 HTTP handler 的端到端契约：Relay 拿到的 plan_tier 必须是
// root 任务自己的快照（docs/credit-plan-rules-20261109.md「按到期前费率跑到结束」）。
// 需要 CATS_PG_TEST_DSN；没有就跳过（CI 的 backend_db job 会带上）。
func TestModelCreditTaskHTTPPlanTierSnapshotAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN to run the PostgreSQL credit task HTTP contract")
	}
	ctx := context.Background()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("CATS_PG_TEST_DSN must be a postgres URL DSN: %v", err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test PostgreSQL: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("cats_task_rate_http_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	db := &postgres.Adapter{}
	if err := db.Open(parsed.String()); err != nil {
		t.Fatalf("open schema postgres connection: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create schema objects: %v", err)
	}

	username := fmt.Sprintf("task-rate-%d", time.Now().UnixNano())
	uid, err := db.CreateUser(&types.User{
		Username: username, Email: username + "@example.test", DisplayName: username,
		AccountType: types.AccountHuman, PassHash: []byte(username + "-hash"),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             fmt.Sprintf("task-rate-tier2-%d", time.Now().UnixNano()),
		Name:             "Task rate tier 2",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         2,
		ModelCreditUnits: 4_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	// 走真实的邀请兑换入口拿 credit 权限 + 20 credit 赠送额度（不手写 entitlement SQL）。
	code := fmt.Sprintf("TASK-RATE-%d", time.Now().UnixNano())
	if _, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
		Code: code, PlanID: planID, MaxRedemptions: 1, ModelCreditUnitsInherit: true,
	}); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if _, err := db.RedeemCommercialInvite(uid, code); err != nil {
		t.Fatalf("redeem invite: %v", err)
	}

	const secret = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	t.Setenv("CATSCO_RELAY_CREDIT_PEER_SECRET", secret)
	handler := NewModelCreditTaskHandler(db)
	uidText := strconv.FormatInt(uid, 10)

	call := func(action, taskID string) map[string]interface{} {
		t.Helper()
		body := `{"uid":` + uidText + `,"action":"` + action + `","task_id":"` + taskID + `"}`
		req := httptest.NewRequest(http.MethodPost, "/internal/relay/credit-tasks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", action, taskID, recorder.Code, recorder.Body.String())
		}
		payload := map[string]interface{}{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s response: %v", action, err)
		}
		return payload
	}
	planTierOf := func(payload map[string]interface{}) int {
		t.Helper()
		tier, ok := payload["plan_tier"].(float64)
		if !ok {
			t.Fatalf("payload has no plan_tier: %+v", payload)
		}
		return int(tier)
	}

	started := call("start", "http-tier-root")
	if started["enabled"] != true || started["allowed"] != true || started["state"] != store.ModelCreditTaskRunning {
		t.Fatalf("start payload %+v", started)
	}
	if tier := planTierOf(started); tier != 2 {
		t.Fatalf("start plan_tier = %d, want the server-side tier 2", tier)
	}
	for _, action := range []string{"status", "start"} {
		payload := call(action, "http-tier-root")
		if tier := planTierOf(payload); tier != 2 {
			t.Fatalf("%s plan_tier = %d, want the same snapshot 2", action, tier)
		}
	}
	finished := call("finish", "http-tier-root")
	if finished["state"] != store.ModelCreditTaskCompleted || finished["allowed"] != false {
		t.Fatalf("finish payload %+v", finished)
	}
	if tier := planTierOf(finished); tier != 2 {
		t.Fatalf("finish plan_tier = %d, want the same snapshot 2", tier)
	}

	// 客户端自报档位：请求体里带 plan_tier 是未知字段，直接 400（DisallowUnknownFields）。
	req := httptest.NewRequest(http.MethodPost, "/internal/relay/credit-tasks",
		strings.NewReader(`{"uid":`+uidText+`,"action":"start","task_id":"http-tier-inject","plan_tier":3}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("client-supplied plan_tier status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
}

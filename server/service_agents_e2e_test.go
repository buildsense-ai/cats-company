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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openchat/openchat/server/db/postgres"
)

// TestServiceAgentsEndToEnd 走完整链路（计划① Task 9）：
// MCP tools/call（余额预检）→ 假实例 HTTP（claim/events/complete）→ 同步结果 /
// 异步句柄 + run_status / 租约过期换实例续跑（checkpoint 下发）/ 额度耗尽拒单。
func TestServiceAgentsEndToEnd(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run the service-agents end-to-end test")
	}
	ctx := context.Background()

	// ---- PG 独立 schema（照 db/postgres 集成测试模式，避免互相污染）----
	parsed, err := url.Parse(rawDSN)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("CATS_PG_TEST_DSN must be a postgres URL DSN: %v", err)
	}
	schemaName := fmt.Sprintf("cats_e2e_service_agents_%d", time.Now().UnixNano())
	admin, err := sql.Open("pgx", rawDSN)
	if err != nil {
		t.Fatalf("open admin pg: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.Exec(`CREATE SCHEMA "` + schemaName + `"`); err != nil {
		t.Fatalf("create e2e schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA "` + schemaName + `" CASCADE`) })
	q := parsed.Query()
	q.Set("search_path", schemaName)
	parsed.RawQuery = q.Encode()
	scopedDSN := parsed.String()
	scoped, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatalf("open scoped pg: %v", err)
	}
	t.Cleanup(func() { _ = scoped.Close() })
	// 这个 e2e 每次都在全新 schema 上跑，清单必须覆盖它要读的每一列。
	// 漏一个迁移不是「少一列」，而是 ListServiceAgents 整条查询失败——
	// owner_kind（000045）和 self_intro_*（000062）都踩过这个坑。
	for _, name := range []string{
		"000027_service_agents.up.sql",
		"000033_service_run_receipts.up.sql",
		"000034_service_agent_subscriptions.up.sql",
		"000045_service_agents_ownership.up.sql",
		"000062_service_agents_self_intro.up.sql",
	} {
		statement, err := os.ReadFile("db/migrations/postgres/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := scoped.Exec(string(statement)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	adapter := &postgres.Adapter{}
	if err := adapter.Open(scopedDSN); err != nil {
		t.Fatalf("open store adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })

	// ---- relay stub：预检放行（可切换为超额）----
	var relayBlocked atomic.Bool
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/441/key/limits" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer relay-admin-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		usage := "1.5"
		if relayBlocked.Load() {
			usage = "100"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"uid":441,"configured":true,"limits":{"monthly_budget":{"max_limit":100,"current_usage":%s},"model_limits":[]}}`, usage)
	}))
	t.Cleanup(relay.Close)
	t.Setenv("CATS_RELAY_ADMIN_URL", relay.URL)
	t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-token")

	// ---- 组装 MCP 端点 + 编排内部 API（同一 HTTP 服务）----
	const callerUID = int64(441)
	mcpHandler := NewServiceAgentsMCPHandler(adapter)
	mcpHandler.SetBalanceChecker(NewServiceBalanceCheckerFromEnv())
	mcpHandler.syncWait = 5 * time.Second // Run1：同步路径给假实例留出完成窗口
	orchestrator := NewServiceRunOrchestrator(adapter, "inst-token-1", "test-secret")
	mux := http.NewServeMux()
	mux.Handle("/api/mcp", mcpAuthStub(callerUID, mcpHandler))
	orchestrator.RegisterRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL+"/api/mcp")

	// ---- 假实例辅助 ----
	claimRuns := func(instanceID string) []ServiceClaimRun {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			status, body := doInternal(t, "POST", ts.URL+"/internal/service-runs/claim", "inst-token-1",
				fmt.Sprintf(`{"limit":5,"instance_id":%q}`, instanceID))
			if status != http.StatusOK {
				t.Fatalf("claim status=%d body=%s", status, body)
			}
			var out ServiceClaimResult
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("claim decode: %v body=%s", err, body)
			}
			if len(out.Runs) > 0 {
				return out.Runs
			}
			if time.Now().After(deadline) {
				t.Fatalf("no claimable run within deadline (instance=%s)", instanceID)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	buildResult := func(run ServiceClaimRun, answer string) string {
		return fmt.Sprintf(`{"contract_version":"catsco.service-run-result.v1","run_id":%q,"session_id":%q,"status":"completed","output":{"answer":%q}}`,
			run.RunID, run.SessionID, answer)
	}
	emitResultEvent := func(run ServiceClaimRun, answer string) {
		t.Helper()
		status, body := doInternal(t, "POST", ts.URL+"/internal/service-runs/"+run.RunID+"/events", "inst-token-1",
			fmt.Sprintf(`{"type":"result","payload":%s}`, buildResult(run, answer)))
		if status != http.StatusOK {
			t.Fatalf("result event status=%d body=%s", status, body)
		}
	}
	completeRun := func(run ServiceClaimRun, resultID, answer, usage string) {
		t.Helper()
		status, body := doInternal(t, "POST", ts.URL+"/internal/service-runs/"+run.RunID+"/complete", "inst-token-1",
			fmt.Sprintf(`{"result_id":%q,"result":%s,"usage":%s}`, resultID, buildResult(run, answer), usage))
		if status != http.StatusOK || !strings.Contains(string(body), `"completed":true`) {
			t.Fatalf("complete status=%d body=%s", status, body)
		}
	}

	// ---- Run 1：同步路径（MCP 调用阻塞等待，假实例并发干活）----
	type callOutcome struct {
		call *mcp.CallToolResult
		err  error
	}
	outcomes := make(chan callOutcome, 1)
	go func() {
		call, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "catsco_hello",
			Arguments: map[string]any{"input": map[string]any{"q": "sync"}, "client_token": "ct_e2e_sync"},
		})
		outcomes <- callOutcome{call, err}
	}()
	firstRun := claimRuns("inst-a")[0]
	if status, body := doInternal(t, "POST", ts.URL+"/internal/service-runs/"+firstRun.RunID+"/events", "inst-token-1",
		`{"type":"progress","payload":{"pct":50}}`); status != http.StatusOK {
		t.Fatalf("progress event status=%d body=%s", status, body)
	}
	emitResultEvent(firstRun, "hello-sync")
	completeRun(firstRun, "res_e2e_1", "hello-sync", `{"prompt_tokens":3,"completion_tokens":5}`)
	var outcome callOutcome
	select {
	case outcome = <-outcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("sync MCP call did not return")
	}
	if outcome.err != nil || outcome.call.IsError {
		t.Fatalf("sync call: err=%v result=%+v", outcome.err, outcome.call)
	}
	syncEnv := decodeServiceEnvelope(t, outcome.call.StructuredContent)
	if syncEnv.Status != ServiceRunCompleted || syncEnv.RunID != firstRun.RunID || !strings.Contains(string(syncEnv.Output), "hello-sync") {
		t.Fatalf("sync envelope = %+v", syncEnv)
	}
	// 账扣对：claim 现场签发的 service token 归属调用者并绑定 run/agent/models
	tokenClaims, err := VerifyServiceToken("test-secret", firstRun.ServiceToken)
	if err != nil {
		t.Fatalf("verify service token: %v", err)
	}
	if tokenClaims.CallerUID != callerUID || tokenClaims.Agent != "hello" || tokenClaims.RunID != firstRun.RunID || len(tokenClaims.Models) == 0 {
		t.Fatalf("service token claims = %+v", tokenClaims)
	}
	if firstRun.DefinitionRev != 1 || firstRun.ThresholdSeconds != 60 || string(firstRun.Input) != `{"q":"sync"}` {
		t.Fatalf("claim entry = %+v", firstRun)
	}

	// ---- Run 2：同会话续聊 → 超阈值异步句柄 → run_status 读到终态 ----
	mcpHandler.syncWait = 50 * time.Millisecond
	call2, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "catsco_hello",
		Arguments: map[string]any{
			"input": map[string]any{"q": "async"}, "client_token": "ct_e2e_async", "session_id": firstRun.SessionID,
		},
	})
	if err != nil || call2.IsError {
		t.Fatalf("async call: err=%v result=%+v", err, call2)
	}
	handle := decodeServiceEnvelope(t, call2.StructuredContent)
	if handle.ContractVersion != "catsco.service-run-async.v1" || handle.Status != ServiceRunRunning || handle.SessionID != firstRun.SessionID {
		t.Fatalf("async handle = %+v", handle)
	}
	secondRun := claimRuns("inst-a")[0]
	if secondRun.SessionID != firstRun.SessionID || secondRun.RunID != handle.RunID {
		t.Fatalf("resume claim mismatch: %+v handle=%+v", secondRun, handle)
	}
	emitResultEvent(secondRun, "hello-async")
	completeRun(secondRun, "res_e2e_2", "hello-async", `{"prompt_tokens":4,"completion_tokens":6}`)
	status2, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_run_status", Arguments: map[string]any{"run_id": handle.RunID}})
	if err != nil || status2.IsError {
		t.Fatalf("run status: err=%v result=%+v", err, status2)
	}
	finalEnv2 := decodeServiceEnvelope(t, status2.StructuredContent)
	if finalEnv2.Status != ServiceRunCompleted || !strings.Contains(string(finalEnv2.Output), "hello-async") {
		t.Fatalf("final envelope (async) = %+v", finalEnv2)
	}

	// ---- Run 3：续跑（checkpoint → 租约过期 → reaper → 换实例接管）----
	call3, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "catsco_hello",
		Arguments: map[string]any{
			"input": map[string]any{"q": "resume"}, "client_token": "ct_e2e_resume", "session_id": firstRun.SessionID,
		},
	})
	if err != nil || call3.IsError {
		t.Fatalf("resume call: err=%v result=%+v", err, call3)
	}
	handle3 := decodeServiceEnvelope(t, call3.StructuredContent)
	if handle3.Status != ServiceRunRunning {
		t.Fatalf("resume handle = %+v", handle3)
	}
	thirdRun := claimRuns("inst-a")[0]
	if thirdRun.RunID != handle3.RunID {
		t.Fatalf("third claim mismatch: %+v vs %+v", thirdRun, handle3)
	}
	if status, body := doInternal(t, "POST", ts.URL+"/internal/service-runs/"+thirdRun.RunID+"/checkpoint", "inst-token-1",
		`{"revision":7,"snapshot":{"cursor":"e2e-cursor"}}`); status != http.StatusOK {
		t.Fatalf("checkpoint status=%d body=%s", status, body)
	}
	if _, err := scoped.ExecContext(ctx, `UPDATE service_runs SET lease_expires_at = now() - interval '1 minute' WHERE run_id = $1`, thirdRun.RunID); err != nil {
		t.Fatalf("force lease expired: %v", err)
	}
	reaped, err := orchestrator.ReapOnce(ctx)
	if err != nil || reaped != 1 {
		t.Fatalf("reap = %d err=%v, want 1", reaped, err)
	}
	reclaimed := claimRuns("inst-b")[0]
	if reclaimed.RunID != thirdRun.RunID {
		t.Fatalf("reclaim mismatch: %+v vs %+v", reclaimed, thirdRun)
	}
	if reclaimed.Checkpoint == nil || reclaimed.Checkpoint.Revision != 7 || !strings.Contains(string(reclaimed.Checkpoint.Snapshot), "e2e-cursor") {
		t.Fatalf("reclaim checkpoint = %+v", reclaimed.Checkpoint)
	}
	emitResultEvent(reclaimed, "hello-resumed")
	completeRun(reclaimed, "res_e2e_3", "hello-resumed", `{"prompt_tokens":5,"completion_tokens":7}`)
	finalRun, err := adapter.GetServiceRun(ctx, thirdRun.RunID)
	if err != nil || finalRun.State != "completed" || finalRun.Attempts != 2 {
		t.Fatalf("resumed run = %+v err=%v", finalRun, err)
	}
	status3, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_run_status", Arguments: map[string]any{"run_id": thirdRun.RunID}})
	if err != nil || status3.IsError {
		t.Fatalf("run status (resumed): err=%v result=%+v", err, status3)
	}
	finalEnv3 := decodeServiceEnvelope(t, status3.StructuredContent)
	if finalEnv3.Status != ServiceRunCompleted || !strings.Contains(string(finalEnv3.Output), "hello-resumed") {
		t.Fatalf("final envelope (resumed) = %+v", finalEnv3)
	}

	// ---- Run 4：额度耗尽 → 预检拒单（且不创建新会话）----
	relayBlocked.Store(true)
	call4, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "catsco_hello",
		Arguments: map[string]any{"input": map[string]any{"q": "quota"}, "client_token": "ct_e2e_quota"},
	})
	if err != nil {
		t.Fatalf("quota call transport error: %v", err)
	}
	if !call4.IsError {
		t.Fatalf("quota call should be a tool error: %+v", call4.StructuredContent)
	}
	quotaText := ""
	for _, content := range call4.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			quotaText += text.Text
		}
	}
	if !strings.Contains(quotaText, "quota_exhausted") {
		t.Fatalf("quota text = %q", quotaText)
	}
	var sessionCount int
	if err := scoped.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_sessions`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("sessions = %d, want 1 (precheck must run before session creation)", sessionCount)
	}

	// ---- Run 5：回合制返问（awaiting_input）→ 同会话补充 → completed ----
	relayBlocked.Store(false)
	mcpHandler.syncWait = 5 * time.Second
	awaitOutcomes := make(chan callOutcome, 1)
	go func() {
		call, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "catsco_hello",
			Arguments: map[string]any{"input": map[string]any{"q": "vague"}, "client_token": "ct_e2e_await"},
		})
		awaitOutcomes <- callOutcome{call, err}
	}()
	fifthRun := claimRuns("inst-a")[0]
	// 实例上报返问收尾：result 事件（envelope awaiting_input）+ await-input（终态 + 回执）
	awaitEnvelope := fmt.Sprintf(`{"contract_version":"catsco.service-run-result.v1","run_id":%q,"session_id":%q,"status":"awaiting_input","output":{"question":"请补充周报日期范围"}}`,
		fifthRun.RunID, fifthRun.SessionID)
	if status, body := doInternal(t, "POST", ts.URL+"/internal/service-runs/"+fifthRun.RunID+"/events", "inst-token-1",
		fmt.Sprintf(`{"type":"result","payload":%s}`, awaitEnvelope)); status != http.StatusOK {
		t.Fatalf("await result event status=%d body=%s", status, body)
	}
	if status, body := doInternal(t, "POST", ts.URL+"/internal/service-runs/"+fifthRun.RunID+"/await-input", "inst-token-1",
		fmt.Sprintf(`{"result_id":"awt_e2e_1","result":%s}`, awaitEnvelope)); status != http.StatusOK {
		t.Fatalf("await-input status=%d body=%s", status, body)
	}
	var awaitOutcome callOutcome
	select {
	case awaitOutcome = <-awaitOutcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("awaiting MCP call did not return")
	}
	if awaitOutcome.err != nil || awaitOutcome.call.IsError {
		t.Fatalf("awaiting call: err=%v result=%+v", awaitOutcome.err, awaitOutcome.call)
	}
	awaitEnv := decodeServiceEnvelope(t, awaitOutcome.call.StructuredContent)
	if awaitEnv.Status != ServiceRunAwaitingInput || awaitEnv.RunID != fifthRun.RunID {
		t.Fatalf("awaiting envelope = %+v", awaitEnv)
	}
	awaitText := ""
	for _, content := range awaitOutcome.call.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			awaitText += text.Text
		}
	}
	if !strings.Contains(awaitText, "请补充周报日期范围") {
		t.Fatalf("awaiting tool text must carry the question: %q", awaitText)
	}
	// 回执已入箱（awaiting_input + question）
	pendingReceipts, err := adapter.ListUndeliveredServiceRunReceipts(ctx, 20)
	if err != nil {
		t.Fatalf("list receipts: %v", err)
	}
	var awaitReceiptFound bool
	for _, receipt := range pendingReceipts {
		if receipt.RunID == fifthRun.RunID {
			awaitReceiptFound = receipt.Status == "awaiting_input" && strings.Contains(string(receipt.Payload), "请补充周报日期范围")
		}
	}
	if !awaitReceiptFound {
		t.Fatalf("awaiting receipt missing: %+v", pendingReceipts)
	}

	// 补充输入 = 同会话下一次 tools/call（await 收尾不占会话串行位）→ completed
	contOutcomes := make(chan callOutcome, 1)
	go func() {
		call, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "catsco_hello",
			Arguments: map[string]any{
				"input": map[string]any{"q": "1 月 1 日至 1 月 7 日"}, "client_token": "ct_e2e_await_2", "session_id": awaitEnv.SessionID,
			},
		})
		contOutcomes <- callOutcome{call, err}
	}()
	contRun := claimRuns("inst-a")[0]
	if contRun.SessionID != awaitEnv.SessionID || contRun.RunID == fifthRun.RunID {
		t.Fatalf("continuation claim mismatch: %+v vs %+v", contRun, awaitEnv)
	}
	emitResultEvent(contRun, "hello-continued")
	completeRun(contRun, "res_e2e_5", "hello-continued", `{"prompt_tokens":6,"completion_tokens":8}`)
	var contOutcome callOutcome
	select {
	case contOutcome = <-contOutcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("continuation MCP call did not return")
	}
	if contOutcome.err != nil || contOutcome.call.IsError {
		t.Fatalf("continuation call: err=%v result=%+v", contOutcome.err, contOutcome.call)
	}
	contEnv := decodeServiceEnvelope(t, contOutcome.call.StructuredContent)
	if contEnv.Status != ServiceRunCompleted || contEnv.SessionID != awaitEnv.SessionID || !strings.Contains(string(contEnv.Output), "hello-continued") {
		t.Fatalf("continuation envelope = %+v", contEnv)
	}

	// ---- Run 6：目录订阅——屏蔽 hello → 调用拒；恢复 → 调用通（设计 §3.3-9）----
	if err := adapter.SetServiceAgentSubscriptionHidden(ctx, callerUID, 1, true); err != nil {
		t.Fatalf("hide hello: %v", err)
	}
	mcpHandler.NotifySubscriptionChanged(callerUID)
	blocked, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "catsco_hello",
		Arguments: map[string]any{"input": map[string]any{"q": "blocked"}, "client_token": "ct_e2e_hidden"},
	})
	if err == nil && (blocked == nil || !blocked.IsError) {
		t.Fatalf("hidden agent call must be rejected: err=%v result=%+v", err, blocked)
	}
	// run_status 工具始终可用（清单不裁剪查询面）
	statusCall, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_run_status", Arguments: map[string]any{"run_id": "run_missing"}})
	if err != nil {
		t.Fatalf("run_status transport after hide: %v", err)
	}
	if !statusCall.IsError || !strings.Contains(strings.ToLower(textOfCall(statusCall)), "run_not_found") {
		t.Fatalf("run_status after hide = %+v, want run_not_found tool error", statusCall)
	}
	// 恢复（添加）→ 正常调用（同步置 autoComplete 以免等太久：直接内联完成）
	if err := adapter.SetServiceAgentSubscriptionHidden(ctx, callerUID, 1, false); err != nil {
		t.Fatalf("restore hello: %v", err)
	}
	mcpHandler.NotifySubscriptionChanged(callerUID)
	mcpHandler.syncWait = 5 * time.Second
	restoreOutcomes := make(chan callOutcome, 1)
	go func() {
		call, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "catsco_hello",
			Arguments: map[string]any{"input": map[string]any{"q": "restored"}, "client_token": "ct_e2e_restored"},
		})
		restoreOutcomes <- callOutcome{call, err}
	}()
	restoredRun := claimRuns("inst-a")[0]
	emitResultEvent(restoredRun, "hello-restored")
	completeRun(restoredRun, "res_e2e_6", "hello-restored", `{"prompt_tokens":2,"completion_tokens":3}`)
	var restoreOutcome callOutcome
	select {
	case restoreOutcome = <-restoreOutcomes:
	case <-time.After(10 * time.Second):
		t.Fatal("restored MCP call did not return")
	}
	if restoreOutcome.err != nil || restoreOutcome.call.IsError {
		t.Fatalf("restored call: err=%v result=%+v", restoreOutcome.err, restoreOutcome.call)
	}
	restoreEnv := decodeServiceEnvelope(t, restoreOutcome.call.StructuredContent)
	if restoreEnv.Status != ServiceRunCompleted || !strings.Contains(string(restoreEnv.Output), "hello-restored") {
		t.Fatalf("restored envelope = %+v", restoreEnv)
	}
}

// textOfCall 汇总工具结果文本（e2e 断言用）。
func textOfCall(call *mcp.CallToolResult) string {
	text := ""
	for _, content := range call.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	return text
}

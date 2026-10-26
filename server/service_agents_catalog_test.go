package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openchat/openchat/server/store"
)

// ---- 应用目录订阅清单（设计 §3.3-9"模式二：默认开放 + 个人清单"）----

type fakeSubscriptionNotifier struct {
	calls []int64
}

func (f *fakeSubscriptionNotifier) NotifySubscriptionChanged(uid int64) {
	f.calls = append(f.calls, uid)
}

func testToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func toolErrorText(t *testing.T, call *mcp.CallToolResult) string {
	t.Helper()
	text := ""
	for _, content := range call.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	return text
}

// 屏蔽 → tools/list 隐去 + 调用拒绝；恢复 → 重新可见可调。清单跟账号（uid 隔离）。
func TestServiceAgentsMCPSubscriptionFilter(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	handler := NewServiceAgentsMCPHandler(st)
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)
	session := newMCPSession(t, ts.URL)
	ctx := context.Background()

	// 默认可见（无行=可见）
	if names := testToolNames(t, session); !containsName(names, "catsco_hello") {
		t.Fatalf("default tools = %v, want catsco_hello visible", names)
	}

	// 屏蔽（uid 441）——先不通知会话（模拟未同步/多副本路径）：
	// 调用应被 invoke 层拦截（agent_not_subscribed 工具错误）。
	if err := st.SetServiceAgentSubscriptionHidden(ctx, 441, 1, true); err != nil {
		t.Fatalf("hide: %v", err)
	}
	call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{"input": map[string]any{"q": "x"}}})
	if err != nil {
		t.Fatalf("stale-session call transport: %v", err)
	}
	if !call.IsError || !strings.Contains(toolErrorText(t, call), "agent_not_subscribed") {
		t.Fatalf("stale-session hidden call = %+v, want agent_not_subscribed tool error", call)
	}

	// 通知在线会话 → 增量同步：tools/list 隐去；再调用由协议层拒绝（unknown tool）。
	handler.NotifySubscriptionChanged(441)
	names := testToolNames(t, session)
	if containsName(names, "catsco_hello") {
		t.Fatalf("hidden agent must be removed from tool list: %v", names)
	}
	if !containsName(names, "catsco_run_status") {
		t.Fatalf("run_status must stay visible: %v", names)
	}
	call, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{"input": map[string]any{"q": "x"}}})
	if err == nil && (call == nil || !call.IsError) {
		t.Fatalf("synced-session hidden call must be rejected: err=%v result=%+v", err, call)
	}

	// 账号隔离：另一 uid 不受影响（后台直调路径以 uid 442 校验）
	if hidden, err := st.ListHiddenServiceAgentIDs(ctx, 442); err != nil || len(hidden) != 0 {
		t.Fatalf("uid 442 hidden = %v err=%v, want empty", hidden, err)
	}

	// 恢复（添加）→ 重新可见可调
	if err := st.SetServiceAgentSubscriptionHidden(ctx, 441, 1, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	handler.NotifySubscriptionChanged(441)
	if names := testToolNames(t, session); !containsName(names, "catsco_hello") {
		t.Fatalf("restored tools = %v, want catsco_hello visible", names)
	}
	call, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "catsco_hello", Arguments: map[string]any{"input": map[string]any{"q": "y"}}})
	if err != nil || call.IsError {
		t.Fatalf("restored call: err=%v result=%+v", err, call)
	}
	env := decodeServiceEnvelope(t, call.StructuredContent)
	if env.Status != ServiceRunCompleted {
		t.Fatalf("restored call envelope = %+v", env)
	}
}

// 清单变更对在线 MCP 会话自动下发 tools/list_changed 通知（SDK 能力声明 + 增量同步）。
func TestServiceAgentsMCPSubscriptionListChanged(t *testing.T) {
	st := newFakeServiceAgentStore()
	handler := NewServiceAgentsMCPHandler(st)
	ts := httptest.NewServer(mcpAuthStub(441, handler))
	t.Cleanup(ts.Close)

	notified := make(chan struct{}, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "svc-test", Version: "0.1.0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(_ context.Context, _ *mcp.ToolListChangedRequest) {
			notified <- struct{}{}
		},
	})
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: ts.URL, HTTPClient: http.DefaultClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect mcp: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// 屏蔽 + 同步 → 应收到 list_changed（SDK 通知有 debounce，给足窗口）
	if err := st.SetServiceAgentSubscriptionHidden(context.Background(), 441, 1, true); err != nil {
		t.Fatalf("hide: %v", err)
	}
	handler.NotifySubscriptionChanged(441)
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("no tools/list_changed notification after subscription change")
	}
	if names := testToolNames(t, session); containsName(names, "catsco_hello") {
		t.Fatalf("after notification tools = %v, want hello removed", names)
	}
}

// ---- 应用目录管理 API ----

func newCatalogServer(t *testing.T, st *fakeServiceAgentStore, uid int64, whitelist ...int64) (*ServiceAgentCatalogHandler, string) {
	t.Helper()
	handler := NewServiceAgentCatalogHandler(st)
	// 内测门：默认把当前测试 uid 放进白名单（不传则以空名单构造，专测禁用路径）。
	if len(whitelist) == 0 {
		whitelist = []int64{uid}
	} else if whitelist[0] < 0 {
		whitelist = nil
	}
	handler.allowedUIDs = whitelist
	mux := http.NewServeMux()
	wrap := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mcpAuthStub(uid, next).ServeHTTP(w, r)
		}
	}
	mux.HandleFunc("GET /api/service-agents/catalog/access", wrap(handler.HandleAccess))
	mux.HandleFunc("GET /api/service-agents/catalog", wrap(handler.HandleCatalog))
	mux.HandleFunc("PUT /api/service-agents/{slug}/subscription", wrap(handler.HandleAdd))
	mux.HandleFunc("DELETE /api/service-agents/{slug}/subscription", wrap(handler.HandleRemove))
	mux.HandleFunc("DELETE /api/service-agents/{slug}/self-intro", wrap(handler.HandleDismissSelfIntro))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return handler, ts.URL
}

func doCatalog(t *testing.T, method, url string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, payload
}

// 目录是「编辑已有应用」的唯一数据来源：
//   - description 对所有人可见（它就是调用方 Agent 看到的文字，看起来空就是没人会用）；
//   - definition 只回给 owner 本人（编辑要带着 runner/pricing 合并，发布是全量覆盖）。
func TestServiceAgentCatalogExposesDescriptionAndOwnDefinition(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.agents = []store.ServiceAgentDescriptor{
		{
			ID: 11, Slug: "my-clock", Name: "我的时钟", Version: 2,
			AsyncThresholdSeconds: 30,
			OwnerKind:             store.ServiceAgentOwnerUser,
			OwnerUID:              441,
			Visibility:            store.ServiceAgentVisibilityPublic,
			Description:           "读我电脑的当前时间。",
			DefinitionJSON:        `{"runner":{"type":"worker","command":"node clock.js"},"pricing":{"unit":"token","per_1k_tokens":0.02}}`,
		},
		{
			ID: 12, Slug: "other-agent", Name: "别人的应用", Version: 1,
			AsyncThresholdSeconds: 60,
			OwnerKind:             store.ServiceAgentOwnerUser,
			OwnerUID:              999,
			Visibility:            store.ServiceAgentVisibilityPublic,
			Description:           "别人写的描述。",
			DefinitionJSON:        `{"runner":{"type":"worker","command":"secret.sh"}}`,
		},
	}
	_, base := newCatalogServer(t, st, 441)
	status, payload := doCatalog(t, "GET", base+"/api/service-agents/catalog")
	if status != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", status, payload)
	}
	var catalog struct {
		Agents []serviceCatalogEntry `json:"agents"`
	}
	if err := json.Unmarshal(payload, &catalog); err != nil {
		t.Fatalf("decode: %v %s", err, payload)
	}
	bySlug := map[string]serviceCatalogEntry{}
	for _, entry := range catalog.Agents {
		bySlug[entry.Slug] = entry
	}
	mine := bySlug["my-clock"]
	if mine.Description != "读我电脑的当前时间。" {
		t.Fatalf("description = %q", mine.Description)
	}
	if len(mine.Definition) == 0 || !strings.Contains(string(mine.Definition), "node clock.js") {
		t.Fatalf("owner must get own definition back for editing: %s", mine.Definition)
	}
	other := bySlug["other-agent"]
	if other.Description != "别人写的描述。" {
		t.Fatalf("description should be public metadata: %q", other.Description)
	}
	if len(other.Definition) != 0 {
		t.Fatalf("must not leak other owner's runner config: %s", other.Definition)
	}
}

func TestServiceAgentCatalogAPI(t *testing.T) {
	st := newFakeServiceAgentStore()
	handler, base := newCatalogServer(t, st, 441)
	notifier := &fakeSubscriptionNotifier{}
	handler.SetSubscriptionNotifier(notifier)

	// 未认证（无 uid）：401
	unauth := httptest.NewRecorder()
	handler.HandleCatalog(unauth, httptest.NewRequest("GET", "/api/service-agents/catalog", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("no uid status = %d, want 401", unauth.Code)
	}

	// 目录：全部 agent + 默认可见
	status, payload := doCatalog(t, "GET", base+"/api/service-agents/catalog")
	if status != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", status, payload)
	}
	var catalog struct {
		Agents []serviceCatalogEntry `json:"agents"`
	}
	if err := json.Unmarshal(payload, &catalog); err != nil || len(catalog.Agents) == 0 {
		t.Fatalf("catalog decode: %v %s", err, payload)
	}
	for _, entry := range catalog.Agents {
		if entry.Hidden {
			t.Fatalf("default must be visible: %+v", entry)
		}
	}

	// 删除（屏蔽）：幂等两次
	if status, payload := doCatalog(t, "DELETE", base+"/api/service-agents/hello/subscription"); status != http.StatusOK || !strings.Contains(string(payload), `"hidden":true`) {
		t.Fatalf("remove status=%d body=%s", status, payload)
	}
	if status, _ := doCatalog(t, "DELETE", base+"/api/service-agents/hello/subscription"); status != http.StatusOK {
		t.Fatalf("remove replay status = %d, want 200", status)
	}
	status, payload = doCatalog(t, "GET", base+"/api/service-agents/catalog")
	if status != http.StatusOK || !strings.Contains(string(payload), `"hidden":true`) {
		t.Fatalf("catalog after remove status=%d body=%s", status, payload)
	}

	// 添加（恢复）：幂等
	if status, payload := doCatalog(t, "PUT", base+"/api/service-agents/hello/subscription"); status != http.StatusOK || !strings.Contains(string(payload), `"hidden":false`) {
		t.Fatalf("add status=%d body=%s", status, payload)
	}
	if status, _ := doCatalog(t, "PUT", base+"/api/service-agents/hello/subscription"); status != http.StatusOK {
		t.Fatalf("add replay status = %d, want 200", status)
	}
	status, payload = doCatalog(t, "GET", base+"/api/service-agents/catalog")
	if status != http.StatusOK || strings.Contains(string(payload), `"hidden":true`) {
		t.Fatalf("catalog after add status=%d body=%s", status, payload)
	}

	// 未知 slug：404
	if status, _ := doCatalog(t, "PUT", base+"/api/service-agents/ghost/subscription"); status != http.StatusNotFound {
		t.Fatalf("unknown slug status = %d, want 404", status)
	}

	// 通知器：每次成功变更各一次（2 remove + 2 add），404 不通知
	if len(notifier.calls) != 4 {
		t.Fatalf("notifier calls = %v, want 4", notifier.calls)
	}
	for _, uid := range notifier.calls {
		if uid != 441 {
			t.Fatalf("notifier uid = %d, want 441", uid)
		}
	}
}

// 内测门（2026-10-16）：白名单 uid 可见可用；非白名单静默看不到（access=false）+ 管理 API 403。
func TestServiceAgentCatalogInternalGate(t *testing.T) {
	st := newFakeServiceAgentStore()

	// 白名单内（uid 38）：access=true + 管理 API 正常
	_, baseAllowed := newCatalogServer(t, st, 38)
	status, payload := doCatalog(t, "GET", baseAllowed+"/api/service-agents/catalog/access")
	if status != http.StatusOK || !strings.Contains(string(payload), `"allowed":true`) {
		t.Fatalf("whitelisted access status=%d body=%s", status, payload)
	}
	if status, _ := doCatalog(t, "GET", baseAllowed+"/api/service-agents/catalog"); status != http.StatusOK {
		t.Fatalf("whitelisted catalog status = %d, want 200", status)
	}
	if status, _ := doCatalog(t, "PUT", baseAllowed+"/api/service-agents/hello/subscription"); status != http.StatusOK {
		t.Fatalf("whitelisted add status = %d, want 200", status)
	}

	// 白名单外（uid 442，白名单仍只含 38）：access=false + 管理 API 403
	_, baseDenied := newCatalogServer(t, st, 442, 38)
	status, payload = doCatalog(t, "GET", baseDenied+"/api/service-agents/catalog/access")
	if status != http.StatusOK || !strings.Contains(string(payload), `"allowed":false`) {
		t.Fatalf("non-whitelisted access status=%d body=%s", status, payload)
	}
	for _, probe := range []struct{ method, path string }{
		{"GET", "/api/service-agents/catalog"},
		{"PUT", "/api/service-agents/hello/subscription"},
		{"DELETE", "/api/service-agents/hello/subscription"},
	} {
		if status, _ := doCatalog(t, probe.method, baseDenied+probe.path); status != http.StatusForbidden {
			t.Fatalf("%s %s status = %d, want 403", probe.method, probe.path, status)
		}
	}

	// 空白名单（未配置）：全员禁用（含任意 uid）
	_, baseDisabled := newCatalogServer(t, st, 38, -1)
	status, payload = doCatalog(t, "GET", baseDisabled+"/api/service-agents/catalog/access")
	if status != http.StatusOK || !strings.Contains(string(payload), `"allowed":false`) {
		t.Fatalf("empty whitelist access status=%d body=%s", status, payload)
	}
	if status, _ := doCatalog(t, "GET", baseDisabled+"/api/service-agents/catalog"); status != http.StatusForbidden {
		t.Fatalf("empty whitelist catalog status = %d, want 403", status)
	}
}

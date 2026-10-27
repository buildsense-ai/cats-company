package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// ---- 用户自助上架（设计 §4.3 P2）----

func newPublishTestServer(t *testing.T, st *fakeServiceAgentStore, uid int64, allow bool) *httptest.Server {
	t.Helper()
	handler := NewServiceAgentPublishHandlerFromEnv(st)
	handler.SetAllowed(func(int64) bool { return allow })
	ts := httptest.NewServer(mcpAuthStub(uid, http.HandlerFunc(handler.HandlePublish)))
	t.Cleanup(ts.Close)
	return ts
}

func postPublish(t *testing.T, url string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("post publish: %v", err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func bundleB64(payload string) string {
	return base64.StdEncoding.EncodeToString([]byte(payload))
}

func TestServiceAgentPublishRequiresWhitelist(t *testing.T) {
	st := newFakeServiceAgentStore()
	ts := newPublishTestServer(t, st, 441, false)
	status, _ := postPublish(t, ts.URL, map[string]any{
		"slug": "my-agent", "bundle_base64": bundleB64("pkg"),
	})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
}

func TestServiceAgentPublishValidatesInput(t *testing.T) {
	st := newFakeServiceAgentStore()
	ts := newPublishTestServer(t, st, 441, true)

	cases := []struct {
		name   string
		body   map[string]any
		status int
		errKey string
	}{
		{name: "bad slug", body: map[string]any{"slug": "Bad Slug!", "bundle_base64": bundleB64("x")}, status: 400, errKey: "invalid_slug"},
		{name: "bad visibility", body: map[string]any{"slug": "ok-slug", "visibility": "secret", "bundle_base64": bundleB64("x")}, status: 400, errKey: "invalid_visibility"},
		{name: "missing bundle", body: map[string]any{"slug": "ok-slug"}, status: 400, errKey: "bundle_required"},
		{name: "bad base64", body: map[string]any{"slug": "ok-slug", "bundle_base64": "!!!notb64"}, status: 400, errKey: "invalid_bundle"},
		// 公开分享但不写描述：调用方 Agent 只看到名字，等于不知道能不能用。
		{name: "public without description", body: map[string]any{"slug": "ok-slug", "bundle_base64": bundleB64("x")}, status: 400, errKey: "description_required"},
		// 好友级同理：能被别人看到，就得说得清自己是干什么的。
		{name: "friends without description", body: map[string]any{"slug": "ok-slug", "visibility": "friends", "bundle_base64": bundleB64("x")}, status: 400, errKey: "description_required"},
		// 自用不强制：没人会用它，写不写是自己的事。但它得有包，所以仍然不合法。
		{name: "private keeps description optional", body: map[string]any{"slug": "ok-slug", "visibility": "private", "bundle_base64": "!!!notb64"}, status: 400, errKey: "invalid_bundle"},
		// 入参 schema 与包通道同一套规则：写错了要当场报，不能落库成「input 是 string」。
		{name: "input schema not object", body: map[string]any{"slug": "ok-slug", "bundle_base64": bundleB64("x"), "definition": map[string]any{"input_schema": "just a string"}}, status: 400, errKey: "invalid_input_schema"},
		{name: "input schema wrong top type", body: map[string]any{"slug": "ok-slug", "bundle_base64": bundleB64("x"), "definition": map[string]any{"input_schema": map[string]any{"type": "string"}}}, status: 400, errKey: "invalid_input_schema"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := postPublish(t, ts.URL, tc.body)
			if status != tc.status {
				t.Fatalf("status = %d, want %d (%v)", status, tc.status, out)
			}
			if got := out["error"]; got != tc.errKey {
				t.Fatalf("error = %v, want %s", got, tc.errKey)
			}
		})
	}
}

// definition 里的 input_schema 与包通道（agent.json）共用同一套校验与紧凑化；
// 其它字段必须原样保留（不能因为规范化把 pricing/runner 吃掉）。
func TestServiceAgentDefinitionNormalizesInputSchema(t *testing.T) {
	raw := json.RawMessage(`{
  "description": "润色英文稿",
  "async_threshold_seconds": 30,
  "runner": {"type": "worker"},
  "input_schema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}
}`)
	out, err := normalizeServiceAgentDefinition(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var view struct {
		Description           string          `json:"description"`
		AsyncThresholdSeconds int             `json:"async_threshold_seconds"`
		Runner                map[string]any  `json:"runner"`
		InputSchema           json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		t.Fatalf("normalized not valid json: %v", err)
	}
	if view.Description != "润色英文稿" || view.AsyncThresholdSeconds != 30 || view.Runner["type"] != "worker" {
		t.Fatalf("other fields lost: %s", out)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(view.InputSchema, &schema); err != nil {
		t.Fatalf("input_schema not valid json: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "text" {
		t.Fatalf("required = %v", schema.Required)
	}

	// 没声明 schema：不改动 definition（返回 nil 表示「保持原样」）。
	if out, err := normalizeServiceAgentDefinition(json.RawMessage(`{"description":"x"}`)); err != nil || out != nil {
		t.Fatalf("no schema: out=%s err=%v", out, err)
	}
}

// 首次发布占位 owner；同内容重发幂等；他人抢同一 slug 被拒。
func TestServiceAgentPublishOwnershipAndIdempotency(t *testing.T) {
	st := newFakeServiceAgentStore()
	owner := int64(441)
	ts := newPublishTestServer(t, st, owner, true)

	payload := bundleB64("package-v1")
	status, out := postPublish(t, ts.URL, map[string]any{
		"slug": "my-helper", "name": "我的助手", "bundle_base64": payload,
		"definition": map[string]any{
			"async_threshold_seconds": 30,
			"description":             "把中文技术文档润色成对外英文稿，保留代码块。",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("publish status = %d (%v)", status, out)
	}
	sum := sha256.Sum256([]byte("package-v1"))
	if got := out["package_hash"]; got != hex.EncodeToString(sum[:]) {
		t.Fatalf("package_hash = %v, want sha256(bundle)", got)
	}
	if v, ok := out["version"].(float64); !ok || int(v) != 1 {
		t.Fatalf("version = %v, want 1", out["version"])
	}

	// 同内容重发：版本不变（内容寻址幂等）。
	status, again := postPublish(t, ts.URL, map[string]any{
		"slug": "my-helper", "name": "我的助手", "bundle_base64": payload,
		"definition": map[string]any{"description": "把中文技术文档润色成对外英文稿，保留代码块。"},
	})
	if status != http.StatusOK {
		t.Fatalf("republish status = %d (%v)", status, again)
	}
	if again["version"] != out["version"] {
		t.Fatalf("republish version = %v, want %v", again["version"], out["version"])
	}

	// 新内容 → 版本递增。
	status, next := postPublish(t, ts.URL, map[string]any{
		"slug": "my-helper", "bundle_base64": bundleB64("package-v2"),
		"definition": map[string]any{"description": "把中文技术文档润色成对外英文稿，保留代码块。"},
	})
	if status != http.StatusOK {
		t.Fatalf("new content status = %d (%v)", status, next)
	}
	if v, _ := next["version"].(float64); int(v) != 2 {
		t.Fatalf("new content version = %v, want 2", next["version"])
	}

	// 他人抢同一 slug → 409 slug_taken。
	otherTS := newPublishTestServer(t, st, 442, true)
	status, conflict := postPublish(t, otherTS.URL, map[string]any{
		"slug": "my-helper", "bundle_base64": bundleB64("hijack"),
		"definition": map[string]any{"description": "抢注测试"},
	})
	if status != http.StatusConflict || conflict["error"] != "slug_taken" {
		t.Fatalf("hijack = %d %v, want 409 slug_taken", status, conflict)
	}

	// 平台内部 slug（hello）不可抢占。
	status, reserved := postPublish(t, ts.URL, map[string]any{
		"slug": "hello", "bundle_base64": bundleB64("fake"),
		"definition": map[string]any{"description": "抢注内部 slug 测试"},
	})
	if status != http.StatusConflict || reserved["error"] != "slug_reserved" {
		t.Fatalf("reserved = %d %v, want 409 slug_reserved", status, reserved)
	}
}

// 三级分享的发布侧：friends 是合法取值，且会被存下来（否则面板选了也不生效）。
func TestServiceAgentPublishAcceptsFriendsVisibility(t *testing.T) {
	st := newFakeServiceAgentStore()
	ts := newPublishTestServer(t, st, 441, true)

	status, out := postPublish(t, ts.URL, map[string]any{
		"slug": "friends-only", "name": "好友专属", "visibility": "friends",
		"bundle_base64": bundleB64("friends-pkg"),
		"definition":    map[string]any{"description": "只分享给好友用的工具"},
	})
	if status != http.StatusOK {
		t.Fatalf("publish status = %d (%v)", status, out)
	}
	if got := out["visibility"]; got != "friends" {
		t.Fatalf("visibility = %v, want friends", got)
	}

	st.mu.Lock()
	var saved string
	for _, agent := range st.agents {
		if agent.Slug == "friends-only" {
			saved = agent.Visibility
		}
	}
	st.mu.Unlock()
	if saved != "friends" {
		t.Fatalf("落库 visibility = %q, want friends", saved)
	}
}

// private agent 只对 owner 可见：别人目录里没有，工具面也没有，硬调返回 agent_not_found。
func TestServiceAgentVisibilityScope(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	owner := int64(441)
	other := int64(442)
	// 目录接口有独立内测白名单门，测试里放开两个账号。
	t.Setenv("CATSCO_SERVICE_CATALOG_UID_WHITELIST", "441,442")

	if err := st.ClaimServiceAgentOwnership(context.Background(), "mine-only", "私有助手", "user", owner, "private"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.PublishServiceAgentPackage(context.Background(), "mine-only", "私有助手",
		json.RawMessage(`{}`), "hash-private", json.RawMessage(`{"schema":"x"}`), false); err != nil {
		t.Fatalf("publish: %v", err)
	}

	catalog := NewServiceAgentCatalogHandlerFromEnv(st)
	catalog.SetSubscriptionNotifier(&fakeSubscriptionNotifier{})

	ownerSrv := httptest.NewServer(mcpAuthStub(owner, http.HandlerFunc(catalog.HandleCatalog)))
	t.Cleanup(ownerSrv.Close)
	otherSrv := httptest.NewServer(mcpAuthStub(other, http.HandlerFunc(catalog.HandleCatalog)))
	t.Cleanup(otherSrv.Close)

	fetch := func(url string) []map[string]any {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("get catalog: %v", err)
		}
		defer resp.Body.Close()
		var payload struct {
			Agents []map[string]any `json:"agents"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode catalog: %v", err)
		}
		return payload.Agents
	}

	ownerAgents := fetch(ownerSrv.URL)
	var mine map[string]any
	for _, agent := range ownerAgents {
		if agent["slug"] == "mine-only" {
			mine = agent
		}
	}
	if mine == nil {
		t.Fatalf("owner catalog misses private agent: %v", ownerAgents)
	}
	if mine["mine"] != true || mine["visibility"] != "private" {
		t.Fatalf("owner entry = %v, want mine=true visibility=private", mine)
	}

	for _, agent := range fetch(otherSrv.URL) {
		if agent["slug"] == "mine-only" {
			t.Fatalf("private agent leaked into other account catalog: %v", agent)
		}
	}

	// 工具面：别人的 tools/list 没有它。
	handler := NewServiceAgentsMCPHandler(st)
	otherMCP := httptest.NewServer(mcpAuthStub(other, handler))
	t.Cleanup(otherMCP.Close)
	session := newMCPSession(t, otherMCP.URL)
	if names := testToolNames(t, session); containsName(names, "catsco_mine-only") {
		t.Fatalf("private agent leaked into other account tools: %v", names)
	}

	// 硬调（已知 slug）也被 invoke 层拦住（不依赖 tools/list 是否同步）。
	privateAgent := store.ServiceAgentDescriptor{
		ID: 99, Slug: "mine-only", Name: "私有助手", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner, Visibility: store.ServiceAgentVisibilityPrivate,
	}
	res, _, err := handler.invoke(context.Background(), privateAgent, other, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res == nil || !res.IsError || !strings.Contains(mcpToolErrorText(res), "agent_not_found") {
		t.Fatalf("private agent should be rejected for non-owner: %+v", res)
	}

	// owner 自己能调（走正常链路，会真的建会话/run）。
	res, _, err = handler.invoke(context.Background(), privateAgent, owner, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("owner invoke: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("owner should be allowed: %s", mcpToolErrorText(res))
	}
}

// 「好友」级：发布者的好友、以及这个 bot 的好友都能看到并调用；陌生人看不到、
// 硬调被拒、连订阅（探测存在性）都拿不到。
func TestServiceAgentFriendsVisibilityScope(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	const (
		owner         = int64(441)
		friendOfOwner = int64(442)
		friendOfBot   = int64(443)
		stranger      = int64(444)
		botUID        = int64(982)
	)
	t.Setenv("CATSCO_SERVICE_CATALOG_UID_WHITELIST", "441,442,443,444")

	// 好友级应用：背后是 bot 982。
	st.agents = append(st.agents, store.ServiceAgentDescriptor{
		ID: 501, Slug: "friends-app", Name: "好友应用", Version: 1, AsyncThresholdSeconds: 60,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
		Visibility:     store.ServiceAgentVisibilityFriends,
		DefinitionJSON: `{"description":"只有好友能用","runner":{"type":"bot","bot_uid":982},"input_schema":{"type":"object"}}`,
	})

	// 两个账号各有一条好友边：一个连着发布者，一个连着这个 bot。
	listSlugs := func(url string) map[string]bool {
		resp, err := http.Get(url + "/api/service-agents/catalog")
		if err != nil {
			t.Fatalf("get catalog: %v", err)
		}
		defer resp.Body.Close()
		var payload struct {
			Agents []map[string]any `json:"agents"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode catalog: %v", err)
		}
		out := map[string]bool{}
		for _, agent := range payload.Agents {
			out[fmt.Sprint(agent["slug"])] = true
		}
		return out
	}

	// 发布者本人
	if !listSlugs(catalogForUID(t, st, owner, &stubFriendship{}))["friends-app"] {
		t.Fatal("发布者应该看得到自己的应用")
	}
	// 发布者的好友
	if !listSlugs(catalogForUID(t, st, friendOfOwner, &stubFriendship{friends: map[int64]bool{owner: true}}))["friends-app"] {
		t.Fatal("发布者的好友应该看得到")
	}
	// 这个 bot 的好友（不认识发布者也算）
	if !listSlugs(catalogForUID(t, st, friendOfBot, &stubFriendship{friends: map[int64]bool{botUID: true}}))["friends-app"] {
		t.Fatal("这个 bot 的好友应该看得到")
	}
	// 陌生人
	strangerCatalog := catalogForUID(t, st, stranger, &stubFriendship{})
	if listSlugs(strangerCatalog)["friends-app"] {
		t.Fatal("好友级应用不该出现在陌生人的目录里")
	}
	// 陌生人连「探测存在」都不该成功：订阅返回 404，与不存在走同一条路。
	req, err := http.NewRequest(http.MethodPut, strangerCatalog+"/api/service-agents/friends-app/subscription", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("陌生人订阅看不见的应用 = %d, want 404", resp.StatusCode)
	}

	// 工具面：好友看得到工具，陌生人没有；陌生人硬调被拒。
	handlerFor := func(isFriend bool) *ServiceAgentsMCPHandler {
		h := NewServiceAgentsMCPHandler(st)
		if isFriend {
			h.SetFriendship(&stubFriendship{friends: map[int64]bool{owner: true}})
		} else {
			h.SetFriendship(&stubFriendship{})
		}
		return h
	}
	friendMCP := httptest.NewServer(mcpAuthStub(friendOfOwner, handlerFor(true)))
	t.Cleanup(friendMCP.Close)
	if names := testToolNames(t, newMCPSession(t, friendMCP.URL)); !containsName(names, "catsco_friends-app") {
		t.Fatalf("好友的工具面应该含这个应用: %v", names)
	}

	strangerHandler := handlerFor(false)
	strangerMCP := httptest.NewServer(mcpAuthStub(stranger, strangerHandler))
	t.Cleanup(strangerMCP.Close)
	if names := testToolNames(t, newMCPSession(t, strangerMCP.URL)); containsName(names, "catsco_friends-app") {
		t.Fatalf("陌生人的工具面不该含这个应用: %v", names)
	}
	agent := st.agents[len(st.agents)-1]
	res, _, err := strangerHandler.invoke(context.Background(), agent, stranger, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res == nil || !res.IsError || !strings.Contains(mcpToolErrorText(res), "agent_not_found") {
		t.Fatalf("陌生人硬调好友级应用应被拒: %+v", res)
	}
}

// catalogForUID 造一个只带目录读接口的测试服务端（每个 uid 一个）。
func catalogForUID(t *testing.T, st *fakeServiceAgentStore, uid int64, friendship ServiceAgentFriendResolver) string {
	t.Helper()
	handler := NewServiceAgentCatalogHandlerFromEnv(st)
	handler.SetFriendship(friendship)
	mux := http.NewServeMux()
	wrap := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { mcpAuthStub(uid, next).ServeHTTP(w, r) }
	}
	mux.HandleFunc("GET /api/service-agents/catalog", wrap(handler.HandleCatalog))
	mux.HandleFunc("PUT /api/service-agents/{slug}/subscription", wrap(handler.HandleAdd))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

// 收回分享（下架）：只有发布者本人能操作；下架后目录/tools/list 来源立即不含它，
// 已缓存工具名的客户端再调用拿到“已被发布者收回”；重复收回幂等；重新发布恢复上架。
func TestServiceAgentUnpublishRevokesShare(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	owner := int64(441)
	other := int64(442)
	t.Setenv("CATSCO_SERVICE_CATALOG_UID_WHITELIST", "441,442")

	ctx := context.Background()
	if err := st.ClaimServiceAgentOwnership(ctx, "shared-agent", "分享的应用", "user", owner, "public"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.PublishServiceAgentPackage(ctx, "shared-agent", "分享的应用",
		json.RawMessage(`{}`), "hash-shared", json.RawMessage(`{"schema":"x"}`), false); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var descriptor store.ServiceAgentDescriptor
	agents, err := st.ListServiceAgents(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, agent := range agents {
		if agent.Slug == "shared-agent" {
			descriptor = agent
		}
	}
	if descriptor.Slug == "" {
		t.Fatalf("published agent not listed: %+v", agents)
	}

	publishHandler := NewServiceAgentPublishHandlerFromEnv(st)
	publishHandler.SetAllowed(func(int64) bool { return true })
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/service-agents/{slug}", publishHandler.HandleUnpublish)
	ownerSrv := httptest.NewServer(mcpAuthStub(owner, mux))
	t.Cleanup(ownerSrv.Close)
	otherSrv := httptest.NewServer(mcpAuthStub(other, mux))
	t.Cleanup(otherSrv.Close)

	del := func(base string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, base+"/api/service-agents/shared-agent", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		defer resp.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		return resp.StatusCode, payload
	}

	// 别人不能收回，且应用仍然上架。
	if status, payload := del(otherSrv.URL); status != http.StatusForbidden {
		t.Fatalf("other delete = %d %v, want 403", status, payload)
	}
	if _, _, _, _, err := st.GetServiceAgentBySlug(ctx, "shared-agent"); err != nil {
		t.Fatalf("agent should still be active after foreign delete: %v", err)
	}

	// 本人收回。
	if status, payload := del(ownerSrv.URL); status != http.StatusOK || payload["state"] != "inactive" {
		t.Fatalf("owner delete = %d %v, want 200 inactive", status, payload)
	}
	// 目录来源不再包含（tools/list 与目录接口都走这个列表）。
	agents, err = st.ListServiceAgents(ctx)
	if err != nil {
		t.Fatalf("list after unpublish: %v", err)
	}
	for _, agent := range agents {
		if agent.Slug == "shared-agent" {
			t.Fatalf("unpublished agent still listed: %+v", agents)
		}
	}
	if _, _, _, _, err := st.GetServiceAgentBySlug(ctx, "shared-agent"); err == nil {
		t.Fatal("GetServiceAgentBySlug should fail after unpublish")
	}

	// 已缓存工具名的客户端再调用 → 明确告知已被收回。
	handler := NewServiceAgentsMCPHandler(st)
	res, _, err := handler.invoke(ctx, descriptor, other, serviceInvokeToolArgs{Input: map[string]any{"q": "hi"}})
	if err != nil {
		t.Fatalf("invoke after unpublish: %v", err)
	}
	if res == nil || !res.IsError || !strings.Contains(mcpToolErrorText(res), "已被发布者收回") {
		t.Fatalf("invoke after unpublish = %s, want 已被发布者收回", mcpToolErrorText(res))
	}

	// 重复收回幂等。
	if status, _ := del(ownerSrv.URL); status != http.StatusOK {
		t.Fatalf("repeat delete = %d, want 200", status)
	}

	// 重新发布同一调用名即可恢复上架。
	if err := st.ClaimServiceAgentOwnership(ctx, "shared-agent", "分享的应用", "user", owner, "public"); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if _, _, _, _, err := st.GetServiceAgentBySlug(ctx, "shared-agent"); err != nil {
		t.Fatalf("re-publish should restore active: %v", err)
	}
}

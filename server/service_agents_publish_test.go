package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
		// 分享给别人（公开 / 好友）但不写描述：调用方 Agent 只看到名字，等于不知道能不能用。
		{name: "public without description", body: map[string]any{"slug": "ok-slug", "bundle_base64": bundleB64("x")}, status: 400, errKey: "description_required"},
		{name: "friends without description", body: map[string]any{"slug": "ok-slug", "visibility": "friends", "bundle_base64": bundleB64("x")}, status: 400, errKey: "description_required"},
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

// 三级可见性的中间档：friends。
//
// 可见性按**访问者**求值，所以三处入口（目录、tools/list、调用准入）都要拿到
// 「看的人是不是发布者的好友」。这里走一遍完整链路，钉住三点：
//   - 好友在目录里看得到、能加、能调；
//   - 陌生人在目录/tools/list 里看不到，硬调返回 agent_not_found（不泄露存在性）；
//   - 订阅接口也不能当探测器（看不见的应用返回 404）。
func TestServiceAgentFriendsVisibilityScope(t *testing.T) {
	st := newFakeServiceAgentStore()
	st.autoCompleteAfter = 5 * time.Millisecond
	owner, friend, stranger := int64(441), int64(442), int64(900)
	// 目录管理面还有独立的内测白名单门；测试里把三个账号都放开。
	t.Setenv("CATSCO_SERVICE_CATALOG_UID_WHITELIST", "441,442,900")

	ctx := context.Background()
	if err := st.ClaimServiceAgentOwnership(ctx, "friends-agent", "好友可见应用", "user", owner, "friends"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.PublishServiceAgentPackage(ctx, "friends-agent", "好友可见应用",
		json.RawMessage(`{"description":"给好友用的翻译助手。"}`), "hash-friends",
		json.RawMessage(`{"schema":"x"}`), false); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// 好友图：441 ↔ 442；900 不是任何人的好友。
	graph := newFakeFriendGraph([2]int64{441, 442})

	catalog := NewServiceAgentCatalogHandlerFromEnv(st)
	catalog.SetSubscriptionNotifier(&fakeSubscriptionNotifier{})
	catalog.SetFriendReader(graph)

	fetchCatalog := func(uid int64) []map[string]any {
		srv := httptest.NewServer(mcpAuthStub(uid, http.HandlerFunc(catalog.HandleCatalog)))
		t.Cleanup(srv.Close)
		resp, err := http.Get(srv.URL + "/api/service-agents/catalog")
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
	hasSlug := func(agents []map[string]any, slug string) bool {
		for _, agent := range agents {
			if agent["slug"] == slug {
				return true
			}
		}
		return false
	}

	if !hasSlug(fetchCatalog(owner), "friends-agent") {
		t.Fatal("发布者自己必须看得到")
	}
	if !hasSlug(fetchCatalog(friend), "friends-agent") {
		t.Fatal("好友应该看得到（这就是这一级的意义）")
	}
	if hasSlug(fetchCatalog(stranger), "friends-agent") {
		t.Fatal("陌生人不应在目录里看到好友级应用")
	}

	// 订阅接口不能当探测器：看不见的应用（陌生人看好友级）统一 404，
	// 否则「加/删成功与否」就能用来确认别人的应用是否存在。
	subscribeStatus := func(uid int64, method string) int {
		mux := http.NewServeMux()
		wrap := func(next http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				mcpAuthStub(uid, next).ServeHTTP(w, r)
			}
		}
		mux.HandleFunc("PUT /api/service-agents/{slug}/subscription", wrap(catalog.HandleAdd))
		mux.HandleFunc("DELETE /api/service-agents/{slug}/subscription", wrap(catalog.HandleRemove))
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		req, err := http.NewRequest(method, srv.URL+"/api/service-agents/friends-agent/subscription", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := subscribeStatus(stranger, http.MethodPut); got != http.StatusNotFound {
		t.Fatalf("陌生人订阅不可见应用 = %d, want 404", got)
	}
	if got := subscribeStatus(friend, http.MethodPut); got != http.StatusOK {
		t.Fatalf("好友订阅该应用 = %d, want 200", got)
	}

	// 工具面：好友能看到工具，陌生人看不到也调不动。
	mcpHandler := NewServiceAgentsMCPHandler(st)
	mcpHandler.SetFriendReader(graph)
	friendMCP := httptest.NewServer(mcpAuthStub(friend, mcpHandler))
	t.Cleanup(friendMCP.Close)
	friendSession := newMCPSession(t, friendMCP.URL)
	if names := testToolNames(t, friendSession); !containsName(names, "catsco_friends-agent") {
		t.Fatalf("好友的 tools/list 应包含该工具: %v", names)
	}

	strangerMCP := httptest.NewServer(mcpAuthStub(stranger, mcpHandler))
	t.Cleanup(strangerMCP.Close)
	strangerSession := newMCPSession(t, strangerMCP.URL)
	if names := testToolNames(t, strangerSession); containsName(names, "catsco_friends-agent") {
		t.Fatalf("陌生人的 tools/list 不该包含它: %v", names)
	}

	descriptor := store.ServiceAgentDescriptor{
		ID: 77, Slug: "friends-agent", Name: "好友可见应用", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
		Visibility: store.ServiceAgentVisibilityFriends,
	}
	res, _, err := mcpHandler.invoke(ctx, descriptor, stranger, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res == nil || !res.IsError || !strings.Contains(mcpToolErrorText(res), "agent_not_found") {
		t.Fatalf("陌生人硬调应被拒: %s", mcpToolErrorText(res))
	}

	// 好友走正常链路：会真的建会话/run。
	res, _, err = mcpHandler.invoke(ctx, descriptor, friend, serviceInvokeToolArgs{})
	if err != nil {
		t.Fatalf("friend invoke: %v", err)
	}
	if res != nil && res.IsError {
		t.Fatalf("好友应该能调: %s", mcpToolErrorText(res))
	}

	// 好友关系查不到时按「不是好友」处理（fail-closed）：不能让存储抖动变成公开。
	brokenHandler := NewServiceAgentsMCPHandler(st)
	brokenGraph := newFakeFriendGraph([2]int64{441, 442})
	brokenGraph.err = errors.New("friends table unavailable")
	brokenHandler.SetFriendReader(brokenGraph)
	if names := testToolNames(t, newMCPSession(t, httptest.NewServer(mcpAuthStub(friend, brokenHandler)).URL)); containsName(names, "catsco_friends-agent") {
		t.Fatalf("好友查询失败时不该放行: %v", names)
	}
}

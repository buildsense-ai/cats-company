package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// —— bot-as-tool 的两条硬规则：bot 归属、计价方式 ——
//
// 这两条以前都没有拦住：
//   - 归属：publish 只看 bot 存不存在，于是任何人都能把别人的 bot 挂成自己的工具；
//   - 计价：bot 型配 unit=token 会一直算成 0 元（usage 由 bot 侧产生，平台取不到）。

type stubBotOwners map[int64]int64

func (s stubBotOwners) GetBotOwner(botUID int64) (int64, error) {
	if owner, ok := s[botUID]; ok {
		return owner, nil
	}
	return 0, sql.ErrNoRows
}

func newBotGuardPublishServer(t *testing.T, st *fakeServiceAgentStore, uid int64, owners stubBotOwners) *httptest.Server {
	t.Helper()
	handler := NewServiceAgentPublishHandlerFromEnv(st)
	handler.SetAllowed(func(int64) bool { return true })
	handler.SetBotOwners(owners)
	ts := httptest.NewServer(mcpAuthStub(uid, http.HandlerFunc(handler.HandlePublish)))
	t.Cleanup(ts.Close)
	return ts
}

func botDefinition(runner map[string]any, extra map[string]any) map[string]any {
	definition := map[string]any{"runner": runner, "description": "把一段话交给杰克处理"}
	for k, v := range extra {
		definition[k] = v
	}
	return definition
}

func TestServiceAgentPublishChecksBotOwnership(t *testing.T) {
	st := newFakeServiceAgentStore()
	// 982 是账号 38 的 bot；发布者是 441。
	ts := newBotGuardPublishServer(t, st, 441, stubBotOwners{982: 38})

	cases := []struct {
		name    string
		uid     int64
		runner  map[string]any
		status  int
		wantErr string
	}{
		{
			name:   "别人的 bot 不让上架",
			runner: map[string]any{"type": "bot", "bot_uid": 982},
			status: http.StatusBadRequest, wantErr: "bot_not_owned",
		},
		{
			name:   "自己的 bot 正常上架",
			uid:    38,
			runner: map[string]any{"type": "bot", "bot_uid": 982},
			status: http.StatusOK,
		},
		{
			name:   "不存在的 bot 报 bot_not_found",
			runner: map[string]any{"type": "bot", "bot_uid": 777},
			status: http.StatusBadRequest, wantErr: "bot_not_found",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid := int64(441)
			if tc.uid != 0 {
				uid = tc.uid
			}
			srv := ts
			if uid != 441 {
				// 换个发布者：直接用同一套 fake store，但换成 38 的身份。
				srv = newBotGuardPublishServer(t, st, uid, stubBotOwners{982: 38})
			}
			status, body := postPublish(t, srv.URL, map[string]any{
				"slug":          "jack-bot-tool" + strings.Repeat("x", i),
				"visibility":    "private",
				"bundle_base64": bundleB64("pkg"),
				"definition":    botDefinition(tc.runner, nil),
			})
			if status != tc.status {
				t.Fatalf("status = %d, want %d（body=%v）", status, tc.status, body)
			}
			if tc.wantErr != "" && body["error"] != tc.wantErr {
				t.Fatalf("error = %v, want %s", body["error"], tc.wantErr)
			}
		})
	}
}

func TestServiceAgentPublishRejectsTokenPricingForBot(t *testing.T) {
	st := newFakeServiceAgentStore()
	ts := newBotGuardPublishServer(t, st, 38, stubBotOwners{982: 38})

	status, body := postPublish(t, ts.URL, map[string]any{
		"slug":          "jack-bot-token",
		"visibility":    "private",
		"bundle_base64": bundleB64("pkg"),
		"definition": botDefinition(
			map[string]any{"type": "bot", "bot_uid": 982},
			map[string]any{"pricing": map[string]any{"unit": "token", "per_1k_tokens": 0.02}},
		),
	})
	if status != http.StatusBadRequest || body["error"] != "invalid_pricing_for_bot" {
		t.Fatalf("status = %d body = %v, want 400 invalid_pricing_for_bot", status, body)
	}

	// 按次计价是 bot 型的正路：不该被拦。
	status, body = postPublish(t, ts.URL, map[string]any{
		"slug":          "jack-bot-run",
		"visibility":    "private",
		"bundle_base64": bundleB64("pkg"),
		"definition": botDefinition(
			map[string]any{"type": "bot", "bot_uid": 982},
			map[string]any{"pricing": map[string]any{"unit": "run", "per_run": 0.05}},
		),
	})
	if status != http.StatusOK {
		t.Fatalf("unit=run status = %d body = %v, want 200", status, body)
	}
}

// —— 执行侧的兜底：归属变了、并发超了 ——

func (f *fakeBotStore) CountActiveServiceRunsForAgent(_ context.Context, _ int64, _ string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeRuns, f.countErr
}

func (f *fakeBotStore) CountActiveServiceRunsForAgentCaller(_ context.Context, _ int64, callerUID int64, _ string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callerErr != nil {
		return 0, f.callerErr
	}
	return f.callerActive[callerUID], nil
}

func TestBotRunnerStopsWhenBotOwnershipChanged(t *testing.T) {
	st := newFakeBotStore()
	st.owner = 38
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(st, hub)

	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)
	// 应用主人是 441，但 bot 982 属于 38 —— 这正是「改一次配置把别人的 bot 挂过来」的样子。
	agent.OwnerUID = 441
	run := store.ServiceRun{RunID: "run_owner", SessionID: "sess_owner", Input: json.RawMessage(`{"text":"hi"}`)}

	go runner.execute(botRunRequest{
		RunID: run.RunID, SessionID: run.SessionID, Slug: agent.Slug,
		BotUID: 982, AgentID: agent.ID, AgentOwnerUID: agent.OwnerUID,
		CallerUID: 441, Input: run.Input, SnapshotDefinition: json.RawMessage(agent.DefinitionJSON),
	})

	if !waitFor(t, func() bool { return st.errorText(run.RunID) != "" }, 2*time.Second) {
		t.Fatal("run 没有收尾")
	}
	if text := st.errorText(run.RunID); !strings.HasPrefix(text, "bot_not_owned") {
		t.Fatalf("error = %q, want bot_not_owned…", text)
	}
	if len(st.delivered(hub)) != 0 {
		t.Fatal("归属不对时不应把消息投给那个 bot")
	}
}

func TestBotRunnerConcurrencyGateFailsFast(t *testing.T) {
	st := newFakeBotStore()
	st.owner = 38
	st.activeRuns = 2 // 已经有 2 单在跑
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(st, hub)

	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982,"max_inflight":2}}`)
	go runner.execute(botRunRequest{
		RunID: "run_busy", SessionID: "sess_busy", Slug: agent.Slug,
		BotUID: 982, AgentID: agent.ID, CallerUID: 38,
		Input: json.RawMessage(`{"text":"hi"}`), SnapshotDefinition: json.RawMessage(agent.DefinitionJSON),
	})

	if !waitFor(t, func() bool { return st.errorText("run_busy") != "" }, 2*time.Second) {
		t.Fatal("run 没有收尾")
	}
	if text := st.errorText("run_busy"); !strings.HasPrefix(text, "bot_busy") {
		t.Fatalf("error = %q, want bot_busy…", text)
	}
	// 没设上限的应用保持原样：此时 count 再大也照派（回归保护）。
	if got := botRunnerTuningFor(json.RawMessage(`{"runner":{"type":"bot","bot_uid":982}}`)); got.MaxInflight != 0 {
		t.Fatalf("MaxInflight = %d, want 0（未配置 = 不限）", got.MaxInflight)
	}
}

// delivered 把 hub 收到的投递数量暴露成一个小函数，避免测试直接摸 fake 的字段语义。
func (f *fakeBotStore) delivered(hub *fakeBotHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), hub.delivered...)
}

// —— 异步句柄必须带 structuredContent ——
//
// 客户端（XiaoBa 的 mcp-tool-bridge）只从 structuredContent 里读 status/run_id 去自动轮询，
// 文本只用来兜底展示。bot 型这条路径以前返回 nil，于是句柄在客户端解析不出来，
// 自动轮询根本不启动，模型看到的就是一句「run=… status=running」当答案。
func TestServiceRunHandleCarriesStructuredEnvelope(t *testing.T) {
	h := NewServiceAgentsMCPHandler(newFakeServiceAgentStore())
	result, env := h.serviceRunHandleResult("sess_async", store.ServiceRun{RunID: "run_async", State: "running"})
	if result == nil || len(result.Content) == 0 {
		t.Fatal("句柄必须同时给文本摘要（有些客户端只渲染 content）")
	}
	if env.Status != ServiceRunRunning || env.RunID != "run_async" || env.SessionID != "sess_async" {
		t.Fatalf("handle envelope = %+v，调用方拿不到 run_id/status 就没法轮询", env)
	}
	// 已终态：如实报终态，别再让调用方白等一轮。
	done := store.ServiceRun{
		RunID: "run_done", State: "completed",
		Result: json.RawMessage(`{"contract_version":"catsco.service-run-result.v1","run_id":"run_done","session_id":"sess_done","status":"completed"}`),
	}
	_, doneEnv := h.serviceRunHandleResult("sess_done", done)
	if doneEnv.Status != ServiceRunCompleted {
		t.Fatalf("status = %q, want completed", doneEnv.Status)
	}
}

// —— 按调用方配额：一个调用方不能把总闸占满 ——

func TestBotRunnerPerCallerQuotaFailsFast(t *testing.T) {
	st := newFakeBotStore()
	st.owner = 38
	// 441 在这个应用上已经占了 2 单（平台默认每调用方上限 2）。
	st.callerActive = map[int64]int{441: 2}
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(st, hub)
	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)

	go runner.execute(botRunRequest{
		RunID: "run_caller_busy", SessionID: "sess_caller_busy", Slug: agent.Slug,
		BotUID: 982, AgentID: agent.ID, CallerUID: 441,
		Input: json.RawMessage(`{"text":"hi"}`), SnapshotDefinition: json.RawMessage(agent.DefinitionJSON),
	})

	if !waitFor(t, func() bool { return st.errorText("run_caller_busy") != "" }, 2*time.Second) {
		t.Fatal("run 没有收尾")
	}
	if text := st.errorText("run_caller_busy"); !strings.HasPrefix(text, "caller_busy") {
		t.Fatalf("error = %q, want caller_busy…", text)
	}
	if len(st.delivered(hub)) != 0 {
		t.Fatal("配额超限时不应把消息投给 bot（那是白等一场）")
	}
	if st.startCalls != 0 {
		t.Fatalf("被配额拦住的单不该去占位（StartServiceRun 调了 %d 次）—— 闸排在占位之后的话，同时到达的几单会互相看见对方、全部被拦", st.startCalls)
	}
}

func TestBotRunnerPerCallerQuotaLeavesOtherCallersAlone(t *testing.T) {
	st := newFakeBotStore()
	st.owner = 38
	// 441 已经满了，但 442 是另一个调用方 —— 不该被别人的额度挡住。
	st.callerActive = map[int64]int{441: 2}
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(st, hub)
	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)
	go runner.execute(botRunRequest{
		RunID: "run_other_caller", SessionID: "sess_other_caller", Slug: agent.Slug,
		BotUID: 982, AgentID: agent.ID, CallerUID: 442,
		Input: json.RawMessage(`{"text":"hi"}`), SnapshotDefinition: json.RawMessage(agent.DefinitionJSON),
	})

	if !waitFor(t, func() bool { return len(st.delivered(hub)) > 0 }, 2*time.Second) {
		t.Fatalf("另一个调用方应当照常派活（error=%q）", st.errorText("run_other_caller"))
	}
	if text := st.errorText("run_other_caller"); strings.HasPrefix(text, "caller_busy") {
		t.Fatalf("不该被别的调用方的额度挡住：%q", text)
	}
}

func TestBotRunnerPerCallerQuotaFailsOpenOnCounterError(t *testing.T) {
	st := newFakeBotStore()
	st.owner = 38
	st.callerErr = fmt.Errorf("boom")
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(st, hub)
	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)

	go runner.execute(botRunRequest{
		RunID: "run_counter_err", SessionID: "sess_counter_err", Slug: agent.Slug,
		BotUID: 982, AgentID: agent.ID, CallerUID: 441,
		Input: json.RawMessage(`{"text":"hi"}`), SnapshotDefinition: json.RawMessage(agent.DefinitionJSON),
	})

	if !waitFor(t, func() bool { return len(st.delivered(hub)) > 0 }, 2*time.Second) {
		t.Fatalf("计数查不到时应放行（预检不该因存储抖动拒绝调用），error=%q", st.errorText("run_counter_err"))
	}
}

// 平台默认值：每调用方 2 单；环境变量可以调，但会被夹到 [0,8]。
func TestCallerInflightDefaultsAndClamp(t *testing.T) {
	if got := builtinBotRunnerTuning().MaxCallerInflight; got != defaultBotRunnerMaxCallerInflight {
		t.Fatalf("内置默认 = %d, want %d", got, defaultBotRunnerMaxCallerInflight)
	}
	if got := (botRunnerTuning{MaxCallerInflight: 999}).clamped().MaxCallerInflight; got != maxBotRunnerCallerInflight {
		t.Fatalf("上限夹取 = %d, want %d", got, maxBotRunnerCallerInflight)
	}
	if got := (botRunnerTuning{MaxCallerInflight: -1}).clamped().MaxCallerInflight; got != 0 {
		t.Fatalf("负值应夹到 0（不限），got %d", got)
	}
}

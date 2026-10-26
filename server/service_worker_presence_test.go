package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 在线 worker 预检：用户没开 worker 时要立刻说清楚，而不是让人白等同步窗口。

// presenceInstance 造一条在线实例快照（owner 必填：预检按账号过滤）。
func presenceInstance(id string, ownerUID int64, metrics map[string]any) store.ServiceInstancePresence {
	raw, _ := json.Marshal(metrics)
	return store.ServiceInstancePresence{InstanceID: id, OwnerUID: ownerUID, Metrics: raw}
}

func agentOwnedBy(slug string, ownerUID int64) store.ServiceAgentDescriptor {
	return store.ServiceAgentDescriptor{ID: 1, Slug: slug, Version: 1, OwnerUID: ownerUID}
}

func TestWorkerPresenceServes(t *testing.T) {
	cases := []struct {
		name      string
		instances []store.ServiceInstancePresence
		slug      string
		want      bool
	}{
		{"无实例", nil, "a", false},
		{"实例声明该 slug", []store.ServiceInstancePresence{
			presenceInstance("w1", 7, map[string]any{"agents": []string{"a", "b"}}),
		}, "a", true},
		{"实例只服务别的 slug", []store.ServiceInstancePresence{
			presenceInstance("w1", 7, map[string]any{"agents": []string{"c"}}),
		}, "a", false},
		{"serve_all 兜住一切", []store.ServiceInstancePresence{
			presenceInstance("w1", 7, map[string]any{"serve_all": true}),
		}, "anything", true},
		{"多实例取并集", []store.ServiceInstancePresence{
			presenceInstance("w1", 7, map[string]any{"agents": []string{"c"}}),
			presenceInstance("w2", 7, map[string]any{"agents": []string{"a"}}),
		}, "a", true},
		{"metrics 非法当没有服务范围", []store.ServiceInstancePresence{
			{InstanceID: "w1", Metrics: json.RawMessage(`not-json`)},
		}, "a", true},
		// 升级兼容：旧版 worker 心跳只有 os/arch/pid，范围未知 → 放行，
		// 否则升级平台会把还在干活的旧 worker 全判成离线。
		{"旧版 worker 心跳（无 agents 字段）", []store.ServiceInstancePresence{
			presenceInstance("w1", 7, map[string]any{"worker": "service-run-worker", "os": "linux", "pid": 7}),
		}, "anything", true},
		{"空 metrics 也算未知→放行", []store.ServiceInstancePresence{
			{InstanceID: "w1"},
		}, "a", true},
		{"新版声明只服务别的应用时才拦", []store.ServiceInstancePresence{
			presenceInstance("w1", 7, map[string]any{"agents": []string{"c"}, "serve_all": false}),
		}, "a", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workerPresence(c.instances).Serves(c.slug); got != c.want {
				t.Fatalf("Serves(%q) = %v, want %v", c.slug, got, c.want)
			}
		})
	}
}

// fakePresenceStore 是 ServiceAgentStore 的最小替身 + 在线实例能力。
type fakePresenceStore struct {
	store.ServiceAgentStore
	definition json.RawMessage
	instances  []store.ServiceInstancePresence
	err        error
}

func (f *fakePresenceStore) GetServiceAgentDefinition(context.Context, int64, int64) (string, int, json.RawMessage, error) {
	return "slug", 60, f.definition, nil
}

func (f *fakePresenceStore) ListOnlineServiceInstances(context.Context, time.Duration) ([]store.ServiceInstancePresence, error) {
	return f.instances, f.err
}

func TestCheckWorkerOnline(t *testing.T) {
	ctx := context.Background()
	agent := agentOwnedBy("my-agent", 441)

	// 一个 worker 都没在线：明确报「没 worker」。
	h := &ServiceAgentsMCPHandler{store: &fakePresenceStore{}}
	err := h.checkWorkerOnline(ctx, agent)
	var offline *ServiceWorkerOfflineError
	if !errors.As(err, &offline) {
		t.Fatalf("err = %v, want ServiceWorkerOfflineError", err)
	}
	if !offline.NoWorkers {
		t.Fatal("NoWorkers should be true when no instance is online")
	}

	// 有 worker 但不服务这个应用。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		presenceInstance("w1", 441, map[string]any{"agents": []string{"other"}}),
	}}}
	err = h.checkWorkerOnline(ctx, agent)
	if !errors.As(err, &offline) || offline.NoWorkers {
		t.Fatalf("err = %v, want offline-but-workers-exist", err)
	}

	// 服务这个应用：放行。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		presenceInstance("w1", 441, map[string]any{"agents": []string{"my-agent"}}),
	}}}
	if err := h.checkWorkerOnline(ctx, agent); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	// 存储报错 -> fail open（宁可等，不可误拦）。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{err: errors.New("db down")}}
	if err := h.checkWorkerOnline(ctx, agent); err != nil {
		t.Fatalf("err = %v, want fail-open nil", err)
	}

	// 存储没实现该能力 -> 跳过预检（老替身仍可用）。
	h = &ServiceAgentsMCPHandler{store: (*noPresenceStore)(nil)}
	if err := h.checkWorkerOnline(ctx, agent); err != nil {
		t.Fatalf("err = %v, want nil without capability", err)
	}
}

// 预检必须按账号过滤：claim 用的是 per-user 凭据，别人的 worker 接不到这一单。
//
// 不过滤的真实后果（2026-10-26 生产）：平台上任何一个人开着 worker，别人的应用
// 就被判成「在线」→ run 停在 submitted 永远没人领，调用方也等不到 worker_offline。
func TestCheckWorkerOnlineIgnoresOtherOwners(t *testing.T) {
	ctx := context.Background()

	// uid 441 的应用；在线的是 uid 442 的 worker（声明 serve_all）。
	h := &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		presenceInstance("other-worker", 442, map[string]any{"serve_all": true}),
	}}}
	err := h.checkWorkerOnline(ctx, agentOwnedBy("my-agent", 441))
	var offline *ServiceWorkerOfflineError
	if !errors.As(err, &offline) {
		t.Fatalf("err = %v, want offline for foreign worker", err)
	}
	// 别人的 worker 不能算「你这边有 worker」——要说清是「没有任何 worker 在线」，
	// 而不是「有 worker 但不服务这个应用」（后者会让用户去改 -agents，白折腾）。
	if !offline.NoWorkers {
		t.Fatal("foreign worker must not count as the caller's own worker")
	}

	// 同一个 uid 的 worker 在线就放行。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		presenceInstance("other-worker", 442, map[string]any{"serve_all": true}),
		presenceInstance("my-worker", 441, map[string]any{"serve_all": true}),
	}}}
	if err := h.checkWorkerOnline(ctx, agentOwnedBy("my-agent", 441)); err != nil {
		t.Fatalf("err = %v, want nil with own worker online", err)
	}

	// 平台内部应用（owner=0）只看内部 worker：用户自己的 worker 不算它的执行环境。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		presenceInstance("user-worker", 441, map[string]any{"serve_all": true}),
	}}}
	err = h.checkWorkerOnline(ctx, agentOwnedBy("internal-agent", 0))
	if !errors.As(err, &offline) || !offline.NoWorkers {
		t.Fatalf("err = %v, want offline for internal agent", err)
	}

	// 历史行 owner_uid=0（加列前的旧数据）按内部 worker 算，域名内的应用照旧能用。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		{InstanceID: "legacy", Metrics: json.RawMessage(`{"serve_all":true}`)},
	}}}
	if err := h.checkWorkerOnline(ctx, agentOwnedBy("internal-agent", 0)); err != nil {
		t.Fatalf("err = %v, want nil for legacy internal worker", err)
	}
}

type noPresenceStore struct{ store.ServiceAgentStore }

// 面板已经不再展示任何启动命令/密钥（桌面端内嵌 worker 自己接单），
// 所以离线提示必须指向「在要跑这个应用的那台机器上登录小八」，
// 不能再让用户去面板找命令——他会找不到。
//
// 也不要再指向「改成平台直连 MCP/HTTP」：那个选项已经从面板撤掉了。
func TestServiceWorkerOfflineToolErrorPointsAtDesktopLogin(t *testing.T) {
	for _, noWorkers := range []bool{true, false} {
		msg := serviceWorkerOfflineToolError(&ServiceWorkerOfflineError{Slug: "my-agent", NoWorkers: noWorkers})
		if !strings.HasPrefix(msg, "worker_offline: ") {
			t.Fatalf("message must keep the worker_offline prefix, got %q", msg)
		}
		if !strings.Contains(msg, "小八") || !strings.Contains(msg, "登录") {
			t.Fatalf("message should tell the user to log in on that machine, got %q", msg)
		}
		// 云员工/云服务器用户也在用同一个应用，不能只写「电脑」。
		if !strings.Contains(msg, "云服务器") {
			t.Fatalf("message should not assume the worker runs on a personal computer, got %q", msg)
		}
		for _, stale := range []string{"命令见", "面板的发布结果", "service-run-worker", "cwk_", "token",
			"MCP/HTTP", "执行位置"} {
			if strings.Contains(msg, stale) {
				t.Fatalf("message still references %q (panel no longer offers it): %q", stale, msg)
			}
		}
	}
}

func TestIsWorkerRunner(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		def  string
		want bool
	}{
		{"worker", `{"runner":{"type":"worker","command":"echo hi"}}`, true},
		{"大写也能认", `{"runner":{"type":"Worker"}}`, true},
		{"http", `{"runner":{"type":"http","url":"https://x"}}`, false},
		{"mcp", `{"runner":{"type":"mcp","url":"https://x"}}`, false},
		{"默认 codex", `{}`, false},
		{"非法 json", `{oops`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &ServiceAgentsMCPHandler{store: &fakePresenceStore{definition: json.RawMessage(c.def)}}
			if got := h.isWorkerRunner(ctx, store.ServiceAgentDescriptor{ID: 1, Version: 1}); got != c.want {
				t.Fatalf("isWorkerRunner = %v, want %v", got, c.want)
			}
		})
	}
}

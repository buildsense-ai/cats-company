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

func presenceInstance(id string, metrics map[string]any) store.ServiceInstancePresence {
	raw, _ := json.Marshal(metrics)
	return store.ServiceInstancePresence{InstanceID: id, Metrics: raw}
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
			presenceInstance("w1", map[string]any{"agents": []string{"a", "b"}}),
		}, "a", true},
		{"实例只服务别的 slug", []store.ServiceInstancePresence{
			presenceInstance("w1", map[string]any{"agents": []string{"c"}}),
		}, "a", false},
		{"serve_all 兜住一切", []store.ServiceInstancePresence{
			presenceInstance("w1", map[string]any{"serve_all": true}),
		}, "anything", true},
		{"多实例取并集", []store.ServiceInstancePresence{
			presenceInstance("w1", map[string]any{"agents": []string{"c"}}),
			presenceInstance("w2", map[string]any{"agents": []string{"a"}}),
		}, "a", true},
		{"metrics 非法当没有服务范围", []store.ServiceInstancePresence{
			{InstanceID: "w1", Metrics: json.RawMessage(`not-json`)},
		}, "a", true},
		// 升级兼容：旧版 worker 心跳只有 os/arch/pid，范围未知 → 放行，
		// 否则升级平台会把还在干活的旧 worker 全判成离线。
		{"旧版 worker 心跳（无 agents 字段）", []store.ServiceInstancePresence{
			presenceInstance("w1", map[string]any{"worker": "service-run-worker", "os": "linux", "pid": 7}),
		}, "anything", true},
		{"空 metrics 也算未知→放行", []store.ServiceInstancePresence{
			{InstanceID: "w1"},
		}, "a", true},
		{"新版声明只服务别的应用时才拦", []store.ServiceInstancePresence{
			presenceInstance("w1", map[string]any{"agents": []string{"c"}, "serve_all": false}),
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

	// 一个 worker 都没在线：明确报「本机没 worker」。
	h := &ServiceAgentsMCPHandler{store: &fakePresenceStore{}}
	err := h.checkWorkerOnline(ctx, "my-agent")
	var offline *ServiceWorkerOfflineError
	if !errors.As(err, &offline) {
		t.Fatalf("err = %v, want ServiceWorkerOfflineError", err)
	}
	if !offline.NoWorkers {
		t.Fatal("NoWorkers should be true when no instance is online")
	}

	// 有 worker 但不服务这个应用。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		presenceInstance("w1", map[string]any{"agents": []string{"other"}}),
	}}}
	err = h.checkWorkerOnline(ctx, "my-agent")
	if !errors.As(err, &offline) || offline.NoWorkers {
		t.Fatalf("err = %v, want offline-but-workers-exist", err)
	}

	// 服务这个应用：放行。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{instances: []store.ServiceInstancePresence{
		presenceInstance("w1", map[string]any{"agents": []string{"my-agent"}}),
	}}}
	if err := h.checkWorkerOnline(ctx, "my-agent"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	// 存储报错 -> fail open（宁可等，不可误拦）。
	h = &ServiceAgentsMCPHandler{store: &fakePresenceStore{err: errors.New("db down")}}
	if err := h.checkWorkerOnline(ctx, "my-agent"); err != nil {
		t.Fatalf("err = %v, want fail-open nil", err)
	}

	// 存储没实现该能力 -> 跳过预检（老替身仍可用）。
	h = &ServiceAgentsMCPHandler{store: (*noPresenceStore)(nil)}
	if err := h.checkWorkerOnline(ctx, "my-agent"); err != nil {
		t.Fatalf("err = %v, want nil without capability", err)
	}
}

type noPresenceStore struct{ store.ServiceAgentStore }

// 面板已经不再展示任何启动命令/密钥（桌面端内嵌 worker 自己接单），
// 所以离线提示必须指向「在要跑这个应用的那台电脑上登录小八」，
// 不能再让用户去面板找命令——他会找不到。
func TestServiceWorkerOfflineToolErrorPointsAtDesktopLogin(t *testing.T) {
	for _, noWorkers := range []bool{true, false} {
		msg := serviceWorkerOfflineToolError(&ServiceWorkerOfflineError{Slug: "my-agent", NoWorkers: noWorkers})
		if !strings.HasPrefix(msg, "worker_offline: ") {
			t.Fatalf("message must keep the worker_offline prefix, got %q", msg)
		}
		if !strings.Contains(msg, "小八") || !strings.Contains(msg, "登录") {
			t.Fatalf("message should tell the user to log in on that computer, got %q", msg)
		}
		for _, stale := range []string{"命令见", "面板的发布结果", "service-run-worker", "cwk_", "token"} {
			if strings.Contains(msg, stale) {
				t.Fatalf("message still references %q (panel no longer shows it): %q", stale, msg)
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

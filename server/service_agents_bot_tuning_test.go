package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 「云端下发 + 实时生效」的验证。
//
// 用户要的是：调参不改代码、不发客户端、不重启 —— 改一次配置，下一次调用就生效。
// 这里把优先级每一层单独钉住，并证明「已连接的会话也会用新值」。

// 三层优先级：内置 → 平台 env → definition.runner。
func TestBotRunnerTuningPrecedence(t *testing.T) {
	defaults := builtinBotRunnerTuning()
	if defaults.Timeout != defaultBotRunnerTimeout ||
		defaults.Settle != defaultBotRunnerSettle ||
		defaults.Poll != defaultBotRunnerPollInterval ||
		defaults.AmbiguityGrace != botReplyAmbiguousGrace {
		t.Fatalf("内置默认应等于历史实现: %+v", defaults)
	}

	// 没有任何配置：等于历史行为（不能因为引入调参就改了默认）。
	if got := botRunnerTuningFor(nil); got != defaults {
		t.Fatalf("空 definition 应回内置默认: got %+v want %+v", got, defaults)
	}

	// 平台 env（全站基调）。
	t.Setenv(envBotRunnerTimeoutSeconds, "600")
	t.Setenv(envBotRunnerSettleSeconds, "5")
	t.Setenv(envBotRunnerPollMillis, "800")
	platform := botRunnerTuningFor(nil)
	if platform.Timeout != 600*time.Second || platform.Settle != 5*time.Second || platform.Poll != 800*time.Millisecond {
		t.Fatalf("平台 env 未生效: %+v", platform)
	}

	// definition.runner（单应用）压过平台 env。
	def := json.RawMessage(`{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":45,"settle_seconds":1,"poll_interval_ms":100,"ambiguity_grace_seconds":3}}`)
	app := botRunnerTuningFor(def)
	if app.Timeout != 45*time.Second {
		t.Fatalf("definition 应压过 env：timeout=%v", app.Timeout)
	}
	if app.Settle != time.Second || app.Poll != 100*time.Millisecond || app.AmbiguityGrace != 3*time.Second {
		t.Fatalf("definition 其它字段未生效: %+v", app)
	}
}

// 手滑的配置不能把平台变成「永不超时」或「立刻超时」。
func TestBotRunnerTuningClampsExternalInput(t *testing.T) {
	cases := []struct {
		name string
		def  string
		want time.Duration
	}{
		{"0 秒会被夹到下限", `{"runner":{"type":"bot","bot_uid":1,"timeout_seconds":0}}`, minBotRunnerTimeout},
		{"负数夹到下限", `{"runner":{"type":"bot","bot_uid":1,"timeout_seconds":-99}}`, minBotRunnerTimeout},
		{"一天夹到上限", `{"runner":{"type":"bot","bot_uid":1,"timeout_seconds":86400}}`, maxBotRunnerTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := botRunnerTuningFor(json.RawMessage(tc.def))
			if got.Timeout != tc.want {
				t.Fatalf("timeout = %v, want %v", got.Timeout, tc.want)
			}
		})
	}

	// 静默窗口/歧义窗口不能长过整单预算，否则永远不会触发（等于没配）。
	tight := botRunnerTuningFor(json.RawMessage(
		`{"runner":{"type":"bot","bot_uid":1,"timeout_seconds":10,"settle_seconds":600,"ambiguity_grace_seconds":600}}`))
	if tight.Settle > tight.Timeout {
		t.Fatalf("settle %v 不能超过 timeout %v", tight.Settle, tight.Timeout)
	}
	if tight.AmbiguityGrace > tight.Timeout {
		t.Fatalf("ambiguityGrace %v 不能超过 timeout %v", tight.AmbiguityGrace, tight.Timeout)
	}

	// settle=0 会把长回复在切段处默默截断（bot 按 4000 字切），必须有下限。
	zeroSettle := botRunnerTuningFor(json.RawMessage(
		`{"runner":{"type":"bot","bot_uid":1,"settle_seconds":0}}`))
	if zeroSettle.Settle < minBotRunnerSettle {
		t.Fatalf("settle=%v 太小，会把分段的回复截断", zeroSettle.Settle)
	}
}

// 坏配置不能让功能变成「不可用」：解析不了就按平台默认走。
func TestBotRunnerTuningToleratesBadDefinition(t *testing.T) {
	for _, raw := range []string{``, `null`, `{}`, `[]`, `not json`, `{"runner":null}`, `{"runner":{"type":"bot"}}`} {
		got := botRunnerTuningFor(json.RawMessage(raw))
		if got.Timeout <= 0 || got.Poll <= 0 {
			t.Fatalf("definition=%q 应退化成平台默认，实际 %+v", raw, got)
		}
	}
}

// **热更新**：已连接的 MCP 会话把 definition 快照进了工具闭包，所以派活时
// 必须重读一次，否则「改了配置要重开会话才生效」。这个测试钉住那次重读。
func TestBotRunnerReloadsDefinitionPerDispatch(t *testing.T) {
	db := newFakeBotStore()
	// 投递故意失败：run 立刻收尾，不会留一个 45s 的轮询 goroutine。
	hub := &fakeBotHub{online: true, deliverErr: true}
	hot := &hotReloadBotStore{fakeBotStore: db}
	// 不用 newTestBotRunner：它会覆盖 timeout，把「用哪套参数」这个观测口搅浑。
	runner := &ServiceBotRunner{store: hot, hub: hub, inFlight: map[string]int{}}

	// 会话快照里的参数是 300s（假想「老的、很宽的配置」）。
	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":300}}`)

	// 第一次派活：云端配置已改成 45s。
	hot.set(`{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":45}}`)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_hot1", SessionID: "sess_h1", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
	if !waitFor(t, func() bool { return db.startCalls >= 1 }, 2*time.Second) {
		t.Fatal("应该已经开始")
	}
	first := db.leaseFor(0)

	// 再改一次配置：30s。同一个 agent 快照（模拟「客户端没重开会话」）。
	hot.set(`{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":30}}`)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_hot2", SessionID: "sess_h2", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
	if !waitFor(t, func() bool { return db.startCalls >= 2 }, 2*time.Second) {
		t.Fatal("第二单应该已经开始")
	}
	second := db.leaseFor(1)

	// 租约 = 超时 + 60s，所以能反推出这次用的是哪个超时。
	if first != 45+60 {
		t.Fatalf("第一次派活应使用云端最新的 45s（租约 %d，want %d）", first, 105)
	}
	if second != 30+60 {
		t.Fatalf("改配置后第二次派活应立刻使用 30s（租约 %d，want %d）—— 还要重开会话才生效就不叫实时", second, 90)
	}
}

// 热更新读失败不能阻断派活：退回会话快照，照常干活。
func TestBotRunnerFallsBackWhenReloadFails(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true, deliverErr: true}
	hot := &hotReloadBotStore{fakeBotStore: db, err: context.DeadlineExceeded}
	runner := &ServiceBotRunner{store: hot, hub: hub, inFlight: map[string]int{}}

	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":120}}`)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_fb", SessionID: "sess_fb", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
	if !waitFor(t, func() bool { return db.startCalls >= 1 }, 2*time.Second) {
		t.Fatal("读不到最新配置也该照常派活")
	}
	// 用快照里的 120s。
	if lease := db.leaseFor(0); lease != 120+60 {
		t.Fatalf("应退回会话快照的 120s（租约 %d，want %d）", lease, 180)
	}
}

// hotReloadBotStore 让 fakeBotStore 具备「按 agent 取最新 definition」的能力。
type hotReloadBotStore struct {
	*fakeBotStore
	writeMu sync.Mutex
	def     json.RawMessage
	err     error
	// block 非空时，读会等到它被关闭才返回 —— 用来验证派活不等这次读。
	block chan struct{}
}

func (h *hotReloadBotStore) set(def string) {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	h.def = json.RawMessage(def)
}

func (h *hotReloadBotStore) GetServiceAgentDefinition(_ context.Context, _ int64, _ int64) (string, int, json.RawMessage, error) {
	h.writeMu.Lock()
	block := h.block
	err := h.err
	def := append(json.RawMessage(nil), h.def...)
	h.writeMu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return "", 0, nil, err
	}
	return "hot", 0, def, nil
}

// **零阻塞承诺**：热更新不能把「立刻拿到运行句柄」拖成「最多慢 2 秒」。
//
// 所以 MaybeDispatch 里不许有 DB 读；重读必须发生在后台。这个测试让那次读
// 一直阻塞，然后断言 MaybeDispatch 仍然立刻返回。
func TestMaybeDispatchNeverBlocksOnDefinitionRead(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true, deliverErr: true}
	block := make(chan struct{})
	hot := &hotReloadBotStore{fakeBotStore: db, block: block}
	runner := &ServiceBotRunner{store: hot, hub: hub, inFlight: map[string]int{}}

	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)
	start := time.Now()
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_noblock", SessionID: "sess_nb", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("MaybeDispatch 等了 %v —— 它在请求路径上，任何 DB 读都属于回归", elapsed)
	}

	// 放开读，确认后台还是继续跑完了（不是把任务丢了）。
	close(block)
	if !waitFor(t, func() bool { return db.startCalls >= 1 }, 2*time.Second) {
		t.Fatal("放开读之后后台应继续派活")
	}
}

// 改 `bot_uid` 也要生效：用户改了配置却发现不生效，才是真隐患。
// 但只在「最新配置仍然是 bot runner」时才换 —— 不能跑到一半把 runner 类型改掉。
func TestBotRunnerHotUpdatesTargetBot(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true, deliverErr: true}
	hot := &hotReloadBotStore{fakeBotStore: db}
	runner := &ServiceBotRunner{store: hot, hub: hub, inFlight: map[string]int{}}

	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)

	// 云端把执行者改成 63。
	hot.set(`{"runner":{"type":"bot","bot_uid":63}}`)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_swap", SessionID: "sess_sw", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
	if !waitFor(t, func() bool { return db.startCalls >= 1 }, 2*time.Second) {
		t.Fatal("应该已经开始")
	}
	db.mu.Lock()
	instance := db.startedRun[0]
	db.mu.Unlock()
	if instance != "bot:63" {
		t.Fatalf("执行者应为最新配置的 bot:63，实际 %q", instance)
	}

	// 最新配置把类型改掉了（不再是 bot runner）：保留会话快照的 bot，
	// 因为 invoke 已经按 bot 返回了句柄，不能中途改类型。
	db2 := newFakeBotStore()
	hot2 := &hotReloadBotStore{fakeBotStore: db2}
	runner2 := &ServiceBotRunner{store: hot2, hub: &fakeBotHub{online: true, deliverErr: true}, inFlight: map[string]int{}}
	hot2.set(`{"runner":{"type":"worker"}}`)
	runner2.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_type", SessionID: "sess_ty", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
	if !waitFor(t, func() bool { return db2.startCalls >= 1 }, 2*time.Second) {
		t.Fatal("应该已经开始")
	}
	db2.mu.Lock()
	instance2 := db2.startedRun[0]
	db2.mu.Unlock()
	if instance2 != "bot:982" {
		t.Fatalf("runner 类型变了就应保留快照的 bot:982，实际 %q", instance2)
	}
}

package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// —— 小时级长任务 ——
//
// 视频渲染这类活是小时级的。两件事必须同时成立：
//   1. 预算能配到小时级（原来上限 30 分钟，长任务必然 bot_timeout）；
//   2. 租约**短 + 续租**（否则进程崩了，这条 run 要挂到预算到期才判死）。

func TestBotRunnerTimeoutAllowsHourLongTasks(t *testing.T) {
	got := botRunnerTuningFor(json.RawMessage(`{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":7200}}`))
	if got.Timeout != 2*time.Hour {
		t.Fatalf("timeout = %v，2 小时的预算应该被接受", got.Timeout)
	}
	clamped := botRunnerTuningFor(json.RawMessage(`{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":86400}}`))
	if clamped.Timeout != maxBotRunnerTimeout {
		t.Fatalf("timeout = %v，超出上限仍要夹住", clamped.Timeout)
	}
	if maxBotRunnerTimeout < time.Hour {
		t.Fatalf("上限 %v 太小：小时级任务会被必然判超时", maxBotRunnerTimeout)
	}
}

// renewCountingStore 记录续租次数（真实适配器有 RenewServiceLease，测试 fake 没有）。
type renewCountingStore struct {
	*fakeBotStore
	mu     sync.Mutex
	renews int
}

func (s *renewCountingStore) RenewServiceLease(_ context.Context, _ string, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renews++
	return true, nil
}

func (s *renewCountingStore) renewCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renews
}

func TestBotRunnerRenewsLeaseWhileWaiting(t *testing.T) {
	st := newFakeBotStore()
	st.owner = 38
	counting := &renewCountingStore{fakeBotStore: st}
	runner := newTestBotRunner(counting, &fakeBotHub{online: true})
	// 测试里把续租压到 10ms（真实是 60s）。
	runner.leaseRenew = 10 * time.Millisecond

	go runner.execute(botRunRequest{
		RunID: "run_long", SessionID: "sess_long", Slug: "jack",
		BotUID: 982, AgentID: 1, CallerUID: 38,
		Input:              json.RawMessage(`{"text":"渲染一段 5 分钟视频"}`),
		SnapshotDefinition: json.RawMessage(`{"runner":{"type":"bot","bot_uid":982,"timeout_seconds":300}}`),
	})

	if !waitFor(t, func() bool { return counting.renewCount() > 0 }, 2*time.Second) {
		t.Fatal("等待期间没有续租：长任务会被 reaper 当成执行方没了")
	}
	// 预算 300s 但租约必须短：崩了才收得掉。
	if lease := st.lastLease(); lease > botRunLeaseSeconds {
		t.Fatalf("lease = %d 秒，应该被压到 %d 秒以内（靠续租保活）", lease, botRunLeaseSeconds)
	}
}

// 没有续租能力的存储（老 fake / 裁剪部署）：不能因为缺能力就不干活。
func TestBotRunnerWorksWithoutLeaseRenewer(t *testing.T) {
	st := newFakeBotStore()
	st.owner = 38
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(st, hub)

	go runner.execute(botRunRequest{
		RunID: "run_norenew", SessionID: "sess_norenew", Slug: "jack",
		BotUID: 982, AgentID: 1, CallerUID: 38,
		Input:              json.RawMessage(`{"text":"hi"}`),
		SnapshotDefinition: json.RawMessage(`{"runner":{"type":"bot","bot_uid":982}}`),
	})

	// 没有续租能力时应该照常派发（走到「等回复」那步：投递已发生）。
	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatal("缺续租能力时不该影响派发")
	}
	_ = store.ErrServiceRunNotFound // 保持 store 包被引用（断言语义在别处）
}

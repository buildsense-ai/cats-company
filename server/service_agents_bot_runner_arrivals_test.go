package server

import (
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// countingBotStore 数 awaitReply 真的查了几次库 —— 这是本次改动的核心指标：
// 事件驱动的实现里，一次长等待不该是「每 500ms 一次」。
type countingBotStore struct {
	*fakeBotStore
	mu    sync.Mutex
	reads int
}

func (s *countingBotStore) GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.fakeBotStore.GetMessagesSince(topicID, sinceID, limit)
}

func (s *countingBotStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func TestReplyWaitBacksOffToCeiling(t *testing.T) {
	tuning := botRunnerTuning{Poll: 500 * time.Millisecond}
	cases := []struct {
		idle time.Duration
		want time.Duration
	}{
		{0, 500 * time.Millisecond},
		{500 * time.Millisecond, time.Second},
		{2 * time.Second, 4 * time.Second},
		{time.Hour, botRunnerReplyPollCeiling},
	}
	for _, tc := range cases {
		if got := botRunnerReplyWait(tuning, tc.idle); got != tc.want {
			t.Fatalf("idle=%s: wait=%s, want %s", tc.idle, got, tc.want)
		}
	}
	// 显式配置比上限还大时尊重配置：那是刻意的选择，不是手滑。
	slow := botRunnerTuning{Poll: 30 * time.Second}
	if got := botRunnerReplyWait(slow, time.Hour); got != 30*time.Second {
		t.Fatalf("explicit poll must win over the ceiling, got %s", got)
	}
	// 零值不能变成「忙等」。
	if got := botRunnerReplyWait(botRunnerTuning{}, 0); got != defaultBotRunnerPollInterval {
		t.Fatalf("zero poll must fall back to the default, got %s", got)
	}
}

func TestTopicArrivalNotifierFanoutAndCancel(t *testing.T) {
	n := newTopicArrivalNotifier()
	first, cancelFirst := n.Subscribe("p2p_1_2")
	second, cancelSecond := n.Subscribe("p2p_1_2")
	other, cancelOther := n.Subscribe("p2p_1_3")
	defer cancelOther()

	n.Notify("p2p_1_2")
	for name, ch := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s subscriber was not notified", name)
		}
	}
	select {
	case <-other:
		t.Fatal("a different topic must not be woken up")
	case <-time.After(20 * time.Millisecond):
	}

	// 重复通知不能阻塞：channel 只有 1 个缓冲，第二条挤掉即可。
	n.Notify("p2p_1_2")
	n.Notify("p2p_1_2")
	// 把残留的那条消费掉：下面断言的是「取消之后还会不会再来」，不是缓冲区。
	<-first
	<-second

	cancelFirst()
	cancelFirst() // 幂等
	if got := n.waiterCount("p2p_1_2"); got != 1 {
		t.Fatalf("waiters=%d after one cancel, want 1", got)
	}
	n.Notify("p2p_1_2")
	select {
	case <-second:
	default:
		t.Fatal("the remaining subscriber must still be notified")
	}
	select {
	case <-first:
		t.Fatal("a cancelled subscriber must not be notified")
	case <-time.After(20 * time.Millisecond):
	}

	cancelSecond()
	if got := n.waiterCount("p2p_1_2"); got != 0 {
		t.Fatalf("waiters=%d after all cancels, want 0", got)
	}
}

func TestSaveNormalizedMessageNotifiesArrival(t *testing.T) {
	arrivals, cancel := SubscribeMessageArrivals("p2p_7_982")
	defer cancel()

	store := &metadataMessageStore{idempotentMessageStore: idempotentMessageStore{id: 11}}
	payload := &normalizedMessagePayload{StoredContent: "好了", StoredType: "text"}
	if _, err := saveNormalizedMessage(store, "p2p_7_982", 982, 0, payload); err != nil {
		t.Fatalf("save: %v", err)
	}
	select {
	case <-arrivals:
	case <-time.After(time.Second):
		t.Fatal("写入一条新消息后必须唤醒同一 topic 上的等待方")
	}

	// 幂等重发命中（duplicate）没有产生新内容，不该唤醒。
	dup := &metadataMessageStore{idempotentMessageStore: idempotentMessageStore{id: 11, duplicate: true}}
	if _, err := saveNormalizedMessage(dup, "p2p_7_982", 982, 0, payload); err != nil {
		t.Fatalf("save duplicate: %v", err)
	}
	select {
	case <-arrivals:
		t.Fatal("重复消息不该唤醒等待方（只会换来一次空查询）")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestAwaitReplyWakesOnArrivalSignal 是这次改动的关键回归：把安全网轮询压到
// 10s（远大于断言窗口），唯一能让它及时返回的就是「落库即通知」。
func TestAwaitReplyWakesOnArrivalSignal(t *testing.T) {
	base := &fakeBotStore{}
	db := &countingBotStore{fakeBotStore: base}
	runner := newTestBotRunner(db, &fakeBotHub{})
	runner.poll = 10 * time.Second
	runner.timeout = 3 * time.Second
	runner.settle = 10 * time.Millisecond
	topic := "p2p_7_982"

	go func() {
		time.Sleep(100 * time.Millisecond)
		base.pushBotReply(topic, 982, "统计好了：3 个进程")
		NotifyMessageArrived(topic)
	}()

	started := time.Now()
	// promptID=0：fake store 的第一条消息 id 就是 1，用 1 当 sinceID 会把它跳过。
	reply, attachments, err := runner.awaitReply(topic, 0, 982, "", runner.tuningFor(nil))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if reply != "统计好了：3 个进程" || len(attachments) != 0 {
		t.Fatalf("reply=%q attachments=%d", reply, len(attachments))
	}
	if elapsed > time.Second {
		t.Fatalf("通知应当立刻唤醒，实际等了 %s（安全网是 10s，说明走的是轮询）", elapsed)
	}
	if reads := db.readCount(); reads > 3 {
		t.Fatalf("读了 %d 次消息表，事件驱动下应该只在收到通知后补读一次", reads)
	}
}

// 通知没送到时（多实例、别的进程直接写库）必须还能靠安全网拿到回复。
func TestAwaitReplyFallsBackToPolling(t *testing.T) {
	base := &fakeBotStore{}
	runner := newTestBotRunner(base, &fakeBotHub{})
	runner.poll = 10 * time.Millisecond
	runner.timeout = 3 * time.Second
	runner.settle = 10 * time.Millisecond
	topic := "p2p_7_983"

	go func() {
		time.Sleep(80 * time.Millisecond)
		base.pushBotReply(topic, 983, "收到")
	}()

	reply, _, err := runner.awaitReply(topic, 0, 983, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if reply != "收到" {
		t.Fatalf("reply=%q", reply)
	}
}

// 退订必须干净：topic 上最后一个等的人走了，表里不能留键。
func TestAwaitReplyUnsubscribes(t *testing.T) {
	base := &fakeBotStore{}
	runner := newTestBotRunner(base, &fakeBotHub{})
	runner.poll = 10 * time.Millisecond
	runner.timeout = 30 * time.Millisecond
	topic := "p2p_7_984"

	if _, _, err := runner.awaitReply(topic, 0, 984, "", runner.tuningFor(nil)); err == nil {
		t.Fatal("expected bot_timeout")
	}
	if got := defaultTopicArrivals.waiterCount(topic); got != 0 {
		t.Fatalf("waiters=%d after awaitReply returned, want 0", got)
	}
}

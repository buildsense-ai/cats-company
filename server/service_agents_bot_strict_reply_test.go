package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// 会回填 reply_to 的 bot，这次没回填：不认。
//
// 这就是内测里看到的形状：prompt 之后 1 秒，目标 bot 冒出一句「回环信息，没有新任务」
// （会话上下文被上一轮互刷带偏），然后才去执行真任务。旧逻辑按「没人抢就认领」
// 把它当成答案交付了。
func TestAwaitReplyRejectsUnownedWhenBotAlwaysQuotes(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	// 上一单的真答案带 reply_to —— 这个 bot 会回填，是能力证据
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "上一单：OK", ReplyTo: 4242})
	promptID := platformPrompt(db, topic, caller, "执行 sleep 120; echo done，只回复 done")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text",
		Content: "回环信息，没有新任务，我不再重复执行。"})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err == nil {
		t.Fatalf("无标记的回话被当成答案交付了：reply=%q", reply)
	}
	if !strings.Contains(err.Error(), "一直是回填 reply_to") {
		t.Fatalf("err=%v，想让它指出「会回填却没回填」这个具体成因", err)
	}
}

// 控制组：这个 bot 从来没有回填过 reply_to（老客户端 / 人工代答），
// 就不能要求归属，否则整片调用都会被判死。
func TestAwaitReplyLenientWhenBotNeverQuotes(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const (
		caller = int64(1619)
		bot    = int64(1658)
	)
	topic := "p2p_1658_1619"
	promptID := platformPrompt(db, topic, caller, "统计进程数")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "3 个进程"})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v（不会回填的老客户端必须继续可用）", err)
	}
	if reply != "3 个进程" {
		t.Fatalf("reply=%q", reply)
	}
}

// probeFailingStore 让能力探测失败，其余行为与 fakeBotStore 一致。
type probeFailingStore struct{ *fakeBotStore }

func (s probeFailingStore) GetRecentMessages(string, int) ([]*types.Message, error) {
	return nil, errors.New("probe unavailable")
}

// 能力探测自己失败时按「不会回填」处理（fail-open）：
// 一条辅助查询挂了不该让所有 bot 调用一起挂。
func TestAwaitReplyProbeFailureKeepsLenient(t *testing.T) {
	db := &fakeBotStore{}
	store := probeFailingStore{fakeBotStore: db}
	runner := newTestBotRunner(store, &fakeBotHub{})
	runner.poll = 10 * time.Millisecond
	runner.timeout = 400 * time.Millisecond
	runner.settle = 10 * time.Millisecond
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	promptID := platformPrompt(db, topic, caller, "统计进程数")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "3 个进程"})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v（探测失败要 fail-open）", err)
	}
	if reply != "3 个进程" {
		t.Fatalf("reply=%q", reply)
	}
}

// 会回填的 bot，这次真的回填了：照收。锁住「不许因为严格模式把真答案也挡掉」。
func TestAwaitReplyAcceptsOwnedWhenBotAlwaysQuotes(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "上一单：OK", ReplyTo: 4242})
	promptID := platformPrompt(db, topic, caller, "执行 sleep 5; echo done，只回复 done")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "回环信息，没有新任务"})
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "done", ReplyTo: promptID})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v（带 reply_to 的真答案必须交付）", err)
	}
	if reply != "done" {
		t.Fatalf("reply=%q", reply)
	}
}

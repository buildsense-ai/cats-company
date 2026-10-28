package server

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// turn_done 的三种写法：true / "true" / 会话 id。其余一律不认。
func TestBotRunnerTurnDoneDetection(t *testing.T) {
	cases := []struct {
		name      string
		metadata  map[string]interface{}
		sessionID string
		want      bool
	}{
		{"布尔 true", map[string]interface{}{botTurnDoneMetadataKey: true}, "sess_1", true},
		{"字符串 true", map[string]interface{}{botTurnDoneMetadataKey: "true"}, "sess_1", true},
		{"大写 TRUE", map[string]interface{}{botTurnDoneMetadataKey: "TRUE"}, "sess_1", true},
		{"会话 id 匹配", map[string]interface{}{botTurnDoneMetadataKey: "sess_1"}, "sess_1", true},
		{"会话 id 不匹配（别人的回合）", map[string]interface{}{botTurnDoneMetadataKey: "sess_other"}, "sess_1", false},
		{"布尔 false", map[string]interface{}{botTurnDoneMetadataKey: false}, "sess_1", false},
		{"字符串 false", map[string]interface{}{botTurnDoneMetadataKey: "false"}, "sess_1", false},
		{"空串", map[string]interface{}{botTurnDoneMetadataKey: ""}, "sess_1", false},
		{"没有这个键", map[string]interface{}{"other": "x"}, "sess_1", false},
		{"metadata 为 nil", nil, "sess_1", false},
		{"类型不认识", map[string]interface{}{botTurnDoneMetadataKey: 1}, "sess_1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := &types.Message{Metadata: c.metadata}
			if got := botRunnerTurnDone(msg, c.sessionID); got != c.want {
				t.Fatalf("= %v，期望 %v", got, c.want)
			}
		})
	}
	if botRunnerTurnDone(nil, "sess_1") {
		t.Fatal("nil 消息不该判成回合结束")
	}
}

// pushBotTextMeta 推一条带 metadata 的 bot text 消息。
func pushBotTextMeta(st *fakeBotStore, topic string, botUID int64, replyTo int64, content, mode string, metadata map[string]interface{}) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextID++
	msg := &types.Message{
		ID: st.nextID, TopicID: topic, FromUID: botUID, Content: content,
		MsgType: "text", ReplyTo: replyTo, Mode: mode, Metadata: metadata,
	}
	st.messages = append(st.messages, msg)
}

// 核心用例：bot 说完前言后停顿很久才给正文。有 turn_done 就该立刻拿到完整答案，
// 而不是在 settle（5s）到点时把前言当答案交出去。
//
// 生产实测的就是这个形状：1619 说完「收到，我先让分镜工具出方案」之后 14.5 秒
// 才给出真正的分镜。当时平台在 5 秒后就交付了那句前言。
func TestBotRunnerTurnDoneBeatsSettle(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	// 故意把 settle 设长：证明这次不是靠等出来的，而是靠信号。
	runner.settle = 30 * time.Second

	pushBotTextMeta(st, topic, bot, promptI, "收到，我先让分镜工具出逐镜头方案。", "", nil)
	pushBotTextMeta(st, topic, bot, promptI, "镜头1｜0.0-4.0s 画面：黄昏的窗台。", "",
		map[string]interface{}{botTurnDoneMetadataKey: "sess_1"})

	started := time.Now()
	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if !strings.Contains(got, "收到，我先让分镜工具") || !strings.Contains(got, "镜头1") {
		t.Fatalf("应该把前言和正文都交付：%s", got)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("有回合结束信号时不该等 settle（用了 %s）", elapsed)
	}
}

// 只有信号、没有正文：正文那几条已经收到过时，要正常收尾。
func TestBotRunnerTurnDoneWithoutContentStillEnds(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 30 * time.Second

	pushBotTextMeta(st, topic, bot, promptI, "这是完整的分镜脚本正文。", "", nil)
	// 纯信号：空内容 + turn_done
	pushBotTextMeta(st, topic, bot, promptI, "", "", map[string]interface{}{botTurnDoneMetadataKey: true})

	started := time.Now()
	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if !strings.Contains(got, "完整的分镜脚本正文") {
		t.Fatalf("正文丢了：%s", got)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("纯信号也该立刻收尾")
	}
}

// 不能因为一个信号就把空答案交出去：parts 还是空时不信它，退回 settle。
func TestBotRunnerTurnDoneDoesNotDeliverEmptyAnswer(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 100 * time.Millisecond
	runner.timeout = 600 * time.Millisecond

	// 只有信号，什么都没有
	pushBotTextMeta(st, topic, bot, promptI, "", "", map[string]interface{}{botTurnDoneMetadataKey: true})

	_, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err == nil {
		t.Fatal("没有任何内容时不该当成功交付，应该按超时如实失败")
	}
	if !strings.Contains(err.Error(), "bot_timeout") {
		t.Fatalf("应报超时，实际: %v", err)
	}
}

// 别人的回合结束信号不能把我们这单收尾。
//
// 同一条 topic 上并发两个 run 时，另一个 run 的 bot 回合结束也会落进这条消息流。
// 只按「看到 turn_done 就收尾」会把这单在它还没产出内容时提前结束 —— 收回去的
// 是一句前言，而且看起来一切正常。
func TestBotRunnerIgnoresForeignTurnDone(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 100 * time.Millisecond
	runner.timeout = 400 * time.Millisecond

	// 指向别人的 prompt 且带 turn_done：不该收尾，也不该被当答案。
	pushBotTextMeta(st, topic, bot, promptI-50, "别人那单的正文", "",
		map[string]interface{}{botTurnDoneMetadataKey: true})

	_, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err == nil {
		t.Fatal("别人的回合结束信号不该让本单成功")
	}
	if !strings.Contains(err.Error(), "bot_timeout") && !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("应如实失败，实际: %v", err)
	}
}

// turn_done 里的会话 id 不是本会话时，不能据此提前收尾。
//
// 场景：同一 topic 上并发几单时，bot 可能把 A 单的回合结束信号写在了 B 单的消息上。
// 那样的内容如果归属明确（reply_to 指回我们），内容照收没错；但不能因为那个
// 信号就把我们这单提前结束 —— 后面可能还有正文没到。
func TestBotRunnerIgnoresTurnDoneForOtherSession(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	// settle 给够：如果错误地提前收尾，第二段根本来不及被读到。
	runner.settle = 900 * time.Millisecond
	runner.timeout = 5 * time.Second

	pushBotTextMeta(st, topic, bot, promptI, "第一段（信号指向别的会话）", "",
		map[string]interface{}{botTurnDoneMetadataKey: "sess_other"})
	// 稍后才有第二段。
	go func() {
		time.Sleep(300 * time.Millisecond)
		pushBotTextMeta(st, topic, bot, promptI, "第二段", "",
			map[string]interface{}{botTurnDoneMetadataKey: "sess_1"})
	}()

	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if !strings.Contains(got, "第一段") {
		t.Fatalf("归属明确的内容应收下：%s", got)
	}
	// 关键断言：没有被那个错误 id 的信号提前截断。
	if !strings.Contains(got, "第二段") {
		t.Fatalf("被错误 id 的信号提前收尾了，第二段丢了：%s", got)
	}
}

package server

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// 三态解析：没表态 / 还在干 / 说完了。
//
// 「还在干」这一档是这次修的重点：只有「说完了」信号时，平台仍然会在静默窗口
// 到点时把已经收到的那段话交出去 —— 而生产事故正是这个形状（bot 说
// 「收到，我先让分镜工具出方案」，14.5 秒后才给正文，平台 5 秒后交了前言）。
func TestBotRunnerTurnState(t *testing.T) {
	cases := []struct {
		name      string
		metadata  map[string]interface{}
		sessionID string
		want      botTurnState
	}{
		{"布尔 true = 说完了", map[string]interface{}{botTurnDoneMetadataKey: true}, "sess_1", botTurnFinished},
		{"布尔 false = 还在干", map[string]interface{}{botTurnDoneMetadataKey: false}, "sess_1", botTurnInProgress},
		{"字符串 true", map[string]interface{}{botTurnDoneMetadataKey: "true"}, "sess_1", botTurnFinished},
		{"字符串 false", map[string]interface{}{botTurnDoneMetadataKey: "false"}, "sess_1", botTurnInProgress},
		{"progress", map[string]interface{}{botTurnDoneMetadataKey: "progress"}, "sess_1", botTurnInProgress},
		{"working", map[string]interface{}{botTurnDoneMetadataKey: "working"}, "sess_1", botTurnInProgress},
		{"running", map[string]interface{}{botTurnDoneMetadataKey: "RUNNING"}, "sess_1", botTurnInProgress},
		{"本会话 id", map[string]interface{}{botTurnDoneMetadataKey: "sess_1"}, "sess_1", botTurnFinished},
		{"别人的会话 id", map[string]interface{}{botTurnDoneMetadataKey: "sess_other"}, "sess_1", botTurnUnspecified},
		{"空串不表态", map[string]interface{}{botTurnDoneMetadataKey: ""}, "sess_1", botTurnUnspecified},
		{"没有这个键", map[string]interface{}{"other": 1}, "sess_1", botTurnUnspecified},
		{"metadata 为 nil", nil, "sess_1", botTurnUnspecified},
		{"类型不认识", map[string]interface{}{botTurnDoneMetadataKey: 1}, "sess_1", botTurnUnspecified},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := botRunnerTurnState(&types.Message{Metadata: c.metadata}, c.sessionID)
			if got != c.want {
				t.Fatalf("= %v，期望 %v", got, c.want)
			}
		})
	}
	if botRunnerTurnState(nil, "sess_1") != botTurnUnspecified {
		t.Fatal("nil 消息不该表态")
	}
}

// 核心：bot 先说「我先去做」并明确表态「还在干」，隔一段（远大于 settle）才给正文。
// 平台必须等正文，不能把那句前言当成最终答案交出去。
//
// 这就是生产事故的形状（14.5 秒停顿、settle 5 秒），只是把时间压缩了。
func TestBotRunnerWaitsWhenBotSaysStillWorking(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 50 * time.Millisecond
	// newTestBotRunner 默认 timeout=300ms，比这里要等的 400ms 还短，得放宽。
	runner.timeout = 5 * time.Second

	// 过程话：明确说「还在干」。
	pushBotTextMeta(st, topic, bot, promptI, "收到，我先让分镜工具出方案。", "",
		map[string]interface{}{botTurnDoneMetadataKey: false})
	// 正文晚到（远大于 settle 的静默窗口）。
	go func() {
		time.Sleep(400 * time.Millisecond)
		pushBotTextMeta(st, topic, bot, promptI, "镜头1｜0.0-4.0s 黄昏的窗台。", "",
			map[string]interface{}{botTurnDoneMetadataKey: "sess_1"})
	}()

	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if !strings.Contains(got, "镜头1") {
		t.Fatalf("正文被丢了，交付的是过程话：%q", got)
	}
	if !strings.Contains(got, "我先让分镜工具出方案") {
		t.Fatalf("过程话也该保留：%q", got)
	}
}

// 反向对照：同样形状但**没有**「还在干」表态（老客户端）→ 仍然按 settle 收尾，
// 交付过程话。这条钉住「向后兼容」：老 bot 的行为一个字都不变。
func TestBotRunnerStillSettlesWithoutProgressSignal(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 50 * time.Millisecond

	pushBotTextMeta(st, topic, bot, promptI, "收到，我先让分镜工具出方案。", "", nil)
	go func() {
		time.Sleep(400 * time.Millisecond)
		pushBotTextMeta(st, topic, bot, promptI, "镜头1｜0.0-4.0s 黄昏的窗台。", "",
			map[string]interface{}{botTurnDoneMetadataKey: "sess_1"})
	}()

	started := time.Now()
	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if time.Since(started) > 300*time.Millisecond {
		t.Fatalf("老客户端应照旧按 settle 立即收尾，实际等了 %s", time.Since(started))
	}
	if strings.Contains(got, "镜头1") {
		t.Fatalf("老客户端不该等到正文（行为应与改动前一致）：%q", got)
	}
}

// bot 说「还在干」之后就崩了：不能一直挂到整单超时，封顶后要交付已收到的内容。
func TestBotRunnerProgressSilenceIsBounded(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 50 * time.Millisecond
	// 把 45s 的封顶压短，测试才跑得完。
	runner.progressSilence = 300 * time.Millisecond
	runner.timeout = 10 * time.Second

	pushBotTextMeta(st, topic, bot, promptI, "我先算一下，稍等。", "",
		map[string]interface{}{botTurnDoneMetadataKey: "progress"})

	started := time.Now()
	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("封顶之后应交付已有内容而不是报错：%v", err)
	}
	if !strings.Contains(got, "我先算一下") {
		t.Fatalf("应交付已收到的内容：%q", got)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("封顶没生效，等了 %s", elapsed)
	}
}

// 「还在干」的表态必须也过归属检查：别人的过程话不能拖住我们这单。
func TestBotRunnerIgnoresForeignProgressSignal(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 50 * time.Millisecond
	runner.timeout = 600 * time.Millisecond

	// 指向别人的 prompt + 「还在干」：不该拖住本单，也不该被当成本单内容。
	pushBotTextMeta(st, topic, bot, promptI-50, "别人那单我看一下", "",
		map[string]interface{}{botTurnDoneMetadataKey: false})

	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	// 本单没有任何属于自己的内容：如实失败（而不是把别人的过程话交出去）。
	if err == nil {
		t.Fatalf("没有任何属于本单的内容时不该当成功，实际交付：%q", got)
	}
	if strings.Contains(got, "别人那单") {
		t.Fatalf("别人的过程话被交出去了：%q", got)
	}
}

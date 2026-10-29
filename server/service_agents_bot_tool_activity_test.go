package server

import (
	"strings"
	"testing"
	"time"
)

// 老客户端（v1.6.4 及更早）不会发「回合结束」标记，平台只能靠静默窗口猜答案。
// 生产里最常见的失败形状是：
//
//	bot: 「收到，我先执行一下」      ← 静默窗口从这里开始计时
//	bot: 工具调用 / stdout（mode=code）← 平台跳过，但不该当没发生
//	bot: 真正的正文（10 秒后）        ← 那时平台早把前言交出去了
//
// 工具活动是「还在干」的可观测证据：手里已经攒着待交付文本时，见到它就把静默
// 窗口往后推一轮。这条测试钉住这个行为 —— 去掉那段刷新，交付结果就只剩前言。
func TestBotRunnerToolActivityExtendsSettleForOldClients(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 60 * time.Millisecond
	runner.timeout = 10 * time.Second

	pushBotTextTo(st, topic, bot, promptI, "收到，我先在本机跑一遍命令。", "")
	// 工具活动持续的时间远大于 settle：每一步都应该把窗口推后。
	go func() {
		for i := 0; i < 8; i++ {
			time.Sleep(40 * time.Millisecond)
			pushBotTextTo(st, topic, bot, promptI, `{"tool":"bash","input":{"command":"ls"}}`, "code")
		}
		time.Sleep(60 * time.Millisecond)
		pushBotTextTo(st, topic, bot, promptI, "-FINAL- 三件事都在，共 3 行。", "")
	}()

	got, _, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if !strings.Contains(got, "-FINAL-") {
		t.Fatalf("工具还在跑就收尾了，交付的是前言：%q", got)
	}
	if !strings.Contains(got, "收到，我先在本机跑一遍命令") {
		t.Fatalf("前言也不该被丢掉：%q", got)
	}
	if strings.Contains(got, `"tool":"bash"`) {
		t.Fatalf("工具过程展示被当成答案了：%q", got)
	}
}

// 反向对照：只发工具活动、始终没有正文时，不能靠「推窗口」把调用方挂到整单超时。
// 手里没有待交付文本就不推 —— 该报的 bot_timeout 照旧报。
func TestBotRunnerToolActivityAloneStillTimesOut(t *testing.T) {
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

	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(40 * time.Millisecond)
			pushBotTextTo(st, topic, bot, promptI, `{"tool":"bash"}`, "code")
		}
	}()

	started := time.Now()
	got, _, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err == nil {
		t.Fatalf("只有工具活动时不该当成功，实际交付：%q", got)
	}
	if !strings.Contains(err.Error(), "bot_timeout") {
		t.Fatalf("应如实报超时，实际：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("超时判定被拖长了：%s", elapsed)
	}
}

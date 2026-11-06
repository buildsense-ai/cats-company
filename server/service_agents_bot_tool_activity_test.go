package server

import (
	"strings"
	"testing"
	"time"
)

// 老客户端（v1.6.4 及更早）不会发「回合结束」标记，平台只能靠静默窗口猜答案。
// 生产里最常见的失败形状是：
//
//	bot: 「收到，开始跑」              ← 静默窗口从这里开始计时
//	bot: execute_shell（mode=code）    ← 平台跳过，但不该当没发生
//	     …命令跑 20 秒，这期间一个字都没有…
//	bot: 真正的正文（20 秒后）
//
// 宽限必须用「说过还在干」的那一份（progressSilenceLimit），不能用 Settle：
// Settle 是 5s 量级的「说完一句可能还要接着说」，盖不住一条命令的耗时。
// 生产实测（bot 1619，2026-10-29 14:14）：前言 :48、工具调用 :49、`sleep 20`，
// 按 5s 刷新仍在 :54 就把前言交了。
func TestBotRunnerToolActivityHoldsThroughLongCommand(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 50 * time.Millisecond
	// 工具宽限压成 400ms，模拟「工具要跑很久」：远大于 settle，测试又能快速跑完。
	runner.progressSilence = 400 * time.Millisecond
	runner.timeout = 10 * time.Second

	pushBotTextTo(st, topic, bot, promptI, "收到，开始跑。", "")
	pushBotTextTo(st, topic, bot, promptI, `execute_shell`, "code")
	// 工具跑起来之后整整 300ms 没有任何消息 —— 相当于 6 个 settle 窗口。
	// 按 Settle 刷新的话，这里早就收尾了。
	go func() {
		time.Sleep(300 * time.Millisecond)
		pushBotTextTo(st, topic, bot, promptI, "1793254488", "")
	}()

	got, _, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if !strings.Contains(got, "1793254488") {
		t.Fatalf("工具还在跑就收尾了，交付的是前言：%q", got)
	}
	if strings.Contains(got, "execute_shell") {
		t.Fatalf("工具过程展示被当成答案了：%q", got)
	}
}

// 反向对照 1：宽限不能把「工具一直在刷输出」变成无限等待 —— 最后一条工具活动
// 之后仍然只有那一段，到了就交付已收到的内容。
func TestBotRunnerToolActivityGraceIsBounded(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 50 * time.Millisecond
	runner.progressSilence = 300 * time.Millisecond
	runner.timeout = 20 * time.Second

	pushBotTextTo(st, topic, bot, promptI, "我先跑一遍命令。", "")
	pushBotTextTo(st, topic, bot, promptI, `execute_shell`, "code")

	started := time.Now()
	got, _, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("宽限到点应交付已有内容而不是报错：%v", err)
	}
	if !strings.Contains(got, "我先跑一遍命令") {
		t.Fatalf("应交付已收到的内容：%q", got)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("宽限没生效，等了 %s", elapsed)
	}
}

// 反向对照 2：只有工具活动、始终没有正文时，不能因为推宽限就把调用方挂到整单超时。
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

// 纯聊天 bot（没有任何工具活动）的行为必须一个字都不变：settle 到点立刻交付。
// 这条钉住「这次改动不会让普通问答变慢」。
func TestBotRunnerPlainChatStillSettlesImmediately(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})
	runner.settle = 50 * time.Millisecond
	runner.progressSilence = 5 * time.Second // 若被误用在纯聊天上，这个测试会超时
	runner.timeout = 10 * time.Second

	pushBotTextTo(st, topic, bot, promptI, "三件事的清单如下：买牛奶、修灯泡、还书。", "")

	started := time.Now()
	got, _, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("纯聊天不该等到工具宽限，实际等了 %s", elapsed)
	}
	if !strings.Contains(got, "买牛奶") {
		t.Fatalf("交付内容不对：%q", got)
	}
}

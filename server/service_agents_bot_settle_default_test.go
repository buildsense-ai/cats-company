package server

import (
	"testing"
	"time"
)

// 默认 settle 必须留够「bot 说完要做什么、再去真的做」的时间。
//
// 这不是拍脑袋的数字。bot 干活是「先说 → 再做 → 报结果」，中间那步（跑命令、
// 读文件）常常超过 2s。默认值太小的话，平台会在它开口那一刻就收尾，用户等了
// 十几秒只拿到「我先统计一下进程数量。」这种前言，真正的内容一个字都没有 ——
// 而且从结果上看不出被截断过。
//
// 实测（生产、真 bot、同一组需要动手的问题）：
//
//	2s → 17 次里 2 次只拿到前言
//	5s → 13 次全部拿到数据
//
// 这条用例守的是「别再把它调回两秒」：可以调小，但得先有同样量级的证据。
func TestDefaultBotRunnerSettleLeavesRoomForToolWork(t *testing.T) {
	if defaultBotRunnerSettle < 3*time.Second {
		t.Fatalf("默认 settle = %s，对「说完还要动手」的 bot 太短了："+
			"它会先交付一句前言，真正的内容留在下一条消息里没人接", defaultBotRunnerSettle)
	}
	// 上限也要留余地：慢 bot 靠 settle_seconds 自己调大，默认值不该直接顶到 max。
	if defaultBotRunnerSettle >= maxBotRunnerSettle {
		t.Fatalf("默认 settle = %s 已经顶到允许上限，作者没有调大的空间了", defaultBotRunnerSettle)
	}
	// 必须落在允许区间内，否则 clamp 会把默认值悄悄改掉。
	tuning := botRunnerTuning{Settle: defaultBotRunnerSettle}.clamped()
	if tuning.Settle != defaultBotRunnerSettle {
		t.Fatalf("默认 settle 被 clamp 改动：%s → %s", defaultBotRunnerSettle, tuning.Settle)
	}
}

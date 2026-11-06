package server

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// 「code: 一句说明」形态的失败原因，也要走到那条可行动的翻译上。
//
// 执行器写进库的从来不是裸短码：生产实测是
// `no_command: 该应用没有声明本机执行命令`、`bot_offline: 目标 bot 当前不在线`。
// 以前这里按整串相等 switch，于是那几条写得最用心的文案全程是死代码 ——
// 用户看到的只有原始短码，不知道该找谁、要不要重试。
func TestServiceRunErrorTextMatchesPrefixedCodes(t *testing.T) {
	cases := []struct {
		name    string
		errText string
		want    []string
		reject  []string
	}{
		{
			name:    "no_command 带说明",
			errText: "no_command: 该应用没有声明本机执行命令",
			want:    []string{"no_command", "执行命令", "发布者", "Agent 工具"},
		},
		{
			name:    "not_picked_up 带说明",
			errText: "not_picked_up: 没有实例认领",
			want:    []string{"not_picked_up", "发布者", "重试不会变好"},
			// 不能让调用方以为是自己参数的问题
			reject: []string{"invalid_arguments"},
		},
		{
			name:    "worker_offline 带说明",
			errText: "worker_offline: 目标 worker 不在线",
			want:    []string{"worker_offline", "启动小八", "登录"},
		},
		{
			// 裸短码的老行为不变
			name:    "裸 no_command",
			errText: "no_command",
			want:    []string{"no_command", "执行命令", "发布者"},
		},
		{
			// 不认识的短码原样透出，不自作聪明
			name:    "未知短码带说明",
			errText: "exit_2: ls: cannot access '/x': No such file or directory",
			want:    []string{"exit_2", "No such file or directory"},
			reject:  []string{"run terminated", "发布者"},
		},
		{
			name:    "未知裸短码保留兜底尾巴",
			errText: "boom",
			want:    []string{"boom", "run terminated"},
		},
		{
			name:    "完全没有原因",
			errText: "",
			want:    []string{"run terminated"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := serviceRunErrorText(store.ServiceRun{State: "failed", ErrorText: c.errText})
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Fatalf("缺少 %q：%s", want, got)
				}
			}
			for _, bad := range c.reject {
				if strings.Contains(got, bad) {
					t.Fatalf("不该出现 %q：%s", bad, got)
				}
			}
		})
	}
}

// 「应用指向一个已删除的 bot」不能报成「不在线」。
//
// 报错方向错了比不说更糟：用户会去让发布者「把 bot 启动起来」，而正确的处置是
// 重新选一个 bot。所以存在性必须在在线状态之前判。
func TestServiceBotRunnerBotExists(t *testing.T) {
	missing := newFakeBotStore()
	missing.ownerErr = sql.ErrNoRows
	found := newFakeBotStore()

	cases := []struct {
		name   string
		runner *ServiceBotRunner
		botUID int64
		want   bool
	}{
		{"存在", &ServiceBotRunner{store: found}, 982, true},
		{"已删除", &ServiceBotRunner{store: missing}, 982, false},
		// 存储抖动不能当成「bot 被删」：预检是体验优化，不是拦截点。
		{"存储出错按存在处理", &ServiceBotRunner{store: &errStore{}}, 982, true},
		{"nil runner", nil, 982, true},
		{"botUID=0", &ServiceBotRunner{store: missing}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.runner.BotExists(c.botUID); got != c.want {
				t.Fatalf("BotExists(%d) = %v，期望 %v", c.botUID, got, c.want)
			}
		})
	}
}

// errStore 的 GetBotOwner 返回一个「不是 ErrNoRows」的错误（模拟连不上库）。
type errStore struct{ fakeBotStore }

func (*errStore) GetBotOwner(int64) (int64, error) { return 0, errors.New("db unreachable") }

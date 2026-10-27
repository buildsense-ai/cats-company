package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 「一次调用一个会话」：投递时把 service session id 带给 bot，
// bot 侧据此开会话/回原会话 —— 上下文不会把上一个任务的判断带到下一个。
func TestBotRunnerCarriesSessionIDToBot(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_s1", SessionID: "sess_a", Input: json.RawMessage(`{"text":"查 A"}`)}, 38)
	if !waitFor(t, func() bool { return len(db.saved) > 0 }, 2*time.Second) {
		t.Fatal("应该已经落库")
	}
	if got := db.saved[0].session; got != "sess_a" {
		t.Fatalf("metadata[%s] = %q, want sess_a", botSessionMetadataKey, got)
	}
	// 仍然是「有人派活」而不是「主人闲聊」
	if flag, _ := db.saved[0].metadata["catsco_bot_call"].(bool); !flag {
		t.Fatalf("catsco_bot_call 应为 true: %+v", db.saved[0].metadata)
	}
}

// 串话的判定规则本身。
func TestBotReplyBelongsTo(t *testing.T) {
	cases := []struct {
		name      string
		msg       *types.Message
		competing int
		want      bool
	}{
		{
			name: "reply_to 指回自己那条 prompt：明确是我的",
			msg:  &types.Message{ID: 9, ReplyTo: 100},
			// 即使还有别人在等，也认得出来
			competing: 3,
			want:      true,
		},
		{
			name:      "回复里带了同一个会话 id：也算明确",
			msg:       &types.Message{ID: 9, Metadata: map[string]interface{}{botSessionMetadataKey: "sess_a"}},
			competing: 3,
			want:      true,
		},
		{
			name:      "旧客户端 + 只有我一单在等：认（兼容）",
			msg:       &types.Message{ID: 9},
			competing: 0,
			want:      true,
		},
		{
			name:      "旧客户端 + 别人也在等：不认（宁可如实失败也不交错的答案）",
			msg:       &types.Message{ID: 9},
			competing: 1,
			want:      false,
		},
		{
			name:      "reply_to 指向别人的 prompt：不是我的",
			msg:       &types.Message{ID: 9, ReplyTo: 101},
			competing: 0,
			want:      false,
		},
		{
			name:      "nil 消息不算",
			msg:       nil,
			competing: 0,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := botReplyBelongsTo(tc.msg, 100, "sess_a", tc.competing); got != tc.want {
				t.Fatalf("botReplyBelongsTo = %v, want %v", got, tc.want)
			}
		})
	}
}

// 并发两单、bot 回填了 reply_to：各拿各的，谁也不会把对方的结果交出去。
func TestBotRunnerConcurrentRunsKeepTheirOwnReplies(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)
	runner.timeout = 2 * time.Second

	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_a", SessionID: "sess_a", Input: json.RawMessage(`{"text":"查 A"}`)}, 38)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_b", SessionID: "sess_b", Input: json.RawMessage(`{"text":"查 B"}`)}, 38)

	// 两条 prompt 都投递出去后才有 msgID 可用。
	if !waitFor(t, func() bool { return len(db.saved) >= 2 }, 2*time.Second) {
		t.Fatalf("两单都应落库: %+v", db.saved)
	}
	db.mu.Lock()
	promptA, promptB := int64(0), int64(0)
	for idx, saved := range db.saved {
		if saved.session == "sess_a" {
			promptA = int64(101 + idx) // nextID 从 100 起，逐条 +1
		}
		if saved.session == "sess_b" {
			promptB = int64(101 + idx)
		}
	}
	db.mu.Unlock()
	if promptA == 0 || promptB == 0 {
		t.Fatalf("找不到两单的 prompt id: %+v", db.saved)
	}

	// 故意「后发的先回」，两个 run 都在等。
	db.pushBotReplyFor("p2p_38_982", 982, "B 的结果", promptB, "sess_b")
	db.pushBotReplyFor("p2p_38_982", 982, "A 的结果", promptA, "sess_a")

	if !waitFor(t, func() bool { return len(db.completed) >= 2 }, 3*time.Second) {
		t.Fatalf("两单都应收尾；failed=%v", db.failed)
	}

	bySession := map[string]string{}
	db.mu.Lock()
	for runID, env := range db.completedByRun {
		var output struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(env.Output, &output)
		bySession[runID] = output.Text
	}
	db.mu.Unlock()

	if got := bySession["run_a"]; !strings.Contains(got, "A 的结果") || strings.Contains(got, "B 的结果") {
		t.Fatalf("run_a 拿到了 %q，应该只有 A 的结果", got)
	}
	if got := bySession["run_b"]; !strings.Contains(got, "B 的结果") || strings.Contains(got, "A 的结果") {
		t.Fatalf("run_b 拿到了 %q，应该只有 B 的结果", got)
	}
}

// 旧客户端（不回填 reply_to）并发两单：分不清就如实失败，不交一份可能是别人的答案。
func TestBotRunnerAmbiguousLegacyReplyFailsHonestly(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)
	runner.timeout = 2 * time.Second
	runner.ambiguityGrace = 40 * time.Millisecond

	agent := botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_a", SessionID: "sess_a", Input: json.RawMessage(`{"text":"查 A"}`)}, 38)
	runner.MaybeDispatch(context.Background(), agent,
		store.ServiceRun{RunID: "run_b", SessionID: "sess_b", Input: json.RawMessage(`{"text":"查 B"}`)}, 38)

	if !waitFor(t, func() bool { return len(db.saved) >= 2 }, 2*time.Second) {
		t.Fatal("两单都应落库")
	}
	// 旧客户端：两条回复都不带 reply_to，也带不上会话标记。
	db.pushBotReply("p2p_38_982", 982, "模糊的回复一")
	db.pushBotReply("p2p_38_982", 982, "模糊的回复二")

	if !waitFor(t, func() bool { return len(db.failed) >= 1 }, 2*time.Second) {
		t.Fatalf("应该如实失败；completed=%v", db.completed)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.completed) != 0 {
		t.Fatalf("分不清归属时不能交出结果，却完成了 %v", db.completed)
	}
	found := false
	for runID, errText := range db.failed {
		if strings.HasPrefix(errText, "bot_reply_ambiguous") {
			found = true
		}
		_ = runID
	}
	if !found {
		t.Fatalf("失败原因应为 bot_reply_ambiguous，实际 %v", db.failed)
	}
}

// 单人在等时旧客户端照旧能用：不能因为加了严格认领就把老版本全废掉。
func TestBotRunnerSoleLegacyReplyStillWorks(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_only", SessionID: "sess_only", Input: json.RawMessage(`{"text":"润色"}`)}, 38)
	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatal("应该已经投递")
	}
	db.pushBotReply("p2p_38_982", 982, "润色好了")

	if !waitFor(t, func() bool { return len(db.completed) > 0 }, 2*time.Second) {
		t.Fatalf("单人时旧客户端也该能收尾；failed=%v", db.failed)
	}
	if db.completedE.Status != ServiceRunCompleted {
		t.Fatalf("status = %v", db.completedE.Status)
	}
}

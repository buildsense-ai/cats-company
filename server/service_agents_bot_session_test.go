package server

import (
	"context"
	"encoding/json"
	"errors"
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
	// bot 收的是 WS 实时消息，不轮询消息表 —— 会话 id 必须随推送一起到。
	// 只写进库而漏了推送，bot 就只能按 topic 分会话，上下文照样串。
	if len(hub.sessions) == 0 {
		t.Fatal("应该已经推送")
	}
	if got := hub.sessions[0]; got != "sess_a" {
		t.Fatalf("推送时的 bot 会话 id = %q, want sess_a", got)
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

// 投递前必须先建 topic。
//
// `messages.topic_id` 有外键指向 `topics`，而投递只 INSERT 消息。调用方和这个
// bot 从来没聊过天时 topic 不存在 —— 而这正是「别人的 agent 调我的 bot」的主
// 场景。生产实测：跨账号首调直接 bot_send_failed（foreign key 23503）。
func TestBotRunnerCreatesTopicBeforeSending(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_t1", SessionID: "sess_t", Input: json.RawMessage(`{"text":"第一次打交道"}`)}, 38)
	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatalf("应该已经投递；failed=%v", db.failed)
	}
	db.mu.Lock()
	topics := append([]string(nil), db.topics...)
	db.mu.Unlock()
	if len(topics) != 1 || topics[0] != "p2p_38_982" {
		t.Fatalf("投递前应建 topic p2p_38_982，实际 %v", topics)
	}

	// 建 topic 失败要如实失败，不能继续发（否则报了外键错，原因更难懂）。
	db2 := newFakeBotStore()
	db2.topicErr = errors.New("boom")
	runner2 := newTestBotRunner(db2, hub)
	hub2 := &fakeBotHub{online: true}
	runner2.hub = hub2
	runner2.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_t2", SessionID: "sess_t2", Input: json.RawMessage(`{"text":"hi"}`)}, 38)
	if !waitFor(t, func() bool { return len(db2.failed) > 0 }, 2*time.Second) {
		t.Fatal("建 topic 失败应如实失败")
	}
	db2.mu.Lock()
	errText := db2.failed["run_t2"]
	db2.mu.Unlock()
	if !strings.HasPrefix(errText, "bot_topic_failed") {
		t.Fatalf("失败原因应为 bot_topic_failed，实际 %q", errText)
	}
	if len(hub2.delivered) != 0 {
		t.Fatalf("topic 都没建成，不该投递: %v", hub2.delivered)
	}
}

// 生产实测：一次调用的真答复 121 字（mode=normal，带 reply_to），而它的
// skill 文件内容、execute_shell stdout 也是 msg_type=text，一共 10KB，
// 且都没有 reply_to。只按 msg_type 收，答案就变成了一坨日志。
func TestBotRunnerSkipsToolProcessMessages(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_proc", SessionID: "sess_p", Input: json.RawMessage(`{"text":"查一下"}`)}, 38)
	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatal("应该已经投递")
	}
	db.mu.Lock()
	promptID := int64(0)
	for idx := range db.saved {
		if db.saved[idx].session == "sess_p" {
			promptID = int64(101 + idx)
		}
	}
	db.mu.Unlock()
	if promptID == 0 {
		t.Fatalf("找不到 prompt id: %+v", db.saved)
	}

	// 真实的时序：先工具过程（无归属），再真答复（带 reply_to）。
	db.pushBotMessage("p2p_38_982", 982, "skill", 0, "", "code")
	db.pushBotMessage("p2p_38_982", 982, strings.Repeat("SKILL-BODY ", 500), 0, "", "code")
	db.pushBotMessage("p2p_38_982", 982, "Command completed\nstatus: succeeded", 0, "", "code")
	db.pushBotReplyFor("p2p_38_982", 982, "答案是 42", promptID, "sess_p")

	if !waitFor(t, func() bool { return db.completedCount() > 0 }, 3*time.Second) {
		t.Fatalf("应该完成；failed=%v", db.failed)
	}
	var output struct {
		Text string `json:"text"`
	}
	err := json.Unmarshal(db.lastResult(t).Output, &output)
	if err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if output.Text != "答案是 42" {
		t.Fatalf("output.text = %.160q，应该只有答案，不能带工具过程", output.Text)
	}
}

// 已经有明确归属的回复后，不再收「没人抢就认」的兜底：那时出现的无标记消息
// 必然是过程记录或别人的活。
func TestBotRunnerStopsFallbackAfterExplicitReply(t *testing.T) {
	db := newFakeBotStore()
	hub := &fakeBotHub{online: true}
	runner := newTestBotRunner(db, hub)
	runner.settle = 400 * time.Millisecond

	runner.MaybeDispatch(context.Background(), botAgent(t, `{"runner":{"type":"bot","bot_uid":982}}`),
		store.ServiceRun{RunID: "run_mix", SessionID: "sess_m", Input: json.RawMessage(`{"text":"干活"}`)}, 38)
	if !waitFor(t, func() bool { return len(hub.delivered) > 0 }, 2*time.Second) {
		t.Fatal("应该已经投递")
	}
	db.mu.Lock()
	promptID := int64(0)
	for idx := range db.saved {
		if db.saved[idx].session == "sess_m" {
			promptID = int64(101 + idx)
		}
	}
	db.mu.Unlock()

	db.pushBotReplyFor("p2p_38_982", 982, "真正的答案", promptID, "sess_m")
	// 答案之后又来一条无归属的 normal 消息（比如主人的闲聊回复被顺手写进同一 topic）。
	db.pushBotMessage("p2p_38_982", 982, "这条不该被收进来", 0, "", "")

	if !waitFor(t, func() bool { return db.completedCount() > 0 }, 3*time.Second) {
		t.Fatalf("应该完成；failed=%v", db.failed)
	}
	var output struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(db.lastResult(t).Output, &output)
	if output.Text != "真正的答案" {
		t.Fatalf("output.text = %.120q，不该收答复之后的无归属消息", output.Text)
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

	if !waitFor(t, func() bool { return db.completedCount() >= 2 }, 3*time.Second) {
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

	if !waitFor(t, func() bool { return db.failedCount() >= 1 }, 2*time.Second) {
		t.Fatalf("应该如实失败；completed=%v", db.completed)
	}
	// 已经持有 db.mu，这里只能用裸字段（completedCount 会二次加锁 → 死锁）。
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

	if !waitFor(t, func() bool { return db.completedCount() > 0 }, 2*time.Second) {
		t.Fatalf("单人时旧客户端也该能收尾；failed=%v", db.failed)
	}
	// 必须走线程安全读：写入在 fake 的 mu 内，不加锁看不到（表现为读到零值）。
	if status := db.lastResult(t).Status; status != ServiceRunCompleted {
		t.Fatalf("status = %v", status)
	}
}

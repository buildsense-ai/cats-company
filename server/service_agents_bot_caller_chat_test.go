package server

import (
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// pushMsg 直接往 fake store 塞一条消息。
//
// pushBotMessage 只会造 text、只带 session 标记，覆盖不了两件本次必须验证的事：
// 平台自己写的 prompt（带 catsco_bot_call）和附件（type=file）。
func pushMsg(f *fakeBotStore, msg types.Message) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	msg.ID = f.nextID
	cp := msg
	f.messages = append(f.messages, &cp)
	return cp.ID
}

func platformPrompt(f *fakeBotStore, topic string, uid int64, text string) int64 {
	return pushMsg(f, types.Message{
		TopicID: topic, FromUID: uid, Content: text, MsgType: "text",
		Metadata: map[string]interface{}{botCallMetadataKey: true},
	})
}

func newChatterRunner(db *fakeBotStore) *ServiceBotRunner {
	runner := newTestBotRunner(db, &fakeBotHub{})
	runner.poll = 10 * time.Millisecond
	runner.timeout = 400 * time.Millisecond
	runner.settle = 10 * time.Millisecond
	return runner
}

// 复现 2026-11-04 12:12 的生产事故（topic p2p_982_1619）：
//
//	1619 发 prompt → 982 回一条 mode=code 的进度 → **1619 自己回话**（它也是 bot，
//	运行时把对方的进度消息当成新任务）→ 982 再回 → 双方互刷 18 条。
//
// 平台 27.5s 就「completed」了，交出去的是 982 那句「回环碎片，没有新任务」，
// 真正的 `sleep 200; echo done` 结果从未产生（回环之后这个 topic 再没有新消息）。
//
// 修法：这个会话里除目标 bot 之外有人说过话之后，目标 bot 那些**没有归属标记**的
// 消息一律不认 —— 它们是对那句话的回应，不是对我们 prompt 的答复。
func TestAwaitReplyRejectsChatterFromAnotherParticipant(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	promptID := platformPrompt(db, topic, caller, "请执行 sleep 200，只回复 done")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Mode: "code",
		Content: `{"command":"sleep 200; echo done"}`})
	pushMsg(db, types.Message{TopicID: topic, FromUID: caller, MsgType: "text",
		Content: "这条还是回环碎片，没有新的指令，我不执行"})
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text",
		Content: "回环碎片，没有新任务。视频已交付。"})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err == nil {
		t.Fatalf("回环碎片被当成答案交付了：reply=%q", reply)
	}
	if !strings.Contains(err.Error(), "bot_reply_ambiguous") {
		t.Fatalf("err=%v，想要 bot_reply_ambiguous（分不清 ≠ 没回）", err)
	}
}

// 控制组：没人插话时，无标记回复仍按老逻辑认领。
// 否则所有老客户端（不带 reply_to / session 标记）都会被误杀成 ambiguous。
func TestAwaitReplyStillAcceptsUnownedWithoutChatter(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	promptID := platformPrompt(db, topic, caller, "请执行 sleep 200，只回复 done")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "done"})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if reply != "done" {
		t.Fatalf("reply=%q, want done", reply)
	}
}

// 有人插过话之后，**带明确归属**的回复依然要认 —— 那才是真答案。
// 这条同时锁住「不许因为插话就提前失败」：答案晚到也要拿到。
func TestAwaitReplyAcceptsOwnedReplyAfterChatter(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	runner.ambiguityGrace = 30 * time.Millisecond
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	promptID := platformPrompt(db, topic, caller, "请执行 sleep 200，只回复 done")
	pushMsg(db, types.Message{TopicID: topic, FromUID: caller, MsgType: "text", Content: "回环碎片，我不执行"})
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "回环碎片，没有新任务"})
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "done", ReplyTo: promptID})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v（带 reply_to 的真答案必须交付）", err)
	}
	if reply != "done" {
		t.Fatalf("reply=%q, want done", reply)
	}
}

// 同一账号并发两单时，第二单的 prompt 也以调用方身份落在同一个 topic 里。
// 它带着 catsco_bot_call 标记，是平台自己写的，不能算「有人插话」。
func TestAwaitReplyIgnoresPlatformPromptOfAnotherRun(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	promptID := platformPrompt(db, topic, caller, "第一单：统计进程数")
	platformPrompt(db, topic, caller, "第二单：统计磁盘")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "3 个进程"})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v（并发 prompt 不该算插话，直接把答案判成 ambiguous）", err)
	}
	if reply != "3 个进程" {
		t.Fatalf("reply=%q", reply)
	}
}

// 附件天生不带 reply_to / session 标记（老客户端走另一条发送路径）。
// 插话之后也不能拦，否则就是「正文说已发送、文件永远收不到」。
func TestAwaitReplyAcceptsAttachmentAfterChatter(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const (
		caller = int64(1619)
		bot    = int64(982)
	)
	topic := "p2p_982_1619"
	promptID := platformPrompt(db, topic, caller, "把清单发文件给我")
	pushMsg(db, types.Message{TopicID: topic, FromUID: caller, MsgType: "text", Content: "回环碎片，我不执行"})
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "file",
		Content: `{"type":"file","payload":{"url":"/uploads/files/list.txt","name":"list.txt"}}`})

	reply, attachments, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v（附件不能因为插话被丢掉）", err)
	}
	if len(attachments) != 1 || attachments[0].Name != "list.txt" {
		t.Fatalf("attachments=%+v", attachments)
	}
	if reply == "" {
		t.Fatal("附件说明不该是空的")
	}
}

// 自分享（catsco_share_self）时调用方与目标 bot 是同一个 uid，会话里「说话的人」
// 只有它自己 —— 不能把它的回复全判成「别人插话」。
func TestAwaitReplySelfShareNotTreatedAsChatter(t *testing.T) {
	db := &fakeBotStore{}
	runner := newChatterRunner(db)
	const bot = int64(982)
	topic := "p2p_982_982"
	promptID := platformPrompt(db, topic, bot, "帮我查一下磁盘")
	pushMsg(db, types.Message{TopicID: topic, FromUID: bot, MsgType: "text", Content: "/ 用了 71%"})

	reply, _, err := runner.awaitReply(topic, promptID, bot, "", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v（自分享场景不该被判成回环）", err)
	}
	if reply != "/ 用了 71%" {
		t.Fatalf("reply=%q", reply)
	}
}

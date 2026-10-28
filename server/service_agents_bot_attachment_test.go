package server

import (
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// 附件消息不能默默丢掉。
//
// 真实场景：让杰克输出一份 60 行的清单，它把内容写进文件发出来，正文只剩
// 「清单.txt 已发送。」。平台原来只收 msg_type=text，文件被整个丢弃 ——
// 用户等了几十秒，拿到的是一句「已发送」，既没有内容也没有线索。
//
// 文件内容确实取不到（平台文件存储 + 需要鉴权），但「有这么个文件、叫什么、
// 多大、在哪儿」是可以如实交付的。装上附件说明和假装成功是两件不同的事，
// 后者才是真正伤人的。
func TestBotRunnerAttachmentNote(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
		empty   bool
	}{
		{
			name:    "完整 payload",
			content: `{"type":"file","payload":{"url":"/uploads/files/abc.txt","name":"number-list-1-60.txt","size":430}}`,
			want:    []string{"附件", "number-list-1-60.txt", "430 字节", "/uploads/files/abc.txt"},
		},
		{
			name:    "只有名字没有地址",
			content: `{"type":"file","payload":{"name":"报告.md"}}`,
			want:    []string{"附件", "报告.md"},
		},
		{
			name:    "只有地址（名字缺失时要给个称呼，不能空着）",
			content: `{"type":"file","payload":{"url":"/uploads/files/x.pdf"}}`,
			want:    []string{"附件", "未命名文件", "/uploads/files/x.pdf"},
		},
		{
			// 解析不出来就退回原行为：宁可什么都不加，也不要伪造一个附件。
			name:    "不是 JSON",
			content: `清单.txt 已发送。`,
			empty:   true,
		},
		{
			name:    "JSON 但没有文件信息",
			content: `{"type":"file","payload":{}}`,
			empty:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := botRunnerAttachmentNote(c.content)
			if c.empty {
				if got != "" {
					t.Fatalf("解析不出文件信息时应返回空，实际: %q", got)
				}
				return
			}
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Fatalf("说明里缺少 %q：%s", want, got)
				}
			}
		})
	}
}

// pushBotFile 推一条 file 类型的 bot 消息（模拟「结果写进文件」这类行为）。
func pushBotFile(st *fakeBotStore, topic string, botUID int64, replyTo int64, content string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextID++
	st.messages = append(st.messages, &types.Message{
		ID: st.nextID, TopicID: topic, FromUID: botUID, Content: content, MsgType: "file", ReplyTo: replyTo,
	})
}

// pushBotTextTo 推一条 text 类型的 bot 消息，可指定 reply_to 与 mode。
func pushBotTextTo(st *fakeBotStore, topic string, botUID int64, replyTo int64, content, mode string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextID++
	st.messages = append(st.messages, &types.Message{
		ID: st.nextID, TopicID: topic, FromUID: botUID, Content: content, MsgType: "text", ReplyTo: replyTo, Mode: mode,
	})
}

// bot 发「过程展示 + 文件 + 一句已发送」时，交付结果里要看得出附件在哪。
func TestBotRunnerDeliversAttachmentReference(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100)
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})

	pushBotTextTo(st, topic, bot, promptI, `{"tool":"write_file","input":{}}`, "code")
	pushBotFile(st, topic, bot, promptI, `{"type":"file","payload":{"url":"/uploads/files/abc.txt","name":"清单.txt","size":430}}`)
	pushBotTextTo(st, topic, bot, promptI, "清单.txt 已发送。", "")

	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	for _, want := range []string{"清单.txt", "附件", "/uploads/files/abc.txt", "已发送"} {
		if !strings.Contains(got, want) {
			t.Fatalf("交付结果里缺少 %q：%s", want, got)
		}
	}
	// 过程展示（mode=code）不能混进来。
	if strings.Contains(got, "write_file") {
		t.Fatalf("工具过程展示被当成答案了：%s", got)
	}
}

// 归属规则对附件同样生效：指回别人 prompt 的附件不能算我的结果。
func TestBotRunnerIgnoresForeignAttachment(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100) // 同时是消息读取游标：fake 的 id 从 100 往上递增
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})

	pushBotFile(st, topic, bot, promptI-50, `{"type":"file","payload":{"name":"别人的文件.txt","url":"/uploads/files/other.txt"}}`)
	pushBotTextTo(st, topic, bot, promptI, "我的答案", "")

	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("awaitReply: %v", err)
	}
	if strings.Contains(got, "别人的文件") {
		t.Fatalf("别人的附件被当成我的结果了：%s", got)
	}
	if !strings.Contains(got, "我的答案") {
		t.Fatalf("自己的答案丢了：%s", got)
	}
}

// 只有附件、没有任何正文时，也要交付附件说明而不是干等到超时。
func TestBotRunnerAttachmentOnlyDoesNotTimeOut(t *testing.T) {
	const (
		caller  = int64(38)
		bot     = int64(982)
		promptI = int64(100) // 同时是消息读取游标
	)
	st := newFakeBotStore()
	topic := botRunnerTopic(caller, bot)
	runner := newTestBotRunner(st, &fakeBotHub{online: true})

	pushBotFile(st, topic, bot, promptI, `{"type":"file","payload":{"name":"结果.csv","url":"/uploads/files/r.csv","size":12}}`)

	got, err := runner.awaitReply(topic, promptI, bot, "sess_1", runner.tuningFor(nil))
	if err != nil {
		t.Fatalf("只有附件时应正常交付，实际报错: %v", err)
	}
	if !strings.Contains(got, "结果.csv") {
		t.Fatalf("附件说明没交付：%s", got)
	}
}

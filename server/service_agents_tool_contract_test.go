package server

import (
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// 工具描述是唯一的契约面：调用方模型只能靠它决定怎么下单、怎么理解结果。
//
// 这两句话以前完全缺失，代价是实测出来的：
//   - 下单时不知道「要文件得让对方发」，只写「给我一份报告」；
//   - 收货时不知道 attachments 是产物，把对方那句「文件已生成在 /tmp/x.pdf」
//     当成交付成功转告用户，用户去点一个不存在的路径。
func TestServiceAgentToolDescriptionExplainsFileDelivery(t *testing.T) {
	botAgent := store.ServiceAgentDescriptor{
		ID: 1, Slug: "report", Name: "报告助手",
		Description:    "把需求写成报告。",
		DefinitionJSON: `{"runner":{"type":"bot","bot_uid":982}}`,
	}
	desc := serviceAgentToolDescription(botAgent)

	// 作者原文必须原样保留在最前面：那是「这个应用干什么」，不能被平台话术挤掉。
	if !strings.HasPrefix(desc, "把需求写成报告。") {
		t.Fatalf("作者描述应原样打头：%s", desc)
	}
	for _, want := range []string{"send_file", "attachments", "原样转给用户", "写路径"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("文件交付说明里缺少 %q：%s", want, desc)
		}
	}
}

// 非 bot 型（worker / http / mcp / 平台自带）不该背上这段说明：
// 它们不是「另一个 Agent 在另一台机器上跑」，写进去只会误导模型。
func TestServiceAgentToolDescriptionSkipsFileNoteForNonBotRunners(t *testing.T) {
	for _, definition := range []string{
		`{"runner":{"type":"worker","command":"ls"}}`,
		`{"runner":{"type":"http","url":"https://example.test/run"}}`,
		``,
	} {
		desc := serviceAgentToolDescription(store.ServiceAgentDescriptor{
			ID: 2, Slug: "x", Name: "X", Description: "干活。", DefinitionJSON: definition,
		})
		if strings.Contains(desc, "send_file") {
			t.Fatalf("definition=%q 不该带 bot 的文件交付说明：%s", definition, desc)
		}
	}
}

// 作者没写描述时，那句「作者未说明」必须留着 —— 它是模型判断该不该用的唯一依据。
func TestServiceAgentToolDescriptionKeepsMissingDescriptionNotice(t *testing.T) {
	desc := serviceAgentToolDescription(store.ServiceAgentDescriptor{
		ID: 3, Slug: "mystery", Name: "神秘应用",
		DefinitionJSON: `{"runner":{"type":"bot","bot_uid":982}}`,
	})
	if !strings.Contains(desc, "作者未说明") {
		t.Fatalf("缺描述时应有明确提示：%s", desc)
	}
	if !strings.Contains(desc, "send_file") {
		t.Fatalf("bot 型仍然要带文件交付说明：%s", desc)
	}
}

// 兜底：模型仍然只写路径时，把「没交付」如实说出来。
//
// 平台已经从两头提醒过（工具描述 + 客户端提示词），但模型仍可能漏掉。
// 这时至少要让调用方知道文件没到手，而不是把路径当成拿到了文件。
func TestBotRunnerUndeclaredPathNote(t *testing.T) {
	cases := []struct {
		name        string
		reply       string
		attachments []ServiceRunAttachment
		want        bool
	}{
		{
			name:  "Windows 路径 + 扩展名",
			reply: "报告已生成：D:\\work\\out\\report_20261028.pdf，请查收。",
			want:  true,
		},
		{
			name:  "Linux 路径 + 扩展名",
			reply: "文件在 /tmp/cats/report.pdf",
			want:  true,
		},
		{
			name:  "有扩展名之外的本地路径也算（home 目录）",
			reply: "写到 /home/ubuntu/notes.md 了",
			want:  true,
		},
		{
			// 平台自己的地址：调用方本来就取得到，不该被说成「没交付」。
			name:  "平台上传地址不算",
			reply: "已发送，地址 https://app.catsco.cc/uploads/files/20261028_abc.txt",
			want:  false,
		},
		{
			// 有附件时路径只是补充说明，不需要解释。
			name:        "有附件就不加",
			reply:       "报告已生成：/tmp/report.pdf",
			attachments: []ServiceRunAttachment{{Name: "report.pdf", URL: "https://app.catsco.cc/uploads/files/x.pdf"}},
			want:        false,
		},
		{
			name:  "纯文字没提路径",
			reply: "三个结论里两条通过，一条待跟进。",
			want:  false,
		},
		{
			name:  "空答复",
			reply: "   ",
			want:  false,
		},
		{
			// 相对路径没有本地根，判断不了是不是本机文件 —— 不乱说。
			name:  "相对路径不算",
			reply: "放在 out/report.pdf 了",
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := botRunnerUndeclaredPathNote(c.reply, c.attachments)
			if c.want && got == "" {
				t.Fatalf("应该提示「没有文件随结果返回」：%s", c.reply)
			}
			if !c.want && got != "" {
				t.Fatalf("不该加提示：%s → %s", c.reply, got)
			}
			if got != "" && !strings.Contains(got, "send_file") {
				t.Fatalf("提示里要给出下一步（让对方用 send_file 发）：%s", got)
			}
		})
	}
}

// 派活正文不能被平台话术污染：模型判断意图靠的是调用方原文。
func TestBotRunnerPromptTextIsVerbatim(t *testing.T) {
	got := botRunnerPromptText([]byte(`{"text":"给我一份报告"}`))
	if got != "给我一份报告" {
		t.Fatalf("原文应一字不改，实际 %q", got)
	}
}

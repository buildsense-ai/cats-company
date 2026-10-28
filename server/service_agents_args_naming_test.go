package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 受理响应必须用 session_id 渲染，而不是 session。
//
// 真实踩法：响应文本原来写 `session=<id>`，请求参数却叫 `session_id`。客户端
// （尤其 LLM）照着响应里的名字回传 `{"session": "..."}`，被静默忽略 —— 于是
// 「追问」变成了开一个全新会话，上下文全丢，而且没有任何提示。
func TestServiceRunResultTextUsesSessionId(t *testing.T) {
	text := mcpToolErrorText(serviceRunToolResult(ServiceRunEnvelope{
		ContractVersion: serviceRunAsyncContractVersion,
		RunID:           "run_1", SessionID: "sess_1", Status: ServiceRunRunning,
	}))
	if !strings.Contains(text, "session_id=sess_1") {
		t.Fatalf("响应要用 session_id 渲染: %s", text)
	}
	if strings.Contains(text, " session=") {
		t.Fatalf("不能再用 session= 渲染（会诱导客户端传错参数名）: %s", text)
	}
}

// 未知参数要被抓出来并给出正确写法，而不是静默丢弃。
func TestServiceInvokeArgsRecordsIgnoredNames(t *testing.T) {
	var args serviceInvokeToolArgs
	raw := `{"input":{"text":"hi"},"session":"sess_1","sessionId":"sess_2","client_token":"t","bot":"982","weird":1}`
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sortStrings(args.Ignored)
	want := []string{"bot", "session", "sessionId", "weird"}
	if strings.Join(args.Ignored, ",") != strings.Join(want, ",") {
		t.Fatalf("ignored = %v，期望 %v", args.Ignored, want)
	}
	// 认识的参数不能被误报。
	for _, known := range []string{"input", "client_token"} {
		for _, got := range args.Ignored {
			if got == known {
				t.Fatalf("%q 是合法参数，不该算未识别", known)
			}
		}
	}
}

// 提示要指到正确字段名上：用户拼错的往往只是叫法。
func TestServiceArgsNotePointsAtTheRightName(t *testing.T) {
	note := serviceArgsNote([]string{"session", "clientToken", "bot"})
	for _, want := range []string{"没被识别", "session", "session_id", "client_token", "bot_uid"} {
		if !strings.Contains(note, want) {
			t.Fatalf("提示里缺少 %q：%s", want, note)
		}
	}
	if serviceArgsNote(nil) != "" {
		t.Fatal("没有未识别参数时不该加噪音")
	}
}

// 端到端：传了 session（而不是 session_id）时，受理响应要当面提醒。
func TestServiceAgentInvokeWarnsAboutWrongParameterName(t *testing.T) {
	const owner = int64(38)
	base := newFakeServiceAgentStore()
	base.autoCompleteAfter = 5 * time.Millisecond
	agent := store.ServiceAgentDescriptor{
		ID: 61, Slug: "jack", Name: "杰克", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
		Visibility: store.ServiceAgentVisibilityPublic, DefinitionJSON: `{"runner":{"type":"bot","bot_uid":982}}`,
	}
	base.agents = []store.ServiceAgentDescriptor{agent}
	st := &accessAwareStore{
		fakeServiceAgentStore: base,
		ownerUID:              owner,
		visibility:            store.ServiceAgentVisibilityPublic,
		definitionJSON:        agent.DefinitionJSON,
	}
	handler := NewServiceAgentsMCPHandler(st)
	handler.SetBotIdentity(selfCallBotIdentity{})

	var args serviceInvokeToolArgs
	if err := json.Unmarshal([]byte(`{"input":{"text":"hi"},"session":"sess_old"}`), &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res, _, err := handler.invoke(context.Background(), agent, owner, args)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	text := mcpToolErrorText(res)
	if !strings.Contains(text, "没被识别") || !strings.Contains(text, "session_id") {
		t.Fatalf("受理响应应提醒参数名写错了: %s", text)
	}
	// 但调用本身必须照常进行（只提示，不拒绝）。
	if !strings.Contains(text, "status=") {
		t.Fatalf("应该照常受理: %s", text)
	}
}

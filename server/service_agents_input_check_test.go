package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 入参体检：只查 required 在不在，目标是「传错字段名时立刻告诉他」。
func TestServiceAgentInputProblemOnlyChecksRequired(t *testing.T) {
	schema := `{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}`
	cases := []struct {
		name    string
		input   string
		wantBad bool
	}{
		{"正常传了", `{"text":"你好"}`, false},
		{"多带字段不管", `{"text":"你好","extra":1}`, false},
		// 这三个是真实踩过的：字段名拼错、完全空、给了个不相干的键。
		{"字段名拼错", `{"query":"你好"}`, true},
		{"空对象", `{}`, true},
		{"显式 null", `{"text":null}`, true},
		{"空串", `{"text":""}`, true},
		{"只给空白", `{"text":"   "}`, false}, // 空白有内容，交给下游判断
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problem := serviceAgentInputProblem(schema, json.RawMessage(c.input))
			if c.wantBad != (problem != "") {
				t.Fatalf("input=%s problem=%q，期望有问题=%v", c.input, problem, c.wantBad)
			}
			if !c.wantBad {
				return
			}
			// 提示必须能让用户自己定位：说清缺什么、他传了什么、该去哪儿核对。
			for _, want := range []string{"invalid_input", "text"} {
				if !strings.Contains(problem, want) {
					t.Fatalf("提示里缺少 %q：%s", want, problem)
				}
			}
		})
	}
}

// fail-open：没声明 schema / 解析不出 / 没有 required 时一律不拦。
// 老应用没 schema 是常态，不能因为「声明得不完整」把正常调用挡掉。
func TestServiceAgentInputProblemFailsOpen(t *testing.T) {
	for _, schema := range []string{
		"", "null", "{}", `{"properties":{"text":{}}}`, "not json", `{"required":[]}`,
	} {
		if problem := serviceAgentInputProblem(schema, json.RawMessage(`{"query":"x"}`)); problem != "" {
			t.Fatalf("schema=%q 不该拦，却报：%s", schema, problem)
		}
	}
	// 直接把一句话当 input 传（bot 型应用的兼容路径）不该被 schema 堵死。
	if problem := serviceAgentInputProblem(`{"required":["text"]}`, json.RawMessage(`"一句话"`)); problem != "" {
		t.Fatalf("字符串入参应放行：%s", problem)
	}
}

// 提示里要带上用户实际传的键名 —— 拼错时这一条能让他自己发现。
func TestServiceAgentInputProblemNamesWhatWasSent(t *testing.T) {
	problem := serviceAgentInputProblem(`{"required":["text"]}`, json.RawMessage(`{"query":"x","page":2}`))
	for _, want := range []string{"page", "query"} {
		if !strings.Contains(problem, want) {
			t.Fatalf("应列出用户传的键 %q：%s", want, problem)
		}
	}
}

func TestServiceAgentInputProblemListsEveryMissingField(t *testing.T) {
	problem := serviceAgentInputProblem(`{"required":["text","path"]}`, json.RawMessage(`{"path":"/tmp"}`))
	// 缺的只有 text：不能把已经传了的 path 也算进「缺了」那个列表。
	if !strings.Contains(problem, "里有 text，") {
		t.Fatalf("应只指出缺 text：%s", problem)
	}
	for _, bad := range []string{"text、path", "path、text"} {
		if strings.Contains(problem, bad) {
			t.Fatalf("path 已经传了，不该算缺：%s", problem)
		}
	}
}

// bot 离线要在建 run 之前拦：以前是受理成功、几秒后 failed，用户白等一轮。
func TestServiceAgentInvokeRejectsOfflineBotBeforeCreatingRun(t *testing.T) {
	const owner, jack = int64(38), int64(982)
	base := newFakeServiceAgentStore()
	base.autoCompleteAfter = 5 * time.Millisecond
	st := &accessAwareStore{
		fakeServiceAgentStore: base,
		ownerUID:              owner,
		visibility:            store.ServiceAgentVisibilityPublic,
		definitionJSON:        `{"runner":{"type":"bot","bot_uid":982}}`,
	}
	handler := NewServiceAgentsMCPHandler(st)
	handler.SetBotIdentity(selfCallBotIdentity{})
	// hub 不在线。
	handler.SetBotRunner(&ServiceBotRunner{hub: offlineBotHub{}})

	agent := store.ServiceAgentDescriptor{
		ID: 51, Slug: "jack", Name: "杰克", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
		Visibility: store.ServiceAgentVisibilityPublic, DefinitionJSON: `{"runner":{"type":"bot","bot_uid":982}}`,
	}
	base.agents = []store.ServiceAgentDescriptor{agent}

	res, _, err := handler.invoke(context.Background(), agent, owner, serviceInvokeToolArgs{Input: map[string]any{"text": "在吗"}})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	text := mcpToolErrorText(res)
	if !strings.Contains(text, "bot_offline") {
		t.Fatalf("离线应在受理前被拒，实际: %s", text)
	}
	// 关键：不能建 run —— 建了就是一次注定失败的空转。
	base.mu.Lock()
	runs := len(base.runs)
	base.mu.Unlock()
	if runs != 0 {
		t.Fatalf("离线预检必须在建 run 之前，实际 run 数 = %d", runs)
	}
}

// 预检判不了就放行：hub 未注入时不能变成新的拦截点。
func TestServiceAgentBotOnlineFailsOpen(t *testing.T) {
	if !(&ServiceBotRunner{}).BotOnline(982) {
		t.Fatal("hub 未注入时应放行")
	}
	var nilRunner *ServiceBotRunner
	if !nilRunner.BotOnline(982) {
		t.Fatal("nil runner 应放行")
	}
	if !(&ServiceBotRunner{hub: offlineBotHub{}}).BotOnline(0) {
		t.Fatal("botUID=0 应放行")
	}
}

// offlineBotHub 只实现 BotRuntimeOnline（其余能力用不到）。
type offlineBotHub struct{}

func (offlineBotHub) BotRuntimeOnline(int64) bool { return false }

func (offlineBotHub) DeliverServiceBotPrompt(int64, int64, string, string, int64, string) bool {
	return false
}

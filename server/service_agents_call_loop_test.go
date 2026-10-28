package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

// 两跳回环：A 刚把任务交给 B，B 又把活交回 A —— 必须拦。
//
// 这条拦不住会一直转下去，而且不会超时：bot 型应用是异步的，调用方几十毫秒就
// 拿到句柄返回，5 分钟超时只兜「对方不吭声」，兜不住「对方很勤快地和你互推」。
func TestServiceAgentCallLoopGuardBlocksReverseEdge(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Minute)
	if g.wouldLoop(982, 110) {
		t.Fatal("还没记录过委托方向，不该判成回环")
	}
	g.record(982, 110) // 杰克把活交给小八
	if !g.wouldLoop(110, 982) {
		t.Fatal("小八把活交回杰克应判成回环")
	}
	// 同一个方向重复记录不算回环（同一对 bot 连续同向协作是正常的）。
	if g.wouldLoop(982, 110) {
		t.Fatal("同向重复不算回环")
	}
}

func TestServiceAgentCallLoopGuardIgnoresUnrelatedAndSelf(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Minute)
	g.record(982, 110)
	for _, tc := range []struct{ caller, target int64 }{
		{982, 110}, // 同向
		{110, 275}, // 无关目标
		{275, 982}, // 无关来源
		{982, 982}, // 自己（另由自调用拦截负责）
		{0, 110},   // 身份缺失
	} {
		if g.wouldLoop(tc.caller, tc.target) {
			t.Fatalf("%d→%d 不该判成回环", tc.caller, tc.target)
		}
	}
}

func TestServiceAgentCallLoopGuardExpires(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Minute)
	base := time.Now()
	g.now = func() time.Time { return base }
	g.record(982, 110)

	g.now = func() time.Time { return base.Add(59 * time.Second) }
	if !g.wouldLoop(110, 982) {
		t.Fatal("窗口内应判成回环")
	}
	g.now = func() time.Time { return base.Add(61 * time.Second) }
	if g.wouldLoop(110, 982) {
		t.Fatal("窗口外应放行（慢 bot 隔很久才交回来不算回环）")
	}
	// 过期边被顺手清掉，表不会无限长。
	g.mu.Lock()
	left := len(g.edges)
	g.mu.Unlock()
	if left != 0 {
		t.Fatalf("过期边应被清理，还剩 %d 条", left)
	}
}

// 调用方身份归一化：显式 bot_uid 优先，没带就退回账号 uid
// （bot 用自己凭证调 MCP 时就是这个样子）。与自调用拦截的判据必须一致。
func TestServiceAgentCallLoopOrigin(t *testing.T) {
	for _, tc := range []struct {
		callerUID, botUID, want int64
	}{
		{38, 982, 982}, // 账号代 bot 调：用 bot 身份
		{982, 0, 982},  // bot 用自己凭证：退回账号
		{0, 0, 0},
	} {
		if got := serviceAgentCallLoopOrigin(tc.callerUID, tc.botUID); got != tc.want {
			t.Fatalf("origin(%d,%d)=%d，期望 %d", tc.callerUID, tc.botUID, got, tc.want)
		}
	}
}

// 端到端：两个 bot 互相把对方挂成工具，A 调 B 之后 B 调 A 要被拦住，
// 且**不能建 run**（建了就是一次真实的、分钟级的空转）。
func TestServiceAgentInvokeRejectsTwoHopLoop(t *testing.T) {
	const owner, jack, xiaoba = int64(38), int64(982), int64(110)
	base := newFakeServiceAgentStore()
	base.autoCompleteAfter = 5 * time.Millisecond
	st := &accessAwareStore{
		fakeServiceAgentStore: base,
		ownerUID:              owner,
		visibility:            store.ServiceAgentVisibilityPublic,
	}
	handler := NewServiceAgentsMCPHandler(st)
	handler.SetBotIdentity(selfCallBotIdentity{})

	// 两个应用：杰克（bot 982）与小八（bot 110），都是 public。
	jackAgent := store.ServiceAgentDescriptor{
		ID: 51, Slug: "jack", Name: "杰克", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
		Visibility: store.ServiceAgentVisibilityPublic, DefinitionJSON: `{"runner":{"type":"bot","bot_uid":982}}`,
	}
	xiaobaAgent := store.ServiceAgentDescriptor{
		ID: 52, Slug: "xiaoba", Name: "小八", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
		Visibility: store.ServiceAgentVisibilityPublic, DefinitionJSON: `{"runner":{"type":"bot","bot_uid":110}}`,
	}
	base.agents = []store.ServiceAgentDescriptor{jackAgent, xiaobaAgent}

	invoke := func(agent store.ServiceAgentDescriptor, callerUID, botUID int64) string {
		t.Helper()
		st.definitionJSON = agent.DefinitionJSON
		res, _, err := handler.invoke(context.Background(), agent, callerUID, serviceInvokeToolArgs{BotUID: botUID})
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		return mcpToolErrorText(res)
	}

	// 第一跳：杰克（982）→ 小八（110）。正常派活，必须放行。
	if text := invoke(xiaobaAgent, owner, jack); strings.Contains(text, "agent_call_loop") || strings.Contains(text, "agent_self_call") {
		t.Fatalf("第一跳不该被拦: %s", text)
	}
	// 第二跳：小八（110）→ 杰克（982）。原路交回，必须拦。
	if text := invoke(jackAgent, owner, xiaoba); !strings.Contains(text, "agent_call_loop") {
		t.Fatalf("反向那一跳应判成回环，实际: %s", text)
	}
	// 拦住时不能留下 run（建了就是一次真实空转）。
	st.fakeServiceAgentStore.mu.Lock()
	runs := len(st.fakeServiceAgentStore.runs)
	st.fakeServiceAgentStore.mu.Unlock()
	if runs != 1 {
		t.Fatalf("回环应在建 run 之前被拦，实际 run 数 = %d", runs)
	}
}

// 单向协作不受影响：A→B 之后 A 再调 C，以及 C→A，都不该被误拦。
func TestServiceAgentInvokeAllowsNonLoopAfterDelegation(t *testing.T) {
	const owner, jack, xiaoba = int64(38), int64(982), int64(110)
	base := newFakeServiceAgentStore()
	base.autoCompleteAfter = 5 * time.Millisecond
	st := &accessAwareStore{
		fakeServiceAgentStore: base,
		ownerUID:              owner,
		visibility:            store.ServiceAgentVisibilityPublic,
	}
	handler := NewServiceAgentsMCPHandler(st)
	handler.SetBotIdentity(selfCallBotIdentity{})
	xiaobaAgent := store.ServiceAgentDescriptor{
		ID: 52, Slug: "xiaoba", Name: "小八", Version: 1,
		OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
		Visibility: store.ServiceAgentVisibilityPublic, DefinitionJSON: `{"runner":{"type":"bot","bot_uid":110}}`,
	}
	base.agents = []store.ServiceAgentDescriptor{xiaobaAgent}
	st.definitionJSON = xiaobaAgent.DefinitionJSON

	call := func(callerUID, botUID int64) string {
		t.Helper()
		res, _, err := handler.invoke(context.Background(), xiaobaAgent, callerUID, serviceInvokeToolArgs{BotUID: botUID})
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		return mcpToolErrorText(res)
	}
	// 杰克把活交给小八。
	if text := call(owner, jack); strings.Contains(text, "agent_call_loop") {
		t.Fatalf("首次派活不该被拦: %s", text)
	}
	// 第三个人（275）调小八：与刚才那次委托无关，必须放行。
	if text := call(275, 0); strings.Contains(text, "agent_call_loop") {
		t.Fatalf("无关方向的调用不该被拦: %s", text)
	}
}

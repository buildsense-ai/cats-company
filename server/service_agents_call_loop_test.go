package server

import (
	"context"
	"strconv"
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
	g.record(982, 110, "run_1") // 杰克把活交给小八
	if !g.wouldLoop(110, 982) {
		t.Fatal("小八把活交回杰克应判成回环")
	}
	// 同一个方向重复记录不算回环（同一对 bot 连续同向协作是正常的）。
	if g.wouldLoop(982, 110) {
		t.Fatal("同向重复不算回环")
	}
}

// 三跳回环：A→B→C→A。旧版只看「最近一次方向」，这种情况每一条边都新鲜、
// 方向也不重复，全部放行 —— 于是一直转下去。
func TestServiceAgentCallLoopGuardBlocksThreeHopCycle(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Minute)
	g.record(982, 110, "run_1") // 杰克 → 小八
	// 二跳时小八→C 是正常委派，必须放行（C 不在自己的上游链上）。
	if g.wouldLoop(110, 275) {
		t.Fatal("小八把活交给第三个 bot 不该被拦")
	}
	g.record(110, 275, "run_2") // 小八 → C
	// 三跳：C 交回杰克 —— 这是环，必须拦。
	chain := g.cycleFor(275, 982)
	if len(chain) == 0 {
		t.Fatal("C 把活交回杰克应判成三跳回环")
	}
	if len(chain) != 3 {
		t.Fatalf("链应该是三个节点（C→小八→杰克），实际 %v", chain)
	}
	// 四跳：C 交回小八也是环（小八在链上游）。
	if !g.wouldLoop(275, 110) {
		t.Fatal("C 把活交回小八也应该是环")
	}
}

// 长链上的无关方向不能被误判：任务在 A→B→C 之间流转，但 D 调其中任何一个都不是环。
func TestServiceAgentCallLoopGuardAllowsSidewaysDelegation(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Minute)
	g.record(982, 110, "run_1")
	g.record(110, 275, "run_2")
	for _, tc := range []struct{ caller, target int64 }{
		{275, 43},  // 链尾往下游调一个新人
		{43, 275},  // 新人反过来调链尾（43 不在链上）
		{43, 982},  // 新人调链头（43 自己没有上游）
		{982, 275}, // 链头跳级直接调链尾
	} {
		if g.wouldLoop(tc.caller, tc.target) {
			t.Fatalf("%d→%d 不该判成回环", tc.caller, tc.target)
		}
	}
}

func TestServiceAgentCallLoopGuardIgnoresUnrelatedAndSelf(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Minute)
	g.record(982, 110, "run_1")
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
	g.record(982, 110, "run_1")

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
	left := 0
	for _, upstreams := range g.waiting {
		left += len(upstreams)
	}
	g.mu.Unlock()
	if left != 0 {
		t.Fatalf("过期边应被清理，还剩 %d 条", left)
	}
}

// run 收尾要能摘掉自己那条边：只靠 TTL 的话，一次正常交付过的协作会在窗口内
// 继续参与判定，B 干完活之后再正当委派一次就会被说成「在绕圈」。
func TestServiceAgentCallLoopGuardReleasesOnRunEnd(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Minute)
	g.record(982, 110, "run_1")
	if !g.wouldLoop(110, 982) {
		t.Fatal("run 还没结束，反向那一跳应判成回环")
	}
	g.releaseRun("run_1")
	if g.wouldLoop(110, 982) {
		t.Fatal("run 已收尾，边应该已经摘掉")
	}
	// 摘除要幂等（重复收尾、未知 run 都不能崩）。
	g.releaseRun("run_1")
	g.releaseRun("")
	g.releaseRun("run_unknown")
	// 摘除掉的是边，不是整张表：另一个 run 建立的边必须还在。
	g.record(982, 110, "run_2")
	g.record(110, 275, "run_3")
	g.releaseRun("run_2")
	if !g.wouldLoop(275, 110) {
		t.Fatal("只该摘 run_2 那条边，run_3 还在")
	}
}

// 链长到顶按回环处理：这种形状本身就是互相踢皮球，再走下去每跳都是一个
// 分钟级回合，成本远超收益。
func TestServiceAgentCallLoopGuardStopsAtMaxHops(t *testing.T) {
	g := newServiceAgentCallLoopGuard(time.Hour)
	// 造一条 982 → 110 → 275 → 43 → 44 ... 的长链。
	chain := []int64{982, 110, 275, 43, 44, 45, 46, 47, 48, 49}
	for i := 0; i+1 < len(chain); i++ {
		g.record(chain[i], chain[i+1], "run_"+string(rune('a'+i)))
	}
	// 链尾往源头交：超过上限，必须拦（而不是一路放行）。
	if !g.wouldLoop(49, 982) {
		t.Fatal("超过追溯上限的委托应直接按回环拦下")
	}
}

// 端到端三跳：A 把活交给 B，B 交给 C，C 再交回 A —— 必须拦住，且要说清「转了几手」。
//
// 这是「根治」的核心用例：旧版只看最近一次方向，这条链上每条边都新鲜、方向也不
// 重复，于是 C→A 被当成正常委派放行，任务开始绕第二圈。
func TestServiceAgentInvokeRejectsThreeHopLoop(t *testing.T) {
	const owner = int64(38)
	const jack, xiaoba, third = int64(982), int64(110), int64(275)
	base := newFakeServiceAgentStore()
	base.autoCompleteAfter = 5 * time.Millisecond
	st := &accessAwareStore{
		fakeServiceAgentStore: base,
		ownerUID:              owner,
		visibility:            store.ServiceAgentVisibilityPublic,
	}
	handler := NewServiceAgentsMCPHandler(st)
	handler.SetBotIdentity(selfCallBotIdentity{})

	agentFor := func(id int64, slug string, botUID int64) store.ServiceAgentDescriptor {
		return store.ServiceAgentDescriptor{
			ID: id, Slug: slug, Name: slug, Version: 1,
			OwnerKind: store.ServiceAgentOwnerUser, OwnerUID: owner,
			Visibility: store.ServiceAgentVisibilityPublic,
			DefinitionJSON: `{"runner":{"type":"bot","bot_uid":` + strconv.FormatInt(botUID, 10) + `}}`,
		}
	}
	jackAgent := agentFor(61, "jack", jack)
	xiaobaAgent := agentFor(62, "xiaoba", xiaoba)
	thirdAgent := agentFor(63, "third", third)
	base.agents = []store.ServiceAgentDescriptor{jackAgent, xiaobaAgent, thirdAgent}

	invoke := func(agent store.ServiceAgentDescriptor, callerBot int64) string {
		t.Helper()
		st.definitionJSON = agent.DefinitionJSON
		res, _, err := handler.invoke(context.Background(), agent, owner, serviceInvokeToolArgs{BotUID: callerBot})
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		return mcpToolErrorText(res)
	}

	// 第一跳：杰克(982) → 小八(110)。
	if text := invoke(xiaobaAgent, jack); strings.Contains(text, "agent_call_loop") {
		t.Fatalf("第一跳不该被拦: %s", text)
	}
	// 第二跳：小八(110) → 第三方(275)。往下游走，不是环。
	if text := invoke(thirdAgent, xiaoba); strings.Contains(text, "agent_call_loop") {
		t.Fatalf("继续往下游委派不该被拦: %s", text)
	}
	// 第三跳：第三方(275) → 杰克(982)。回到链头，是环。
	text := invoke(jackAgent, third)
	if !strings.Contains(text, "agent_call_loop") {
		t.Fatalf("三跳回环必须拦下，实际: %s", text)
	}
	// 文案要说清「转了几手」，否则模型只会换个应用再试一次（还是同一个环）。
	if !strings.Contains(text, "2 手") {
		t.Fatalf("文案应说明已经转了几手，实际: %s", text)
	}
	// 拦住时不能留下 run：建了就是一次真实的、分钟级的空转。
	st.fakeServiceAgentStore.mu.Lock()
	runs := len(st.fakeServiceAgentStore.runs)
	st.fakeServiceAgentStore.mu.Unlock()
	if runs != 2 {
		t.Fatalf("回环应在建 run 之前被拦（前两跳共 2 个 run），实际 run 数 = %d", runs)
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

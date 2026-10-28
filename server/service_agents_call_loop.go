package server

import (
	"sync"
	"time"
)

// serviceAgentCallLoopWindow 是回环判定的时间窗。
//
// 取 10 分钟：bot 的一轮是分钟级的（上下文压缩 + 多轮推理），反向调用往往发生在
// 对方那一轮跑完的时候。窗口太短会漏判「慢 bot 才刚交回来」，太长则会把
// 「上午互相调用过一次」也算进来。
const serviceAgentCallLoopWindow = 10 * time.Minute

// serviceAgentCallEdge 是一次委托方向：Caller 把任务交给了 Target。
type serviceAgentCallEdge struct {
	Caller int64
	Target int64
}

// serviceAgentCallLoopGuard 用一张带 TTL 的有向边表识别 A→B→A 的两跳回环。
//
// 为什么需要它：自己调自己（A→A）已经拦住了，但「A 把任务交给 B，B 又把同一件事
// 交回 A」不会被任何一端的自我检查命中 —— 双方都觉得自己在正常协作。结果是两个
// bot 互相踢皮球，每一跳都是一次完整的、分钟级的回合，而且**不会超时**（bot 型
// 应用是异步的，调用方几十毫秒就拿到句柄返回了），只会在额度烧完后才停。
//
// 判定只看方向，不看内容：A→B 之后窗口内出现 B→A 就认为回环。这不保证一定是
// 同一个任务在来回，但「刚交出去、马上又交回来」本来就不像正当需求；宁可让调用
// 方自己干，也不要把两边都拖进空转。
//
// 状态在进程内：多副本部署时每个副本只看得见自己经手的边，后果是**漏判**而不是
// 误拦 —— 与现有 inFlight 的取舍一致。要做精确判定得让调用链自己带上级信息，
// 那是客户端协议的事，不在这一层。
type serviceAgentCallLoopGuard struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	edges  map[serviceAgentCallEdge]time.Time
}

func newServiceAgentCallLoopGuard(window time.Duration) *serviceAgentCallLoopGuard {
	if window <= 0 {
		window = serviceAgentCallLoopWindow
	}
	return &serviceAgentCallLoopGuard{
		window: window,
		now:    time.Now,
		edges:  make(map[serviceAgentCallEdge]time.Time),
	}
}

// serviceAgentCallLoopOrigin 归一化「发起这次调用的 bot 是谁」。
//
// 显式 bot_uid 优先（XiaoBa 的 tool bridge 会带）；没带就退回账号 uid ——
// bot 用自己凭证调 MCP 时就是这个样子。与自调用拦截的判据保持一致：
// 两处对「调用方身份」的理解不能有分叉，否则总有一条路漏掉。
func serviceAgentCallLoopOrigin(callerUID, botUID int64) int64 {
	if botUID != 0 {
		return botUID
	}
	return callerUID
}

// wouldLoop 判断「target 刚刚把任务交给了 caller」是否还在窗口内。
// 即：这次 caller→target 调用是否构成反向边。
func (g *serviceAgentCallLoopGuard) wouldLoop(caller, target int64) bool {
	if g == nil || caller == 0 || target == 0 || caller == target {
		return false
	}
	reverse := serviceAgentCallEdge{Caller: target, Target: caller}
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.edges[reverse]
	if !ok {
		return false
	}
	if g.now().Sub(at) > g.window {
		delete(g.edges, reverse)
		return false
	}
	return true
}

// record 记下「caller 把任务交给了 target」，并顺手清掉过期边。
// 只在调用真正被受理后调用 —— 被拒的调用不该污染判定表。
func (g *serviceAgentCallLoopGuard) record(caller, target int64) {
	if g == nil || caller == 0 || target == 0 || caller == target {
		return
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.edges == nil {
		g.edges = make(map[serviceAgentCallEdge]time.Time)
	}
	g.edges[serviceAgentCallEdge{Caller: caller, Target: target}] = now
	for edge, at := range g.edges {
		if now.Sub(at) > g.window {
			delete(g.edges, edge)
		}
	}
}

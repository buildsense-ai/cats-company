package server

import (
	"sync"
	"time"
)

const (
	// serviceAgentCallLoopWindow 是回环判定的时间窗。
	//
	// 取 10 分钟：bot 的一轮是分钟级的（上下文压缩 + 多轮推理），反向调用往往发生在
	// 对方那一轮跑完的时候。窗口太短会漏判「慢 bot 才刚交回来」，太长则会把
	// 「上午互相调用过一次」也算进来。
	serviceAgentCallLoopWindow = 10 * time.Minute

	// serviceAgentCallMaxHops 是向上追溯的层数上限。
	//
	// 正常协作不会超过两三层。超过这个深度还在往上走，说明这条委托链已经长得没有
	// 意义了 —— 每一跳都是一个分钟级的完整回合，成本远超收益，而且这种形状本身
	// 就是「互相踢皮球」的另一种表现。到顶就按回环处理。
	serviceAgentCallMaxHops = 8

	// serviceAgentCallMaxNodes 限制单次判定访问的 bot 数，防止异常数据把遍历拖长。
	serviceAgentCallMaxNodes = 64
)

// serviceAgentCallEdge 是一条委托关系：Caller 把任务交给了 Target。
type serviceAgentCallEdge struct {
	Caller int64
	Target int64
}

// serviceAgentCallLoopGuard 用一张「谁在等谁」的表识别委托链上的回环，跳数不限。
//
// 为什么需要它：自己调自己（A→A）已有单独的拦截，但「A 把任务交给 B，B 又交给 C，
// C 再交回 A」不会被任何一端的自我检查命中 —— 每一方都觉得自己在正常协作。结果是
// 几个 bot 互相踢皮球，每一跳都是一次完整的、分钟级的回合，而且**不会超时**（bot 型
// 应用是异步的，调用方几十毫秒就拿到句柄返回了），只会在额度烧完后才停。
//
// 上一版只记住「最近一次方向」，所以只认得出 A→B→A 两跳：三跳以上每一条边都是
// 新鲜的、方向也不重复，全部放行。现在改成记「谁在等谁」的全图，判定变成一次向上
// 的遍历 —— 从本次调用方出发，沿「谁在等我」一路往上，只要路上出现目标 bot，就
// 说明这个任务正要被交回链上游的某一环。跳数天然不受限。
//
// 举例（括号里是 waiting 表的形状）：
//
//	A→B（waiting[B]={A}）→ B→C（waiting[C]={B}）→ C→A：
//	从 C 往上走 C → B → A，目标 A 出现在链上 ⇒ 回环。
//	A→B → B→C：从 B 往上走得到 {A}，目标 C 不在其中 ⇒ 放行。
//
// 状态在进程内：多副本部署时每个副本只看得见自己经手的委托，后果是**漏判**而不是
// 误拦 —— 与 inFlight 的取舍一致。run 收尾时会把边摘掉（releaseRun），所以正常
// 交付过的协作不会在窗口内继续影响判定；TTL 只是崩溃/丢消息时的兜底。
type serviceAgentCallLoopGuard struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	// waiting[下游][上游] = 记录时间：上游这个 bot 正在等下游这个 bot 干完。
	waiting map[int64]map[int64]time.Time
	// byRun 记住每条边是被哪个 run 建立的，run 收尾时据此精确摘除。
	byRun map[string]serviceAgentCallEdge
}

func newServiceAgentCallLoopGuard(window time.Duration) *serviceAgentCallLoopGuard {
	if window <= 0 {
		window = serviceAgentCallLoopWindow
	}
	return &serviceAgentCallLoopGuard{
		window:  window,
		now:     time.Now,
		waiting: make(map[int64]map[int64]time.Time),
		byRun:   make(map[string]serviceAgentCallEdge),
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

// cycleFor 判断「caller 把活交给 target」是否会让任务回到链上的某一环。
//
// 返回值是这条链（从本次调用方一路上溯到重复出现的那一环），空表示不是回环。
// 把链带回去是为了能对模型说清楚「这件事已经转了几手」—— 只说「检测到循环」
// 对模型没有任何指导意义，它只会换个工具再试一次。
func (g *serviceAgentCallLoopGuard) cycleFor(caller, target int64) []int64 {
	if g == nil || caller == 0 || target == 0 {
		return nil
	}
	// 自己调自己不归这里管：那条路有专门的自调用拦截，而且文案更准
	// （「这个应用就是你自己在执行」）。两处都报会让模型收到两条互相矛盾的解释。
	if caller == target {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()

	type hop struct {
		bot  int64
		path []int64
	}
	queue := []hop{{bot: caller, path: []int64{caller}}}
	visited := map[int64]struct{}{caller: {}}
	var longest []int64
	truncated := false
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if len(cur.path) > serviceAgentCallMaxHops {
			// 链比上限还长，而且还没绕回来。这种形状只能按回环处理：再往下走
			// 每一跳都是一个分钟级回合，成本远超收益，而真正正当的协作链不会有
			// 这么深。返回目前走到的这条链，让提示能说「已经超过 N 手」。
			truncated = true
			if len(cur.path) > len(longest) {
				longest = cur.path
			}
			continue
		}
		for upstream, at := range g.waiting[cur.bot] {
			if now.Sub(at) > g.window {
				// 顺手清掉过期边：表不能无限长，过期边也不该参与判定。
				delete(g.waiting[cur.bot], upstream)
				continue
			}
			next := append(append([]int64(nil), cur.path...), upstream)
			if upstream == target {
				return next
			}
			if _, ok := visited[upstream]; ok {
				continue
			}
			visited[upstream] = struct{}{}
			if len(visited) > serviceAgentCallMaxNodes {
				truncated = true
				break
			}
			queue = append(queue, hop{bot: upstream, path: next})
		}
	}
	if truncated {
		return longest
	}
	return nil
}

// wouldLoop 是 cycleFor 的布尔形态（只需要「拦不拦」的调用点用）。
func (g *serviceAgentCallLoopGuard) wouldLoop(caller, target int64) bool {
	return len(g.cycleFor(caller, target)) > 0
}

// record 记下「caller 把任务交给了 target」，并绑定到建立它的 run。
//
// 只在调用真正被受理后调用 —— 被拒的调用不该污染判定表。
func (g *serviceAgentCallLoopGuard) record(caller, target int64, runID string) {
	if g == nil || caller == 0 || target == 0 || caller == target {
		return
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiting == nil {
		g.waiting = make(map[int64]map[int64]time.Time)
	}
	if g.waiting[target] == nil {
		g.waiting[target] = make(map[int64]time.Time)
	}
	g.waiting[target][caller] = now
	if runID != "" {
		if g.byRun == nil {
			g.byRun = make(map[string]serviceAgentCallEdge)
		}
		g.byRun[runID] = serviceAgentCallEdge{Caller: caller, Target: target}
	}
	g.purgeLocked(now)
}

// releaseRun 在 run 收尾时摘掉它建立的那条边。
//
// 只靠 TTL 的话，一次正常交付过的协作会在窗口内继续参与判定：A 把活交给 B、B 干完
// 正常回话之后，10 分钟内 B 再想正当委派一次都会被判成「你在把活交回去」。那不是
// 回环，是误拦 —— 边该存在的时间正好是「下游还在为上游干活」的那段。
func (g *serviceAgentCallLoopGuard) releaseRun(runID string) {
	if g == nil || runID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	edge, ok := g.byRun[runID]
	if !ok {
		return
	}
	delete(g.byRun, runID)
	g.dropEdgeLocked(edge)
}

func (g *serviceAgentCallLoopGuard) dropEdgeLocked(edge serviceAgentCallEdge) {
	upstreams, ok := g.waiting[edge.Target]
	if !ok {
		return
	}
	delete(upstreams, edge.Caller)
	if len(upstreams) == 0 {
		delete(g.waiting, edge.Target)
	}
}

func (g *serviceAgentCallLoopGuard) purgeLocked(now time.Time) {
	for target, upstreams := range g.waiting {
		for upstream, at := range upstreams {
			if now.Sub(at) > g.window {
				delete(upstreams, upstream)
			}
		}
		if len(upstreams) == 0 {
			delete(g.waiting, target)
		}
	}
	// byRun 与 waiting 同源：边被清掉后指针也该跟着消失，否则表会一直长。
	for runID, edge := range g.byRun {
		if _, ok := g.waiting[edge.Target][edge.Caller]; !ok {
			delete(g.byRun, runID)
		}
	}
}

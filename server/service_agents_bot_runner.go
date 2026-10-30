package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// bot-as-tool（设计：bot 可分享）：把一次 run 交给「另一个 bot」（虚拟员工）执行。
//
// 为什么单独一种 runner，而且**天生异步**：
//
//	bot 的一个回合是分钟级的 —— 上下文压缩、多轮模型推理、它自己再去调工具。
//	实测一次 90s 压缩 + 20s 推理。而 tools/call 的同步等待上限只有 45s
//	（maxServiceSyncWait），硬等只会让主 Agent 一直卡着，用户看到的就是没反应。
//
// 所以这里只做「投递 + 收尾」：
//
//  1. 平台把这个 run 的 input 作为一条私聊消息发给 bot；
//  2. 后台 goroutine 轮询消息表，等 bot 自己回一条 text；
//  3. 拿到就写 run 结果，调用方通过 catsco_run_status / 回执继续。
//
// 主 Agent 拿到的是立刻返回的 status=running 句柄，零阻塞。
const (
	// ServiceAgentRunnerBot 是把执行权交给另一个 bot 的 runner 类型。
	// definition 形如 {"runner":{"type":"bot","bot_uid":982}}。
	ServiceAgentRunnerBot = "bot"

	defaultBotRunnerTimeout      = 5 * time.Minute
	defaultBotRunnerPollInterval = 500 * time.Millisecond
	// 收到第一段回复后再等这么久没有新消息就收尾：bot 会把长回复切成多段
	// （XiaoBa 按 4000 字切），收得太早会把答案截断。
	//
	// 为什么是 5s 而不是 2s：bot 干活时会「先说要做什么 → 真的去做 → 再报结果」。
	// 中间那步（跑一条命令、读一个文件）经常超过 2s，于是平台在它开口的那一刻
	// 就收尾了 —— 用户等了十几秒，拿到的却是「我先统计一下进程数量。」这种前言，
	// 真正的内容一个字都没有，而且从结果上看不出是被截断的。
	//
	// 实测（同一组需要动手的问题，生产环境、真 bot）：
	//   settle=2s  → 17 次里 2 次只拿到前言（88%）
	//   settle=5s  → 13 次全部拿到数据，平均多花 2-3s
	//   settle=8s  → 13 次全部拿到数据，平均多花 5s
	// 多等两三秒换「一定能拿到内容」是划算的：用户不会因为多等 3s 抱怨，但会因为
	// 白等一场且没有任何结果而放弃这个功能。慢 bot 想让平台等更久可以自己调大
	// settle_seconds（云端改，不发版）；反过来想抢速度也可以调小。
	defaultBotRunnerSettle = 5 * time.Second
	botRunnerMessageBatch  = 50
	// 单次回复的文本上限，防止对端刷屏把 run 结果撑爆。
	maxBotRunnerReplyBytes = 64 << 10

	// botSessionMetadataKey 是「这次调用属于哪个 bot 会话」的键。
	// 值就是 run 的 service session id —— 调用方不传 session_id 平台就新建一个，
	// 传了就回到那个会话。bot 侧按它算自己的会话身份，于是：
	//
	//	不同调用 = 不同会话（互不污染上下文，也不会把上一个任务的结论带进来）
	//	同一 session_id = 同一个会话（追问能接上）
	//
	// 没有这个键时 bot 只能按 topic 分会话，而 topic 是「两个人之间的私聊」，
	// 所有调用挤在一条会话里 —— 上一个任务的判断会漏到下一个任务。
	botSessionMetadataKey = "catsco_bot_session_id"

	// botTurnDoneMetadataKey 是「这一轮我说完了」的键，由 bot 侧在回合真正结束时
	// 打在**最后那条回复**的 metadata 上。值是会话 id（与 catsco_bot_session_id 一致），
	// 也接受 true —— 前者更严，后者是老客户端可能的写法。
	//
	// 为什么要这个信号：bot 干活是「先说要做啥 → 真去做 → 再报结果」，中间那步
	// 实测能超过 14 秒（生产：1619 说完「收到，我先让分镜工具出方案」之后 14.5 秒
	// 才给出真正的分镜）。平台只能拿「静默多久算说完」去猜，猜错就把前言当答案
	// 交付了 —— 而调用方从结果上看不出被截断，只会觉得这个 bot 很没用。
	//
	// 有了信号就不必猜：看到它就立刻收尾，没看到就退回 settle 兜底（老客户端
	// 行为不变）。这也让 settle 不必为了极端情况调到十几秒 —— 那会让所有简单
	// 调用都平白多等。
	botTurnDoneMetadataKey = "catsco_turn_done"

	// botRunnerProgressSilenceLimit 是「bot 说了还在干」之后最多再等多久。
	//
	// bot 用 {"catsco_turn_done": false} 明说「这段是过程话，别当成答案」，平台因此
	// 不按静默窗口（settle）收尾。但等待要有上限：bot 说完就崩掉的话，调用方会一直
	// 挂到整单超时（默认 5 分钟）才知道结果。45s 远大于任何单步工具耗时（实测最长
	// 的一次停顿 14.5s），超过就交付已收到的内容 —— 有东西总比干等到超时好。
	botRunnerProgressSilenceLimit = 45 * time.Second

	// botReplyAmbiguousGrace 是「明明看见 bot 回了，却判断不出是不是给我」时的等待窗口。
	// 超过它就如实失败，不干等到 5 分钟超时 —— 让调用方尽快知道是客户端版本问题。
	botReplyAmbiguousGrace = 20 * time.Second
)

// botRunnerStore 是 bot runner 需要的落库能力。
// 用窄接口而不是 store.Store：调用方传进来的可能只是 ServiceAgentStore，
// 由装配层（cmd/server.go）显式断言，缺能力时 runner 为 nil、能力自动关闭。
type botRunnerStore interface {
	StartServiceRun(ctx context.Context, runID, instanceID string, leaseSeconds int) (bool, error)
	CompleteServiceRun(ctx context.Context, runID, resultID string, result json.RawMessage, usage json.RawMessage) (bool, error)
	FailServiceRun(ctx context.Context, runID, errorText string, retryable bool) error
	// CreateTopic 幂等建私聊 topic（ON CONFLICT DO NOTHING）。
	//
	// 必需：`messages.topic_id` 有外键指向 `topics`，而投递是直接 INSERT 消息。
	// 调用方和这个 bot 从来没聊过天时 topic 不存在 —— 而这正是「别人的 agent
	// 调我的 bot」的主场景，漏了就是一条 foreign key 报错（实测跨账号首调必炸）。
	CreateTopic(id, topicType string, ownerID int64) error
	// SaveMessageWithMetadata 是投递用的落库：要带 catsco_bot_session_id，
	// 才能让 bot 知道这次调用该归到哪个会话（普通 SaveMessage 存不了 metadata）。
	SaveMessageWithMetadata(topicID string, fromUID int64, content string, blocks []types.ContentBlock, mode, role, msgType string, replyTo int64, clientMsgID string, metadata map[string]interface{}) (int64, bool, error)
	GetMessagesSince(topicID string, sinceID int64, limit int) ([]*types.Message, error)
	GetBotOwner(botUID int64) (int64, error)
}

// botRunnerHub 是推送与在线判定能力（Hub 实现）。
type botRunnerHub interface {
	// DeliverServiceBotPrompt 的最后一个参数是 bot 会话 id：bot 收的是 WS 实时消息，
	// 不轮询消息表，所以会话 id 必须随推送一起到它手上。
	DeliverServiceBotPrompt(callerUID, botUID int64, topicID, text string, msgID int64, botSessionID string) bool
	BotRuntimeOnline(botUID int64) bool
}

// ServiceBotRunner 把 run 转交给 bot 执行。
type ServiceBotRunner struct {
	store botRunnerStore
	hub   botRunnerHub
	// —— 以下是**进程内显式覆盖**，零值 = 不覆盖（走平台默认 + definition）。——
	//
	// 保留它们是为了不改变已有语义：测试用它把超时压到 100ms，运维脚本也可能
	// 用它钉一个值。显式设置的值**不做区间夹取** —— 那是代码里写死的意图，
	// 不是外部配置里的手滑。
	timeout        time.Duration
	poll           time.Duration
	settle         time.Duration
	ambiguityGrace time.Duration
	// progressSilence 覆盖「bot 说还在干」后的额外等待上限（0=用平台默认）。
	progressSilence time.Duration

	// loops 是委托链回环表。run 收尾时靠它摘掉本次建立的边 —— 边存在的时间
	// 应当正好等于「下游还在为上游干活」的那段，而不是一个固定窗口。
	// 可能为 nil（测试或未装配），所有调用都做了空值保护。
	loops *serviceAgentCallLoopGuard

	// inFlight 记录每个 topic 上正在等回复的 run 数。
	// topic 是「两个人之间的私聊」，同一个账号并发调同一个 bot 时会共用它，
	// 所以「有几单在等」决定了「bot 的一条没标 reply_to 的回复能不能归给我」。
	mu       sync.Mutex
	inFlight map[string]int
}

// NewServiceBotRunner 构造 runner；store/hub 缺能力时返回 nil（能力关闭）。
//
// 不再预置计时字段：那会让「零值 = 没覆盖」的约定失效，也把平台默认写死在
// 两个地方。默认值统一由 platformBotRunnerTuning() 提供。
func NewServiceBotRunner(db any, hub botRunnerHub) *ServiceBotRunner {
	store, ok := db.(botRunnerStore)
	if !ok || hub == nil {
		return nil
	}
	return &ServiceBotRunner{
		store:    store,
		hub:      hub,
		inFlight: map[string]int{},
	}
}

// tuningFor 解析一次 run 要用的参数。
//
//	平台默认(内置+env，已夹区间) ← definition.runner(已夹区间) ← 进程内显式覆盖(原值)
//
// 最后一层不夹区间，见字段注释。
func (r *ServiceBotRunner) tuningFor(definition json.RawMessage) botRunnerTuning {
	t := botRunnerTuningFor(definition)
	if r.timeout > 0 {
		t.Timeout = r.timeout
	}
	if r.poll > 0 {
		t.Poll = r.poll
	}
	if r.settle > 0 {
		t.Settle = r.settle
	}
	if r.ambiguityGrace > 0 {
		t.AmbiguityGrace = r.ambiguityGrace
	}
	return t
}

// BotExists 报告目标 bot 是否还存在（没被删除）。
//
// 用来把「应用指向了一个已删除的 bot」和「bot 只是没在线」区分开：两者的处置
// 完全不同 —— 前者要发布者改配置（重试无用），后者等它上线就好。
//
// 只有 ErrNoRows 才判「不存在」；其它错误（连不上库、超时）一律放行 —— 存储抖
// 一下就把正常调用挡掉，比偶尔晚一步发现 bot 被删更糟。预检是体验优化，不是
// 拦截点；真删了的话派发那一步还会再报一次 bot_not_found。
func (r *ServiceBotRunner) BotExists(botUID int64) bool {
	if r == nil || r.store == nil || botUID == 0 {
		return true
	}
	if _, err := r.store.GetBotOwner(botUID); err != nil {
		return !errors.Is(err, sql.ErrNoRows)
	}
	return true
}

// BotOnline 报告目标 bot 是否在线（运行时心跳）。
//
// 给调用面做「建 run 之前」的预检用：bot 不在线时投递必然失败，但那要等 run 建完
// 才发现 —— 调用方看到的是「受理成功 → 几秒后 failed」，白等一轮还要再查原因。
//
// 判不了就放行：hub 未注入、bot 身份缺失时返回 true。预检是体验优化，不能变成
// 新的拦截点（worker 型应用的 checkWorkerOnline 也是同样的 fail-open 取舍）。
func (r *ServiceBotRunner) BotOnline(botUID int64) bool {
	if r == nil || r.hub == nil || botUID == 0 {
		return true
	}
	return r.hub.BotRuntimeOnline(botUID)
}

// claimTopic 登记「我要在这个 topic 上等一条回复」，返回当前共有几单在等。
func (r *ServiceBotRunner) claimTopic(topic string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == nil {
		r.inFlight = map[string]int{}
	}
	r.inFlight[topic]++
	return r.inFlight[topic]
}

// releaseTopic 在 run 收尾时释放登记（defer）。
func (r *ServiceBotRunner) releaseTopic(topic string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == nil {
		return
	}
	if r.inFlight[topic] <= 1 {
		delete(r.inFlight, topic)
		return
	}
	r.inFlight[topic]--
}

// competingOnTopic 返回除了自己之外还有几单在同一 topic 上等。
func (r *ServiceBotRunner) competingOnTopic(topic string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == nil {
		return 0
	}
	if n := r.inFlight[topic] - 1; n > 0 {
		return n
	}
	return 0
}

// ParseBotRunnerBotUID 判断是不是 bot runner，是则返回目标 bot。
func ParseBotRunnerBotUID(definition json.RawMessage) (int64, bool) {
	if len(definition) == 0 {
		return 0, false
	}
	var parsed struct {
		Runner *struct {
			Type   string `json:"type"`
			BotUID int64  `json:"bot_uid"`
		} `json:"runner"`
	}
	if err := json.Unmarshal(definition, &parsed); err != nil || parsed.Runner == nil {
		return 0, false
	}
	if strings.ToLower(strings.TrimSpace(parsed.Runner.Type)) != ServiceAgentRunnerBot {
		return 0, false
	}
	if parsed.Runner.BotUID <= 0 {
		return 0, false
	}
	return parsed.Runner.BotUID, true
}

// MaybeDispatch 在 run 新建成功后调用：bot runner 才投递，其余静默忽略。
//
// 投递失败直接改 run 状态（fail），不留给「恢复扫描」——这里没有可以重放的副作用：
// 消息发出去就已经发了，重发会让 bot 收到两遍同一个请求。
//
// **这里必须保持零阻塞**。它跑在 MCP `tools/call` 的返回路径上，而这个功能对
// 主 Agent 的承诺就是「立刻拿到运行句柄」。所以：
//   - 只做内存里的 JSON 解析（判断是不是 bot runner、目标 bot 是谁）；
//   - **任何 DB 读一律放进后台 goroutine**（见 execute）。
//
// 早期版本在这里同步读一次最新 definition，意在热更新，代价是给「零阻塞」
// 偷偷加了最多 2 秒 —— 拿核心承诺去换次要需求，不能接受。
func (r *ServiceBotRunner) MaybeDispatch(ctx context.Context, agent store.ServiceAgentDescriptor, run store.ServiceRun, callerUID int64) {
	_ = ctx // 请求级 ctx 不往下传：本函数返回后它就会被取消，而后台还要干活。
	if r == nil {
		return
	}
	definition := json.RawMessage(agent.DefinitionJSON)
	botUID, ok := ParseBotRunnerBotUID(definition)
	if !ok {
		return
	}
	req := botRunRequest{
		RunID:         run.RunID,
		SessionID:     run.SessionID,
		Slug:          agent.Slug,
		BotUID:        botUID,
		AgentID:       agent.ID,
		AgentOwnerUID: agent.OwnerUID,
		CallerUID:     callerUID,
		Input:         run.Input,
		// 会话快照：热更新读不到时用它（不能因为一次读失败就不派活）。
		SnapshotDefinition: definition,
	}
	go r.execute(req)
}

// applyFreshConfig 用**最新** definition 覆盖本次 run 的执行配置。
//
// 为什么要重读：MCP 会话建立时把 definition 快照进了工具闭包，用快照意味着
// 「改了应用配置，已连接的调用方要重开 MCP 会话才生效」—— 那不叫实时更新。
//
// 热更新范围：
//   - 执行参数（超时/静默/歧义窗口/轮询）—— 总是跟随最新配置；
//   - `bot_uid` —— 最新配置**仍然是 bot runner** 时才跟随。
//
// 为什么 `bot_uid` 要加条件：`runner.type` 是在 invoke 时就定下的（它决定了
// 本次返回句柄还是同步等），不能跑到一半把执行者从 bot 变成 worker。
// 但如果最新配置依旧是 bot、只是换了人，那就应该换 —— 用户改了配置却发现
// 不生效，才是真正的隐患。
func (r *ServiceBotRunner) applyFreshConfig(req botRunRequest) botRunRequest {
	fresh := r.freshDefinition(req.AgentID, req.Slug, req.SnapshotDefinition)
	if freshBotUID, ok := ParseBotRunnerBotUID(fresh); ok {
		req.BotUID = freshBotUID
	}
	req.Tuning = r.tuningFor(fresh)
	return req
}

// freshDefinition 尽力取到最新 definition；取不到就用会话快照。
//
// 用 context.Background() 而不是请求 ctx：调用它的时候请求早已返回，
// 请求 ctx 已经（或即将）被取消，拿它去查库会立刻失败 —— 然后「热更新永远
// 退化成快照」，而且不报错。这是个容易静默失效的点，所以固定用后台 ctx。
func (r *ServiceBotRunner) freshDefinition(agentID int64, slug string, snapshot json.RawMessage) json.RawMessage {
	reader, ok := r.store.(botDefinitionReader)
	if !ok || agentID <= 0 {
		return snapshot
	}
	ctx, cancel := context.WithTimeout(context.Background(), botDefinitionReadTimeout)
	defer cancel()
	// definitionRev=0 = 最新版本。
	_, _, fresh, err := reader.GetServiceAgentDefinition(ctx, agentID, 0)
	if err != nil || len(fresh) == 0 {
		if err != nil {
			log.Printf("bot runner: read fresh definition agent=%s: %v (fallback to session snapshot)", slug, err)
		}
		return snapshot
	}
	return json.RawMessage(fresh)
}

// botDefinitionReader 是「按 agent 取最新 definition」的窄能力。
// 与 botRunnerStore 分开：缺这个能力时热更新退化成会话快照，其余功能不受影响。
type botDefinitionReader interface {
	GetServiceAgentDefinition(ctx context.Context, agentID int64, definitionRev int64) (slug string, thresholdSeconds int, definitionJSON json.RawMessage, err error)
}

// botRunCounter 是「数一下同一个应用还有几单在跑」的窄能力（并发闸用）。
// 与 botRunnerStore 分开：缺这个能力时并发闸自动关闭，其余功能不受影响。
type botRunCounter interface {
	CountActiveServiceRunsForAgent(ctx context.Context, agentID int64, excludeRunID string) (int, error)
}

type botRunRequest struct {
	RunID     string
	SessionID string
	Slug      string
	BotUID    int64
	// AgentID 用于后台重读最新 definition（热更新）。
	AgentID int64
	// AgentOwnerUID 是应用主人的 uid，用来校验 bot 归属（0 = 平台内部应用，不校验）。
	AgentOwnerUID int64
	CallerUID     int64
	Input         json.RawMessage
	// SnapshotDefinition 是 invoke 时拿到的那份（会话快照），热更新读不到时兜底。
	SnapshotDefinition json.RawMessage
	// Tuning 是本次 run 的执行参数（已合并平台默认与 definition 覆盖）。
	// 每次调用重新解析，所以改应用配置下一次调用就生效。
	Tuning botRunnerTuning
}

func (r *ServiceBotRunner) execute(req botRunRequest) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("bot runner panic run=%s: %v", req.RunID, rec)
			_ = r.store.FailServiceRun(context.Background(), req.RunID, "bot_runner_panic", false)
		}
	}()
	// 回环边必须在**所有**退出路径上摘掉，包括「抢不到 run / bot 已换人 / 并发闸拦住」
	// 这些还没开始干活就返回的分支。以前这个 defer 放在 StartServiceRun 成功之后，
	// 上面那些分支会把边留到 TTL 过期（10 分钟），窗口内正当的委派会被误判成绕圈。
	if r.loops != nil {
		defer r.loops.releaseRun(req.RunID)
	}

	// 热更新放在这里（后台 goroutine）而不是 MaybeDispatch：
	// 那张「立即返回运行句柄」的承诺不能被一次 DB 读拖慢。
	// 只影响「bot 多快开始干活」（最多 2s），不影响调用方拿到句柄的时间。
	req = r.applyFreshConfig(req)

	tuning := req.Tuning
	if tuning.Timeout <= 0 {
		// 兜底：调用方造了零值（或直接调 execute）时不能变成「立刻超时」。
		tuning = r.tuningFor(req.SnapshotDefinition)
	}

	// 先在落库层面占住这个 run：抢不到说明已被别的执行方接管 / 已终态。
	started, err := r.store.StartServiceRun(context.Background(), req.RunID,
		fmt.Sprintf("bot:%d", req.BotUID), int(tuning.Timeout.Seconds())+60)
	if err != nil {
		log.Printf("bot runner: start run=%s: %v", req.RunID, err)
		return
	}
	if !started {
		log.Printf("bot runner: run=%s already taken or terminal; skip", req.RunID)
		return
	}

	// 只确认目标 bot 还存在。谁能调这个应用由**可见性**决定（MCP invoke 已经做过了）：
	// 系统里不能有两套不同步的授权规则 —— 曾经这里另有一套「bot 的 owner 必须在白名单里」，
	// 结果应用标了 public，别人调用仍然报 bot_not_shared；而且两处都要改才能改对。
	// 现在 runner 只管执行，授权口径只有一个（store.ServiceAgentVisibleTo）。
	owner, err := r.store.GetBotOwner(req.BotUID)
	if err != nil {
		r.fail(req, "bot_not_found: 目标 bot 不存在或已删除")
		return
	}
	// 归属校验（第二道，发布接口是第一道）：definition 会热更新，内部发布通道也不经过
	// 用户发布接口 —— 只靠发布时拦一次，等于给「改一次配置就把别人的 bot 挂过来」留了门。
	if req.AgentOwnerUID != 0 && owner != req.AgentOwnerUID {
		r.fail(req, "bot_not_owned: 这个应用指向的 bot 不属于应用主人，已停止派发（配置被改过？请让发布者重新选自己的 bot）")
		return
	}
	if !r.hub.BotRuntimeOnline(req.BotUID) {
		// 不干等：bot 没有在线运行时，消息发出去也没人接。
		r.fail(req, "bot_offline: 目标 bot 当前不在线，等它上线后再试")
		return
	}
	// 并发闸（可选能力）：设了上限就先数一下同一个应用还有几单在跑。
	//
	// 没有上限时的行为是「全部派出去 → bot 侧串行 → 后面几单等到超时」，而超时的
	// 文案是「bot 没回」，把「在忙」说成了「不理你」。查不到数就放行 —— 预检是体验
	// 优化，不能因为存储抖一下就拒绝正常调用。
	if tuning.MaxInflight > 0 {
		if counter, ok := r.store.(botRunCounter); ok {
			if n, err := counter.CountActiveServiceRunsForAgent(context.Background(), req.AgentID, req.RunID); err == nil && n >= tuning.MaxInflight {
				r.failRetryable(req, fmt.Sprintf(
					"bot_busy: 这个应用同时已经有 %d 单在跑（上限 %d），这一单没有派出去。请稍后重试；同一任务不要反复重发",
					n, tuning.MaxInflight))
				return
			}
		}
	}

	prompt := botRunnerPromptText(req.Input)
	if prompt == "" {
		r.fail(req, "bot_empty_input: 没有可发送的内容（input 里要有 text）")
		return
	}
	topic := botRunnerTopic(req.CallerUID, req.BotUID)
	// 先占住 topic 再发消息：之后任何「这条回复算谁的」判断都要用到「有几单在等」。
	r.claimTopic(topic)
	defer r.releaseTopic(topic)

	// topic 必须先存在：messages.topic_id 有外键，而这里只 INSERT 消息不建会话。
	// 调用方从没和这个 bot 聊过天时（别人的 agent 第一次调你的 bot）没有这条记录。
	// owner 记调用方：这是他的私聊窗口，也跟平台其它建 p2p topic 的地方一致。
	if err := r.store.CreateTopic(topic, "p2p", req.CallerUID); err != nil {
		r.fail(req, "bot_topic_failed: "+err.Error())
		return
	}

	// 水位线 = 我们这条消息的 id：只认它之后 bot 发的消息，
	// 避免把历史回复（或同一 topic 的并发调用）当成本次结果。
	//
	// 同时把会话 id 写进 metadata：bot 侧据此决定「这是新会话还是接着上一个说」。
	// 这是「上下文不串」的那一半；「结果不串」靠下面的 reply_to 认领。
	msgID, _, err := r.store.SaveMessageWithMetadata(
		topic, req.CallerUID, prompt, nil, "", "", "text", 0, "",
		map[string]interface{}{
			"catsco_bot_call":     true,
			"catsco_caller_uid":   req.CallerUID,
			botSessionMetadataKey: req.SessionID,
		},
	)
	if err != nil {
		r.fail(req, "bot_send_failed: "+err.Error())
		return
	}
	if msgID <= 0 {
		r.fail(req, "bot_send_failed: 消息未能落库")
		return
	}
	if !r.hub.DeliverServiceBotPrompt(req.CallerUID, req.BotUID, topic, prompt, msgID, req.SessionID) {
		// 落库成功了但推送失败：bot 那边永远看不到。如实失败，让调用方重试。
		r.fail(req, "bot_deliver_failed: 消息未能送达目标 bot 的连接，请稍后重试")
		return
	}

	reply, attachments, waitErr := r.awaitReply(topic, msgID, req.BotUID, req.SessionID, tuning)
	if waitErr != nil {
		r.fail(req, waitErr.Error())
		return
	}

	// 只在「提到了本机路径、却什么都没发出来」时补一句如实说明。
	//
	// 不去读对方的磁盘、不去猜内容 —— 那是另一件事（等于给了一个「把本机任意可读
	// 文件传出去」的原语）。这里只解决「两边都看不出发生过什么」：调用方据此能
	// 让 bot 重新用 send_file 发一次，而不是把那句路径当成拿到了文件。
	if note := botRunnerUndeclaredPathNote(reply, attachments); note != "" {
		reply = strings.TrimSpace(reply) + "\n\n" + note
		log.Printf("bot runner: run=%s 答复提到本机路径但没有附件，已附平台提示", req.RunID)
	}

	envelope := ServiceRunEnvelope{
		ContractVersion: serviceRunResultContractVersion,
		RunID:           req.RunID,
		SessionID:       req.SessionID,
		Status:          ServiceRunCompleted,
		Output:          mustJSON(map[string]any{"text": reply}),
		Attachments:     attachments,
	}
	// Files 是给旧调用方看的扁平清单（只放地址）；Attachments 才是完整信息。
	// 两个都填：XiaoBa 现有版本读 files，新版本可以读 attachments。
	for _, a := range attachments {
		if a.URL != "" {
			envelope.Files = append(envelope.Files, a.URL)
		}
	}
	if _, err := r.store.CompleteServiceRun(context.Background(), req.RunID,
		"res_"+req.RunID, mustJSON(envelope), nil); err != nil {
		log.Printf("bot runner: complete run=%s: %v", req.RunID, err)
	}
}

// awaitReply 轮询消息表，等 bot 回一条属于**本次调用**的 text。
//
// 认领规则（依次）：
//
//  1. `reply_to == promptID`：明确指向我们那条 prompt，一定是我要的；
//  2. metadata 里 `catsco_bot_session_id` 与本次会话一致：明确表态属于哪个会话；
//  3. 两者都没有（旧客户端）：只有「这个 topic 上除我之外没有别的单在等」时才认领 ——
//     单子在等的时候，任何一条流水归谁都有可能。
//
// 之所以要这么严：topic 是固定的 `p2p_调用方_bot`，同一账号并发调同一个 bot 时
// 所有 run 共用一条消息流；只按「bot 说的都算」会把别人的结果当自己的一起交出去。
//
// `mode=code` 的消息一律跳过：那是 bot 的工具调用/输出展示（skill 文件内容、
// execute_shell 的 stdout），不是给调用方的答案。实测一次调用的真答复只有 121 字，
// 而过程展示有 10KB —— 混进来就变成把日志当结果。
//
// 一旦收到过**明确归属**的回复，就不再接受第 3 类兜底：那时出现的无标记消息
// 必然是过程记录或别人的活，收进来只会污染答案。旧客户端不受影响（它从头到尾
// 都没有明确归属，全部走兜底）。
//
// 若第 3 类消息出现过、但因为有别的单在等而无法认领，`botReplyAmbiguousGrace`
// 之后如实失败：已经看见 bot 回了，就不是「没回」，而是「分不清」，这两件事
// 对调用方的处置完全不同（前者该升级客户端，后者该重试）。
//
// 收到第一段后每出现新内容就把收尾时间往后推 settle，直到静默：bot 会把长回复
// 按 4000 字切段，收得太早会把答案截断。
func (r *ServiceBotRunner) awaitReply(topic string, promptID int64, botUID int64, sessionID string, tuning botRunnerTuning) (string, []ServiceRunAttachment, error) {
	deadline := time.Now().Add(tuning.Timeout)
	var (
		sinceID     = promptID
		parts       []string
		seen        = map[int64]bool{}
		settleTill  time.Time
		size        int
		sawUnowned  bool
		sawOwned    bool
		ambiguousAt time.Time
		// bot 明确说过「我还在干」：此时不许按静默窗口收尾。
		stillWorking   bool
		lastProgressAt time.Time
		// 附件单独收一份结构化数据：文本说明是给人看的，这一份是给调用方程序化
		// 处理用的（重新发出去 / 渲染成下载卡片），两者不能互相替代。
		attachments []ServiceRunAttachment
		seenAttach  = map[string]bool{}
	)
	collect := func(a ServiceRunAttachment) {
		key := a.URL + "\x00" + a.Name
		if seenAttach[key] {
			return
		}
		seenAttach[key] = true
		attachments = append(attachments, a)
	}
	for {
		msgs, err := r.store.GetMessagesSince(topic, sinceID, botRunnerMessageBatch)
		if err != nil {
			return "", nil, fmt.Errorf("bot_reply_read_failed: %v", err)
		}
		for _, msg := range msgs {
			if msg == nil || seen[msg.ID] {
				continue
			}
			seen[msg.ID] = true
			if msg.ID > sinceID {
				sinceID = msg.ID
			}
			if msg.FromUID != botUID {
				continue
			}
			// 附件不能默默丢掉。bot 有时把长结果写成文件（或图片）发出来，正文只剩
			// 一句「清单.txt 已发送。」—— 只收 text 的话，调用方拿到的是这句话，
			// 文件本身和它的地址都消失了，而且看不出发生过什么。
			//
			// 图片和文件走同一条路：send_file 对 png/jpg 发的是 type=image，payload
			// 形状一模一样。以前只认 file，于是 bot 生成的图片被整个丢弃 —— 那是最
			// 常见的一类产物。
			isAttachment := msg.MsgType == "file" || msg.MsgType == "image"
			if !isAttachment && msg.MsgType != "text" {
				continue
			}
			// 过程展示（工具调用、stdout、skill 内容）不是答案。
			//
			// 但它是一条**可观测的「bot 还在干」证据**：老客户端（v1.6.4 及更早，
			// 也就是线上全部真实 bot）不会发「回合结束」标记，平台只能靠静默窗口
			// 猜。于是最常见的失败形状是：bot 先说一句「收到，我开始跑」，接着
			// 调工具，工具跑完才给正文 —— 静默窗口在工具还在跑的时候就到点，把
			// 那句前言当答案交了出去。生产数据里，近 7 天交付的 bot 答案有近一半
			// 是这种前言。
			//
			// 关键是宽限不能用 Settle：Settle 是「bot 说完一句可能还要接着说」的
			// 停顿（5s 量级），而一条命令跑 20 秒是常态。生产实测（bot 1619）：
			// 前言 14:14:48、工具调用 14:14:49、命令 `sleep 20`，按 5s 刷新仍在
			// 14:14:54 就把前言交了。所以这里用「说过还在干」的同一份封顶
			// （progressSilenceLimit，45s）：它本来就表示「有正面证据表明 bot 在
			// 干活，等到这段沉默为止」，语义完全一致。
			//
			// 只推不无限等：最后一条工具活动之后仍然只有这一段宽限，bot 中途崩掉
			// 不会把调用方一直挂到整单超时；没有工具活动的 bot（纯聊天）行为一个
			// 字都不变，不会因为这条多等。
			if strings.EqualFold(strings.TrimSpace(msg.Mode), "code") {
				if len(parts) > 0 {
					settleTill = time.Now().Add(r.progressSilenceLimit())
				}
				continue
			}
			txt := strings.TrimSpace(msg.Content)
			var attachment ServiceRunAttachment
			if isAttachment {
				parsed, ok := botRunnerAttachmentOf(msg.Content, msg.MsgType)
				if ok {
					attachment = parsed
					txt = parsed.note()
				} else {
					txt = ""
				}
				if txt == "" {
					// payload 解不出来：退回原行为（既不丢证据也不伪造附件）。
					continue
				}
			}
			ownership := botReplyOwnershipOf(msg, promptID, sessionID)
			switch ownership {
			case botReplyOwnedExplicit:
				sawOwned = true
			case botReplyOwnedFallback:
				// 已经有过明确归属的回复，或者还有别的单在抢：这条不可能是答案。
				//
				// 附件例外：老客户端发附件走的是另一条发送路径，天生不带 reply_to
				// 和会话标记（实测：正文带标记、紧接着的 file/image 消息什么都没有）。
				// 按「见过明确归属就关门」处理的话，附件会被稳定地当成过程记录丢掉 ——
				// 那正是「bot 明明发了文件，用户却什么都收不到」的形态。
				// 所以只保留「有没有别的单在抢」这一个条件：抢的时候仍然不认。
				if r.competingOnTopic(topic) > 0 || (sawOwned && !isAttachment) {
					sawUnowned = true
					if ambiguousAt.IsZero() {
						ambiguousAt = time.Now()
					}
					continue
				}
			default:
				// 明确指回**别人**的 prompt：跟本次调用无关，直接跳过。
				//
				// 这里以前错算成「分不清」的证据，后果很实际：上一单超时后 bot 还在干活，
				// 它的迟回复带着上一单的 reply_to 落进这个 topic，正在等的下一单看见
				// 「有回复但我认领不了」，grace 一过就报 bot_reply_ambiguous 失败 ——
				// 明明分得清（那条写着别人的 prompt id），却被判成看不清。
				// 归属明确的他单消息不是证据，是噪音，不该影响本次调用的判断。
				continue
			}
			// 回合结束信号：bot 明确说「这一轮我说完了」，立刻交付，不等 settle。
			//
			// 放在归属判定**之后**：只有这条消息确实归本次调用时才信它。否则同一条
			// topic 上另一个 run 的回合结束信号会把我们这单提前收尾 —— 收回去的还是
			// 一句前言，而且看起来一切正常。
			//
			// 允许「只有信号、没有正文」：bot 完全可能先把正文发完，再补一条纯信号
			// （正文那几条已经在 parts 里了）。但如果 parts 还是空的，就别信 ——
			// 空答案对调用方毫无价值，退回 settle 兜底更安全。
			turnState := botRunnerTurnState(msg, sessionID)
			if turnState == botTurnFinished {
				if txt != "" && size+len(txt) <= maxBotRunnerReplyBytes {
					parts = append(parts, txt)
				}
				if attachment.URL != "" || attachment.Name != "" {
					collect(attachment)
				}
				if len(parts) > 0 {
					return strings.Join(parts, "\n\n"), attachments, nil
				}
				continue
			}
			if txt == "" {
				continue
			}
			if size+len(txt) > maxBotRunnerReplyBytes {
				continue
			}
			size += len(txt)
			parts = append(parts, txt)
			if attachment.URL != "" || attachment.Name != "" {
				collect(attachment)
			}
			settleTill = time.Now().Add(tuning.Settle)
			// 「我还在干」：这一段是过程话，绝不能因为静默窗口到点就当成最终答案交出去。
			//
			// 这正是生产事故的形状：bot 说「收到，我先让分镜工具出方案」，然后真的去
			// 干活，14.5 秒后才给正文；平台 5 秒后收尾，把前言交付了，调用方还以为
			// 这就是全部结果。有了这个显式表态就不必猜。
			//
			// 但不能无限等：bot 说完「还在干」之后崩掉的话，用户会一直挂到整单超时
			// （默认 5 分钟）才知道结果。所以给一个远大于任何单步工具耗时的封顶
			// （progressSilenceLimit，45s），超过就交付已收到的内容 —— 有东西总比
			// 干等到超时好。
			if turnState == botTurnInProgress {
				stillWorking = true
				lastProgressAt = time.Now()
			}
		}
		// 静默窗口到点就收尾 —— 但**只有当 bot 没有明确说「我还在干」时**。
		//
		// 这一条是整件事的关键：没有它，「说完了」信号只能让正常收尾更快，救不了
		// 「前言先说、正文晚十几秒」这种最伤人的情形（平台 5 秒后就把前言交了）。
		// 封顶之后仍要交付，避免 bot 说完「还在干」就崩掉、用户一直挂到整单超时。
		readyToSettle := time.Now().After(settleTill)
		if stillWorking && time.Since(lastProgressAt) < r.progressSilenceLimit() {
			readyToSettle = false
		}
		if len(parts) > 0 && readyToSettle {
			return strings.Join(parts, "\n\n"), attachments, nil
		}
		// 看见回复了却认不了：别干等到超时，尽快让调用方知道是「分不清」而非「没回」。
		if len(parts) == 0 && sawUnowned && time.Since(ambiguousAt) > tuning.AmbiguityGrace {
			return "", nil, fmt.Errorf(
				"bot_reply_ambiguous: 目标 bot 的回复没有指回本次调用（缺 reply_to），" +
					"而同一会话上还有别的调用在等，无法判断哪条是这次的。请升级客户端后重试，或改为串行调用")
		}
		if time.Now().After(deadline) {
			if len(parts) > 0 {
				// 已经有内容：按「说到一半」交付，比丢掉更有用。
				return strings.Join(parts, "\n\n"), attachments, nil
			}
			return "", nil, fmt.Errorf("bot_timeout: 等了 %s 目标 bot 没有回复", tuning.Timeout)
		}
		time.Sleep(tuning.Poll)
	}
}

// botRunnerLocalPathPattern 匹配「本机绝对路径 + 扩展名」的形态。
//
// 只覆盖确实属于执行环境本地的根，**故意不含 /uploads/** —— 那是平台自己的地址，
// 调用方本来就取得到，把它算成「没交付的文件」只会制造噪音。
var botRunnerLocalPathPattern = regexp.MustCompile(
	`(?:[A-Za-z]:\\[^\s"'”）)】]*|/(?:tmp|var|home|Users|root|opt|srv|mnt|private)/[^\s"'”）)】]*)\.[A-Za-z0-9]{1,8}`)

// botRunnerUndeclaredPathNote 在「答复提到本机路径、却没有任何附件」时补一句如实说明。
//
// 这是「提醒失败」时的兜底：平台已经从两头提醒过（派活时说明 + 客户端系统提示），
// 但模型仍可能只写路径。这时至少要让调用方知道文件没到手 —— 否则它会把那句路径
// 当成「文件已交付」转告用户，用户去点一个不存在的地址。
//
// 有附件时一句话都不加：那时路径只是补充信息，不需要解释。
func botRunnerUndeclaredPathNote(reply string, attachments []ServiceRunAttachment) string {
	if len(attachments) > 0 || strings.TrimSpace(reply) == "" {
		return ""
	}
	if !botRunnerLocalPathPattern.MatchString(reply) {
		return ""
	}
	return "（平台提示：上面提到了本机文件路径，但本次没有任何文件随结果返回 —— " +
		"那个文件在对方机器上，取不到。需要文件本身的话，请让对方用 send_file 发送。）"
}

// botReplyAttachmentNote 把一条 file/image 消息翻成调用方能读懂的附件说明。
//
// 为什么需要：bot 有时把长结果写成文件发出来，正文只剩一句「清单.txt 已发送。」。
// 平台原来只收 msg_type=text，文件被整个丢掉 —— 用户等了几十秒，拿到的是一句
// 「已发送」，既没有内容也没有线索，而且完全看不出中间发生过什么。
//
// 内容确实取不到（文件在平台的文件存储里），所以这里只能如实交付「有这么个文件、
// 叫什么、多大、在哪儿」。调用方据此可以告诉用户「对方生成的是文件，请到对话里取」，
// 而不是自己编一段内容。
//
// 消息形如：{"type":"file","payload":{"url":"/uploads/files/xxx.txt","name":"n.txt","size":430}}
// 图片走同一条路（send_file 对 png/jpg 发的是 type=image，payload 形状一样）。
func botRunnerAttachmentNote(content string) string {
	attachment, ok := botRunnerAttachmentOf(content, "file")
	if !ok {
		return ""
	}
	return attachment.note()
}

// botRunnerAttachmentOf 解析 file/image 消息的 payload。
//
// msgType 决定 kind：图片不能被当成普通文件展示（客户端会当附件下载，而不是
// 直接显示）。解析失败时 ok=false —— 宁可退回原来的行为，也不要伪造一个附件。
func botRunnerAttachmentOf(content string, msgType string) (ServiceRunAttachment, bool) {
	var parsed struct {
		Payload struct {
			URL  string `json:"url"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &parsed); err != nil {
		return ServiceRunAttachment{}, false
	}
	name := strings.TrimSpace(parsed.Payload.Name)
	url := strings.TrimSpace(parsed.Payload.URL)
	if name == "" && url == "" {
		return ServiceRunAttachment{}, false
	}
	if name == "" {
		name = "未命名文件"
	}
	kind := "file"
	if strings.EqualFold(strings.TrimSpace(msgType), "image") {
		kind = "image"
	}
	return ServiceRunAttachment{
		Name: name,
		// 绝对地址：调用方不知道平台域名，相对路径对它是死字符串；
		// 对用户来说也只有绝对地址才会被渲染成可点的链接。
		URL:  serviceAgentAbsoluteUploadURL(url),
		Size: parsed.Payload.Size,
		Kind: kind,
	}, true
}

// note 是人类可读的一行附件说明（也是文本通道里唯一能交付的东西）。
func (a ServiceRunAttachment) note() string {
	note := "📎 附件：" + a.Name
	if a.Size > 0 {
		note += fmt.Sprintf("，%d 字节", a.Size)
	}
	if a.URL != "" {
		note += "，位置 " + a.URL
	}
	return note
}

// serviceAgentAbsoluteUploadURL 把平台内的相对上传路径补成绝对地址。
//
// 只处理以 "/" 开头的站内路径：外链原样保留（外部 runner 可能直接给 CDN 地址），
// 已经是绝对地址的也不动。
func serviceAgentAbsoluteUploadURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if !strings.HasPrefix(raw, "/") {
		return raw
	}
	return strings.TrimRight(publicBaseURL(nil), "/") + raw
}

// botTurnState 是一条 bot 消息对「这一轮说完了没有」的表态。
type botTurnState int

const (
	// botTurnUnspecified：老客户端，没这个字段 —— 只能靠 settle 静默窗口猜。
	botTurnUnspecified botTurnState = iota
	// botTurnInProgress：明确说「还没完，我还在干」。带这个标记的消息绝不能
	// 触发按 settle 收尾 —— 那是把「收到，我先去做」当前言的根因。
	botTurnInProgress
	// botTurnFinished：明确说「这一轮说完了」，立刻交付。
	botTurnFinished
)

// botRunnerTurnState 解析 `catsco_turn_done`。
//
// 认这些写法（向后兼容，也方便 bot 侧先上简单的）：
//
//	{"catsco_turn_done": true}               -> finished
//	{"catsco_turn_done": false}              -> inProgress
//	{"catsco_turn_done": "<本会话 id>"}       -> finished
//	{"catsco_turn_done": "progress"}         -> inProgress（"working"/"running" 同）
//	其它取值 / 别的会话 id / 没有这个键         -> unspecified（不表态）
//
// 为什么需要 inProgress：只有「说完」这一个信号时，平台仍然会在 settle 窗口到点时
// 把已经收到的那段话交出去。生产实测的正是这个形状 —— bot 先说「收到，我先让分镜
// 工具出方案」，14.5 秒后才给正文，而平台 5 秒后就把那句前言当答案交付了。
// 「说完了」只能让正常收尾更快，救不了这次；必须有一个「还没完」的显式表态。
func botRunnerTurnState(msg *types.Message, sessionID string) botTurnState {
	if msg == nil || msg.Metadata == nil {
		return botTurnUnspecified
	}
	raw, ok := msg.Metadata[botTurnDoneMetadataKey]
	if !ok {
		return botTurnUnspecified
	}
	switch value := raw.(type) {
	case bool:
		if value {
			return botTurnFinished
		}
		return botTurnInProgress
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return botTurnUnspecified
		}
		switch {
		case strings.EqualFold(trimmed, "true"):
			return botTurnFinished
		case strings.EqualFold(trimmed, "false"),
			strings.EqualFold(trimmed, "progress"),
			strings.EqualFold(trimmed, "working"),
			strings.EqualFold(trimmed, "running"):
			return botTurnInProgress
		}
		// 给了具体会话 id：必须就是本次调用，否则是别人的回合结束。
		if sessionID != "" && trimmed == sessionID {
			return botTurnFinished
		}
		return botTurnUnspecified
	default:
		return botTurnUnspecified
	}
}

// botRunnerTurnDone 只要「明确说完了」这一个判断（供现有测试与调用点使用）。
func botRunnerTurnDone(msg *types.Message, sessionID string) bool {
	return botRunnerTurnState(msg, sessionID) == botTurnFinished
}

// progressSilenceLimit 解析「bot 说还在干」之后的额外等待上限。
// 显式设置优先（测试用它把 45s 压短），否则用平台默认。
func (r *ServiceBotRunner) progressSilenceLimit() time.Duration {
	if r != nil && r.progressSilence > 0 {
		return r.progressSilence
	}
	return botRunnerProgressSilenceLimit
}

// botReplyOwnership 是一条 bot 消息的归属判定结果。
//
// 区分「明确」与「兜底」是关键：明确归属的消息一旦出现过，「兜底」就该关门 ——
// 否则 bot 的工具过程记录会跟着答案一起被当成结果。
type botReplyOwnership int

const (
	// botReplyForeign：明确不是给这次调用的（比如 reply_to 指向别人的 prompt）。
	botReplyForeign botReplyOwnership = iota
	// botReplyOwnedExplicit：明确属于（reply_to 指回我的 prompt，或带了本会话 id）。
	botReplyOwnedExplicit
	// botReplyOwnedFallback：没有任何归属标记（旧客户端），只能靠「没人跟我抢」兜底。
	botReplyOwnedFallback
)

// botReplyOwnershipOf 判断一条 bot 的 text 归属于谁。
func botReplyOwnershipOf(msg *types.Message, promptID int64, sessionID string) botReplyOwnership {
	if msg == nil {
		return botReplyForeign
	}
	// 1) 明确指回我那条 prompt。
	if promptID > 0 && msg.ReplyTo == promptID {
		return botReplyOwnedExplicit
	}
	// 2) 明确声明了属于哪个 bot 会话。
	if sessionID != "" && msg.Metadata != nil {
		if got, ok := msg.Metadata[botSessionMetadataKey].(string); ok && got == sessionID {
			return botReplyOwnedExplicit
		}
	}
	// 3) 指了别人的 prompt：明确不是我的。
	if msg.ReplyTo != 0 {
		return botReplyForeign
	}
	// 4) 什么都没标：可能是旧客户端的答复，也可能是过程记录，交给「没人抢」兜底。
	return botReplyOwnedFallback
}

// botReplyBelongsTo 判断一条 bot 的 text 是不是本次调用的结果。
// competing 是同一 topic 上除自己之外还在等的单数（0 = 我是唯一一个）。
func botReplyBelongsTo(msg *types.Message, promptID int64, sessionID string, competing int) bool {
	switch botReplyOwnershipOf(msg, promptID, sessionID) {
	case botReplyOwnedExplicit:
		return true
	case botReplyOwnedFallback:
		return competing == 0
	default:
		return false
	}
}

func (r *ServiceBotRunner) fail(req botRunRequest, errorText string) {
	if err := r.store.FailServiceRun(context.Background(), req.RunID, errorText, false); err != nil &&
		!errors.Is(err, store.ErrServiceRunConflict) {
		log.Printf("bot runner: fail run=%s: %v", req.RunID, err)
	}
}

// failRetryable 与 fail 相同，但把「可重试」标出来：并发闸拦住这类失败重试就能成功，
// 执行环境的问题（bot 不在线、命令缺失）重试没有意义 —— 两者的区别要让调用方看见。
func (r *ServiceBotRunner) failRetryable(req botRunRequest, errorText string) {
	if err := r.store.FailServiceRun(context.Background(), req.RunID, errorText, true); err != nil &&
		!errors.Is(err, store.ErrServiceRunConflict) {
		log.Printf("bot runner: fail run=%s: %v", req.RunID, err)
	}
}

// botRunnerPromptText 把 run 的 input 变成发给 bot 的一段话。
//
// 约定：`{"text": "..."}` 直接取 text（最常见）；纯字符串直接用；其余整体按 JSON 发过去，
// 至少不丢信息 —— 对端能不能理解是它的事，平台不替它猜。
//
// 注意 `{"text":"   "}` 要当成「没有内容」而不是退回发原始 JSON：
// 作者显式声明了 text 字段却给了空白，这跟没填是同一件事。
func botRunnerPromptText(input json.RawMessage) string {
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err == nil {
		raw, hasText := fields["text"]
		if !hasText {
			return trimmed
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			// text 不是字符串（对象/数组）：整体下发，别猜。
			return trimmed
		}
		return strings.TrimSpace(text)
	}
	var asString string
	if err := json.Unmarshal(input, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	return trimmed
}

// botRunnerTopic 按平台约定拼私聊 topic：p2p_{小uid}_{大uid}。
func botRunnerTopic(a, b int64) string {
	if a > b {
		a, b = b, a
	}
	return fmt.Sprintf("p2p_%d_%d", a, b)
}

func mustJSON(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	defaultBotRunnerSettle = 2 * time.Second
	botRunnerMessageBatch  = 50
	// 单次回复的文本上限，防止对端刷屏把 run 结果撑爆。
	maxBotRunnerReplyBytes = 64 << 10

	// botSessionMetadataKey 是「这次调用属于哪个 bot 会话」的键。
	//
	// 值就是 run 的 service session id —— 调用方不传 session_id 平台就新建一个，
	// 传了就回到那个会话。bot 侧按它算自己的会话身份，于是：
	//
	//	不同调用 = 不同会话（互不污染上下文，也不会把上一个任务的结论带进来）
	//	同一 session_id = 同一个会话（追问能接上）
	//
	// 没有这个键时 bot 只能按 topic 分会话，而 topic 是「两个人之间的私聊」，
	// 所有调用挤在一条会话里 —— 上一个任务的判断会漏到下一个任务。
	botSessionMetadataKey = "catsco_bot_session_id"

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
		RunID:     run.RunID,
		SessionID: run.SessionID,
		Slug:      agent.Slug,
		BotUID:    botUID,
		AgentID:   agent.ID,
		CallerUID: callerUID,
		Input:     run.Input,
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

type botRunRequest struct {
	RunID     string
	SessionID string
	Slug      string
	BotUID    int64
	// AgentID 用于后台重读最新 definition（热更新）。
	AgentID   int64
	CallerUID int64
	Input     json.RawMessage
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
	if _, err := r.store.GetBotOwner(req.BotUID); err != nil {
		r.fail(req, "bot_not_found: 目标 bot 不存在或已删除")
		return
	}
	if !r.hub.BotRuntimeOnline(req.BotUID) {
		// 不干等：bot 没有在线运行时，消息发出去也没人接。
		r.fail(req, "bot_offline: 目标 bot 当前不在线，等它上线后再试")
		return
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

	reply, waitErr := r.awaitReply(topic, msgID, req.BotUID, req.SessionID, tuning)
	if waitErr != nil {
		r.fail(req, waitErr.Error())
		return
	}

	envelope := ServiceRunEnvelope{
		ContractVersion: serviceRunResultContractVersion,
		RunID:           req.RunID,
		SessionID:       req.SessionID,
		Status:          ServiceRunCompleted,
		Output:          mustJSON(map[string]any{"text": reply}),
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
func (r *ServiceBotRunner) awaitReply(topic string, promptID int64, botUID int64, sessionID string, tuning botRunnerTuning) (string, error) {
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
	)
	for {
		msgs, err := r.store.GetMessagesSince(topic, sinceID, botRunnerMessageBatch)
		if err != nil {
			return "", fmt.Errorf("bot_reply_read_failed: %v", err)
		}
		for _, msg := range msgs {
			if msg == nil || seen[msg.ID] {
				continue
			}
			seen[msg.ID] = true
			if msg.ID > sinceID {
				sinceID = msg.ID
			}
			if msg.FromUID != botUID || msg.MsgType != "text" {
				continue
			}
			// 过程展示（工具调用、stdout、skill 内容）不是答案。
			if strings.EqualFold(strings.TrimSpace(msg.Mode), "code") {
				continue
			}
			text := strings.TrimSpace(msg.Content)
			if text == "" {
				continue
			}
			ownership := botReplyOwnershipOf(msg, promptID, sessionID)
			switch ownership {
			case botReplyOwnedExplicit:
				sawOwned = true
			case botReplyOwnedFallback:
				// 已经有过明确归属的回复，或者还有别的单在抢：这条不可能是答案。
				if sawOwned || r.competingOnTopic(topic) > 0 {
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
			if size+len(text) > maxBotRunnerReplyBytes {
				continue
			}
			size += len(text)
			parts = append(parts, text)
			settleTill = time.Now().Add(tuning.Settle)
		}
		if len(parts) > 0 && time.Now().After(settleTill) {
			return strings.Join(parts, "\n\n"), nil
		}
		// 看见回复了却认不了：别干等到超时，尽快让调用方知道是「分不清」而非「没回」。
		if len(parts) == 0 && sawUnowned && time.Since(ambiguousAt) > tuning.AmbiguityGrace {
			return "", fmt.Errorf(
				"bot_reply_ambiguous: 目标 bot 的回复没有指回本次调用（缺 reply_to），" +
					"而同一会话上还有别的调用在等，无法判断哪条是这次的。请升级客户端后重试，或改为串行调用")
		}
		if time.Now().After(deadline) {
			if len(parts) > 0 {
				// 已经有内容：按「说到一半」交付，比丢掉更有用。
				return strings.Join(parts, "\n\n"), nil
			}
			return "", fmt.Errorf("bot_timeout: 等了 %s 目标 bot 没有回复", tuning.Timeout)
		}
		time.Sleep(tuning.Poll)
	}
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

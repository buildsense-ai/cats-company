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
	store   botRunnerStore
	hub     botRunnerHub
	timeout time.Duration
	poll    time.Duration
	settle  time.Duration
	// ambiguityGrace：看见回复却认不了时等多久才如实失败（测试可调小）。
	ambiguityGrace time.Duration
	// ownerWhitelist 非空时，只有这些账号名下的 bot 可以被当作工具调用。
	ownerWhitelist map[int64]bool
	// inFlight 记录每个 topic 上正在等回复的 run 数。
	// topic 是「两个人之间的私聊」，同一个账号并发调同一个 bot 时会共用它，
	// 所以「有几单在等」决定了「bot 的一条没标 reply_to 的回复能不能归给我」。
	mu       sync.Mutex
	inFlight map[string]int
}

// NewServiceBotRunner 构造 runner；store/hub 缺能力时返回 nil（能力关闭）。
func NewServiceBotRunner(db any, hub botRunnerHub) *ServiceBotRunner {
	store, ok := db.(botRunnerStore)
	if !ok || hub == nil {
		return nil
	}
	return &ServiceBotRunner{
		store:          store,
		hub:            hub,
		timeout:        defaultBotRunnerTimeout,
		poll:           defaultBotRunnerPollInterval,
		settle:         defaultBotRunnerSettle,
		ambiguityGrace: botReplyAmbiguousGrace,
		inFlight:       map[string]int{},
	}
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

// SetOwnerWhitelist 限制可被当作工具调用的 bot 归属（空=不限制）。
func (r *ServiceBotRunner) SetOwnerWhitelist(uids []int64) {
	if r == nil {
		return
	}
	if len(uids) == 0 {
		r.ownerWhitelist = nil
		return
	}
	set := make(map[int64]bool, len(uids))
	for _, uid := range uids {
		if uid > 0 {
			set[uid] = true
		}
	}
	r.ownerWhitelist = set
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
func (r *ServiceBotRunner) MaybeDispatch(ctx context.Context, agent store.ServiceAgentDescriptor, run store.ServiceRun, callerUID int64) {
	if r == nil {
		return
	}
	botUID, ok := ParseBotRunnerBotUID(json.RawMessage(agent.DefinitionJSON))
	if !ok {
		return
	}
	req := botRunRequest{
		RunID:     run.RunID,
		SessionID: run.SessionID,
		Slug:      agent.Slug,
		BotUID:    botUID,
		CallerUID: callerUID,
		Input:     run.Input,
	}
	go r.execute(req)
}

type botRunRequest struct {
	RunID     string
	SessionID string
	Slug      string
	BotUID    int64
	CallerUID int64
	Input     json.RawMessage
}

func (r *ServiceBotRunner) execute(req botRunRequest) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("bot runner panic run=%s: %v", req.RunID, rec)
			_ = r.store.FailServiceRun(context.Background(), req.RunID, "bot_runner_panic", false)
		}
	}()

	// 先在落库层面占住这个 run：抢不到说明已被别的执行方接管 / 已终态。
	started, err := r.store.StartServiceRun(context.Background(), req.RunID,
		fmt.Sprintf("bot:%d", req.BotUID), int(r.timeout.Seconds())+60)
	if err != nil {
		log.Printf("bot runner: start run=%s: %v", req.RunID, err)
		return
	}
	if !started {
		log.Printf("bot runner: run=%s already taken or terminal; skip", req.RunID)
		return
	}

	// 授权：v1 只允许「自己名下的 bot」或白名单账号名下的 bot。
	owner, err := r.store.GetBotOwner(req.BotUID)
	if err != nil {
		r.fail(req, "bot_not_found: 目标 bot 不存在或已删除")
		return
	}
	if owner != req.CallerUID && len(r.ownerWhitelist) > 0 && !r.ownerWhitelist[owner] {
		r.fail(req, "bot_not_shared: 这个 bot 尚未开放给其他账号调用")
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

	reply, waitErr := r.awaitReply(topic, msgID, req.BotUID, req.SessionID)
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
// 若第 3 类消息出现过、但因为有别的单在等而无法认领，`botReplyAmbiguousGrace`
// 之后如实失败：已经看见 bot 回了，就不是「没回」，而是「分不清」，这两件事
// 对调用方的处置完全不同（前者该升级客户端，后者该重试）。
//
// 收到第一段后每出现新内容就把收尾时间往后推 settle，直到静默：bot 会把长回复
// 按 4000 字切段，收得太早会把答案截断。
func (r *ServiceBotRunner) awaitReply(topic string, promptID int64, botUID int64, sessionID string) (string, error) {
	deadline := time.Now().Add(r.timeout)
	var (
		sinceID     = promptID
		parts       []string
		seen        = map[int64]bool{}
		settleTill  time.Time
		size        int
		sawUnowned  bool
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
			text := strings.TrimSpace(msg.Content)
			if text == "" {
				continue
			}
			if !botReplyBelongsTo(msg, promptID, sessionID, r.competingOnTopic(topic)) {
				sawUnowned = true
				if ambiguousAt.IsZero() {
					ambiguousAt = time.Now()
				}
				continue
			}
			if size+len(text) > maxBotRunnerReplyBytes {
				continue
			}
			size += len(text)
			parts = append(parts, text)
			settleTill = time.Now().Add(r.settle)
		}
		if len(parts) > 0 && time.Now().After(settleTill) {
			return strings.Join(parts, "\n\n"), nil
		}
		// 看见回复了却认不了：别干等到超时，尽快让调用方知道是「分不清」而非「没回」。
		if len(parts) == 0 && sawUnowned && time.Since(ambiguousAt) > r.grace() {
			return "", fmt.Errorf(
				"bot_reply_ambiguous: 目标 bot 的回复没有指回本次调用（缺 reply_to），" +
					"而同一会话上还有别的调用在等，无法判断哪条是这次的。请升级客户端后重试，或改为串行调用")
		}
		if time.Now().After(deadline) {
			if len(parts) > 0 {
				// 已经有内容：按「说到一半」交付，比丢掉更有用。
				return strings.Join(parts, "\n\n"), nil
			}
			return "", fmt.Errorf("bot_timeout: 等了 %s 目标 bot 没有回复", r.timeout)
		}
		time.Sleep(r.poll)
	}
}

// grace 返回「看见回复却认不了」的容忍窗口。
func (r *ServiceBotRunner) grace() time.Duration {
	if r.ambiguityGrace > 0 {
		return r.ambiguityGrace
	}
	return botReplyAmbiguousGrace
}

// botReplyBelongsTo 判断一条 bot 的 text 是不是本次调用的结果。
// competing 是同一 topic 上除自己之外还在等的单数（0 = 我是唯一一个）。
func botReplyBelongsTo(msg *types.Message, promptID int64, sessionID string, competing int) bool {
	if msg == nil {
		return false
	}
	// 1) 明确指回我那条 prompt。
	if promptID > 0 && msg.ReplyTo == promptID {
		return true
	}
	// 2) 明确声明了属于哪个 bot 会话。
	if sessionID != "" && msg.Metadata != nil {
		if got, ok := msg.Metadata[botSessionMetadataKey].(string); ok && got == sessionID {
			return true
		}
	}
	// 3) 旧客户端：既没 reply_to 也没会话标记，只有「没人跟我抢」时才能认。
	if msg.ReplyTo == 0 && competing == 0 {
		return true
	}
	return false
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

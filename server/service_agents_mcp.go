package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// ServiceAgentsMCPHandler 是服务调用面的 MCP 端点（Streamable HTTP；设计 §3.3）。
// 工具：每个 agent 一个 catsco_<slug>（调用=创建/复用会话+run），外加 catsco_run_status 查询。
// 同步交付=阈值内返回结果 envelope；超时返回运行句柄（catsco.service-run-async.v1），随后用
// catsco_run_status 查询终态。对象均经 structuredContent 承载（content 给文本摘要）。
//
// 目录与订阅（设计 §3.3-9"模式二：默认开放 + 个人清单"）：
// tools/list = 全部 active agent − 调用账号的屏蔽集（清单跟账号）；
// 清单变更时对在线 MCP 会话做增量同步（AddTool/RemoveTools）→ SDK 自动发
// notifications/tools/list_changed；客户端亦可随时重查 tools/list（刷新兜底）。
type ServiceAgentsMCPHandler struct {
	store    store.ServiceAgentStore
	bots     ServiceAgentBotIdentity // bot 身份校验（可选；未注入=带 bot_uid 的调用被拒）
	external *ServiceExternalRunner  // 外部执行后端（runner.type=http；可选）
	balance  *ServiceBalanceChecker  // 计费预检（可选；未设置=跳过，调用面单测不受影响）
	syncWait time.Duration           // 0=按 agent 定义阈值（封顶 maxServiceSyncWait）；测试可注入覆盖
	http     *mcp.StreamableHTTPHandler

	// liveMu/liveServers 跟踪每账号的在线 MCP 会话 server（getServer 每新会话调用一次，
	// 每会话一个独立 *mcp.Server 实例）——清单变更时据此下发 list_changed。
	liveMu      sync.Mutex
	liveServers map[int64]map[*mcp.Server]map[string]struct{} // uid → server → 已注册工具名集
}

const (
	serviceRunAsyncContractVersion  = "catsco.service-run-async.v1"
	serviceRunResultContractVersion = "catsco.service-run-result.v1"
	maxServiceSyncWait              = 45 * time.Second
)

var errServiceRunNotOwned = errors.New("service run not owned by caller")

func NewServiceAgentsMCPHandler(st store.ServiceAgentStore) *ServiceAgentsMCPHandler {
	h := &ServiceAgentsMCPHandler{
		store:       st,
		liveServers: map[int64]map[*mcp.Server]map[string]struct{}{},
	}
	h.http = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return h.buildServer(UIDFromContext(r.Context()))
	}, nil)
	return h
}

// SetBalanceChecker 注入调用方额度预检（设计 §5.3；未注入=跳过预检）。
func (h *ServiceAgentsMCPHandler) SetBalanceChecker(checker *ServiceBalanceChecker) {
	h.balance = checker
}

// ServiceAgentBotIdentity 是调用面校验 bot 身份所需的最小存储接口（主库实现）。
// 归属校验：bot 的 owner 必须是调用者；清单校验：bot 定义里若显式设了
// serviceAgents，则该 agent 必须在内（nil=未限制，与 XiaoBa 侧过滤语义一致）。
type ServiceAgentBotIdentity interface {
	GetBotOwner(botUID int64) (int64, error)
	GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error)
}

// SetBotIdentity 注入 bot 身份来源（设计 P1b：tools/call 携带 bot_uid）。
// 未注入时，带 bot_uid（>0）的调用会被显式拒绝（bot_identity_unavailable），
// 不会静默降级成账号级调用——避免归属与计费错位。
func (h *ServiceAgentsMCPHandler) SetBotIdentity(identity ServiceAgentBotIdentity) {
	h.bots = identity
}

// SetExternalRunner 注入外部执行后端（设计 §4.4 P3-a）：声明了 runner.type=http 的
// agent 由平台把 run 转发到用户 endpoint；未注入 = 只支持平台自带执行。
func (h *ServiceAgentsMCPHandler) SetExternalRunner(runner *ServiceExternalRunner) {
	h.external = runner
}

func (h *ServiceAgentsMCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 纵深防御：即便中间件配置漂移，无身份也不放行。
	if UIDFromContext(r.Context()) == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.http.ServeHTTP(w, r)
}

// visibleServiceAgents 计算调用账号的可见清单：全部 active agent − 屏蔽集（设计 §3.3-9）。
// 屏蔽集查询失败时按"全可见"降级（清单是个人聚焦设置，不因存储抖动阻断工具面）。
func (h *ServiceAgentsMCPHandler) visibleServiceAgents(ctx context.Context, callerUID int64) []store.ServiceAgentDescriptor {
	agents, err := h.store.ListServiceAgents(ctx)
	if err != nil {
		log.Printf("service mcp list agents: %v", err)
		return nil
	}
	hiddenIDs, err := h.store.ListHiddenServiceAgentIDs(ctx, callerUID)
	if err != nil {
		log.Printf("service mcp list hidden agents uid=%d: %v", callerUID, err)
		// 只跳过「个人屏蔽」这一层过滤（它只是聚焦设置）；可见性过滤必须照旧执行。
		return serviceAgentsVisibleOnly(agents, callerUID, nil)
	}
	hidden := make(map[int64]struct{}, len(hiddenIDs))
	for _, id := range hiddenIDs {
		hidden[id] = struct{}{}
	}
	return serviceAgentsVisibleOnly(agents, callerUID, hidden)
}

// serviceAgentsVisibleOnly 按可见性（+ 可选个人屏蔽集）过滤 agent 清单。
func serviceAgentsVisibleOnly(agents []store.ServiceAgentDescriptor, callerUID int64, hidden map[int64]struct{}) []store.ServiceAgentDescriptor {
	visible := make([]store.ServiceAgentDescriptor, 0, len(agents))
	for _, agent := range agents {
		// 可见性（设计 L2）：private 只对 owner 本人可见，不进别人的 tools/list。
		if !store.ServiceAgentVisibleTo(agent, callerUID) {
			continue
		}
		if _, blocked := hidden[agent.ID]; blocked {
			continue
		}
		visible = append(visible, agent)
	}
	return visible
}

func (h *ServiceAgentsMCPHandler) buildServer(callerUID int64) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "catsco-agents", Version: "0.1.0"}, &mcp.ServerOptions{
		// 目录与订阅：工具清单会随账号清单变化动态调整（list_changed 通知）。
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	registered := map[string]struct{}{}
	for _, agent := range h.visibleServiceAgents(context.Background(), callerUID) {
		h.addAgentTool(srv, agent, callerUID)
		registered["catsco_"+agent.Slug] = struct{}{}
	}
	h.addRunStatusTool(srv, callerUID)
	registered["catsco_run_status"] = struct{}{}
	h.registerLiveServer(callerUID, srv, registered)
	return srv
}

func (h *ServiceAgentsMCPHandler) registerLiveServer(callerUID int64, srv *mcp.Server, tools map[string]struct{}) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.liveServers[callerUID] == nil {
		h.liveServers[callerUID] = map[*mcp.Server]map[string]struct{}{}
	}
	h.liveServers[callerUID][srv] = tools
}

// NotifySubscriptionChanged 清单变更后同步该账号全部在线 MCP 会话的工具集。
// 差量：移除已屏蔽 agent 的 tool、补上新可见 agent 的 tool——SDK 的 AddTool/RemoveTools
// 自带 list_changed 通知（capabilities.Tools.ListChanged 已声明）。
// 无会话的 server 从注册表剪枝（会话关闭后 server 实例不再可达）。
func (h *ServiceAgentsMCPHandler) NotifySubscriptionChanged(callerUID int64) {
	h.liveMu.Lock()
	servers := h.liveServers[callerUID]
	h.liveServers[callerUID] = map[*mcp.Server]map[string]struct{}{}
	h.liveMu.Unlock()
	if len(servers) == 0 {
		return
	}
	desired := map[string]store.ServiceAgentDescriptor{}
	for _, agent := range h.visibleServiceAgents(context.Background(), callerUID) {
		desired["catsco_"+agent.Slug] = agent
	}
	for srv, tools := range servers {
		if !serviceMCPHasLiveSessions(srv) {
			continue // 会话已关：实例不再可用，丢弃
		}
		removeNames := make([]string, 0)
		for name := range tools {
			if name == "catsco_run_status" {
				continue
			}
			if _, ok := desired[name]; !ok {
				removeNames = append(removeNames, name)
			}
		}
		if len(removeNames) > 0 {
			srv.RemoveTools(removeNames...)
			for _, name := range removeNames {
				delete(tools, name)
			}
		}
		for name, agent := range desired {
			if _, ok := tools[name]; ok {
				continue
			}
			h.addAgentTool(srv, agent, callerUID)
			tools[name] = struct{}{}
		}
		// 重新登记（存活 server 继续跟踪）。
		h.registerLiveServer(callerUID, srv, tools)
	}
}

// NotifyCatalogChanged 目录发生全局变化（上架/下架）后，把所有在线 MCP 会话的工具集
// 对齐到最新可见清单。逐个账号走 NotifySubscriptionChanged（幂等差量 + list_changed）。
func (h *ServiceAgentsMCPHandler) NotifyCatalogChanged() {
	h.liveMu.Lock()
	uids := make([]int64, 0, len(h.liveServers))
	for uid := range h.liveServers {
		uids = append(uids, uid)
	}
	h.liveMu.Unlock()
	for _, uid := range uids {
		h.NotifySubscriptionChanged(uid)
	}
}

// serviceMCPHasLiveSessions 判断 server 是否仍挂着会话（用于注册表剪枝）。
func serviceMCPHasLiveSessions(srv *mcp.Server) bool {
	for range srv.Sessions() {
		return true
	}
	return false
}

// isAgentHiddenFor 判断 agent 是否被该账号屏蔽（清单移除）。
func (h *ServiceAgentsMCPHandler) isAgentHiddenFor(ctx context.Context, callerUID, agentID int64) (bool, error) {
	hiddenIDs, err := h.store.ListHiddenServiceAgentIDs(ctx, callerUID)
	if err != nil {
		return false, err
	}
	for _, id := range hiddenIDs {
		if id == agentID {
			return true, nil
		}
	}
	return false, nil
}

type serviceInvokeToolArgs struct {
	Input       any    `json:"input,omitempty" jsonschema:"调用参数（任意 JSON）"`
	SessionID   string `json:"session_id,omitempty" jsonschema:"续聊会话 id；不传=新会话"`
	ClientToken string `json:"client_token,omitempty" jsonschema:"幂等键（同会话同 token 重发返回同一 run）"`
	BotUID      int64  `json:"bot_uid,omitempty" jsonschema:"调用方 bot 身份（可选）；传了就按该 bot 归属校验并写入会话"`
}

func (h *ServiceAgentsMCPHandler) addAgentTool(srv *mcp.Server, agent store.ServiceAgentDescriptor, callerUID int64) {
	tool := &mcp.Tool{
		Name:        "catsco_" + agent.Slug,
		Description: fmt.Sprintf("%s（CatsCo 定制应用，slug=%s，版本 v%d）", agent.Name, agent.Slug, agent.Version),
	}
	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, args serviceInvokeToolArgs) (*mcp.CallToolResult, any, error) {
		return h.invoke(ctx, agent, callerUID, args)
	})
}

func (h *ServiceAgentsMCPHandler) invoke(ctx context.Context, agent store.ServiceAgentDescriptor, callerUID int64, args serviceInvokeToolArgs) (*mcp.CallToolResult, any, error) {
	if callerUID == 0 {
		return serviceToolError("unauthorized"), nil, nil
	}
	// 可见性准入：private agent 即便被猜到 slug 也不能调用（tools/list 里也不会出现）。
	if !store.ServiceAgentVisibleTo(agent, callerUID) {
		return serviceToolError("agent_not_found"), nil, nil
	}
	// 上架状态实时校验：这条兜底覆盖「list_changed 尚未送达客户端」的窗口期
	// （正常路径下应用被收回后工具会立即从 tools/list 移除，旧名字调用由 MCP 层
	// 返回 unknown tool）。
	if _, _, _, _, err := h.store.GetServiceAgentBySlug(ctx, agent.Slug); err != nil {
		if errors.Is(err, store.ErrServiceAgentNotFound) {
			return serviceToolError("agent_not_found: 该应用已被发布者收回"), nil, nil
		}
		return serviceToolError("service_unavailable: 应用状态校验失败，请稍后重试"), nil, nil
	}
	// bot 身份（设计 P1b）：bot_uid=0 仍允许（账号级调用，兼容旧客户端）；
	// >0 则必须归属本账号，且（若 bot 定义了工具清单）该 agent 在清单内。
	botUID, botErr := h.resolveBotScope(ctx, callerUID, args.BotUID, agent.Slug)
	if botErr != nil {
		return botErr, nil, nil
	}
	if h.balance != nil {
		if err := h.balance.Check(ctx, callerUID); err != nil {
			if errors.Is(err, ErrQuotaExhausted) {
				return serviceToolError("quota_exhausted: 调用方额度不足，请充值后再试"), nil, nil
			}
			return serviceToolError("service_unavailable: 计费预检暂不可用，请稍后再试"), nil, nil
		}
	}
	// 目录与订阅（设计 §3.3-9）：被该账号从清单移除（屏蔽）的 agent 拒绝调用——
	// 尊重用户自己的设置；查询失败按放行降级（清单是聚焦设置，非访问控制）。
	if hidden, err := h.isAgentHiddenFor(ctx, callerUID, agent.ID); err != nil {
		log.Printf("service mcp hidden check agent=%d uid=%d: %v", agent.ID, callerUID, err)
	} else if hidden {
		return serviceToolError("agent_not_subscribed: 该应用已从你的清单中移除（可在应用目录重新添加后再调用）"), nil, nil
	}
	// 本机 worker 型应用：先确认有在线实例能接单，否则用户要白等一个完整同步窗口
	// 才知道「自己电脑上的 worker 根本没开」。查不到监控数据时放行（fail-open）。
	if h.isWorkerRunner(ctx, agent) {
		if err := h.checkWorkerOnline(ctx, agent.Slug); err != nil {
			var offline *ServiceWorkerOfflineError
			if errors.As(err, &offline) {
				return serviceToolError(serviceWorkerOfflineToolError(offline)), nil, nil
			}
		}
	}
	input, err := json.Marshal(args.Input)
	if err != nil || len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	session, err := h.resolveSession(ctx, agent, callerUID, botUID, args.SessionID)
	if err != nil {
		if errors.Is(err, errServiceRunNotOwned) || errors.Is(err, store.ErrServiceSessionNotFound) {
			return serviceToolError("session_not_found_or_not_owned"), nil, nil
		}
		if errors.Is(err, store.ErrServiceAgentNotFound) {
			return serviceToolError("agent_not_found"), nil, nil
		}
		return serviceToolError(fmt.Sprintf("session_error: %v", err)), nil, nil
	}
	run, reused, err := h.store.CreateServiceRunIdempotent(ctx, session.SessionID, args.ClientToken, input)
	if err != nil {
		if errors.Is(err, store.ErrServiceSessionBusy) {
			return serviceToolError("session_busy: 同会话已有进行中的 run（可先查询 catsco_run_status）"), nil, nil
		}
		return serviceToolError(fmt.Sprintf("create_run_failed: %v", err)), nil, nil
	}
	// 只有「本次新建」的 run 才转发：幂等命中（reused）说明上一次调用已经转发过，
	// 重复转发会让对端收到两次同一个 run。
	if !reused {
		// 外部执行后端（runner.type=http）：异步转发到用户 endpoint，结果写成 result 事件；
		// 平台自带执行（codex）不声张，照旧由 worker claim。
		h.external.MaybeDispatch(ctx, agent, run, callerUID, botUID)
	}
	return h.awaitRun(ctx, session.SessionID, run, agent.AsyncThresholdSeconds)
}

// resolveBotScope 校验可选的 bot 身份，返回要写进会话的 callerBotUID（0=账号级）。
// 失败时返回可直接回给客户端的工具错误结果。
func (h *ServiceAgentsMCPHandler) resolveBotScope(ctx context.Context, callerUID, botUID int64, agentSlug string) (int64, *mcp.CallToolResult) {
	if botUID == 0 {
		return 0, nil
	}
	if h.bots == nil {
		return 0, serviceToolError("bot_identity_unavailable: 服务端未启用 bot 身份校验，请暂不要传 bot_uid")
	}
	owner, err := h.bots.GetBotOwner(botUID)
	if err != nil {
		return 0, serviceToolError("bot_not_found: 指定的 bot 不存在或已删除")
	}
	if owner != callerUID {
		return 0, serviceToolError("bot_not_owned: 该 bot 不属于当前账号")
	}
	definition, err := h.bots.GetBotDefinition(botUID)
	if err != nil || definition == nil {
		// 清单是“该 bot 要哪几个工具”的偏好设置，不是访问控制——读取失败按不限制降级，不阻断调用。
		if err != nil {
			log.Printf("service mcp bot definition bot=%d caller=%d: %v", botUID, callerUID, err)
		}
		return botUID, nil
	}
	slugs := definition.Definition.ServiceAgents
	if slugs == nil {
		return botUID, nil // nil=未限制（账号可见全部）
	}
	for _, slug := range slugs {
		if slug == agentSlug {
			return botUID, nil
		}
	}
	return 0, serviceToolError("agent_not_enabled_for_bot: 该 bot 的工具清单里没有这个应用（请在「Agent 工具」面板勾选后重试）")
}

func (h *ServiceAgentsMCPHandler) resolveSession(ctx context.Context, agent store.ServiceAgentDescriptor, callerUID, botUID int64, sessionID string) (store.ServiceSession, error) {
	if sessionID == "" {
		// 会话钉快照（设计 A §2）：建会话时固化当时最新包 hash；此后发布新包不影响本会话。
		assetsHash := ""
		if hash, err := h.store.GetServiceAgentPackageHash(ctx, agent.ID); err == nil {
			assetsHash = hash
		} else {
			log.Printf("service mcp pin package hash agent=%s: %v", agent.Slug, err)
		}
		id, err := h.store.CreateServiceSession(ctx, agent.ID, agent.Version, callerUID, botUID, assetsHash)
		if err != nil {
			return store.ServiceSession{}, err
		}
		return h.store.GetServiceSession(ctx, id)
	}
	session, err := h.store.GetServiceSession(ctx, sessionID)
	if err != nil {
		return store.ServiceSession{}, err
	}
	// 会话归属：账号一致且 bot 身份一致（账号级会话不能用 bot 身份续，反之亦然）。
	if session.CallerUID != callerUID || session.AgentID != agent.ID || session.CallerBotUID != botUID {
		return store.ServiceSession{}, errServiceRunNotOwned
	}
	return session, nil
}

func (h *ServiceAgentsMCPHandler) awaitRun(ctx context.Context, sessionID string, run store.ServiceRun, thresholdSeconds int) (*mcp.CallToolResult, any, error) {
	if terminal, env := serviceRunTerminalEnvelope(run); terminal {
		return serviceRunToolResult(env), env, nil
	}
	wait := h.syncWait
	if wait <= 0 {
		wait = time.Duration(thresholdSeconds) * time.Second
		if wait > maxServiceSyncWait {
			wait = maxServiceSyncWait
		}
	}
	deadline := time.Now().Add(wait)
	for {
		events, err := h.store.ListServiceRunEvents(ctx, run.RunID, 0, 100)
		if err == nil {
			for _, ev := range events {
				if ev.Type != "result" || len(ev.Payload) == 0 {
					continue
				}
				var env ServiceRunEnvelope
				if json.Unmarshal(ev.Payload, &env) == nil {
					return serviceRunToolResult(env), env, nil
				}
			}
		}
		if current, err := h.store.GetServiceRun(ctx, run.RunID); err == nil {
			if terminal, env := serviceRunTerminalEnvelope(current); terminal {
				return serviceRunToolResult(env), env, nil
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			wait = 0
		case <-time.After(10 * time.Millisecond):
		}
		if wait == 0 {
			break
		}
	}
	handle := ServiceRunEnvelope{
		ContractVersion: serviceRunAsyncContractVersion,
		RunID:           run.RunID,
		SessionID:       sessionID,
		Status:          ServiceRunRunning,
	}
	return serviceRunToolResult(handle), handle, nil
}

func serviceRunTerminalEnvelope(run store.ServiceRun) (bool, ServiceRunEnvelope) {
	switch run.State {
	case "completed":
		var env ServiceRunEnvelope
		if len(run.Result) > 0 && json.Unmarshal(run.Result, &env) == nil && env.ContractVersion != "" {
			return true, env
		}
		return true, ServiceRunEnvelope{
			ContractVersion: serviceRunResultContractVersion,
			RunID:           run.RunID, SessionID: run.SessionID, Status: ServiceRunCompleted,
		}
	case "awaiting_input":
		// 回合制返问收尾（设计 §3.3-8）：视同终态交付；调用方以同一 session_id 补充输入续跑。
		var env ServiceRunEnvelope
		if len(run.Result) > 0 && json.Unmarshal(run.Result, &env) == nil && env.ContractVersion != "" {
			return true, env
		}
		return true, ServiceRunEnvelope{
			ContractVersion: serviceRunResultContractVersion,
			RunID:           run.RunID, SessionID: run.SessionID, Status: ServiceRunAwaitingInput,
		}
	case "failed", "cancelled":
		return true, ServiceRunEnvelope{
			ContractVersion: serviceRunResultContractVersion,
			RunID:           run.RunID, SessionID: run.SessionID,
			Status: ServiceRunState(run.State), Error: "run terminated",
		}
	}
	return false, ServiceRunEnvelope{}
}

type serviceRunStatusArgs struct {
	RunID string `json:"run_id" jsonschema:"run id（catsco_<slug> 返回的 run_id）"`
}

func (h *ServiceAgentsMCPHandler) addRunStatusTool(srv *mcp.Server, callerUID int64) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "catsco_run_status",
		Description: "查询 CatsCo 定制应用 run 的状态与结果（run 句柄交付后的查询入口）",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args serviceRunStatusArgs) (*mcp.CallToolResult, any, error) {
		if args.RunID == "" {
			return serviceToolError("run_id_required"), nil, nil
		}
		run, err := h.store.GetServiceRun(ctx, args.RunID)
		if err != nil {
			return serviceToolError("run_not_found"), nil, nil
		}
		session, err := h.store.GetServiceSession(ctx, run.SessionID)
		if err != nil || session.CallerUID != callerUID {
			return serviceToolError("run_not_found"), nil, nil
		}
		if terminal, env := serviceRunTerminalEnvelope(run); terminal {
			return serviceRunToolResult(env), env, nil
		}
		handle := ServiceRunEnvelope{
			ContractVersion: serviceRunAsyncContractVersion,
			RunID:           run.RunID,
			SessionID:       run.SessionID,
			Status:          ServiceRunRunning,
		}
		return serviceRunToolResult(handle), handle, nil
	})
}

func serviceRunToolResult(env ServiceRunEnvelope) *mcp.CallToolResult {
	text := fmt.Sprintf("run=%s session=%s status=%s", env.RunID, env.SessionID, env.Status)
	if env.Error != "" {
		text += " error=" + env.Error
	}
	if env.Status == ServiceRunAwaitingInput {
		// 返问：把问题直接带进文本摘要，调用方 LLM 无需解析 structuredContent 即可续聊。
		text += " question=" + serviceAwaitQuestionText(env) + "（补充=以同一 session_id 再次调用）"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// serviceAwaitQuestionText 从 await envelope 的 output.question 提取问题文本（展示用）。
func serviceAwaitQuestionText(env ServiceRunEnvelope) string {
	var parsed struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(env.Output, &parsed); err != nil || parsed.Question == "" {
		return "(问题内容缺失——请用 catsco_run_status 复核 run)"
	}
	return parsed.Question
}

func serviceToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
	}
}

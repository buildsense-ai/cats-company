package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openchat/openchat/server/store"
)

// ServiceAgentsMCPHandler 是服务调用面的 MCP 端点（Streamable HTTP；设计 §3.3）。
// 工具：每个 agent 一个 catsco_<slug>（调用=创建/复用会话+run），外加 catsco_run_status 查询。
// 同步交付=阈值内返回结果 envelope；超时返回运行句柄（catsco.service-run-async.v1），随后用
// catsco_run_status 查询终态。对象均经 structuredContent 承载（content 给文本摘要）。
type ServiceAgentsMCPHandler struct {
	store    store.ServiceAgentStore
	syncWait time.Duration // 0=按 agent 定义阈值（封顶 maxServiceSyncWait）；测试可注入覆盖
	http     *mcp.StreamableHTTPHandler
}

const (
	serviceRunAsyncContractVersion  = "catsco.service-run-async.v1"
	serviceRunResultContractVersion = "catsco.service-run-result.v1"
	maxServiceSyncWait              = 45 * time.Second
)

var errServiceRunNotOwned = errors.New("service run not owned by caller")

func NewServiceAgentsMCPHandler(st store.ServiceAgentStore) *ServiceAgentsMCPHandler {
	h := &ServiceAgentsMCPHandler{store: st}
	h.http = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return h.buildServer(UIDFromContext(r.Context()))
	}, nil)
	return h
}

func (h *ServiceAgentsMCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 纵深防御：即便中间件配置漂移，无身份也不放行。
	if UIDFromContext(r.Context()) == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.http.ServeHTTP(w, r)
}

func (h *ServiceAgentsMCPHandler) buildServer(callerUID int64) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "catsco-agents", Version: "0.1.0"}, nil)
	agents, err := h.store.ListServiceAgents(context.Background())
	if err == nil {
		for _, agent := range agents {
			h.addAgentTool(srv, agent, callerUID)
		}
	}
	h.addRunStatusTool(srv, callerUID)
	return srv
}

type serviceInvokeToolArgs struct {
	Input       any    `json:"input,omitempty" jsonschema:"调用参数（任意 JSON）"`
	SessionID   string `json:"session_id,omitempty" jsonschema:"续聊会话 id；不传=新会话"`
	ClientToken string `json:"client_token,omitempty" jsonschema:"幂等键（同会话同 token 重发返回同一 run）"`
	BotUID      int64  `json:"bot_uid,omitempty" jsonschema:"预留：bot 身份（当前不支持，须为 0）"`
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
	if args.BotUID != 0 {
		// 归属校验（GetBot(owner)==caller）留待阶段 2 接线；本期显式拒绝而不是静默忽略。
		return serviceToolError("bot_identity_not_supported"), nil, nil
	}
	input, err := json.Marshal(args.Input)
	if err != nil || len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	session, err := h.resolveSession(ctx, agent, callerUID, args.SessionID)
	if err != nil {
		if errors.Is(err, errServiceRunNotOwned) || errors.Is(err, store.ErrServiceSessionNotFound) {
			return serviceToolError("session_not_found_or_not_owned"), nil, nil
		}
		if errors.Is(err, store.ErrServiceAgentNotFound) {
			return serviceToolError("agent_not_found"), nil, nil
		}
		return serviceToolError(fmt.Sprintf("session_error: %v", err)), nil, nil
	}
	run, _, err := h.store.CreateServiceRunIdempotent(ctx, session.SessionID, args.ClientToken, input)
	if err != nil {
		if errors.Is(err, store.ErrServiceSessionBusy) {
			return serviceToolError("session_busy: 同会话已有进行中的 run（可先查询 catsco_run_status）"), nil, nil
		}
		return serviceToolError(fmt.Sprintf("create_run_failed: %v", err)), nil, nil
	}
	return h.awaitRun(ctx, session.SessionID, run, agent.AsyncThresholdSeconds)
}

func (h *ServiceAgentsMCPHandler) resolveSession(ctx context.Context, agent store.ServiceAgentDescriptor, callerUID int64, sessionID string) (store.ServiceSession, error) {
	if sessionID == "" {
		id, err := h.store.CreateServiceSession(ctx, agent.ID, agent.Version, callerUID, 0, "")
		if err != nil {
			return store.ServiceSession{}, err
		}
		return h.store.GetServiceSession(ctx, id)
	}
	session, err := h.store.GetServiceSession(ctx, sessionID)
	if err != nil {
		return store.ServiceSession{}, err
	}
	if session.CallerUID != callerUID || session.AgentID != agent.ID {
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
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func serviceToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
	}
}

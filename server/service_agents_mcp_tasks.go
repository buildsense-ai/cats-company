package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openchat/openchat/server/store"
)

// MCP Tasks（标准任务面，SEP-1686 语义）——go-sdk v1.8.0 尚未内建 tasks，
// 这里在 Streamable HTTP 前面加一层薄 JSON-RPC 中间件实现：
//
//	tools/call + params._meta["io.modelcontextprotocol/task"] → 立即返回 Task 对象
//	（status=working），后台复用 invoke 管线执行；任务完成后用 tasks/get 取结果。
//	tasks/get | tasks/list | tasks/cancel                  → 查询/枚举/撤销。
//
// 为什么要拦在 HTTP 层而不是 SDK 里：SDK 的 tools/call 是同步请求-响应，
// 而 task 语义要求「受理即返回句柄」。中途改 SDK 的应答就等于重新实现传输层，
// 薄中间件只接管四个方法，其余全部原样透传（含 initialize/tools/list/elicitation）。
//
// 协议版本兼容：tasks 方法面与协商版本无关 —— 2026-07-28 与 2025-11-25 客户端
// 走同一份实现；回执投递（WS receipt）照旧由 ServiceRunReceiptDeliverer 负责，
// 不经过这层。

const (
	mcpTaskMetaKey          = "io.modelcontextprotocol/task"
	mcpTaskRetention        = 60 * time.Minute // 终态任务结果保留时长
	mcpTaskMaxPerOwner      = 200              // 单账号在册任务上限（防爆内存）
	mcpTaskPollIntervalHint = 1000             // 建议客户端轮询间隔（ms）
)

type mcpTaskStatus string

const (
	mcpTaskWorking   mcpTaskStatus = "working"
	mcpTaskCompleted mcpTaskStatus = "completed"
	mcpTaskFailed    mcpTaskStatus = "failed"
	mcpTaskCancelled mcpTaskStatus = "cancelled"
)

// mcpTask 是一个任务的完整状态（按 MCP Tasks 的 Task 对象形状序列化）。
type mcpTask struct {
	TaskID        string       `json:"taskId"`
	Status        mcpTaskStatus `json:"status"`
	StatusMessage string       `json:"statusMessage,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	LastUpdatedAt time.Time    `json:"lastUpdatedAt"`
	TTL           int64        `json:"ttl"` // 终态后保留毫秒数
	PollInterval  int64        `json:"pollInterval,omitempty"`

	// 平台内部字段（不序列化进 Task 对象）：
	ownerUID  int64           `json:"-"`
	toolName  string          `json:"-"`
	runID     string          `json:"-"`
	sessionID string          `json:"-"`
	result    json.RawMessage `json:"-"` // 完成后的 tool result（tasks/get 返回）
	errText   string          `json:"-"`
}

// mcpTasksLayer 拦截 POST 体做方法分流。
type mcpTasksLayer struct {
	next   http.Handler
	invoke taskInvoker

	mu    sync.Mutex
	tasks map[string]*mcpTask // taskID → task
	now   func() time.Time
}

// taskInvoker 抽象「执行一次 tools/call」，生产 = ServiceAgentsMCPHandler.invokeByToolName。
type taskInvoker func(ctx context.Context, ownerUID int64, toolName string, args json.RawMessage) (result json.RawMessage, runID, sessionID string, err error)

// NewMCPTasksLayer 生产装配：把 tasks 层绑到调用面的 invoke 管线上
// （校验/计费/会话/幂等/派发全保真，task 只是换了一种等待姿势）。
func NewMCPTasksLayer(h *ServiceAgentsMCPHandler) http.Handler {
	return newMCPTasksLayer(h, h.invokeByToolName)
}

// newMCPTasksLayer 包装 MCP 传输层。invoke 为 nil 时 task 化调用被拒绝
// （tasks/get 等只读方法仍可用）。
func newMCPTasksLayer(next http.Handler, invoke taskInvoker) *mcpTasksLayer {
	return &mcpTasksLayer{
		next:   next,
		invoke: invoke,
		tasks:  map[string]*mcpTask{},
		now:    time.Now,
	}
}

// jsonRPCRequest 只需方法分流的最小字段。
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (l *mcpTasksLayer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		l.next.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		l.next.ServeHTTP(w, r)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req jsonRPCRequest
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &req) != nil {
		// 批量请求/非 JSON-RPC：原样透传给 SDK 传输层处理。
		l.next.ServeHTTP(w, r)
		return
	}
	uid := UIDFromContext(r.Context())
	switch req.Method {
	case "tasks/get", "tasks/list", "tasks/cancel", "tasks/result":
		l.handleTaskMethod(w, r, uid, &req)
		return
	case "tools/call":
		if !hasTaskMeta(req.Params) {
			l.next.ServeHTTP(w, r)
			return
		}
		l.handleTaskedToolCall(w, r, uid, &req)
		return
	case "initialize":
		// tasks 能力宣告：go-sdk v1.8.0 的 ServerCapabilities 还没有 tasks 字段，
		// 在 initialize 响应里补齐，让支持 MCP Tasks 的客户端知道可以走
		// tools/call + _meta task 的提交形态。不支持 tasks 的客户端按协议忽略未知能力。
		l.forwardWithTasksCapability(w, r)
		return
	}
	l.next.ServeHTTP(w, r)
}

// forwardWithTasksCapability 透传 initialize 并给响应注入 capabilities.tasks。
// Streamable HTTP 的 initialize 应答通常是 SSE 帧（event: message / data: {...}），
// 也可能是裸 JSON —— 两种形态都处理；注入失败时原样放行（客户端退回无 tasks 兼容路径）。
func (l *mcpTasksLayer) forwardWithTasksCapability(w http.ResponseWriter, r *http.Request) {
	capture := newResponseCapture(w)
	l.next.ServeHTTP(capture, r)
	body := capture.body.Bytes()
	if !json.Valid(body) {
		if patched, ok := injectTasksCapabilitySSE(body); ok {
			capture.flush(patched)
			return
		}
		capture.flush(body)
		return
	}
	patched, ok := injectTasksCapabilityJSON(body)
	if !ok {
		capture.flush(body)
		return
	}
	capture.flush(patched)
}

// injectTasksCapabilitySSE 在 SSE 帧里找到 JSON-RPC initialize 应答的 data 行并改写。
// SSE 帧的其余行（event 行、注释、其它事件）原样保留 —— 帧结构一个字节都不动。
func injectTasksCapabilitySSE(body []byte) ([]byte, bool) {
	text := string(body)
	lines := strings.Split(text, "\n")
	patched := false
	for i, line := range lines {
		payload := strings.TrimPrefix(line, "data: ")
		if payload == line || payload == "" {
			continue
		}
		injected, ok := injectTasksCapabilityJSON([]byte(payload))
		if !ok {
			continue
		}
		lines[i] = "data: " + string(injected)
		patched = true
		break
	}
	if !patched {
		return body, false
	}
	return []byte(strings.Join(lines, "\n")), true
}

// injectTasksCapabilityJSON 给一条 JSON-RPC initialize 应答补 capabilities.tasks。
func injectTasksCapabilityJSON(body []byte) ([]byte, bool) {
	var reply map[string]json.RawMessage
	if json.Unmarshal(body, &reply) != nil {
		return body, false
	}
	var result struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(reply["result"], &result) != nil || result.Capabilities == nil {
		return body, false
	}
	if _, exists := result.Capabilities["tasks"]; !exists {
		result.Capabilities["tasks"] = json.RawMessage(`{"list":{},"cancel":{},"requests":{"tools":{"call":{}}}}`)
	}
	patched, err := json.Marshal(result.Capabilities)
	if err != nil {
		return body, false
	}
	var rawResult map[string]json.RawMessage
	if json.Unmarshal(reply["result"], &rawResult) != nil {
		return body, false
	}
	rawResult["capabilities"] = patched
	newResult, err := json.Marshal(rawResult)
	if err != nil {
		return body, false
	}
	reply["result"] = newResult
	out, err := json.Marshal(reply)
	if err != nil {
		return body, false
	}
	return out, true
}

// responseCapture 捕获下游写的响应以便改写。
type responseCapture struct {
	inner      http.ResponseWriter
	body       bytes.Buffer
	header     http.Header
	status     int
	wroteHead  bool
}

func newResponseCapture(inner http.ResponseWriter) *responseCapture {
	return &responseCapture{inner: inner, header: http.Header{}, status: http.StatusOK}
}

func (c *responseCapture) Header() http.Header { return c.header }

func (c *responseCapture) WriteHeader(status int) {
	c.status = status
	c.wroteHead = true
}

func (c *responseCapture) Write(p []byte) (int, error) { return c.body.Write(p) }

// flush 把（可能改写过的）响应体写回真实 writer。
func (c *responseCapture) flush(body []byte) {
	for key, values := range c.header {
		for _, value := range values {
			c.inner.Header().Add(key, value)
		}
	}
	c.inner.Header().Del("Content-Length")
	c.inner.WriteHeader(c.status)
	_, _ = c.inner.Write(body)
}

// hasTaskMeta 识别 task 增强请求。标准位置是 params.task（TaskAugmentedRequestParams）；
// 部分早期草案把它放在 _meta["io.modelcontextprotocol/task"] —— 两种都认，
// 宁多勿漏：认出 task 意图却静默降级成同步等待，是调用方最吃亏的一种失败。
func hasTaskMeta(params json.RawMessage) bool {
	if len(params) == 0 {
		return false
	}
	var probe struct {
		Task json.RawMessage            `json:"task"`
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &probe) != nil {
		return false
	}
	if len(probe.Task) > 0 && string(probe.Task) != "null" {
		return true
	}
	if probe.Meta != nil {
		if _, ok := probe.Meta[mcpTaskMetaKey]; ok {
			return true
		}
	}
	return false
}

func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSONRPC(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(idOrNull(id)), "result": result})
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeJSONRPC(w, map[string]any{
		"jsonrpc": "2.0", "id": json.RawMessage(idOrNull(id)),
		"error": map[string]any{"code": code, "message": message},
	})
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func writeJSONRPC(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

// handleTaskedToolCall 受理 task 化的 tools/call：立即返回 Task 对象，后台执行。
func (l *mcpTasksLayer) handleTaskedToolCall(w http.ResponseWriter, r *http.Request, uid int64, req *jsonRPCRequest) {
	if uid == 0 {
		writeJSONRPCError(w, req.ID, -32000, "unauthorized")
		return
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Task      struct {
			TTL int64 `json:"ttl"`
		} `json:"task"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeJSONRPCError(w, req.ID, -32602, "invalid tools/call params")
		return
	}
	if l.invoke == nil {
		writeJSONRPCError(w, req.ID, -32601, "tasks not supported by this server")
		return
	}
	now := l.now().UTC()
	// 客户端可请求保留时长（TaskMetadata.ttl，毫秒）；服务端在上限内尊重请求。
	ttl := int64(mcpTaskRetention / time.Millisecond)
	if params.Task.TTL > 0 && params.Task.TTL < ttl {
		ttl = params.Task.TTL
	}
	task := &mcpTask{
		TaskID:        newMCPTaskID(),
		Status:        mcpTaskWorking,
		CreatedAt:     now,
		LastUpdatedAt: now,
		TTL:           ttl,
		PollInterval:  mcpTaskPollIntervalHint,
		ownerUID:      uid,
		toolName:      params.Name,
	}
	if !l.register(task) {
		writeJSONRPCError(w, req.ID, -32000, "too many active tasks")
		return
	}
	// 后台执行：detach 请求 ctx（HTTP 返回后请求 ctx 即作废，任务必须继续跑），
	// 但保留一个独立超时 —— 没有上限的后台 goroutine 是泄漏。
	args := append(json.RawMessage(nil), params.Arguments...)
	go l.runTask(task, args)
	// CreateTaskResult：result 里包一层 task 字段（不是裸 Task 对象）。
	writeJSONRPCResult(w, req.ID, map[string]any{"task": task.public()})
}

func newMCPTaskID() string {
	id, err := randomURLSafe(16)
	if err != nil {
		// crypto/rand 不可用是致命的，但不要让调用方崩 —— 退化成时间戳。
		return "task_" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return "task_" + id
}

func (l *mcpTasksLayer) register(task *mcpTask) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, t := range l.tasks {
		if t.ownerUID == task.ownerUID {
			count++
		}
	}
	if count >= mcpTaskMaxPerOwner {
		return false
	}
	l.tasks[task.TaskID] = task
	return true
}

// runTask 执行任务并把终态写回任务表。
func (l *mcpTasksLayer) runTask(task *mcpTask, args json.RawMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	result, runID, sessionID, err := l.invoke(ctx, task.ownerUID, task.toolName, args)
	l.mu.Lock()
	defer l.mu.Unlock()
	current, ok := l.tasks[task.TaskID]
	if !ok || current.Status == mcpTaskCancelled {
		return // 已被撤销或清退：不覆盖终态
	}
	current.runID, current.sessionID = runID, sessionID
	current.LastUpdatedAt = l.now().UTC()
	if err != nil {
		current.Status = mcpTaskFailed
		current.StatusMessage = err.Error()
		current.errText = err.Error()
		return
	}
	current.Status = mcpTaskCompleted
	current.result = result
	// 终态之后按 TTL 清退（结果在保留期内可被 tasks/get/tasks/result 取走）。
	// TTL 自创建起算（spec: retention from creation），不是自完成起算。
	remaining := time.Duration(current.TTL)*time.Millisecond - l.now().UTC().Sub(current.CreatedAt)
	if remaining < time.Second {
		remaining = time.Second
	}
	time.AfterFunc(remaining, func() { l.drop(task.TaskID) })
}

func (l *mcpTasksLayer) drop(taskID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.tasks, taskID)
}

func (l *mcpTasksLayer) find(ownerUID int64, taskID string) *mcpTask {
	l.mu.Lock()
	defer l.mu.Unlock()
	task, ok := l.tasks[taskID]
	if !ok || task.ownerUID != ownerUID {
		return nil
	}
	// 拷一份再出锁：读侧拿到的快照不受后台写影响。
	copied := *task
	return &copied
}

// public 是序列化给客户端的 Task 对象（不含内部字段）。
func (t *mcpTask) public() map[string]any {
	out := map[string]any{
		"taskId":        t.TaskID,
		"status":        t.Status,
		"createdAt":     t.CreatedAt.Format(time.RFC3339Nano),
		"lastUpdatedAt": t.LastUpdatedAt.Format(time.RFC3339Nano),
		"ttl":           t.TTL,
	}
	if t.StatusMessage != "" {
		out["statusMessage"] = t.StatusMessage
	}
	if t.PollInterval > 0 && t.Status == mcpTaskWorking {
		out["pollInterval"] = t.PollInterval
	}
	return out
}

func (l *mcpTasksLayer) handleTaskMethod(w http.ResponseWriter, r *http.Request, uid int64, req *jsonRPCRequest) {
	switch req.Method {
	case "tasks/get":
		var params struct {
			TaskID string `json:"taskId"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.TaskID == "" {
			writeJSONRPCError(w, req.ID, -32602, "taskId required")
			return
		}
		task := l.find(uid, params.TaskID)
		if task == nil {
			writeJSONRPCError(w, req.ID, -32602, "task not found")
			return
		}
		writeJSONRPCResult(w, req.ID, task.public())
	case "tasks/list":
		l.mu.Lock()
		tasks := make([]*mcpTask, 0)
		for _, t := range l.tasks {
			if t.ownerUID == uid {
				copied := *t
				tasks = append(tasks, &copied)
			}
		}
		l.mu.Unlock()
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
		out := make([]map[string]any, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, t.public())
		}
		writeJSONRPCResult(w, req.ID, map[string]any{"tasks": out})
	case "tasks/cancel":
		var params struct {
			TaskID string `json:"taskId"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.TaskID == "" {
			writeJSONRPCError(w, req.ID, -32602, "taskId required")
			return
		}
		l.mu.Lock()
		task, ok := l.tasks[params.TaskID]
		if !ok || task.ownerUID != uid {
			l.mu.Unlock()
			writeJSONRPCError(w, req.ID, -32602, "task not found")
			return
		}
		if task.Status == mcpTaskWorking {
			task.Status = mcpTaskCancelled
			task.StatusMessage = "cancelled by client"
			task.LastUpdatedAt = l.now().UTC()
			// 底层 service run 不强行打断：执行端可能已经在 bot 回合里，
			// 撤销语义是「调用方不再关心结果」，结果来了也被丢弃。
		}
		copied := *task
		l.mu.Unlock()
		writeJSONRPCResult(w, req.ID, copied.public())
	case "tasks/result":
		var params struct {
			TaskID string `json:"taskId"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.TaskID == "" {
			writeJSONRPCError(w, req.ID, -32602, "taskId required")
			return
		}
		task := l.find(uid, params.TaskID)
		if task == nil {
			writeJSONRPCError(w, req.ID, -32602, "task not found")
			return
		}
		switch task.Status {
		case mcpTaskCompleted:
			// 返回原始 tools/call 的 CallToolResult 结构（spec：与原请求结果同构）。
			var result json.RawMessage
			if json.Unmarshal(task.result, &result) == nil && len(result) > 0 {
				writeJSONRPCResult(w, req.ID, result)
				return
			}
			writeJSONRPCError(w, req.ID, -32000, "task result unavailable")
		case mcpTaskFailed:
			message := task.StatusMessage
			if message == "" {
				message = "task failed"
			}
			writeJSONRPCError(w, req.ID, -32000, message)
		case mcpTaskCancelled:
			writeJSONRPCError(w, req.ID, -32000, "task was cancelled")
		default:
			writeJSONRPCError(w, req.ID, -32000, "task not completed yet")
		}
	}
}

// invokeByToolName 是 ServiceAgentsMCPHandler 的任务执行入口：
// 按工具名找到 agent 并复用 invoke 管线（校验/计费/会话/幂等/派发全保真）。
// 与 HTTP 路径的唯一差别是 dryen——不等同步窗口之外的状态：拿到 running 句柄后
// 继续轮询 run 到终态，把终态 tool result 作为任务结果。
func (h *ServiceAgentsMCPHandler) invokeByToolName(ctx context.Context, ownerUID int64, toolName string, rawArgs json.RawMessage) (json.RawMessage, string, string, error) {
	slug := strings.TrimPrefix(toolName, "catsco_")
	if slug == toolName {
		return nil, "", "", errMCPUnknownTool(toolName)
	}
	var agent store.ServiceAgentDescriptor
	found := false
	for _, candidate := range h.visibleServiceAgents(ctx, ownerUID) {
		if candidate.Slug == slug {
			agent = candidate
			found = true
			break
		}
	}
	if !found {
		return nil, "", "", errMCPUnknownTool(toolName)
	}
	var args serviceInvokeToolArgs
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, "", "", err
		}
	}
	result, structured, err := h.invoke(ctx, agent, ownerUID, args)
	if err != nil {
		return nil, "", "", err
	}
	if result == nil {
		return nil, "", "", errMCPUnknownTool(toolName)
	}
	if result.IsError {
		return marshalToolResult(result), "", "", &mcpToolExecError{message: firstTextContent(result)}
	}
	// invoke 的 envelope 走第二返回值（低层 AddTool 路径不自动填 structuredContent；
	// 直接调用时更是连 SDK 都不会经手）。两处都看，保真两条注册路径。
	env := envelopeOf(result)
	if env.ContractVersion == "" && structured != nil {
		if raw, marshalErr := json.Marshal(structured); marshalErr == nil {
			_ = json.Unmarshal(raw, &env)
		}
	}
	if terminalStatus(env.Status) {
		// 终态即交付：补上 structuredContent（直接调用绕过了 SDK 的自动填充）。
		if result.StructuredContent == nil && structured != nil {
			result.StructuredContent = structured
		}
		return marshalToolResult(result), env.RunID, env.SessionID, nil
	}
	// 句态（还在跑）：轮询 run 到终态，把终态 envelope 包成 tool result。
	env, err = h.waitRunTerminal(ctx, env.RunID, env.SessionID)
	if err != nil {
		return marshalToolResult(result), env.RunID, env.SessionID, err
	}
	final := serviceRunToolResult(env)
	final.StructuredContent = env
	return marshalToolResult(final), env.RunID, env.SessionID, nil
}

func terminalStatus(status ServiceRunState) bool {
	switch status {
	case ServiceRunCompleted, ServiceRunAwaitingInput, "failed", "cancelled":
		return true
	}
	return false
}

type errMCPUnknownToolT struct{ name string }

func (e *errMCPUnknownToolT) Error() string { return "unknown tool: " + e.name }
func errMCPUnknownTool(name string) error   { return &errMCPUnknownToolT{name: name} }

type mcpToolExecError struct{ message string }

func (e *mcpToolExecError) Error() string { return e.message }

func marshalToolResult(result *mcp.CallToolResult) json.RawMessage {
	raw, err := json.Marshal(result)
	if err != nil {
		log.Printf("mcp tasks marshal result: %v", err)
		return json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"internal result encoding failure"}]}`)
	}
	return raw
}

func firstTextContent(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return "tool error"
}

func envelopeOf(result *mcp.CallToolResult) ServiceRunEnvelope {
	var env ServiceRunEnvelope
	if result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err == nil {
			_ = json.Unmarshal(raw, &env)
		}
	}
	return env
}

// waitRunTerminal 轮询 run 到终态（任务面用；超时由 ctx 控制）。
func (h *ServiceAgentsMCPHandler) waitRunTerminal(ctx context.Context, runID, sessionID string) (ServiceRunEnvelope, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := h.store.GetServiceRun(ctx, runID)
		if err != nil {
			return ServiceRunEnvelope{RunID: runID, SessionID: sessionID}, err
		}
		if terminal, env := serviceRunTerminalEnvelope(run); terminal {
			return env, nil
		}
		select {
		case <-ctx.Done():
			return ServiceRunEnvelope{RunID: runID, SessionID: sessionID}, ctx.Err()
		case <-ticker.C:
		}
	}
}

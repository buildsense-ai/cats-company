package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcp_elicitation 交互协议（catsco.mcp-elicitation.v1）
//
// 目标：给调用方 Agent 一个「向人收结构化输入」的标准通道，而不是把问题混进
// 正文文本里等模型自己领悟。协议按 MCP 双协议栈实现，一个 handler 同时服务
// 两代客户端（SDK 探测已验证）：
//
//   - 2026-07-28（SEP-2322/2575）：首轮回 CallToolResult{InputRequests, RequestState}，
//     SDK 自动打上 resultType=input_required；客户端履行 elicitation 后带
//     InputResponses + RequestState 重试同一 tools/call。
//   - 2025-11-25 及更早：同样的 InputRequests 由 SDK 服务端中间件翻译成
//     elicitation/create 服务器请求，客户端回包后中间件原样重入 handler。
//
// 两条路径在 handler 里都收敛为「第二回合从 req.Params.InputResponses 取回结果」。
//
// 兜底通道：客户端不支持 elicitation capability 时（无头脚本、老桥接器），
// 首回合的 InputRequests 根本走不出去 —— handler 收到空 InputResponses 就只发请求。
// 为此协议额外提供 out-of-band 完成路径：调用方拿到首回合（或超时/报错）后，
// 自行向用户收答案，再以同一 elicitation_id + response 调用本工具，
// handler 把它当作「已履行」直接交付 envelope。契约版本不变，两条路径产物同构。
//
// RequestState 只承载 elicitation_id；状态本体在进程内注册表（TTL 15 分钟）。
// 服务器是 JWT 鉴权的（不是 unauthenticated server），不需要自加密 state；
// 进程重启会让进行中的 elicitation 失效 —— 如实报错，调用方重新发起即可。

const (
	mcpElicitationContractVersion = "catsco.mcp-elicitation.v1"
	mcpElicitationToolName        = "mcp_elicitation_create"
	mcpElicitationTTL             = 15 * time.Minute
	mcpElicitationMaxPerOwner     = 50
)

// mcpElicitationRecord 是一份进行中的 elicitation（跨 HTTP 请求存活）。
type mcpElicitationRecord struct {
	ID        string
	OwnerUID  int64
	Message   string
	Mode      string // form / url
	Schema    json.RawMessage
	URL       string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type mcpElicitationRegistry struct {
	mu      sync.Mutex
	records map[string]*mcpElicitationRecord
	now     func() time.Time
}

func newMCPElicitationRegistry() *mcpElicitationRegistry {
	return &mcpElicitationRegistry{records: map[string]*mcpElicitationRecord{}, now: time.Now}
}

func (r *mcpElicitationRegistry) put(rec *mcpElicitationRecord) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, existing := range r.records {
		if existing.OwnerUID == rec.OwnerUID {
			count++
		}
	}
	if count >= mcpElicitationMaxPerOwner {
		return false
	}
	// 顺带清过期项：没有独立 reaper，写入路径就是最好的清扫时机。
	now := r.now()
	for id, existing := range r.records {
		if now.After(existing.ExpiresAt) {
			delete(r.records, id)
		}
	}
	r.records[rec.ID] = rec
	return true
}

func (r *mcpElicitationRegistry) get(id string, ownerUID int64) *mcpElicitationRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[id]
	if !ok || rec.OwnerUID != ownerUID {
		return nil
	}
	if r.now().After(rec.ExpiresAt) {
		delete(r.records, id)
		return nil
	}
	copied := *rec
	return &copied
}

// consume 完成即销毁：同一 elicitation 不会交付两份答案。
func (r *mcpElicitationRegistry) consume(id string, ownerUID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.records[id]; ok && rec.OwnerUID == ownerUID {
		delete(r.records, id)
	}
}

// mcpElicitationToolArgs 是 mcp_elicitation_create 的入参。
type mcpElicitationToolArgs struct {
	Message       string          `json:"message" jsonschema:"给用户看的提问/说明（必填）"`
	Schema        json.RawMessage `json:"schema,omitempty" jsonschema:"form 模式的 JSON Schema（扁平 properties，无嵌套）"`
	Mode          string          `json:"mode,omitempty" jsonschema:"form（默认）或 url"`
	URL           string          `json:"url,omitempty" jsonschema:"url 模式的跳转地址"`
	ElicitationID string          `json:"elicitation_id,omitempty" jsonschema:"复用/续答的 id；首次调用留空由服务端分配"`
	Response      json.RawMessage `json:"response,omitempty" jsonschema:"out-of-band 完成：{\"action\":\"accept\",\"content\":{...}}，配合 elicitation_id 使用"`
}

// mcpElicitationEnvelope 是协议交付物（structuredContent）。
type mcpElicitationEnvelope struct {
	ContractVersion string          `json:"contract_version"`
	ElicitationID   string          `json:"elicitation_id"`
	Status          string          `json:"status"` // answered / declined / cancelled / expired
	Action          string          `json:"action,omitempty"`
	Content         json.RawMessage `json:"content,omitempty"`
	Mode            string          `json:"mode"`
	Message         string          `json:"message"`
}

// addElicitationTool 注册 mcp_elicitation_create。
//
// 用低层 srv.AddTool（非泛型 mcp.AddTool）：schema 是运行期 JSON 而不是 Go 结构体
// 推导，而且首轮要返回 InputRequests —— 泛型路径会自动塞 structuredContent，
// 与 inputRequests 互斥（SDK 线规），低层路径自己全权控制结果形状。
func (h *ServiceAgentsMCPHandler) addElicitationTool(srv *mcp.Server, callerUID int64) {
	tool := &mcp.Tool{
		Name: mcpElicitationToolName,
		Description: "向用户收集结构化输入（catsco.mcp-elicitation.v1）。给定 message 与可选 JSON Schema，" +
			"协议按客户端能力自动选择 elicitation 通道；不支持 elicitation 的客户端可用 " +
			"elicitation_id + response（out-of-band）完成同一流程。答案以 envelope 交付（action/content）。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message":        map[string]any{"type": "string", "description": "给用户看的提问/说明"},
				"schema":         map[string]any{"type": "object", "description": "form 模式的 JSON Schema（扁平 properties）"},
				"mode":           map[string]any{"type": "string", "enum": []string{"form", "url"}},
				"url":            map[string]any{"type": "string", "description": "url 模式的跳转地址"},
				"elicitation_id": map[string]any{"type": "string", "description": "续答/out-of-band 完成的 id"},
				"response":       map[string]any{"type": "object", "description": "out-of-band 完成：{action, content}"},
			},
			"required": []string{"message"},
		},
	}
	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args mcpElicitationToolArgs
		if raw := req.Params.Arguments; len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &args); err != nil {
				return serviceToolError("invalid_arguments: " + err.Error()), nil
			}
		}
		return h.handleElicitationCreate(ctx, callerUID, req, args), nil
	})
}

func (h *ServiceAgentsMCPHandler) elicitationRegistry() *mcpElicitationRegistry {
	if h.elicitations == nil {
		h.elicitations = newMCPElicitationRegistry()
	}
	return h.elicitations
}

// handleElicitationCreate 是协议核心：首回合发 InputRequests，第二回合（或 out-of-band）交付 envelope。
func (h *ServiceAgentsMCPHandler) handleElicitationCreate(ctx context.Context, callerUID int64, req *mcp.CallToolRequest, args mcpElicitationToolArgs) *mcp.CallToolResult {
	if callerUID == 0 {
		return serviceToolError("unauthorized")
	}
	registry := h.elicitationRegistry()

	// ---- 第二回合：InputResponses 里带回了答案 ----
	if len(req.Params.InputResponses) > 0 {
		elicitID := req.Params.RequestState
		for key, response := range req.Params.InputResponses {
			if key == elicitationRequestKey(elicitID) || key == elicitID {
				if elicitID == "" {
					elicitID = key
				}
				return h.finishElicitation(registry, callerUID, elicitID, response)
			}
		}
		// 有 InputResponses 但 key 对不上：requestState 全量兜底。
		if elicitID != "" {
			for _, response := range req.Params.InputResponses {
				return h.finishElicitation(registry, callerUID, elicitID, response)
			}
		}
		return serviceToolError("elicitation_response_unmatched: 回包的 elicitation id 对不上任何进行中的请求")
	}

	// ---- out-of-band 完成：调用方自己收了答案回传 ----
	if len(args.Response) > 0 && string(args.Response) != "null" {
		if args.ElicitationID == "" {
			return serviceToolError("elicitation_id_required: out-of-band 完成必须带 elicitation_id")
		}
		var parsed struct {
			Action  string          `json:"action"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(args.Response, &parsed); err != nil {
			return serviceToolError("invalid_arguments: response 必须是 {action, content} 形态")
		}
		rec := registry.get(args.ElicitationID, callerUID)
		if rec == nil {
			return serviceToolError("elicitation_not_found_or_expired: 该 elicitation 不存在或已过期（请重新发起）")
		}
		registry.consume(args.ElicitationID, callerUID)
		env := envelopeFromAction(rec, parsed.Action, parsed.Content)
		return mcpElicitationResult(env)
	}

	// ---- 首回合：校验 + 登记 + 发 InputRequests ----
	if strings.TrimSpace(args.Message) == "" {
		return serviceToolError("invalid_arguments: message 不能为空")
	}
	mode := strings.TrimSpace(args.Mode)
	if mode == "" {
		mode = "form"
	}
	switch mode {
	case "form":
		if len(args.Schema) > 0 && !json.Valid(args.Schema) {
			return serviceToolError("invalid_arguments: schema 不是合法 JSON")
		}
	case "url":
		if strings.TrimSpace(args.URL) == "" {
			return serviceToolError("invalid_arguments: url 模式必须给 url")
		}
	default:
		return serviceToolError("invalid_arguments: mode 只支持 form / url")
	}
	elicitID := strings.TrimSpace(args.ElicitationID)
	if elicitID == "" {
		generated, err := randomURLSafe(12)
		if err != nil {
			return serviceToolError("elicitation_unavailable: id 分配失败")
		}
		elicitID = "elc_" + generated
	}
	now := registry.now()
	rec := &mcpElicitationRecord{
		ID: elicitID, OwnerUID: callerUID, Message: args.Message, Mode: mode,
		Schema: append(json.RawMessage(nil), args.Schema...), URL: strings.TrimSpace(args.URL),
		CreatedAt: now, ExpiresAt: now.Add(mcpElicitationTTL),
	}
	if !registry.put(rec) {
		return serviceToolError("elicitation_quota: 进行中的 elicitation 过多（先完成或等过期）")
	}
	params := &mcp.ElicitParams{
		Message:       args.Message,
		ElicitationID: elicitID,
	}
	if mode == "url" {
		params.Mode = "url"
		params.URL = rec.URL
	} else {
		params.Mode = "form"
		if len(args.Schema) > 0 {
			var schema any
			if err := json.Unmarshal(args.Schema, &schema); err == nil {
				params.RequestedSchema = schema
			}
		}
	}
	// RequestState 只带 id（鉴权服务器，无自加密需要）。InputRequests key 可推导，
	// 第二回合按 key 精确取回，不依赖 key 与 state 的巧合。
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{elicitationRequestKey(elicitID): params},
		RequestState:  elicitID,
	}
}

// elicitationRequestKey 从 elicitation id 推导 InputRequests 的 key。
func elicitationRequestKey(elicitID string) string {
	return elicitID
}

func (h *ServiceAgentsMCPHandler) finishElicitation(registry *mcpElicitationRegistry, callerUID int64, elicitID string, response mcp.InputResponse) *mcp.CallToolResult {
	rec := registry.get(elicitID, callerUID)
	if rec == nil {
		return serviceToolError("elicitation_not_found_or_expired: 该 elicitation 不存在或已过期（请重新发起）")
	}
	result, ok := response.(*mcp.ElicitResult)
	if !ok {
		return serviceToolError("elicitation_wrong_type: 回包不是 elicitation 结果")
	}
	registry.consume(elicitID, callerUID)
	var content json.RawMessage
	if result.Content != nil {
		if raw, err := json.Marshal(result.Content); err == nil {
			content = raw
		}
	}
	env := envelopeFromAction(rec, result.Action, content)
	return mcpElicitationResult(env)
}

func envelopeFromAction(rec *mcpElicitationRecord, action string, content json.RawMessage) mcpElicitationEnvelope {
	status := "answered"
	switch action {
	case "accept":
		status = "answered"
	case "decline":
		status = "declined"
	case "cancel":
		status = "cancelled"
	default:
		if action == "" {
			action = "accept"
		}
	}
	return mcpElicitationEnvelope{
		ContractVersion: mcpElicitationContractVersion,
		ElicitationID:   rec.ID,
		Status:          status,
		Action:          action,
		Content:         content,
		Mode:            rec.Mode,
		Message:         rec.Message,
	}
}

func mcpElicitationResult(env mcpElicitationEnvelope) *mcp.CallToolResult {
	text := "elicitation=" + env.ElicitationID + " status=" + env.Status + " action=" + env.Action
	if len(env.Content) > 0 {
		text += " content=" + truncateForSummary(string(env.Content), serviceRunSummaryLimit)
	}
	if env.Status == "declined" {
		text += "（用户明确拒绝：请尊重决定，不要换个说法再问一次）"
	}
	if env.Status == "cancelled" {
		text += "（用户没有作答：可以稍后再问，或改用合理默认值并明示）"
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: env,
	}
}

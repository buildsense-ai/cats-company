package server

import (
	"encoding/json"
	"time"
)

// 服务调用面契约类型（设计文档 §3.3）。
// MCP 面映射（2026-10-14）：对外经 MCP tool result 的 content（文本摘要）+
// structuredContent（envelope JSON）承载；工具命名 catsco_<slug> 与 run 句柄协议见 Task 7。

type ServiceRunState string

const (
	ServiceRunSubmitted ServiceRunState = "submitted"
	ServiceRunRunning   ServiceRunState = "running"
	ServiceRunCompleted ServiceRunState = "completed"
	ServiceRunFailed    ServiceRunState = "failed"
	ServiceRunCancelled ServiceRunState = "cancelled"
	ServiceRunOrphaned  ServiceRunState = "orphaned"
	// ServiceRunAwaitingInput 是回合制返问的收尾态（设计 §3.3-8）：本次 run 以
	// 「待补充输入」终结并随生命周期回注推回调用方；补充 = 同一会话的下一次 tools/call。
	ServiceRunAwaitingInput ServiceRunState = "awaiting_input"
)

type ServiceAgentSummary struct {
	Slug                  string          `json:"slug"`
	Name                  string          `json:"name"`
	Version               int64           `json:"version"`
	InputContract         json.RawMessage `json:"input_contract,omitempty"`
	AsyncThresholdSeconds int             `json:"async_threshold_seconds"`
}

type ServiceInvokeRequest struct {
	SessionID   string          `json:"session_id,omitempty"`
	Input       json.RawMessage `json:"input"`
	ClientToken string          `json:"client_token"`
}

type ServiceRunUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	// CostCNY：运行时未知时填 0；权威成本以 relay 记账为准。
	CostCNY float64 `json:"cost_cny"`
}

// ServiceRunAttachment 是 run 产出的一个文件/图片附件。
//
// 为什么要有它，而不是只把文件压成一行文字：bot 把结果写成文件发出来时，
// 「有附件」这件事必须能被调用方**程序化**地处理（重新发出去、存起来、渲染成
// 下载卡片），压成文字就只剩一句人话，调用方只能照抄给用户看。
//
// URL 是**绝对地址**：相对路径对调用方毫无意义（它不知道平台域名），而对
// 人类用户来说，绝对地址才会被渲染成可点的链接 —— 否则那份文件在对话里
// 等于不存在。
type ServiceRunAttachment struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size,omitempty"`
	// Kind 是 file / image（图片在客户端会按图片渲染，不要当普通文件下载）。
	Kind string `json:"kind,omitempty"`
}

type ServiceRunEnvelope struct {
	ContractVersion string                 `json:"contract_version"`
	RunID           string                 `json:"run_id"`
	SessionID       string                 `json:"session_id"`
	Status          ServiceRunState        `json:"status"`
	Output          json.RawMessage        `json:"output,omitempty"`
	Files           []string               `json:"files,omitempty"`
	Attachments     []ServiceRunAttachment `json:"attachments,omitempty"`
	Usage           *ServiceRunUsage       `json:"usage,omitempty"`
	Error           string                 `json:"error,omitempty"`
}

type ServiceRunEvent struct {
	EventID   int64           `json:"event_id"`
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// ServiceAsyncNotice 即“运行句柄”的契约类型（catsco.service-run-async.v1，
// status:"running"，经 MCP structuredContent 承载）。
type ServiceAsyncNotice struct {
	ContractVersion string `json:"contract_version"`
	RunID           string `json:"run_id"`
	SessionID       string `json:"session_id"`
	Cause           string `json:"cause"`
}

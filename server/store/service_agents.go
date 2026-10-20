package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ---- service agents（计划① Task 3；设计 §3.3/§4）----

var (
	ErrServiceAgentNotFound   = errors.New("service agent not found")
	ErrServiceSessionNotFound = errors.New("service session not found")
	ErrServiceRunNotFound     = errors.New("service run not found")
	ErrServiceSessionBusy     = errors.New("service session busy")
	ErrServiceRunConflict     = errors.New("service run conflict")
	ErrServicePackageNotFound = errors.New("service agent package not found")
)

type ServiceSession struct {
	SessionID     string
	AgentID       int64
	DefinitionRev int64
	AssetsHash    string
	CallerUID     int64
	CallerBotUID  int64
	State         string
	CreatedAt     time.Time
}

type ServiceRun struct {
	RunID          string
	SessionID      string
	State          string
	ClientToken    string
	Input          json.RawMessage
	Result         json.RawMessage
	InstanceID     string
	LeaseExpiresAt time.Time
	Attempts       int
	CheckpointRef  string
	LeaseEpoch     int64 // fencing 单调租约纪元（设计 §4.8-1）
}

type ServiceRunEventRecord struct {
	RunID     string
	Seq       int64
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// ServiceRunReceiptContract 是回执 payload 的契约版本（= WS 消息体，设计 §3.3-2 回流通道）。
const ServiceRunReceiptContract = "catsco.service-run-receipt.v1"

// ServiceRunReceipt 是生命周期回注收件箱条目（设计 §4.8-2：至少一次投递 + 幂等去重 + 离线待取）。
// receipt_id 由 run_id 确定性派生（rcp_<run_id 去前缀>）——同 run 至多一份回执，重投不产生重复。
type ServiceRunReceipt struct {
	ReceiptID        string
	RunID            string
	SessionID        string
	CallerUID        int64
	Status           string
	Payload          json.RawMessage
	DeliveryAttempts int
	CreatedAt        time.Time
}

// BuildServiceRunReceiptPayload 组装回执 JSON（双方言共用；字段即 WS 消息载荷）。
func BuildServiceRunReceiptPayload(receiptID, runID, sessionID, status, question, errorText string) json.RawMessage {
	payload := map[string]any{
		"contract_version": ServiceRunReceiptContract,
		"receipt_id":       receiptID,
		"run_id":           runID,
		"session_id":       sessionID,
		"status":           status,
		"created_at":       time.Now().UTC().Format(time.RFC3339),
	}
	if question != "" {
		payload["question"] = question
	}
	if errorText != "" {
		payload["error"] = errorText
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// ServiceRunReceiptIDForRun 由 run_id 派生回执 id（幂等去重载体；"run_xxx" → "rcp_xxx"）。
func ServiceRunReceiptIDForRun(runID string) string {
	suffix := strings.TrimPrefix(runID, "run_")
	if suffix == "" {
		suffix = runID
	}
	return "rcp_" + suffix
}

// ServiceAwaitQuestionFromResult 从 await 收尾 envelope 提取问题文本（回执展示用；缺失则空）。
func ServiceAwaitQuestionFromResult(result json.RawMessage) string {
	var parsed struct {
		Output struct {
			Question string `json:"question"`
		} `json:"output"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Output.Question)
}

// ServiceAgentDescriptor 是服务目录条目（MCP tools/list 的来源；每个 agent 取当前版本）。
type ServiceAgentDescriptor struct {
	ID                    int64
	Slug                  string
	Name                  string
	Version               int64
	AsyncThresholdSeconds int
	// 归属与可见性（设计 P2）：internal=平台/运维发布，user=用户自助上架。
	OwnerKind  string
	OwnerUID   int64
	Visibility string
}

// Service agent 归属与可见性常量（设计 §4.3）。
const (
	ServiceAgentOwnerInternal = "internal"
	ServiceAgentOwnerUser     = "user"

	ServiceAgentVisibilityPublic  = "public"  // 目录可见，任何人可加/可调
	ServiceAgentVisibilityPrivate = "private" // 仅 owner 自己可见可调（不出现在别人的目录）
)

var (
	// ErrServiceAgentOwnedByOther 表示 slug 已被别的 owner 占用（用户自助上架撞名）。
	ErrServiceAgentOwnedByOther = errors.New("service agent owned by another owner")
	// ErrServiceAgentInternalReserved 表示用户试图抢占平台内部 agent 的 slug。
	ErrServiceAgentInternalReserved = errors.New("service agent slug reserved for internal publishing")
)

// ServiceAgentVisibleTo 判断 agent 是否对调用者可见（目录过滤 + 调用准入共用）。
// public 对所有人可见；其余（private）仅对 owner 本人可见。
func ServiceAgentVisibleTo(agent ServiceAgentDescriptor, callerUID int64) bool {
	if agent.Visibility == "" || agent.Visibility == ServiceAgentVisibilityPublic {
		return true
	}
	return agent.OwnerUID != 0 && agent.OwnerUID == callerUID
}

// ServiceAgentStore 是服务调用面的编排存储（PG 主实现 + MySQL 镜像）。
// 同会话保串行的核心：CreateServiceRunIdempotent 在单事务内锁 session 行，
// client_token 命中返回既有 run；否则存在活跃 run 时返回 ErrServiceSessionBusy。
type ServiceAgentStore interface {
	GetServiceAgentBySlug(ctx context.Context, slug string) (id int64, name string, currentVersion int64, thresholdSeconds int, err error)
	ListServiceAgents(ctx context.Context) ([]ServiceAgentDescriptor, error)
	// GetServiceAgentDefinition 取指定版本的定义（claim 签发服务 token 用）。
	// definitionRev<=0 = 最新版本；指定版本不存在时回退最新（会话钉定语义的兑底，不阻斯工具）。
	GetServiceAgentDefinition(ctx context.Context, agentID int64, definitionRev int64) (slug string, thresholdSeconds int, definitionJSON json.RawMessage, err error)
	// 云端包分发：发布新版本（packageHash 内容寻址；同 hash 幂等返回既有版本）与按 hash 取包。
	// slug 不存在时用 name 自动注册新 agent（发布即上架，设计 A §3）；已存在且非 active → ErrServiceAgentNotFound。
	// force=true 时同 hash 也建新版本（回滚通道：重发旧包 → 新版本指向旧内容）。
	PublishServiceAgentPackage(ctx context.Context, slug, name string, definition json.RawMessage, packageHash string, bundle json.RawMessage, force bool) (version int64, err error)
	// ClaimServiceAgentOwnership 为用户自助上架占位 slug（设计 P2）。
	//   - slug 不存在：以给定 owner/visibility 建立条目（state=active）；
	//   - 已存在且 owner 匹配：更新 name/visibility；
	//   - 已存在且 owner 不同（含平台 internal 条目）→ ErrServiceAgentOwnedByOther / ErrServiceAgentInternalReserved。
	// 幂等：同一 owner 重复调用不报错（用于重复发布时刷新可见性）。
	ClaimServiceAgentOwnership(ctx context.Context, slug, name, ownerKind string, ownerUID int64, visibility string) error
	GetServiceAgentPackage(ctx context.Context, packageHash string) (bundle json.RawMessage, err error)
	// GetServiceAgentPackageHash 返回该 agent 当前最新版本的 assets_hash（无包时为空串）。
	GetServiceAgentPackageHash(ctx context.Context, agentID int64) (string, error)
	CreateServiceSession(ctx context.Context, agentID, definitionRev, callerUID, callerBotUID int64, assetsHash string) (string, error)
	GetServiceSession(ctx context.Context, sessionID string) (ServiceSession, error)
	GetServiceRun(ctx context.Context, runID string) (ServiceRun, error)
	CreateServiceRunIdempotent(ctx context.Context, sessionID, clientToken string, input json.RawMessage) (ServiceRun, bool, error)
	ClaimServiceRuns(ctx context.Context, instanceID string, limit int) ([]ServiceRun, error)
	// StartServiceRun 把指定 run 从 submitted 推到 running 并写入租约（外部执行后端用）。
	// 返回 false = 已被其他执行方接管或已终态（调用方应放弃本次执行）。
	StartServiceRun(ctx context.Context, runID, instanceID string, leaseSeconds int) (bool, error)
	// ---- run 级账本（设计 §4.5：谁调用谁付费、按量计费）----
	// ListBillableServiceRuns 列出已终态但尚未记账的 run（含 agent/会话归属与定义快照）。
	ListBillableServiceRuns(ctx context.Context, limit int) ([]ServiceRunBillable, error)
	// RecordServiceRunLedger 写入一行账本；run 已记账 → (false, nil)（幂等）。
	RecordServiceRunLedger(ctx context.Context, entry ServiceRunLedgerEntry) (bool, error)
	RenewServiceLease(ctx context.Context, runID, instanceID string) (bool, error)
	AppendServiceRunEvent(ctx context.Context, runID, typ string, payload json.RawMessage) (int64, error)
	ListServiceRunEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]ServiceRunEventRecord, error)
	SaveServiceCheckpoint(ctx context.Context, sessionID string, revision int64, snapshot json.RawMessage) error
	LatestServiceCheckpoint(ctx context.Context, sessionID string) (revision int64, snapshot json.RawMessage, err error)
	CompleteServiceRun(ctx context.Context, runID, resultID string, result json.RawMessage, usage json.RawMessage) (bool, error)
	// AwaitServiceRunInput 把 running run 收尾为 awaiting_input（回合制返问；设计 §3.3-8）。
	// 语义同 CompleteServiceRun：同 result_id 幂等重放返回 (false, nil)；非 running → ErrServiceRunConflict。
	// 收尾时同事务写入回执（回注通道），question 从 result envelope 的 output.question 提取。
	AwaitServiceRunInput(ctx context.Context, runID, resultID string, result json.RawMessage) (bool, error)
	FailServiceRun(ctx context.Context, runID, errorText string, retryable bool) error
	ReapExpiredServiceRuns(ctx context.Context) (int, error)
	// ---- 生命周期回注收件箱（设计 §4.8-2）----
	// ListUndeliveredServiceRunReceipts 按创建序返回待投递回执（投递 worker 扫描用）。
	ListUndeliveredServiceRunReceipts(ctx context.Context, limit int) ([]ServiceRunReceipt, error)
	// MarkServiceRunReceiptDelivered 标记回执已送达（≥1 在线连接接收）；已送达则无操作。
	MarkServiceRunReceiptDelivered(ctx context.Context, receiptID string) error
	// TouchServiceRunReceiptDelivery 记录一次投递尝试（离线未送达时累计，可观测）。
	TouchServiceRunReceiptDelivery(ctx context.Context, receiptID string) error
	// ---- 应用目录订阅清单（设计 §3.3-9"模式二：默认开放 + 个人清单"）----
	// ListHiddenServiceAgentIDs 返回该账号从清单中移除（屏蔽）的 agent id 集；
	// tools/list = 全部 active agent − 本集（无行 = 默认可见）。清单跟账号（uid）。
	ListHiddenServiceAgentIDs(ctx context.Context, uid int64) ([]int64, error)
	// SetServiceAgentSubscriptionHidden 调整该账号的目录清单：
	// hidden=true 屏蔽（目录"删除"；调用亦拒绝）；hidden=false 恢复（目录"添加"）。幂等。
	SetServiceAgentSubscriptionHidden(ctx context.Context, uid, agentID int64, hidden bool) error
	// RecordServiceInstanceHeartbeat 落库实例心跳（监控数据源；设计 §7.1"实例在线数"）。
	// UPSERT 语义：每实例一行最新值；last_seen_at 刷新（在线判定=now-last_seen < 阈值）。
	RecordServiceInstanceHeartbeat(ctx context.Context, instanceID string, activeRuns int, metrics json.RawMessage) error
}

package store

import (
	"context"
	"encoding/json"
	"errors"
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

// ServiceAgentDescriptor 是服务目录条目（MCP tools/list 的来源；每个 agent 取当前版本）。
type ServiceAgentDescriptor struct {
	ID                    int64
	Slug                  string
	Name                  string
	Version               int64
	AsyncThresholdSeconds int
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
	GetServiceAgentPackage(ctx context.Context, packageHash string) (bundle json.RawMessage, err error)
	// GetServiceAgentPackageHash 返回该 agent 当前最新版本的 assets_hash（无包时为空串）。
	GetServiceAgentPackageHash(ctx context.Context, agentID int64) (string, error)
	CreateServiceSession(ctx context.Context, agentID, definitionRev, callerUID, callerBotUID int64, assetsHash string) (string, error)
	GetServiceSession(ctx context.Context, sessionID string) (ServiceSession, error)
	GetServiceRun(ctx context.Context, runID string) (ServiceRun, error)
	CreateServiceRunIdempotent(ctx context.Context, sessionID, clientToken string, input json.RawMessage) (ServiceRun, bool, error)
	ClaimServiceRuns(ctx context.Context, instanceID string, limit int) ([]ServiceRun, error)
	RenewServiceLease(ctx context.Context, runID, instanceID string) (bool, error)
	AppendServiceRunEvent(ctx context.Context, runID, typ string, payload json.RawMessage) (int64, error)
	ListServiceRunEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]ServiceRunEventRecord, error)
	SaveServiceCheckpoint(ctx context.Context, sessionID string, revision int64, snapshot json.RawMessage) error
	LatestServiceCheckpoint(ctx context.Context, sessionID string) (revision int64, snapshot json.RawMessage, err error)
	CompleteServiceRun(ctx context.Context, runID, resultID string, result json.RawMessage, usage json.RawMessage) (bool, error)
	FailServiceRun(ctx context.Context, runID, errorText string, retryable bool) error
	ReapExpiredServiceRuns(ctx context.Context) (int, error)
	// RecordServiceInstanceHeartbeat 落库实例心跳（监控数据源；设计 §7.1"实例在线数"）。
	// UPSERT 语义：每实例一行最新值；last_seen_at 刷新（在线判定=now-last_seen < 阈值）。
	RecordServiceInstanceHeartbeat(ctx context.Context, instanceID string, activeRuns int, metrics json.RawMessage) error
}

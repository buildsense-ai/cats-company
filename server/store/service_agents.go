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

// ServiceAgentStore 是服务调用面的编排存储（PG 主实现 + MySQL 镜像）。
// 同会话保串行的核心：CreateServiceRunIdempotent 在单事务内锁 session 行，
// client_token 命中返回既有 run；否则存在活跃 run 时返回 ErrServiceSessionBusy。
type ServiceAgentStore interface {
	GetServiceAgentBySlug(ctx context.Context, slug string) (id int64, name string, currentVersion int64, thresholdSeconds int, err error)
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
}

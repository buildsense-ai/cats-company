package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrComputeTaskNotFound = errors.New("compute task not found")
	ErrComputeTaskConflict = errors.New("compute task state changed")
	ErrComputeQuoteExpired = errors.New("compute quote expired")
)

const (
	ComputeTaskQuoted    = "quoted"
	ComputeTaskQueued    = "queued"
	ComputeTaskActive    = "active"
	ComputeTaskCompleted = "completed"
	ComputeTaskFailed    = "failed"
	ComputeTaskLeaseTTL  = 45 * time.Second
)

// ComputeTaskSpec contains the minimum per-node allocation for the fixed CPU workload.
type ComputeTaskSpec struct {
	Nodes                     int `json:"nodes" jsonschema:"quote 时的节点数，支持 1 至 2 个节点"`
	LogicalCPUs               int `json:"logical_cpus" jsonschema:"每节点最低逻辑 CPU 数，必须为正数"`
	MemoryMiB                 int `json:"memory_mib" jsonschema:"每节点最低内存 MiB，必须为正数"`
	WallSeconds               int `json:"wall_seconds" jsonschema:"Slurm 作业最长运行秒数，60 至 300 秒且必须是整分钟"`
	MaxResourceSecondsPerNode int `json:"max_resource_seconds_per_node" jsonschema:"每节点资源计费上限，从平台实际观察到节点资源开始计算，必须为正数、整分钟且不小于 wall_seconds"`
}

type ComputeTaskRate struct {
	UnitsPerNodePeriod int64 `json:"units_per_node_period"`
	PeriodSeconds      int64 `json:"period_seconds"`
	IncrementSeconds   int64 `json:"increment_seconds"`
}

// ComputeTaskQuote pins the operator-managed SKU and tariff for admission and settlement.
type ComputeTaskQuote struct {
	ID             string          `json:"quote_id"`
	Spec           ComputeTaskSpec `json:"task"`
	SKU            string          `json:"sku"`
	SKUCPUs        int             `json:"sku_logical_cpus"`
	SKUMemoryMiB   int             `json:"sku_memory_mib"`
	Rate           ComputeTaskRate `json:"rate"`
	MaxUnits       int64           `json:"max_units"`
	MaxNodeSeconds int64           `json:"max_node_seconds"`
	ExpiresAt      time.Time       `json:"expires_at"`
}

// ComputeTaskProgress is written only by the platform worker.
type ComputeTaskProgress struct {
	Phase                string     `json:"phase,omitempty"`
	QueueName            string     `json:"queue_name,omitempty"`
	QueueID              string     `json:"queue_id,omitempty"`
	QueueCreateOperation string     `json:"queue_create_operation,omitempty"`
	QueueDeleteOperation string     `json:"queue_delete_operation,omitempty"`
	MasterOrderID        string     `json:"master_order_id,omitempty"`
	InstanceIDs          []string   `json:"instance_ids,omitempty"`
	VolumeIDs            []string   `json:"volume_ids,omitempty"`
	SlurmExitCode        string     `json:"slurm_exit_code,omitempty"`
	SlurmNodeList        []string   `json:"slurm_node_list,omitempty"`
	ExpandOperation      string     `json:"expand_operation,omitempty"`
	NodeIDs              []string   `json:"node_ids,omitempty"`
	NodeNames            []string   `json:"node_names,omitempty"`
	ProvisionStartedAt   *time.Time `json:"provision_started_at,omitempty"`
	SlurmJobID           string     `json:"slurm_job_id,omitempty"`
	SlurmState           string     `json:"slurm_state,omitempty"`
	SlurmElapsed         int64      `json:"slurm_elapsed_seconds,omitempty"`
	SlurmAllocatedCPUs   int        `json:"slurm_allocated_cpus,omitempty"`
	SlurmRequestedMem    string     `json:"slurm_requested_memory,omitempty"`
	ShrinkOperation      string     `json:"shrink_operation,omitempty"`
	ReleasedAt           *time.Time `json:"released_at,omitempty"`
	Problem              string     `json:"problem,omitempty"`
}

type ComputeTaskRun struct {
	ComputeTaskQuote
	UID          int64               `json:"-"`
	State        string              `json:"state"`
	Progress     ComputeTaskProgress `json:"progress"`
	ConfirmedAt  *time.Time          `json:"confirmed_at,omitempty"`
	ChargedUnits *int64              `json:"charged_units,omitempty"`
	Lease        string              `json:"-"`
}

// ComputeTaskStore reserves and settles funds in the same transaction as each
// corresponding state change. The lease fences updates from replaced workers.
type ComputeTaskStore interface {
	CreateComputeTaskQuote(context.Context, int64, ComputeTaskQuote) error
	GetComputeTask(context.Context, int64, string) (*ComputeTaskRun, error)
	ConfirmComputeTask(context.Context, int64, string, int64, time.Time) (*ComputeTaskRun, error)
	GetComputeTaskForWorker(context.Context, string) (*ComputeTaskRun, error)
	ClaimComputeTask(context.Context, string, time.Time, time.Time) (*ComputeTaskRun, error)
	SaveComputeTaskProgress(context.Context, string, string, ComputeTaskProgress) error
	FinishComputeTask(context.Context, string, string, ComputeTaskProgress, int64, bool, time.Time) error
}

// ComputeTaskReceiptContract 是算力任务终态回执 payload 的契约版本（= WS 消息体）。
const ComputeTaskReceiptContract = "catsco.compute-task-receipt.v1"

// ComputeTaskReceipt 是算力任务终态回执的收件箱条目：与 finish 的状态变更同一事务
// 写入，投递器按 caller_uid 推给调用方在线连接（至少一次 + 离线留存 + receipt_id 幂等）。
// 它与 service_run_receipts 是两条独立链路：算力任务不走 service_runs。
type ComputeTaskReceipt struct {
	ReceiptID        string
	QuoteID          string
	CallerUID        int64
	Status           string
	Payload          json.RawMessage
	DeliveryAttempts int
	CreatedAt        time.Time
}

// ComputeTaskReceiptPayload 是回执载荷（字段即 WS 消息载荷）。
// status 是回执状态（= 终态），state 是落盘那一刻任务行的 state；两者同值但分开上报，
// 让客户端不必猜「回执状态」和「任务状态」是不是一个概念。
type ComputeTaskReceiptPayload struct {
	ContractVersion string              `json:"contract_version"`
	ReceiptID       string              `json:"receipt_id"`
	QuoteID         string              `json:"quote_id"`
	Status          string              `json:"status"`
	State           string              `json:"state"`
	ChargedUnits    int64               `json:"charged_units"`
	Progress        ComputeTaskProgress `json:"progress"`
	CreatedAt       string              `json:"created_at"`
}

// BuildComputeTaskReceiptPayload 组装回执 JSON；生成失败时退回最小可解析对象，
// 绝不因为编码问题让结算事务失败。
func BuildComputeTaskReceiptPayload(receiptID, quoteID, status, state string, chargedUnits int64, progress ComputeTaskProgress, createdAt time.Time) json.RawMessage {
	payload := ComputeTaskReceiptPayload{
		ContractVersion: ComputeTaskReceiptContract,
		ReceiptID:       receiptID,
		QuoteID:         quoteID,
		Status:          status,
		State:           state,
		ChargedUnits:    chargedUnits,
		Progress:        progress,
		CreatedAt:       createdAt.UTC().Format(time.RFC3339),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"contract_version":"` + ComputeTaskReceiptContract + `","receipt_id":"` + receiptID + `"}`)
	}
	return encoded
}

// ComputeTaskReceiptIDForQuote 由 quote_id 确定性派生回执 id（幂等去重载体：
// "hpc_xxx" → "ctr_xxx"）——同一个任务至多一份回执，重投不产生重复。
func ComputeTaskReceiptIDForQuote(quoteID string) string {
	suffix := strings.TrimPrefix(quoteID, "hpc_")
	if suffix == "" {
		suffix = quoteID
	}
	return "ctr_" + suffix
}

// ComputeTaskReceiptTerminal 报告 state 是否是会写回执的终态。
func ComputeTaskReceiptTerminal(state string) bool {
	return state == ComputeTaskCompleted || state == ComputeTaskFailed
}

// ComputeTaskReceiptStore 是算力任务回执收件箱的读写能力。它独立于 ComputeTaskStore：
// 老替身（只实现生命周期）不会因为多一个收件箱而编译失败，投递器用 type assertion 注入。
type ComputeTaskReceiptStore interface {
	// ListUndeliveredComputeTaskReceipts 按创建序返回待投递回执（投递器扫描用）。
	ListUndeliveredComputeTaskReceipts(ctx context.Context, limit int) ([]ComputeTaskReceipt, error)
	// MarkComputeTaskReceiptDelivered 标记回执已送达（≥1 在线连接接收）；已送达则无操作。
	MarkComputeTaskReceiptDelivered(ctx context.Context, receiptID string) error
	// TouchComputeTaskReceiptDelivery 记录一次投递尝试（离线未送达时累计，可观测）。
	TouchComputeTaskReceiptDelivery(ctx context.Context, receiptID string) error
}

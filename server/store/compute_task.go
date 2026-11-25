package store

import (
	"context"
	"errors"
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
	Nodes       int `json:"nodes"`
	LogicalCPUs int `json:"logical_cpus"`
	MemoryMiB   int `json:"memory_mib"`
	WallSeconds int `json:"wall_seconds"`
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

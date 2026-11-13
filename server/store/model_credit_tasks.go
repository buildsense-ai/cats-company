package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 原始（root）任务的额度门禁与单任务保留（docs/credit-plan-rules-20261109.md
// P0-2/P0-3/P0-4）。
//
// Relay 每启动一个原始任务就带一个 root task id（重试、子步骤、子代理都沿用它），
// 平台的记账只按 root 任务讲：账本预留必须挂在一个已经 start 且当时 allowed 的
// root 上，额度耗尽时只保留「最近开始」的那个 running root 继续透支，其余暂停。
//
// enabled 与 allowed 是两件事：
//   - enabled 说明这个用户进过 credit 体系（有账本账户，或买过/被发过 credit 套餐）。
//     从没进过的老用户 enabled=false、allowed=true，走原来的额度逻辑。
//   - allowed 说明这个 root 任务此刻能不能跑。套餐过期是 enabled=true、allowed=false
//     （reason=entitlement_expired），绝不能因为权限失效就把用户退回 legacy。
const (
	ModelCreditTaskRunning   = "running"
	ModelCreditTaskPaused    = "paused"
	ModelCreditTaskCompleted = "completed"
)

const (
	ModelCreditTaskActionStart  = "start"
	ModelCreditTaskActionStatus = "status"
	ModelCreditTaskActionResume = "resume"
	ModelCreditTaskActionFinish = "finish"
)

// reason 是给 Relay/前端看的机器码，不翻译。
const (
	ModelCreditTaskReasonOK                 = "ok"
	ModelCreditTaskReasonLegacy             = "legacy"
	ModelCreditTaskReasonQuotaExhausted     = "quota_exhausted"
	ModelCreditTaskReasonDebtOutstanding    = "debt_outstanding"
	ModelCreditTaskReasonEntitlementExpired = "entitlement_expired"
	ModelCreditTaskReasonRetainedOther      = "retained_task_other"
	ModelCreditTaskReasonTaskCompleted      = "task_completed"
	// ModelCreditTaskReasonResumeRequired 表示任务是被暂停的：额度/权限都已经恢复，
	// 但按规则只能由用户手动 resume，系统不会自己把它拉起来（P0-3.7）。
	ModelCreditTaskReasonResumeRequired = "manual_resume_required"
	// ModelCreditTaskReasonPlanTierUnavailable 表示这个 root 没有可信的套餐档位快照：
	// 既算不出费率，也不能猜，只能失败关闭（新任务拒绝开始；旧任务不许 resume、
	// 不许 reserve）。
	ModelCreditTaskReasonPlanTierUnavailable = "plan_tier_unavailable"
)

// 套餐档位快照的取值范围（docs/credit-plan-rules-20261109.md：任务运行中套餐到期，
// 按到期前费率跑到结束）。0 表示「没有可信快照」：legacy 用户，或者列上线之前就
// 写下的任务行。1..3 是 commercial_plans.plan_tier 的合法档位，start 时快照一次，
// 之后 start 重放/status/resume/finish 都返回同一份存档。
const (
	ModelCreditTaskPlanTierNone = 0
	ModelCreditTaskPlanTierMin  = 1
	ModelCreditTaskPlanTierMax  = 3
)

// ModelCreditTaskPlanTierTrusted 报告这份快照能不能用来计价。false 时调用方必须失败
// 关闭，绝不能替它猜一个档位。
func ModelCreditTaskPlanTierTrusted(tier int) bool {
	return tier >= ModelCreditTaskPlanTierMin && tier <= ModelCreditTaskPlanTierMax
}

// ModelCreditTaskIDMaxLen 与 model_credit_tasks.root_task_id 列一致。
const ModelCreditTaskIDMaxLen = 128

var (
	// ErrModelCreditTaskRequired：reserve 没有带 root_task_id。缺 root 不能绕过门禁。
	ErrModelCreditTaskRequired = errors.New("model credit root task id is required")
	// ErrModelCreditTaskNotFound：这个 root 任务没有 start 过。
	ErrModelCreditTaskNotFound = errors.New("model credit root task not found")
	// ErrModelCreditTaskNotRunning：root 存在但不是 running（暂停/已完成），不能再预留。
	ErrModelCreditTaskNotRunning = errors.New("model credit root task is not running")
	// ErrModelCreditTaskTierUnavailable：root 存在且 running，但没有可信的套餐档位快照
	// （列上线前写下的旧行）。算不出费率就不能继续花钱，只能失败关闭。
	ErrModelCreditTaskTierUnavailable = errors.New("model credit root task plan tier unavailable")
)

// ValidateModelCreditTaskID 报告 root task id 能不能作为账本键的一部分。
func ValidateModelCreditTaskID(taskID string) error {
	if !validModelCreditToken(taskID, ModelCreditTaskIDMaxLen) {
		return ErrModelCreditInvalid
	}
	return nil
}

// ModelCreditTask 是一个 root 任务在账本里的状态。StartedSeq 是单用户的启动顺序，
// 额度耗尽时用它挑「最近开始」的那个。PlanTier 是 start 时锁定的套餐档位快照：
// 0 = 没有可信快照（legacy 用户 / 列上线前的旧行），1..3 = 计价档位。
type ModelCreditTask struct {
	UID        int64
	TaskID     string
	State      string
	Reason     string
	PlanTier   int
	StartedAt  time.Time
	StartedSeq int64
	UpdatedAt  time.Time
}

func (t ModelCreditTask) Validate() error {
	if t.UID <= 0 || ValidateModelCreditTaskID(t.TaskID) != nil || t.StartedSeq <= 0 || t.StartedAt.IsZero() {
		return ErrModelCreditInvalid
	}
	if t.PlanTier < ModelCreditTaskPlanTierNone || t.PlanTier > ModelCreditTaskPlanTierMax {
		return ErrModelCreditInvalid
	}
	switch t.State {
	case ModelCreditTaskRunning, ModelCreditTaskPaused, ModelCreditTaskCompleted:
	default:
		return ErrModelCreditInvalid
	}
	return nil
}

// ModelCreditTaskInput 是一次 start/status/resume/finish 请求。
type ModelCreditTaskInput struct {
	UID    int64
	TaskID string
	Action string
}

func (in ModelCreditTaskInput) Validate() error {
	if in.UID <= 0 || ValidateModelCreditTaskID(in.TaskID) != nil {
		return ErrModelCreditInvalid
	}
	switch in.Action {
	case ModelCreditTaskActionStart, ModelCreditTaskActionStatus,
		ModelCreditTaskActionResume, ModelCreditTaskActionFinish:
		return nil
	default:
		return ErrModelCreditInvalid
	}
}

// ModelCreditTaskDecision 是 Relay 拿到的判定结果。Allowed 只看这个 root 任务现在
// 能不能跑；enabled=false + allowed=true 就是「老用户，照旧」。
//
// PlanTier 是这个 root 的套餐档位快照（0 = 没有可信快照），四个 action 都回同一份：
// Relay 用它计价，绝不能改用 eligibility 的现读档位（套餐一过期就没了）。
type ModelCreditTaskDecision struct {
	Enabled        bool
	Allowed        bool
	TaskID         string
	State          string
	PlanTier       int
	RetainedTaskID string
	Reason         string
	SpendableUnits int64
	DebtUnits      int64
}

// ModelCreditTaskStore 是任务门禁的存储面（PostgreSQL 适配器实现）。
type ModelCreditTaskStore interface {
	ApplyModelCreditTask(context.Context, ModelCreditTaskInput) (*ModelCreditTaskDecision, error)
	GetModelCreditTask(context.Context, int64, string) (*ModelCreditTask, error)
}

// NormalizeModelCreditTaskAction 把外部传进来的 action 收紧成四个合法值。
func NormalizeModelCreditTaskAction(action string) (string, bool) {
	switch normalized := strings.ToLower(strings.TrimSpace(action)); normalized {
	case ModelCreditTaskActionStart, ModelCreditTaskActionStatus,
		ModelCreditTaskActionResume, ModelCreditTaskActionFinish:
		return normalized, true
	default:
		return "", false
	}
}

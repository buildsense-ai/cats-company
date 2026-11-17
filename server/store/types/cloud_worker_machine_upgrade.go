package types

import "time"

// 云机器点数升配（machine upgrade）的持久事实。
//
// 三张表对应三种生命周期完全不同的东西：
//   - 报价（quote）：5 分钟就失效的「我现在给你这个价」。
//   - 操作（operation）：一次付点数的升配动作，可能跨进程、可能结果未知。
//   - 权益（term）：升配买到的那一档，独立于任何套餐。
//
// 档位词汇和共享容器集群的 runtime plan 一致（light/standard/pro/heavy）。

const (
	MachinePlanLight    = "light"
	MachinePlanStandard = "standard"
	MachinePlanPro      = "pro"
	MachinePlanHeavy    = "heavy"
)

// MachineUpgradeStatus* 是持久操作的状态机。
//
//	pending     : 已受理，还没动余额和机器
//	running     : 点数已预占，change-plan 脚本正在跑
//	completed   : 脚本成功，点数已结算，升配权益已落库
//	failed      : 明确失败且确认无副作用，预占已释放
//	reconciling : 结果未知（超时/进程中断），预占保留，等后台 reconcile 收尾
const (
	MachineUpgradeStatusPending     = "pending"
	MachineUpgradeStatusRunning     = "running"
	MachineUpgradeStatusCompleted   = "completed"
	MachineUpgradeStatusFailed      = "failed"
	MachineUpgradeStatusReconciling = "reconciling"
	// Automatic attempts are bounded; an operator may authorize one further
	// attempt after investigating the adapter. Attempts remains cumulative.
	MachineUpgradeMaxAutomaticAttempts = 5
)

// MachinePlanRank orders the tiers so "upgrade" has one meaning everywhere.
// Unknown plans return 0.
func MachinePlanRank(plan string) int {
	switch plan {
	case MachinePlanLight:
		return 1
	case MachinePlanStandard:
		return 2
	case MachinePlanPro:
		return 3
	case MachinePlanHeavy:
		return 4
	default:
		return 0
	}
}

// MachineUpgradeQuote is one priced offer bound to a specific buyer, machine,
// current tier and remaining term.
type MachineUpgradeQuote struct {
	QuoteID         string
	UID             int64
	TenantName      string
	LifecycleID     int64
	FromPlan        string
	ToPlan          string
	CostUnits       int64
	WorkerExpiresAt time.Time
	ExpiresAt       time.Time
}

// MachineUpgradeOperation is the durable record of one paid tier change.
type MachineUpgradeOperation struct {
	OperationID string
	UID         int64
	TenantName  string
	LifecycleID int64
	FromPlan    string
	ToPlan      string
	CostUnits   int64
	Status      string
	// Held reports that the units are currently taken out of the buyer's
	// buckets by this operation. Settled reports they have become a charge.
	Held             bool
	Settled          bool
	ClientRequestID  string
	QuoteID          string
	Attempts         int
	Message          string
	MachineExpiresAt time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// MachinePlanTerm is the independent upgrade entitlement of one worker: which
// tier it runs, until when, and which operation bought it. A package renewal
// may extend the worker's term but must never lower this tier inside the window.
//
// It is not a standalone fact: the entitlement belongs to the lifecycle row
// that sold the machine (LifecycleID + OwnerUID), and it is only in force while
// that lifecycle is still active with an unexpired package. A refunded or
// shortened package, a deleted machine, or a tenant name reused by another
// lifecycle therefore cancels it instead of letting the next machine or the
// next owner inherit a tier somebody else paid for.
type MachinePlanTerm struct {
	TenantName  string
	OwnerUID    int64
	LifecycleID int64
	Plan        string
	BasePlan    string
	OperationID string
	// ExpiresAt is already clamped to the current lifecycle package expiry, so
	// comparing it with the clock is the whole "still in force" test.
	ExpiresAt time.Time
	UpdatedAt time.Time
}

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 云机器点数升配的持久层。
//
// 这里做三件必须在一个事务里完成的事，顺序不能颠倒：
//  1. 预占点数（monthly → paid，禁止动 gift，禁止透支，欠债账户直接拒绝）。
//  2. 只有脚本被确认执行成功，才把预占结算成真实扣费并写入升配权益。
//  3. 明确失败且确认无副作用，才把预占退回去；结果未知时保留预占，
//     交给后台 reconcile 继续核对，绝不「既退款又升配」。
//
// gift 点数来自邀请奖励，只能用于模型调用，不能换成机器升配（报价与扣费都排除它）。
//
// 幂等来自三处：账本事件键（reserve/settle/release 各带 operation id）、
// 操作行上的 client_request_id 唯一约束、以及 reconcile 认领时对 updated_at
// 的租约检查与比较交换。同一笔操作重放不会二次扣费，两个 reconciler 也不会
// 同时执行同一笔脚本。

var (
	// ErrMachineUpgradeConflict 表示同一台机器已经有未完成的升配，或同一个
	// client_request_id 被用在了另一笔不同的升配上。
	ErrMachineUpgradeConflict = store.ErrMachineUpgradeConflict
	// ErrMachineUpgradeNotFound 表示这笔操作不属于该用户。
	ErrMachineUpgradeNotFound = store.ErrMachineUpgradeNotFound
	// ErrMachineUpgradeInsufficient 表示余额不足；此时什么都没动。
	ErrMachineUpgradeInsufficient = store.ErrModelCreditInsufficient
)

const machineUpgradeColumns = `operation_id, uid, tenant_name, lifecycle_id, from_plan, to_plan,
	cost_units, status, held, settled, client_request_id, quote_id, attempts, message,
	machine_expires_at, started_at, finished_at, created_at, updated_at`

// machineUpgradeReconcileClaim 是一笔操作被认领后的保护期。reconciler 只有在
// 行上的 updated_at 已经超过这个时长时才允许接手 running/reconciling，否则两个
// reconciler 会把同一个改档脚本跑两遍；认领本身再对 updated_at 做一次比较交换，
// 保证只有一个人真正拿到。
const machineUpgradeReconcileClaim = 3 * time.Minute

func scanMachineUpgrade(scanner interface {
	Scan(dest ...interface{}) error
}) (*types.MachineUpgradeOperation, error) {
	var op types.MachineUpgradeOperation
	var startedAt, finishedAt sql.NullTime
	if err := scanner.Scan(
		&op.OperationID, &op.UID, &op.TenantName, &op.LifecycleID, &op.FromPlan, &op.ToPlan,
		&op.CostUnits, &op.Status, &op.Held, &op.Settled, &op.ClientRequestID, &op.QuoteID,
		&op.Attempts, &op.Message, &op.MachineExpiresAt, &startedAt, &finishedAt,
		&op.CreatedAt, &op.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if startedAt.Valid {
		value := startedAt.Time
		op.StartedAt = &value
	}
	if finishedAt.Valid {
		value := finishedAt.Time
		op.FinishedAt = &value
	}
	return &op, nil
}

// CreateMachineUpgradeQuote stores one short-lived offer.
func (a *Adapter) CreateMachineUpgradeQuote(quote *types.MachineUpgradeQuote) error {
	if quote == nil || strings.TrimSpace(quote.QuoteID) == "" || quote.UID <= 0 ||
		strings.TrimSpace(quote.TenantName) == "" || quote.CostUnits <= 0 {
		return fmt.Errorf("invalid machine upgrade quote")
	}
	if _, err := a.db.Exec(`
		INSERT INTO cloud_worker_machine_upgrade_quotes(
			quote_id, uid, tenant_name, lifecycle_id, from_plan, to_plan, cost_units,
			worker_expires_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		quote.QuoteID, quote.UID, quote.TenantName, quote.LifecycleID, quote.FromPlan, quote.ToPlan,
		quote.CostUnits, quote.WorkerExpiresAt.UTC(), quote.ExpiresAt.UTC()); err != nil {
		return fmt.Errorf("create machine upgrade quote: %w", err)
	}
	return nil
}

// GetMachineUpgradeQuote reads one offer for its owner only.
func (a *Adapter) GetMachineUpgradeQuote(uid int64, quoteID string) (*types.MachineUpgradeQuote, error) {
	quote := types.MachineUpgradeQuote{}
	if err := a.db.QueryRow(`
		SELECT quote_id, uid, tenant_name, lifecycle_id, from_plan, to_plan, cost_units,
		       worker_expires_at, expires_at
		FROM cloud_worker_machine_upgrade_quotes WHERE quote_id = $1 AND uid = $2`,
		strings.TrimSpace(quoteID), uid).Scan(
		&quote.QuoteID, &quote.UID, &quote.TenantName, &quote.LifecycleID, &quote.FromPlan,
		&quote.ToPlan, &quote.CostUnits, &quote.WorkerExpiresAt, &quote.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read machine upgrade quote: %w", err)
	}
	return &quote, nil
}

// OpenMachineUpgrade records the intent. A replay of the same client request id
// returns the stored row with created=false; the same id used for a different
// tier or cost is a conflict, never a second charge.
func (a *Adapter) OpenMachineUpgrade(op *types.MachineUpgradeOperation) (*types.MachineUpgradeOperation, bool, error) {
	if op == nil || op.UID <= 0 || strings.TrimSpace(op.OperationID) == "" ||
		strings.TrimSpace(op.TenantName) == "" || strings.TrimSpace(op.ClientRequestID) == "" ||
		op.CostUnits < 0 {
		return nil, false, fmt.Errorf("invalid machine upgrade operation")
	}
	stored, err := scanMachineUpgrade(a.db.QueryRow(`
		INSERT INTO cloud_worker_machine_upgrades(
			operation_id, uid, tenant_name, lifecycle_id, from_plan, to_plan, cost_units,
			status, client_request_id, quote_id, machine_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9, $10)
		ON CONFLICT (uid, client_request_id) DO NOTHING
		RETURNING `+machineUpgradeColumns,
		op.OperationID, op.UID, op.TenantName, op.LifecycleID, op.FromPlan, op.ToPlan,
		op.CostUnits, op.ClientRequestID, op.QuoteID, op.MachineExpiresAt.UTC()))
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		if strings.Contains(err.Error(), "uk_machine_upgrades_open_tenant") {
			return nil, false, ErrMachineUpgradeConflict
		}
		return nil, false, fmt.Errorf("open machine upgrade: %w", err)
	}
	existing, err := scanMachineUpgrade(a.db.QueryRow(
		`SELECT `+machineUpgradeColumns+` FROM cloud_worker_machine_upgrades WHERE uid = $1 AND client_request_id = $2`,
		op.UID, op.ClientRequestID))
	if err != nil {
		return nil, false, fmt.Errorf("read replayed machine upgrade: %w", err)
	}
	if existing.TenantName != op.TenantName || existing.ToPlan != op.ToPlan || existing.CostUnits != op.CostUnits {
		return nil, false, ErrMachineUpgradeConflict
	}
	return existing, false, nil
}

// GetMachineUpgrade reads one operation for its owner only.
func (a *Adapter) GetMachineUpgrade(uid int64, operationID string) (*types.MachineUpgradeOperation, error) {
	op, err := scanMachineUpgrade(a.db.QueryRow(
		`SELECT `+machineUpgradeColumns+` FROM cloud_worker_machine_upgrades WHERE operation_id = $1 AND uid = $2`,
		strings.TrimSpace(operationID), uid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read machine upgrade: %w", err)
	}
	return op, nil
}

// StartMachineUpgradeCharge claims the operation and holds the points in one
// transaction. It is the only place machine-upgrade credit leaves an account,
// so a crash before the script can only ever leave a hold behind, never a
// silent free upgrade.
//
// allowRetry 只属于 reconciler 和人工复核：它不能把一笔 pending 直接变成扣费
// （那等于绕过用户下单），也必须在行锁之后确认 updated_at 已经超过
// machineUpgradeReconcileClaim 才能接手 running/reconciling，否则两个 reconciler
// 会同时跑同一个脚本。
//
// audit 只由人工复核的 HTTP 路径带进来，并且必须在同一个事务里落库：审计写不进去
// 就连认领一起回滚，绝不出现「操作被授权重跑、却没有留下谁授权的记录」。资金、
// 租约和终态的判断与没有审计时逐字相同——审计是附加事实，不是新的放行条件。
func (a *Adapter) StartMachineUpgradeCharge(operationID string, allowRetry bool) (*types.MachineUpgradeOperation, bool, error) {
	return a.startMachineUpgradeCharge(operationID, allowRetry, false, nil)
}

// RetryMachineUpgradeReview authorizes one leased attempt after operator review.
// It never reserves again and cannot revive pending or terminal operations.
//
// Deprecated: 这是不带审计的兼容入口，只留给已有的兼容性测试。管理端 HTTP 路径
// 必须走 RetryMachineUpgradeReviewAudited：认领必须留下授权人，正是这次修复的
// 目的，没有签字的认领不允许再出现。
func (a *Adapter) RetryMachineUpgradeReview(operationID string) (*types.MachineUpgradeOperation, bool, error) {
	return a.startMachineUpgradeCharge(operationID, true, true, nil)
}

// RetryMachineUpgradeReviewAudited authorizes one leased attempt after operator
// review, and records the authorization (actor, operation, reason and the row it
// was granted against) in the same transaction that takes the lease.
func (a *Adapter) RetryMachineUpgradeReviewAudited(operationID string, actorUID int64, reason string) (*types.MachineUpgradeOperation, bool, error) {
	return a.startMachineUpgradeCharge(operationID, true, true, &machineUpgradeReviewAuditInput{
		ActorUID: actorUID,
		Reason:   reason,
	})
}

func (a *Adapter) startMachineUpgradeCharge(operationID string, allowRetry, manualReview bool, audit *machineUpgradeReviewAuditInput) (*types.MachineUpgradeOperation, bool, error) {
	var reason string
	if audit != nil {
		// 审计只属于人工复核的认领。其它路径带审计进来是调用方写错了：默默丢掉
		// 等于把一次授权吞掉，所以直接拒绝，连行都不锁。
		if !manualReview || audit.ActorUID <= 0 {
			return nil, false, fmt.Errorf("machine upgrade review audit needs a manual review claim from a known actor")
		}
		var err error
		if reason, err = normalizeMachineUpgradeReviewReason(audit.Reason); err != nil {
			return nil, false, err
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("begin machine upgrade charge: %w", err)
	}
	defer tx.Rollback()
	op, err := scanMachineUpgrade(tx.QueryRow(
		`SELECT `+machineUpgradeColumns+` FROM cloud_worker_machine_upgrades WHERE operation_id = $1 FOR UPDATE`,
		strings.TrimSpace(operationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrMachineUpgradeNotFound
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock machine upgrade: %w", err)
	}
	now := time.Now().UTC()
	switch op.Status {
	case types.MachineUpgradeStatusCompleted, types.MachineUpgradeStatusFailed:
		return op, false, nil
	case types.MachineUpgradeStatusPending:
		// 重试永远不扣第一笔钱：pending 行从来没预占过，允许它扣费等于让
		// 后台把一笔没人付钱的操作变成真实扣费。过期 pending 交给 reconciler
		// 取消，不在这里补扣。
		if allowRetry {
			return op, false, nil
		}
		if err := reserveMachineUpgradeCredits(context.Background(), tx, op, now); err != nil {
			return nil, false, err
		}
		op.Held = true
	case types.MachineUpgradeStatusRunning, types.MachineUpgradeStatusReconciling:
		// 第二个请求不再扣费；只有 reconciler 可以续跑，而且只续跑已经预占
		// 但还没结算的操作。
		if !allowRetry || !op.Held || op.Settled {
			return op, false, nil
		}
		if !manualReview && op.Attempts >= types.MachineUpgradeMaxAutomaticAttempts {
			return op, false, nil
		}
		// 行锁之后再看租约：刚被别的 reconciler 认领过的行（updated_at 还在
		// 保护期内）必须放手，让它先跑完自己的脚本。
		if op.UpdatedAt.Add(machineUpgradeReconcileClaim).After(now) {
			return op, false, nil
		}
	default:
		return nil, false, fmt.Errorf("machine upgrade in unexpected state %q", op.Status)
	}
	// updated_at 的比较交换和上面的租约检查一起构成认领：即使有人绕过行锁，
	// 也只有把行从读到的那个 updated_at 推进的人才算真正认领成功。
	updated, err := scanMachineUpgrade(tx.QueryRow(`
		UPDATE cloud_worker_machine_upgrades
		SET status = 'running', held = TRUE, attempts = attempts + 1,
		    started_at = COALESCE(started_at, $2), finished_at = NULL, message = '',
		    updated_at = $2
		WHERE operation_id = $1 AND updated_at = $3
		RETURNING `+machineUpgradeColumns, op.OperationID, now, op.UpdatedAt))
	if errors.Is(err, sql.ErrNoRows) {
		// 这一行在我们读完之后被别人推进了：不重复执行脚本。
		return op, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("mark machine upgrade running: %w", err)
	}
	if audit != nil {
		// 认领已经发生，这里写「谁授权了这次重跑」。写不进去就让整笔事务回滚：
		// 行还停在原状态，谁都没拿到这次重跑，也就不存在无人签字的授权。
		if err := insertMachineUpgradeReviewAudit(tx, machineUpgradeReviewAuditRecord{
			OperationID:      updated.OperationID,
			ActorUID:         audit.ActorUID,
			Action:           machineUpgradeReviewAuditAction,
			Reason:           reason,
			PreviousStatus:   op.Status,
			PreviousMessage:  op.Message,
			PreviousAttempts: op.Attempts,
			ClaimedAttempt:   updated.Attempts,
			ClaimedAt:        updated.UpdatedAt,
		}); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit machine upgrade charge: %w", err)
	}
	return updated, true, nil
}

// CompleteMachineUpgrade settles the hold, records the upgrade entitlement and
// marks the operation completed. Replaying it is a no-op.
//
// 只有「预占还在、状态是 running/reconciling」的操作可以结算：pending 从来没付过钱，
// failed 的预占已经退回去了，让它们走到这里就等于免费升配。
func (a *Adapter) CompleteMachineUpgrade(operationID, plan string, basePlan string, machineExpiresAt time.Time) (*types.MachineUpgradeOperation, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin machine upgrade completion: %w", err)
	}
	defer tx.Rollback()
	op, err := scanMachineUpgrade(tx.QueryRow(
		`SELECT `+machineUpgradeColumns+` FROM cloud_worker_machine_upgrades WHERE operation_id = $1 FOR UPDATE`,
		strings.TrimSpace(operationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMachineUpgradeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock machine upgrade: %w", err)
	}
	if op.Status == types.MachineUpgradeStatusCompleted {
		return op, nil
	}
	if op.Status != types.MachineUpgradeStatusRunning && op.Status != types.MachineUpgradeStatusReconciling {
		return op, fmt.Errorf("machine upgrade %s is not settleable in state %q: %w",
			op.OperationID, op.Status, ErrMachineUpgradeConflict)
	}
	if !op.Held || op.Settled {
		return op, fmt.Errorf("machine upgrade %s has no outstanding hold: %w",
			op.OperationID, ErrMachineUpgradeConflict)
	}
	now := time.Now().UTC()
	if err := settleMachineUpgradeCredits(context.Background(), tx, op, now); err != nil {
		return nil, err
	}
	if err := upsertMachinePlanTerm(tx, op, plan, basePlan, machineExpiresAt, now); err != nil {
		return nil, err
	}
	updated, err := scanMachineUpgrade(tx.QueryRow(`
		UPDATE cloud_worker_machine_upgrades
		SET status = 'completed', held = FALSE, settled = TRUE, finished_at = $2,
		    message = '', updated_at = $2
		WHERE operation_id = $1
		RETURNING `+machineUpgradeColumns, op.OperationID, now))
	if err != nil {
		return nil, fmt.Errorf("complete machine upgrade: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit machine upgrade completion: %w", err)
	}
	return updated, nil
}

// FailMachineUpgrade releases the hold after a confirmed, side-effect-free
// failure and marks the operation failed.
func (a *Adapter) FailMachineUpgrade(operationID, message string) (*types.MachineUpgradeOperation, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin machine upgrade failure: %w", err)
	}
	defer tx.Rollback()
	op, err := scanMachineUpgrade(tx.QueryRow(
		`SELECT `+machineUpgradeColumns+` FROM cloud_worker_machine_upgrades WHERE operation_id = $1 FOR UPDATE`,
		strings.TrimSpace(operationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMachineUpgradeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock machine upgrade: %w", err)
	}
	if op.Status == types.MachineUpgradeStatusCompleted {
		// Never refund a machine that is already upgraded.
		return op, nil
	}
	now := time.Now().UTC()
	if op.Held && !op.Settled {
		if err := releaseMachineUpgradeCredits(context.Background(), tx, op, now); err != nil {
			return nil, err
		}
	}
	updated, err := scanMachineUpgrade(tx.QueryRow(`
		UPDATE cloud_worker_machine_upgrades
		SET status = 'failed', held = FALSE, finished_at = $2, message = $3, updated_at = $2
		WHERE operation_id = $1
		RETURNING `+machineUpgradeColumns, op.OperationID, now, truncateCommercialError(message)))
	if err != nil {
		return nil, fmt.Errorf("fail machine upgrade: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit machine upgrade failure: %w", err)
	}
	return updated, nil
}

// MarkMachineUpgradeReconciling keeps the hold and parks the operation for the
// background recovery loop. It never touches the balance.
//
// 已经是终态（completed/failed）的操作保留原状态；读回时必须按 operation id 查，
// 不能用「uid + operation id」的所有者查询——后台手里没有 uid，传 0 只会查不到。
func (a *Adapter) MarkMachineUpgradeReconciling(operationID, message string) (*types.MachineUpgradeOperation, error) {
	operationID = strings.TrimSpace(operationID)
	updated, err := scanMachineUpgrade(a.db.QueryRow(`
		UPDATE cloud_worker_machine_upgrades
		SET status = 'reconciling', message = $2, updated_at = CURRENT_TIMESTAMP
		WHERE operation_id = $1 AND status IN ('pending','running','reconciling')
		RETURNING `+machineUpgradeColumns, operationID, truncateCommercialError(message)))
	if errors.Is(err, sql.ErrNoRows) {
		return a.machineUpgradeByOperationID(operationID)
	}
	if err != nil {
		return nil, fmt.Errorf("mark machine upgrade reconciling: %w", err)
	}
	return updated, nil
}

// machineUpgradeByOperationID reads one operation without an owner filter. Only
// the reconciler's terminal read-back uses it: it already holds the operation id
// from the row it locked, and it has no uid to present.
func (a *Adapter) machineUpgradeByOperationID(operationID string) (*types.MachineUpgradeOperation, error) {
	op, err := scanMachineUpgrade(a.db.QueryRow(
		`SELECT `+machineUpgradeColumns+` FROM cloud_worker_machine_upgrades WHERE operation_id = $1`,
		strings.TrimSpace(operationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMachineUpgradeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read machine upgrade: %w", err)
	}
	return op, nil
}

// ListMachineUpgradesForReconcile returns operations whose process may be gone.
// Pending rows are included: a pending row whose opener died never took a hold,
// and the reconciler is the only one that can cancel it.
func (a *Adapter) ListMachineUpgradesForReconcile(staleAfter time.Duration, limit int) ([]*types.MachineUpgradeOperation, error) {
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := a.db.Query(`
		SELECT `+machineUpgradeColumns+` FROM cloud_worker_machine_upgrades
		WHERE status IN ('pending','running','reconciling') AND updated_at <= $1
		  AND attempts < $3
		ORDER BY updated_at, operation_id LIMIT $2`, time.Now().UTC().Add(-staleAfter), limit, types.MachineUpgradeMaxAutomaticAttempts)
	if err != nil {
		return nil, fmt.Errorf("list machine upgrades for reconcile: %w", err)
	}
	defer rows.Close()
	out := make([]*types.MachineUpgradeOperation, 0, limit)
	for rows.Next() {
		op, err := scanMachineUpgrade(rows)
		if err != nil {
			return nil, fmt.Errorf("scan machine upgrade for reconcile: %w", err)
		}
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate machine upgrades for reconcile: %w", err)
	}
	return out, nil
}

// machinePlanTermQuery reads a bought entitlement together with the lifecycle
// that sold the machine, and only answers while that binding still holds.
//
// 权益不是一条独立事实：它属于「当初卖出这台机器」的那条 lifecycle。所以读取
// 必须按 lifecycle_id join，并且同时要求 tenant 名对得上、owner 一致、
// lifecycle 还是 active、套餐还没到期；到期时间再取权益窗口和套餐到期时间的
// 较小值。套餐退款/缩期、删机、tenant 名被另一条 lifecycle 复用，都会让这条
// 权益自己失效，而不是被下一台机器或下一个主人继承。
const machinePlanTermQuery = `
	SELECT t.tenant_name, t.owner_uid, t.lifecycle_id, t.plan, t.base_plan, t.operation_id,
	       LEAST(t.expires_at, l.package_expires_at), t.updated_at
	FROM cloud_worker_machine_plan_terms t
	JOIN cloud_worker_lifecycles l ON l.id = t.lifecycle_id
	WHERE t.tenant_name = $1
	  AND l.tenant_name = t.tenant_name
	  AND l.owner_uid = t.owner_uid
	  AND l.state = 'active'
	  AND l.package_expires_at > CURRENT_TIMESTAMP`

// MachinePlanTerm returns the independent upgrade entitlement of a tenant, or
// nil when it has none that is still in force.
func (a *Adapter) MachinePlanTerm(tenantName string) (*types.MachinePlanTerm, error) {
	return a.machinePlanTerm(machinePlanTermQuery, strings.TrimSpace(tenantName))
}

// MachinePlanTermForOwner is the owner-bound read: the caller already knows who
// owns the machine, and a tenant name that now belongs to somebody else must
// answer with no term at all.
func (a *Adapter) MachinePlanTermForOwner(uid int64, tenantName string) (*types.MachinePlanTerm, error) {
	tenantName = strings.TrimSpace(tenantName)
	if uid <= 0 || tenantName == "" {
		return nil, nil
	}
	return a.machinePlanTerm(machinePlanTermQuery+`
	  AND t.owner_uid = $2`, tenantName, uid)
}

func (a *Adapter) machinePlanTerm(query string, args ...interface{}) (*types.MachinePlanTerm, error) {
	term := types.MachinePlanTerm{}
	if err := a.db.QueryRow(query, args...).Scan(&term.TenantName, &term.OwnerUID, &term.LifecycleID,
		&term.Plan, &term.BasePlan, &term.OperationID, &term.ExpiresAt, &term.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read machine plan term: %w", err)
	}
	return &term, nil
}

// MachineUpgradeCreditBalance reports what a quote may spend and whether the
// account is in arrears (an over-spent account may not buy an upgrade).
//
// 报价必须先对账月额度：跨期的旧余额要作废（作废后进 frozen_monthly_units，永远
// 不可花），新的一期要先发出来。拿上一期的余额报价，用户会「看着够、下单失败」。
// frozen_monthly_units 是已过期/已退回的冻结额度，只用于展示，绝不计入可花余额。
//
// gift 不在可花范围里：报价必须和真正能扣的钱一致，否则用户看着够、下单却失败。
func (a *Adapter) MachineUpgradeCreditBalance(uid int64) (int64, bool, error) {
	if uid <= 0 {
		return 0, false, nil
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("begin machine upgrade balance: %w", err)
	}
	defer tx.Rollback()
	if err := reconcileMachineUpgradeMonthly(context.Background(), tx, uid, time.Now().UTC()); err != nil {
		return 0, false, err
	}
	var account store.ModelCreditAccount
	account.UID = uid
	if err := tx.QueryRow(
		`SELECT monthly_units, gift_units, paid_units, debt_units FROM model_credit_accounts WHERE uid = $1`,
		uid).Scan(&account.MonthlyUnits, &account.GiftUnits, &account.PaidUnits, &account.DebtUnits); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read machine upgrade balance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit machine upgrade balance: %w", err)
	}
	return machineUpgradeSpendableUnits(account), account.DebtUnits > 0, nil
}

// reconcileMachineUpgradeMonthly rolls the subscription month over before an
// upgrade quotes, holds or returns monthly credit.
//
// 对账同时完成三件事，缺一不可：
//   - 跨期作废旧余额（进 frozen，不可花）；
//   - 老账本里「只有发放事件、没有批次行」的历史月额度补一条批次，否则将来的释放
//     找不到归属批次，退回的钱会凭空消失；
//   - 新的一期在报价前发出，避免报价与扣费口径不一致。
func reconcileMachineUpgradeMonthly(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) error {
	if _, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, uid, now.UTC()); err != nil {
		return err
	}
	return nil
}

// machineUpgradeSpendableUnits is everything a machine upgrade may spend:
// monthly and paid only, summed without overflowing int64.
//
// frozen_monthly_units 不计入：它是过期或已退回的月额度，和 upgrade 无关。
func machineUpgradeSpendableUnits(account store.ModelCreditAccount) int64 {
	sum, ok := store.ModelCreditAddUnits(account.MonthlyUnits, account.PaidUnits)
	if !ok {
		// 两个桶都已经贴着 int64 上限：当成满额可花，而不是溢出成负数。
		return int64(^uint64(0) >> 1)
	}
	return sum
}

// allocateMachineUpgradeUnits draws units in the only order an upgrade may use:
// monthly -> paid. Gift credits are invite money for model calls and are never
// spent here, so a gift-only balance must fail as insufficient instead of being
// quietly consumed.
func allocateMachineUpgradeUnits(account store.ModelCreditAccount, units int64) (store.ModelCreditSplit, bool) {
	if units <= 0 || account.Validate() != nil {
		return store.ModelCreditSplit{}, false
	}
	var split store.ModelCreditSplit
	remaining := units
	if take := min(remaining, account.MonthlyUnits); take > 0 {
		split.Monthly = take
		remaining -= take
	}
	if take := min(remaining, account.PaidUnits); take > 0 {
		split.Paid = take
		remaining -= take
	}
	if remaining > 0 {
		return store.ModelCreditSplit{}, false
	}
	return split, true
}

// machineUpgradeBucket is one bucket slice held by an operation.
type machineUpgradeBucket struct {
	bucket string
	units  int64
}

// reserveMachineUpgradeCredits takes the units out of the buyer's buckets in the
// one allowed order (monthly -> paid) and records the hold. Gift is never
// touched. An account in arrears is refused outright: buying resources while
// already over-spent is how an unfunded balance becomes free credit.
//
// 月额度按批次扣：预占把「这笔钱来自哪个月的哪一批」写进 hold_lots，释放时才能
// 只退回还活着的那一批。已过期（frozen）和被退款作废（revoked）的批次不可花，
// 所以这里先对账再扣，扣不到就是余额不足，绝不拿过期额度买单。
//
// 实现是共享的（prepaid_resource_credits.go）：Cats 算力任务卖的是同一种点数，
// 两条业务线必须用同一套扣费/退回规则，所以这里只填升配自己的参数。
func reserveMachineUpgradeCredits(ctx context.Context, tx *sql.Tx, op *types.MachineUpgradeOperation, now time.Time) error {
	return reservePrepaidResourceCredits(ctx, tx, prepaidResourceHold{
		Label:     "machine upgrade",
		UID:       op.UID,
		RequestID: op.OperationID,
		Units:     op.CostUnits,
	}, now)
}

// machineUpgradeHeldBuckets reads back what the operation is holding.
func machineUpgradeHeldBuckets(ctx context.Context, tx *sql.Tx, uid int64, operationID string) ([]machineUpgradeBucket, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT bucket, units FROM model_credit_ledger_events
		WHERE uid = $1 AND request_id = $2 AND event_type = 'reserve'
		ORDER BY bucket`, uid, operationID)
	if err != nil {
		return nil, fmt.Errorf("read machine upgrade hold: %w", err)
	}
	defer rows.Close()
	buckets := make([]machineUpgradeBucket, 0, 3)
	for rows.Next() {
		var part machineUpgradeBucket
		if err := rows.Scan(&part.bucket, &part.units); err != nil {
			return nil, fmt.Errorf("scan machine upgrade hold: %w", err)
		}
		buckets = append(buckets, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate machine upgrade hold: %w", err)
	}
	return buckets, nil
}

// settleMachineUpgradeCredits converts the hold into a charge. The balance was
// already debited at reserve time, so this only records the consumption — the
// same split the existing model-credit reservation does.
//
// 月额度批次在预占时就已经扣掉了，结算只是把这笔预占消费掉：清掉 hold_lots 里
// 的分配记录，让子账本不会留下永远还不掉的悬挂批次。
//
// 升配的成本就是报价，所以整笔预占都是消费（consumed = CostUnits），没有任何
// 额度需要退回。reconcileMonthly=false 沿用原行为：升配的预占窗口很短，跨期对账
// 留给预占和释放两条路径。
func settleMachineUpgradeCredits(ctx context.Context, tx *sql.Tx, op *types.MachineUpgradeOperation, now time.Time) error {
	return settlePrepaidResourceCredits(ctx, tx, prepaidResourceHold{
		Label:     "machine upgrade",
		UID:       op.UID,
		RequestID: op.OperationID,
		Units:     op.CostUnits,
	}, op.CostUnits, false, now)
}

// releaseMachineUpgradeCredits gives the held units back to their buckets.
//
// 退款只废除对应订单的批次；未作废的月额度按当前订阅状态退回可用或冻结余额。
// 旧升配没有批次分配记录，按实际预占流水重建归属，不使用开单时刻。
func releaseMachineUpgradeCredits(ctx context.Context, tx *sql.Tx, op *types.MachineUpgradeOperation, now time.Time) error {
	return releasePrepaidResourceCredits(ctx, tx, prepaidResourceHold{
		Label:     "machine upgrade",
		UID:       op.UID,
		RequestID: op.OperationID,
		Units:     op.CostUnits,
	}, now)
}

// upsertMachinePlanTerm records the bought tier against the lifecycle that sold
// the machine.
//
// 三条规则一起保证「钱买到的档位只属于这一台机器、这一段租期」：
//   - 结算时必须能对上同一条 lifecycle（id + tenant + owner），它还 active、
//     套餐还没到期；对不上就说明机器已经被删、被退款或被别人接手，这次结算
//     直接失败留给 reconcile，绝不把档位写给已经不存在的机器。
//   - 窗口取「这次买的窗口」和「套餐到期时间」的较小值：套餐缩期（退款）后
//     权益跟着缩短，不再挂在旧的长窗口上。
//   - 同一条 lifecycle 内不降档、不缩窗口；换了一条 lifecycle（tenant 名被
//     复用）时旧档位和旧窗口一律不继承，直接按这次真正买到的写。
func upsertMachinePlanTerm(tx *sql.Tx, op *types.MachineUpgradeOperation, plan, basePlan string, expiresAt, now time.Time) error {
	if types.MachinePlanRank(plan) == 0 {
		return fmt.Errorf("invalid machine plan %q", plan)
	}
	var packageExpiresAt time.Time
	if err := tx.QueryRow(`
		SELECT package_expires_at FROM cloud_worker_lifecycles
		WHERE id = $1 AND tenant_name = $2 AND owner_uid = $3 AND state = 'active'
		  AND package_expires_at > $4`,
		op.LifecycleID, op.TenantName, op.UID, now.UTC()).Scan(&packageExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("machine upgrade %s: lifecycle %d of tenant %s is not active for uid %d: %w",
				op.OperationID, op.LifecycleID, op.TenantName, op.UID, ErrMachineUpgradeConflict)
		}
		return fmt.Errorf("read machine upgrade lifecycle: %w", err)
	}
	if expiresAt.After(packageExpiresAt) {
		expiresAt = packageExpiresAt
	}
	if _, err := tx.Exec(`
		INSERT INTO cloud_worker_machine_plan_terms(
			tenant_name, owner_uid, lifecycle_id, plan, base_plan, operation_id, expires_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_name) DO UPDATE
		SET plan = CASE
		        -- 换了一条 lifecycle：旧档位属于上一台机器，绝不继承。
		        WHEN cloud_worker_machine_plan_terms.lifecycle_id <> EXCLUDED.lifecycle_id
		        THEN EXCLUDED.plan
		        -- 同一条 lifecycle、窗口还在、而且已经买到更高档：后续任何写入
		        -- （比如套餐按自己的小档位重新同步）都不能把它降下来。
		        WHEN cloud_worker_machine_plan_terms.expires_at > $8
		         AND (CASE cloud_worker_machine_plan_terms.plan
		              WHEN 'heavy' THEN 4 WHEN 'pro' THEN 3 WHEN 'standard' THEN 2 WHEN 'light' THEN 1 ELSE 0 END)
		           > (CASE EXCLUDED.plan
		              WHEN 'heavy' THEN 4 WHEN 'pro' THEN 3 WHEN 'standard' THEN 2 WHEN 'light' THEN 1 ELSE 0 END)
		        THEN cloud_worker_machine_plan_terms.plan ELSE EXCLUDED.plan END,
		    base_plan = EXCLUDED.base_plan,
		    operation_id = EXCLUDED.operation_id,
		    lifecycle_id = EXCLUDED.lifecycle_id,
		    owner_uid = EXCLUDED.owner_uid,
		    expires_at = CASE
		        WHEN cloud_worker_machine_plan_terms.lifecycle_id <> EXCLUDED.lifecycle_id
		        THEN EXCLUDED.expires_at
		        ELSE LEAST(GREATEST(cloud_worker_machine_plan_terms.expires_at, EXCLUDED.expires_at), $9)
		        END,
		    updated_at = EXCLUDED.updated_at`,
		op.TenantName, op.UID, op.LifecycleID, plan, basePlan, op.OperationID, expiresAt.UTC(),
		now.UTC(), packageExpiresAt.UTC()); err != nil {
		return fmt.Errorf("save machine plan term: %w", err)
	}
	return nil
}

// MachineUpgradeOpenForTenant reports whether this machine has an unfinished
// upgrade, so a second quote is refused instead of double-selling.
func (a *Adapter) MachineUpgradeOpenForTenant(tenantName string) (bool, error) {
	var pending bool
	if err := a.db.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM cloud_worker_machine_upgrades
		               WHERE tenant_name = $1 AND status IN ('pending','running','reconciling'))`,
		strings.TrimSpace(tenantName)).Scan(&pending); err != nil {
		return false, fmt.Errorf("read open machine upgrade: %w", err)
	}
	return pending, nil
}

// CloudWorkerLifecycleRuntimePlan reports the shared-fleet tier a tenant was
// provisioned with. It is the fallback "current plan" for a worker that never
// bought an upgrade: the credit that created the machine recorded the slot size
// it was created at.
func (a *Adapter) CloudWorkerLifecycleRuntimePlan(tenantName string) (string, error) {
	tenantName = strings.TrimSpace(tenantName)
	if tenantName == "" {
		return "", nil
	}
	var plan string
	err := a.db.QueryRow(`
		SELECT COALESCE(NULLIF(c.runtime_plan, ''), '')
		FROM cloud_worker_credits c
		JOIN cloud_worker_lifecycles l ON l.worker_uid = c.worker_uid
		WHERE l.tenant_name = $1 AND c.state = 'consumed'
		ORDER BY c.consumed_at DESC NULLS LAST, c.id DESC
		LIMIT 1`, tenantName).Scan(&plan)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read worker runtime plan: %w", err)
	}
	return strings.TrimSpace(plan), nil
}

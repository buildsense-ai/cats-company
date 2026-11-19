package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.ModelCreditTaskStore = (*Adapter)(nil)

// 原始（root）任务门禁与单任务保留的 PostgreSQL 实现
// （docs/credit-plan-rules-20261109.md P0-2/P0-3/P0-4）。
//
// 并发模型和账本一致：任何会改变余额或任务状态的写操作，都先 BEGIN、再对
// model_credit_accounts 的那一行 FOR UPDATE，最后才读写 model_credit_tasks。同一
// 用户的 start/resume/finish 与 reserve/settle 因此严格串行，started_seq 单调递增，
// 「最近开始的 running 任务」不会因为两个事务同时判断而出现两个保留名额。

// modelCreditTaskEnabled 判断这个用户是否进过 credit 体系（credit 身份）：
//
//   - 买过/被发过带 model credit 的套餐（包括已经过期、退订的订阅）；
//   - 或者账本里已经有事件（发放、预留、结算、欠款……）；
//   - 或者已经有任何 credit root（包括暂停且尚未预留的任务）；
//   - 或者账本账户上有真实余额/欠款。
//
// 只有身份不靠「有没有 model_credit_accounts 行」单独判断：余额页 / 资格查询这类
// 只读入口曾经会 upsert 出空账户，拿行本身当身份会把从没进过 credit 的老用户误迁进
// credit 模型。现在的读路径不再建账户（见 reconcileModelCreditMonthlyPeriod），
// 这里再加上「有余额才算」的兜底，历史上留下的空账户也不会误判。
func (a *Adapter) modelCreditTaskEnabled(ctx context.Context, uid int64) (bool, error) {
	var enabled bool
	if err := a.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM commercial_entitlements e
		               JOIN commercial_plans p ON p.id = e.plan_id`+modelCreditEntitlementOrderJoinSQL+`
		               WHERE e.uid = $1 AND `+modelCreditEntitlementUnitsSQL+` > 0)
		    OR EXISTS (SELECT 1 FROM model_credit_ledger_events ev WHERE ev.uid = $1)
		    OR EXISTS (SELECT 1 FROM model_credit_tasks t WHERE t.uid = $1)
		    OR EXISTS (SELECT 1 FROM model_credit_accounts ac WHERE ac.uid = $1
		               AND (ac.monthly_units > 0 OR ac.gift_units > 0
		                    OR ac.paid_units > 0 OR ac.debt_units > 0))`, uid).Scan(&enabled); err != nil {
		return false, fmt.Errorf("load model credit task enrollment: %w", err)
	}
	return enabled, nil
}

// modelCreditTaskPermitted 判断此刻是否还有有效的模型权限：仍然有效的、带
// model credit 的订阅，或者账户里还有花钱买来的 paid 点数（独立充值）。余额本身
// 不是权限（P0-4.4/P0-4.5）—— 那条规则针对的是有期限的月额度，而 paid 桶是用户
// 用真金白银买的永久余额，没有套餐也能花。gift（邀请赠送）不算权限，legacy 用户
// 没有账户行、paid = 0 时保持原来的失败关闭行为。
func modelCreditTaskPermitted(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (bool, error) {
	var permitted bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM commercial_entitlements e
			JOIN commercial_plans p ON p.id = e.plan_id`+modelCreditEntitlementOrderJoinSQL+`
			WHERE e.uid = $1
			  AND e.state = 'active'
			  AND e.starts_at <= $2
			  AND (e.expires_at IS NULL OR e.expires_at > $2)
			  AND `+modelCreditEntitlementUnitsSQL+` > 0)
		OR EXISTS (
			SELECT 1 FROM model_credit_accounts ac
			WHERE ac.uid = $1 AND ac.paid_units > 0)`, uid, now.UTC()).Scan(&permitted); err != nil {
		return false, fmt.Errorf("load model credit task permission: %w", err)
	}
	return permitted, nil
}

// modelCreditTaskScanner 让 *sql.Row 和 *sql.Rows 共用一份扫描逻辑。
type modelCreditTaskScanner interface {
	Scan(...any) error
}

func scanModelCreditTask(row modelCreditTaskScanner) (*store.ModelCreditTask, error) {
	task := store.ModelCreditTask{}
	if err := row.Scan(&task.TaskID, &task.UID, &task.State, &task.Reason, &task.PlanTier,
		&task.StartedAt, &task.StartedSeq, &task.UpdatedAt); err != nil {
		return nil, err
	}
	return &task, nil
}

// modelCreditTaskPlanTier 取「此刻这个用户该按几档计价」：在生效中的、带 model credit
// 的订阅里，按和 eligibility 相同的主套餐排序（付费来源优先 → 档位高 → 剩余时间长）
// 取第一条。返回值 0 表示取不到可信档位；调用方必须失败关闭，不能猜。
//
// 这是 start 时唯一允许写入快照的来源：套餐档位永远由服务端从 commercial_plans 读，
// 客户端请求体里没有、也不接受 tier 字段。
func modelCreditTaskPlanTier(ctx context.Context, tx *sql.Tx, uid int64, now time.Time) (int, error) {
	var tier int
	err := tx.QueryRowContext(ctx, `
		SELECT p.plan_tier
		FROM commercial_entitlements e
		JOIN commercial_plans p ON p.id = e.plan_id`+modelCreditEntitlementOrderJoinSQL+`
		WHERE e.uid = $1
		  AND e.state = 'active'
		  AND e.starts_at <= $2
		  AND (e.expires_at IS NULL OR e.expires_at > $2)
		  AND `+modelCreditEntitlementUnitsSQL+` > 0
		ORDER BY
		  CASE WHEN e.source IN ('order','admin','internal') THEN 0 ELSE 1 END ASC,
		  p.plan_tier DESC,
		  e.expires_at DESC NULLS LAST,
		  e.starts_at DESC,
		  e.id DESC
		LIMIT 1`, uid, now.UTC()).Scan(&tier)
	if errors.Is(err, sql.ErrNoRows) {
		// 没有生效订阅但有独立充值：paid 点数按 Relay 梯度默认的第 1 档计价。
		// 只有真的花钱买过（paid > 0）才给档位，legacy 用户仍然拿不到可信快照
		// （失败关闭），所以充值不会把老账户变成 credit 账户。
		var paid int64
		if paidErr := tx.QueryRowContext(ctx,
			`SELECT COALESCE(paid_units, 0) FROM model_credit_accounts WHERE uid = $1`, uid).Scan(&paid); paidErr != nil {
			if errors.Is(paidErr, sql.ErrNoRows) {
				return store.ModelCreditTaskPlanTierNone, nil
			}
			return store.ModelCreditTaskPlanTierNone, fmt.Errorf("load model credit task plan tier: %w", paidErr)
		}
		if paid > 0 {
			return store.ModelCreditTaskPlanTierMin, nil
		}
		return store.ModelCreditTaskPlanTierNone, nil
	}
	if err != nil {
		return store.ModelCreditTaskPlanTierNone, fmt.Errorf("load model credit task plan tier: %w", err)
	}
	if !store.ModelCreditTaskPlanTierTrusted(tier) {
		return store.ModelCreditTaskPlanTierNone, nil
	}
	return tier, nil
}

// modelCreditTaskByID 读一个 root 任务；没有 start 过返回 nil。
func modelCreditTaskByID(ctx context.Context, tx *sql.Tx, uid int64, taskID string) (*store.ModelCreditTask, error) {
	task, err := scanModelCreditTask(tx.QueryRowContext(ctx, `
		SELECT root_task_id, uid, state, reason, plan_tier, started_at, started_seq, updated_at
		FROM model_credit_tasks WHERE root_task_id = $1 AND uid = $2 FOR UPDATE`, taskID, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read model credit task: %w", err)
	}
	if err := task.Validate(); err != nil {
		return nil, fmt.Errorf("model credit task %q is inconsistent: %w", taskID, err)
	}
	return task, nil
}

// nextModelCreditTaskSeq 给出这个用户的下一个启动序号。调用方必须已经持有该用户的
// 账户行锁，所以「读最大值再加一」不会被并发打断。
func nextModelCreditTaskSeq(ctx context.Context, tx *sql.Tx, uid int64) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(started_seq), 0) + 1 FROM model_credit_tasks WHERE uid = $1`,
		uid).Scan(&seq); err != nil {
		return 0, fmt.Errorf("load model credit task sequence: %w", err)
	}
	return seq, nil
}

func insertModelCreditTask(ctx context.Context, tx *sql.Tx, task *store.ModelCreditTask) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO model_credit_tasks (root_task_id, uid, state, reason, plan_tier, started_at, started_seq, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (root_task_id) DO NOTHING`,
		task.TaskID, task.UID, task.State, task.Reason, task.PlanTier, task.StartedAt.UTC(), task.StartedSeq, task.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("insert model credit task: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read model credit task insert result: %w", err)
	}
	if affected == 0 {
		// 这个 root id 已经属于另一个账户：同一个 root 不能被两个人各自计费。
		return store.ErrModelCreditConflict
	}
	return nil
}

func updateModelCreditTaskState(ctx context.Context, tx *sql.Tx, uid int64, taskID, state, reason string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE model_credit_tasks SET state = $3, reason = $4, updated_at = $5
		WHERE uid = $1 AND root_task_id = $2`, uid, taskID, state, reason, now.UTC()); err != nil {
		return fmt.Errorf("update model credit task state: %w", err)
	}
	return nil
}

// lockModelCreditTaskPlanTier 给「从没合法启动过」的任务补一次档位快照。SQL 里加了
// plan_tier = 0 的守卫：即使并发/重复调用，已经有快照的行也不会被改写 —— 旧任务的
// 费率一旦锁定就永远不变。
func lockModelCreditTaskPlanTier(ctx context.Context, tx *sql.Tx, uid int64, taskID string, planTier int, now time.Time) error {
	if !store.ModelCreditTaskPlanTierTrusted(planTier) {
		return store.ErrModelCreditInvalid
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE model_credit_tasks SET plan_tier = $3, updated_at = $4
		WHERE uid = $1 AND root_task_id = $2 AND plan_tier = 0`,
		uid, taskID, planTier, now.UTC()); err != nil {
		return fmt.Errorf("lock model credit task plan tier: %w", err)
	}
	return nil
}

// A zero hold is only issued by postpaid admission, including before this
// change. Use the existing (uid, root_task_id) index. Settlement and no-usage
// reclamation keep the original amount and row, so any receipt state counts.
// The caller still has to verify that the root is running with a trusted tier.
const modelCreditTaskPostpaidReceiptSQL = `SELECT 1 FROM model_credit_reservations r
	WHERE r.uid = t.uid AND r.root_task_id = t.root_task_id AND r.reserved_units = 0`

func modelCreditTaskHasPostpaidReceipt(ctx context.Context, tx *sql.Tx, uid int64, taskID string) (bool, error) {
	var admitted bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (`+modelCreditTaskPostpaidReceiptSQL+`)
		FROM model_credit_tasks t WHERE t.uid = $1 AND t.root_task_id = $2`, uid, taskID).Scan(&admitted); err != nil {
		return false, fmt.Errorf("check postpaid model credit admission: %w", err)
	}
	return admitted, nil
}

// syncModelCreditTaskRetention keeps the most recently started legacy running
// root eligible for overdraft holds when credit runs out. Postpaid turns that
// have a receipt continue independently. Paused roots still require resume.
// The caller must hold the account lock and have updated the balance.
func syncModelCreditTaskRetention(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount, now time.Time) (bool, error) {
	if account == nil || account.UID <= 0 {
		return false, nil
	}
	if account.SpendableUnits() > 0 {
		running, err := retainedTaskStillRunning(ctx, tx, account.UID, account.RetainedTaskID)
		if err != nil {
			return false, err
		}
		if running {
			return false, nil
		}
		account.RetainedTaskID = ""
		if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
			return false, err
		}
		return true, nil
	}
	var latest string
	err := tx.QueryRowContext(ctx, `
		SELECT t.root_task_id FROM model_credit_tasks t
		WHERE t.uid = $1 AND t.state = 'running' AND NOT EXISTS (`+modelCreditTaskPostpaidReceiptSQL+`)
		ORDER BY t.started_seq DESC LIMIT 1`, account.UID).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		if account.RetainedTaskID == "" {
			return false, nil
		}
		account.RetainedTaskID = ""
		if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("load retained model credit task: %w", err)
	}
	changed := false
	if account.RetainedTaskID != latest {
		account.RetainedTaskID = latest
		if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
			return false, err
		}
		changed = true
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE model_credit_tasks t SET state = 'paused', reason = $3, updated_at = $4
		WHERE t.uid = $1 AND t.state = 'running' AND t.root_task_id <> $2
		  AND NOT EXISTS (`+modelCreditTaskPostpaidReceiptSQL+`)`,
		account.UID, latest, store.ModelCreditTaskReasonQuotaExhausted, now.UTC()); err != nil {
		return false, fmt.Errorf("pause non-retained model credit tasks: %w", err)
	}
	return changed, nil
}

// modelCreditTaskStartBlocker 给出「现在不能开始/恢复」的第一个原因。
func modelCreditTaskStartBlocker(permitted bool, account *store.ModelCreditAccount) (string, bool) {
	switch {
	case !permitted:
		return store.ModelCreditTaskReasonEntitlementExpired, false
	case account == nil:
		return store.ModelCreditTaskReasonQuotaExhausted, false
	case account.DebtUnits > 0:
		return store.ModelCreditTaskReasonDebtOutstanding, false
	case account.SpendableUnits() <= 0:
		return store.ModelCreditTaskReasonQuotaExhausted, false
	default:
		return store.ModelCreditTaskReasonOK, true
	}
}

// retainedTaskStillRunning 报告透支名额是否被另一个仍在跑的任务占着。
func retainedTaskStillRunning(ctx context.Context, tx *sql.Tx, uid int64, retainedTaskID string) (bool, error) {
	if retainedTaskID == "" {
		return false, nil
	}
	var running bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM model_credit_tasks t
		               WHERE t.uid = $1 AND t.root_task_id = $2 AND t.state = 'running'
		                 AND NOT EXISTS (`+modelCreditTaskPostpaidReceiptSQL+`))`,
		uid, retainedTaskID).Scan(&running); err != nil {
		return false, fmt.Errorf("check retained model credit task: %w", err)
	}
	return running, nil
}

// modelCreditTaskDecision 组装判定。planTier 是这个 root 的档位快照（0 = 没有可信
// 快照），所有 action（含 dispatch）都回同一份，Relay 直接拿它计价。
func modelCreditTaskDecision(account *store.ModelCreditAccount, taskID string, planTier int,
	enabled, allowed bool, state, reason string) *store.ModelCreditTaskDecision {
	decision := &store.ModelCreditTaskDecision{
		Enabled: enabled, Allowed: allowed, TaskID: taskID, State: state, Reason: reason,
		PlanTier: planTier,
	}
	if account != nil {
		decision.RetainedTaskID = account.RetainedTaskID
		decision.SpendableUnits = account.SpendableUnits()
		decision.DebtUnits = account.DebtUnits
	}
	return decision
}

// modelCreditTaskStoredPlanTier 只回可确信的存档档位：不在 1..3 就回 0，让调用方失败
// 关闭。绝不用「现在的套餐」替旧任务补一个档位。
func modelCreditTaskStoredPlanTier(task *store.ModelCreditTask) int {
	if task == nil || !store.ModelCreditTaskPlanTierTrusted(task.PlanTier) {
		return store.ModelCreditTaskPlanTierNone
	}
	return task.PlanTier
}

// ApplyModelCreditTask 在单用户事务里推进 root 任务状态。
//
// start 是唯一会创建任务行的动作：一旦某个 root 有了行，重试只会原样返回存档
// （started_at / started_seq 不重写，paused 也不会被提升成 running）。resume 是用户
// 手动恢复的唯一入口，必须满足「无欠款 + 有额度 + 有有效权限 + 透支名额没被别的
// 运行中任务占着」。finish 幂等，并且不会顺手唤醒任何 paused 任务。
//
// dispatch 是 Relay opt-in 派发（X-CatsCo-Billing-Dispatch）用的计费入口，只改计费
// 状态、绝不重放应用请求：root 缺失时等同 start，running/completed 等同 status，
// paused 等同 resume。所以 started_at / started_seq / 档位快照 / 结算回执都由既有
// 函数维护，dispatch 自己不写任何新语义。
func (a *Adapter) ApplyModelCreditTask(ctx context.Context, in store.ModelCreditTaskInput) (*store.ModelCreditTaskDecision, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	// 先判 enabled：老用户不该因为一次 start 查询就被建出账本账户行。
	enabled, err := a.modelCreditTaskEnabled(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		// 从没进过 credit 体系：照旧放行，什么都不落库。
		return modelCreditTaskDecision(nil, in.TaskID, store.ModelCreditTaskPlanTierNone, false, true,
			store.ModelCreditTaskRunning, store.ModelCreditTaskReasonLegacy), nil
	}

	now := time.Now().UTC()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model credit task %s: %w", in.Action, err)
	}
	defer func() { _ = tx.Rollback() }()
	// 既有 credit 用户先拿到账户行锁，串行判断 root 是否已经由 credit 门禁处理过。
	// 纯 legacy 老用户在上面已经返回，不会被建出账本账户。
	if _, err := lockModelCreditAccount(ctx, tx, in.UID); err != nil {
		return nil, err
	}
	task, err := modelCreditTaskByID(ctx, tx, in.UID, in.TaskID)
	if err != nil {
		return nil, err
	}
	if in.PreferLegacy && task == nil {
		// Inference still checks legacy admission. Roll back this lock-only path.
		return modelCreditTaskDecision(nil, in.TaskID, store.ModelCreditTaskPlanTierNone, false, true,
			store.ModelCreditTaskRunning, store.ModelCreditTaskReasonLegacy), nil
	}
	// Credit decisions use the current month's balance and permission.
	account, _, err := reconcileModelCreditMonthlyPeriod(ctx, tx, in.UID, now)
	if err != nil {
		return nil, err
	}
	permitted, err := modelCreditTaskPermitted(ctx, tx, in.UID, now)
	if err != nil {
		return nil, err
	}

	var decision *store.ModelCreditTaskDecision
	switch in.Action {
	case store.ModelCreditTaskActionStart:
		decision, err = applyModelCreditTaskStart(ctx, tx, account, permitted, task, in, now)
	case store.ModelCreditTaskActionStatus:
		if task == nil {
			return nil, store.ErrModelCreditTaskNotFound
		}
		decision, err = modelCreditTaskStatusDecision(ctx, tx, account, permitted, task)
	case store.ModelCreditTaskActionResume:
		if task == nil {
			return nil, store.ErrModelCreditTaskNotFound
		}
		decision, err = applyModelCreditTaskResume(ctx, tx, account, permitted, task, now)
	case store.ModelCreditTaskActionFinish:
		if task == nil {
			return nil, store.ErrModelCreditTaskNotFound
		}
		decision, err = applyModelCreditTaskFinish(ctx, tx, account, task, now)
	case store.ModelCreditTaskActionDispatch:
		decision, err = applyModelCreditTaskDispatch(ctx, tx, account, permitted, task, in, now)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model credit task %s: %w", in.Action, err)
	}
	return decision, nil
}

func applyModelCreditTaskStart(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount,
	permitted bool, task *store.ModelCreditTask, in store.ModelCreditTaskInput, now time.Time) (*store.ModelCreditTaskDecision, error) {
	if task != nil {
		// 重试：只回放存档。这里绝对不能重写 started_at/started_seq，也不能把
		// paused 当成一次新的启动。
		return modelCreditTaskStatusDecision(ctx, tx, account, permitted, task)
	}
	// 快照档位：只有此刻「有生效 credit 权限」时才去读服务端套餐；权限都没了就不存在
	// 可信档位，记 0（之后 resume 拿到权限时再锁一次，见 applyModelCreditTaskResume）。
	// 客户端永远不能自报档位：请求体里根本没有这个字段。
	reason, allowed := modelCreditTaskStartBlocker(permitted, account)
	planTier := store.ModelCreditTaskPlanTierNone
	if permitted {
		resolved, err := modelCreditTaskPlanTier(ctx, tx, in.UID, now)
		if err != nil {
			return nil, err
		}
		planTier = resolved
	}
	if allowed && !store.ModelCreditTaskPlanTierTrusted(planTier) {
		// 有权限、有余额，但服务端取不到 1..3 档：算不出费率就不能开始，也不猜。
		reason, allowed = store.ModelCreditTaskReasonPlanTierUnavailable, false
	}
	state := store.ModelCreditTaskPaused
	if allowed {
		state = store.ModelCreditTaskRunning
	}
	seq, err := nextModelCreditTaskSeq(ctx, tx, in.UID)
	if err != nil {
		return nil, err
	}
	task = &store.ModelCreditTask{
		UID: in.UID, TaskID: in.TaskID, State: state, Reason: reason, PlanTier: planTier,
		StartedAt: now, StartedSeq: seq, UpdatedAt: now,
	}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if err := insertModelCreditTask(ctx, tx, task); err != nil {
		return nil, err
	}
	if allowed {
		if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
			return nil, err
		}
	}
	return modelCreditTaskDecision(account, task.TaskID, planTier, true, allowed, state, reason), nil
}

// modelCreditTaskStatusDecision 是纯读的判定：running 就是 allowed，paused 则重新算
// 一次「此刻缺什么」，让 Relay 能显示真正的原因而不是陈旧的暂停原因。
//
// 档位一律回存档快照：running 的任务即使套餐已过期也 allowed=true（按到期前费率跑到
// 结束）。唯一的例外是存档不可信（0，只可能来自本列上线前的旧行）：running 也只能
// 失败关闭 —— 算不出费率就不许继续，绝不拿现在的套餐替它补。
func modelCreditTaskStatusDecision(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount,
	permitted bool, task *store.ModelCreditTask) (*store.ModelCreditTaskDecision, error) {
	storedTier := modelCreditTaskStoredPlanTier(task)
	switch task.State {
	case store.ModelCreditTaskCompleted:
		return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false,
			store.ModelCreditTaskCompleted, store.ModelCreditTaskReasonTaskCompleted), nil
	case store.ModelCreditTaskRunning:
		if storedTier == store.ModelCreditTaskPlanTierNone {
			return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false,
				store.ModelCreditTaskRunning, store.ModelCreditTaskReasonPlanTierUnavailable), nil
		}
		return modelCreditTaskDecision(account, task.TaskID, storedTier, true, true,
			store.ModelCreditTaskRunning, store.ModelCreditTaskReasonOK), nil
	}
	reason, allowed := modelCreditTaskStartBlocker(permitted, account)
	if allowed {
		// 条件已经恢复，但恢复只能由用户手动触发（P0-3.7）。没有档位快照的暂停任务也
		// 走这里：resume 时才会从当时有效的服务端套餐锁一次档位。
		reason = store.ModelCreditTaskReasonResumeRequired
	} else if blocked, err := retainedTaskStillRunning(ctx, tx, task.UID, account.RetainedTaskID); err != nil {
		return nil, err
	} else if blocked && account.RetainedTaskID != task.TaskID {
		reason = store.ModelCreditTaskReasonRetainedOther
	}
	return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false, store.ModelCreditTaskPaused, reason), nil
}

// applyModelCreditTaskResume 是用户手动恢复唯一入口。门槛不变：当前有效 credit 权限 +
// 无欠款 + 可花余额 + 透支名额没被别的运行中任务占着。
//
// 档位：已经有存档就原样保留（旧任务旧费率，绝不刷成当前套餐），从不重写。只有「从没
// 合法启动过」的任务（start 当时没权限，存档为 0）才在这里锁一次：必须此刻仍有有效权限
// 且服务端能取到 1..3 档，否则失败关闭（plan_tier_unavailable），不给过期任务猜档位。
func applyModelCreditTaskResume(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount,
	permitted bool, task *store.ModelCreditTask, now time.Time) (*store.ModelCreditTaskDecision, error) {
	storedTier := modelCreditTaskStoredPlanTier(task)
	switch task.State {
	case store.ModelCreditTaskCompleted:
		return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false,
			store.ModelCreditTaskCompleted, store.ModelCreditTaskReasonTaskCompleted), nil
	case store.ModelCreditTaskRunning:
		if storedTier == store.ModelCreditTaskPlanTierNone {
			return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false,
				store.ModelCreditTaskRunning, store.ModelCreditTaskReasonPlanTierUnavailable), nil
		}
		return modelCreditTaskDecision(account, task.TaskID, storedTier, true, true,
			store.ModelCreditTaskRunning, store.ModelCreditTaskReasonOK), nil
	}
	reason, allowed := modelCreditTaskStartBlocker(permitted, account)
	if allowed {
		blocked, err := retainedTaskStillRunning(ctx, tx, task.UID, account.RetainedTaskID)
		if err != nil {
			return nil, err
		}
		if blocked && account.RetainedTaskID != task.TaskID {
			reason, allowed = store.ModelCreditTaskReasonRetainedOther, false
		}
	}
	if !allowed {
		// 把当前真正的阻塞原因写回存档：P1-3 要求能查到任务为什么被暂停。
		if task.Reason != reason {
			if err := updateModelCreditTaskState(ctx, tx, task.UID, task.TaskID, store.ModelCreditTaskPaused, reason, now); err != nil {
				return nil, err
			}
			task.Reason = reason
		}
		return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false, store.ModelCreditTaskPaused, reason), nil
	}
	if storedTier == store.ModelCreditTaskPlanTierNone {
		// 这个 root 从没合法启动过：现在条件齐备，按此刻有效的服务端套餐锁一次档位。
		resolved, err := modelCreditTaskPlanTier(ctx, tx, task.UID, now)
		if err != nil {
			return nil, err
		}
		if !store.ModelCreditTaskPlanTierTrusted(resolved) {
			return modelCreditTaskDecision(account, task.TaskID, store.ModelCreditTaskPlanTierNone, true, false,
				store.ModelCreditTaskPaused, store.ModelCreditTaskReasonPlanTierUnavailable), nil
		}
		if err := lockModelCreditTaskPlanTier(ctx, tx, task.UID, task.TaskID, resolved, now); err != nil {
			return nil, err
		}
		storedTier = resolved
	}
	if err := updateModelCreditTaskState(ctx, tx, task.UID, task.TaskID, store.ModelCreditTaskRunning, store.ModelCreditTaskReasonOK, now); err != nil {
		return nil, err
	}
	// 恢复之后余额可能刚好在这笔里回到 0（例如额度只够一个任务），保留名额重新算一次。
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return nil, err
	}
	return modelCreditTaskDecision(account, task.TaskID, storedTier, true, true,
		store.ModelCreditTaskRunning, store.ModelCreditTaskReasonOK), nil
}

// applyModelCreditTaskFinish 幂等收尾。finish 不受档位快照影响：失败路径也要能结束
// 任务、释放透支名额，所以即使存档不可信（0）也照常完成，只是不报档位。
func applyModelCreditTaskFinish(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount,
	task *store.ModelCreditTask, now time.Time) (*store.ModelCreditTaskDecision, error) {
	storedTier := modelCreditTaskStoredPlanTier(task)
	if task.State == store.ModelCreditTaskCompleted {
		return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false,
			store.ModelCreditTaskCompleted, store.ModelCreditTaskReasonTaskCompleted), nil
	}
	if err := updateModelCreditTaskState(ctx, tx, task.UID, task.TaskID,
		store.ModelCreditTaskCompleted, store.ModelCreditTaskReasonTaskCompleted, now); err != nil {
		return nil, err
	}
	// 保留任务结束了，透支名额跟着作废；paused 的任务仍然 paused，不会自动接上
	// （P0-3.8：保留任务结束后不能自动换另一项暂停任务继续透支）。
	if account.RetainedTaskID == task.TaskID {
		account.RetainedTaskID = ""
		if err := saveModelCreditAccount(ctx, tx, account, now); err != nil {
			return nil, err
		}
	}
	if _, err := syncModelCreditTaskRetention(ctx, tx, account, now); err != nil {
		return nil, err
	}
	return modelCreditTaskDecision(account, task.TaskID, storedTier, true, false,
		store.ModelCreditTaskCompleted, store.ModelCreditTaskReasonTaskCompleted), nil
}

// applyModelCreditTaskDispatch 是 Relay opt-in 派发（X-CatsCo-Billing-Dispatch）的入口。
// 它只是把请求路由到既有的三个函数，自己不改任何计费规则：
//
//   - root 缺失：等同 start（同一套权限/额度/欠款/档位快照判定，同样按当前政策决定
//     running 还是 paused；纯 legacy 账户在上面早就返回了，不会被拉进 credit）；
//   - running / completed：等同 status，纯读，不重写 started_at / 档位 / 回执；
//   - paused：等同 resume，是唯一会写状态的分支（只能由这次显式派发拉起）。
//
// 注意 paused 走 resume 而不是 status：额度为 0 或有欠款时，判定必须仍然是
// quota_exhausted / debt_outstanding —— 即使透支名额还被另一个已经在跑的 root 占着
// （那笔已派发的调用还在结算）。status 会把原因改写成 retained_task_other，dispatch
// 不能那样报，否则 Relay 会把「余额耗尽」误当成「等别的任务结束」。
func applyModelCreditTaskDispatch(ctx context.Context, tx *sql.Tx, account *store.ModelCreditAccount,
	permitted bool, task *store.ModelCreditTask, in store.ModelCreditTaskInput, now time.Time) (*store.ModelCreditTaskDecision, error) {
	if task == nil {
		return applyModelCreditTaskStart(ctx, tx, account, permitted, nil, in, now)
	}
	if task.State == store.ModelCreditTaskPaused {
		return applyModelCreditTaskResume(ctx, tx, account, permitted, task, now)
	}
	return modelCreditTaskStatusDecision(ctx, tx, account, permitted, task)
}

// GetModelCreditTask 读一个 root 任务的状态（不含判定，供排障/测试使用）。
func (a *Adapter) GetModelCreditTask(ctx context.Context, uid int64, taskID string) (*store.ModelCreditTask, error) {
	if uid <= 0 {
		return nil, store.ErrModelCreditInvalid
	}
	if err := store.ValidateModelCreditTaskID(taskID); err != nil {
		return nil, err
	}
	task, err := scanModelCreditTask(a.db.QueryRowContext(ctx, `
		SELECT root_task_id, uid, state, reason, plan_tier, started_at, started_seq, updated_at
		FROM model_credit_tasks WHERE root_task_id = $1 AND uid = $2`, taskID, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrModelCreditTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read model credit task: %w", err)
	}
	return task, nil
}

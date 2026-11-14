package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 原始任务门禁的 PostgreSQL 集成测试（docs/credit-plan-rules-20261109.md P0-2/P0-3/P0-4）：
//   - 老用户 enabled=false + allowed=true，不会因为查询被拉进 credit 账本；
//   - 套餐过期是 enabled=true + allowed=false（权限失效），不能退回 legacy；
//   - 额度耗尽只保留最近开始的 running 原始任务，其余暂停；
//   - 已派发调用的超支落成真实欠款，重复结算不重复记债；
//   - 补款先抵欠款但不自动恢复任务；resume 必须由用户触发且条件齐备；
//   - finish 幂等、释放透支名额，且不会自动把暂停任务变成 running。

func modelCreditTaskStartDecision(t *testing.T, db *Adapter, uid int64, taskID string) *store.ModelCreditTaskDecision {
	t.Helper()
	decision, err := db.ApplyModelCreditTask(context.Background(), store.ModelCreditTaskInput{
		UID: uid, TaskID: taskID, Action: store.ModelCreditTaskActionStart,
	})
	if err != nil {
		t.Fatalf("start %s: %v", taskID, err)
	}
	return decision
}

func modelCreditTaskAction(t *testing.T, db *Adapter, uid int64, taskID, action string) (*store.ModelCreditTaskDecision, error) {
	t.Helper()
	return db.ApplyModelCreditTask(context.Background(), store.ModelCreditTaskInput{
		UID: uid, TaskID: taskID, Action: action,
	})
}

func modelCreditTaskState(t *testing.T, db *Adapter, uid int64, taskID string) (string, string) {
	t.Helper()
	task, err := db.GetModelCreditTask(context.Background(), uid, taskID)
	if err != nil {
		t.Fatalf("read task %s: %v", taskID, err)
	}
	return task.State, task.Reason
}

func TestPostgresModelCreditTaskLegacyUserStaysOutsideTheLedger(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	uid := modelCreditTestUser(t, db, "credit-task")
	decision := modelCreditTaskStartDecision(t, db, uid, "legacy-root")
	if decision.Enabled || !decision.Allowed || decision.State != store.ModelCreditTaskRunning ||
		decision.Reason != store.ModelCreditTaskReasonLegacy {
		t.Fatalf("legacy decision %+v", decision)
	}
	var accounts int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_accounts WHERE uid = $1`, uid).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 {
		t.Fatalf("a legacy start must not create a credit account row (rows=%d)", accounts)
	}
	var tasks int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_tasks WHERE uid = $1`, uid).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatalf("a legacy start must not persist a task (rows=%d)", tasks)
	}
}

// 曾经买过 credit 套餐的用户，套餐过期后仍然是 enabled=true、allowed=false：原因必须
// 是「权限失效」，绝不能悄悄退回 legacy 继续白用。
func TestPostgresModelCreditTaskExpiredPlanIsBlockedNotLegacy(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             "credit-task-expired-" + time.Now().UTC().Format("150405.000000000"),
		Name:             "Credit task expired",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         1,
		ModelCreditUnits: 4_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	// order 来源的权益按订单快照读额度：身份判定要有真实订单行，否则 fail closed。
	insertCreditMonthlyTestOrderSnapshot(t, db, uid, planID, "ORDER-EXPIRED")
	expired := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', 'ORDER-EXPIRED', 'active', $3, $4)`,
		uid, planID, expired.AddDate(0, -1, 0), expired); err != nil {
		t.Fatalf("insert expired entitlement: %v", err)
	}
	decision := modelCreditTaskStartDecision(t, db, uid, "expired-root")
	if !decision.Enabled {
		t.Fatalf("expired plan fell back to legacy: %+v", decision)
	}
	if decision.Allowed || decision.State != store.ModelCreditTaskPaused ||
		decision.Reason != store.ModelCreditTaskReasonEntitlementExpired {
		t.Fatalf("expired plan decision %+v", decision)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "expired-reserve", RootTaskID: "expired-root", Units: 1,
	}); !errors.Is(err, store.ErrModelCreditTaskNotRunning) {
		t.Fatalf("reserve on a paused task accepted: %v", err)
	}
}

// 账本预留必须挂在已经 start 且 allowed 的 root 上；缺 root 或 root 不存在都是明确错误。
func TestPostgresModelCreditTaskReserveRequiresStartedRoot(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "root-required-paid")
	modelCreditTestPermission(t, db, uid)
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "no-root", Units: 1,
	}); !errors.Is(err, store.ErrModelCreditTaskRequired) {
		t.Fatalf("reserve without root accepted: %v", err)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "unknown-root", RootTaskID: "never-started", Units: 1,
	}); !errors.Is(err, store.ErrModelCreditTaskNotFound) {
		t.Fatalf("reserve under an unknown root accepted: %v", err)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "bad-root", RootTaskID: "not a token", Units: 1,
	}); !errors.Is(err, store.ErrModelCreditInvalid) {
		t.Fatalf("reserve with an invalid root token accepted: %v", err)
	}
}

// start 重试只回放存档：started_at / started_seq 不重写，暂停的任务不会被提升。
func TestPostgresModelCreditTaskStartRetryIsStable(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 100, "retry-paid")
	modelCreditTestPermission(t, db, uid)
	first := modelCreditTaskStartDecision(t, db, uid, "retry-root")
	if !first.Allowed || first.State != store.ModelCreditTaskRunning {
		t.Fatalf("first start %+v", first)
	}
	stored, err := db.GetModelCreditTask(context.Background(), uid, "retry-root")
	if err != nil {
		t.Fatal(err)
	}
	// 把任务打成暂停（模拟额度耗尽），再用 start 重试。
	if _, err := db.db.Exec(`UPDATE model_credit_tasks SET state = 'paused', reason = 'quota_exhausted' WHERE uid = $1 AND root_task_id = $2`,
		uid, "retry-root"); err != nil {
		t.Fatal(err)
	}
	retried := modelCreditTaskStartDecision(t, db, uid, "retry-root")
	if retried.Allowed || retried.State != store.ModelCreditTaskPaused {
		t.Fatalf("start retry promoted a paused task: %+v", retried)
	}
	after, err := db.GetModelCreditTask(context.Background(), uid, "retry-root")
	if err != nil {
		t.Fatal(err)
	}
	if !after.StartedAt.Equal(stored.StartedAt) || after.StartedSeq != stored.StartedSeq {
		t.Fatalf("start retry rewrote start order: before=%+v after=%+v", stored, after)
	}
}

// 额度耗尽：只保留最近开始的 running 原始任务，其余暂停；保留任务的超支在结算时落成
// 真实欠款；暂停任务不能继续预留；finish 之后也不会有任务被自动唤醒。
func TestPostgresModelCreditTaskExhaustionKeepsOnlyLatestRoot(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 100, "keep-paid")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-earlier")
	modelCreditTestTask(t, db, uid, "root-later")

	if _, account, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "earlier-1", RootTaskID: "root-earlier", Units: 60,
	}); err != nil || account.PaidUnits != 40 {
		t.Fatalf("earlier reserve %v %+v", err, account)
	}
	// 这一笔把余额用到 0：root-later 开始得更晚，所以它成为唯一保留任务，root-earlier
	// 立刻暂停。
	if _, account, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "later-1", RootTaskID: "root-later", Units: 40,
	}); err != nil || account.PaidUnits != 0 {
		t.Fatalf("later reserve %v %+v", err, account)
	}
	if account, err := db.GetModelCreditAccount(ctx, uid); err != nil || account.RetainedTaskID != "root-later" {
		t.Fatalf("retained task %v %+v", err, account)
	}
	if state, reason := modelCreditTaskState(t, db, uid, "root-earlier"); state != store.ModelCreditTaskPaused || reason != store.ModelCreditTaskReasonQuotaExhausted {
		t.Fatalf("earlier task state=%q reason=%q, want paused/quota_exhausted", state, reason)
	}
	if state, _ := modelCreditTaskState(t, db, uid, "root-later"); state != store.ModelCreditTaskRunning {
		t.Fatalf("retained task state=%q, want running", state)
	}
	// 暂停任务不得继续：预算是 0 也好、有预算也好，它都不能再预留。
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "earlier-2", RootTaskID: "root-earlier", Units: 1,
	}); !errors.Is(err, store.ErrModelCreditTaskNotRunning) {
		t.Fatalf("paused task reserved again: %v", err)
	}
	// 保留任务可以透支：余额 0 也能预占，没有余额兜底的部分记成 unfunded。
	hold, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "later-overdraft", RootTaskID: "root-later", Units: 50,
	})
	if err != nil {
		t.Fatalf("retained task overdraft reserve: %v", err)
	}
	if hold.Split.Total() != 0 || hold.UnfundedUnits != 50 {
		t.Fatalf("overdraft hold %+v", hold)
	}
	settled, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "later-overdraft", ActualUnits: 50})
	if err != nil {
		t.Fatalf("settle overdraft: %v", err)
	}
	if settled.State != store.ModelCreditReservationSettled || after.DebtUnits != 50 || after.PaidUnits != 0 {
		t.Fatalf("overdraft debt %+v %+v", settled, after)
	}
	// 重复结算同一 request id 不能再记债。
	if _, replayed, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "later-overdraft", ActualUnits: 50}); err != nil || replayed.DebtUnits != 50 {
		t.Fatalf("replayed overdraft settle %v %+v", err, replayed)
	}
	// 有欠款且额度为 0：新任务不能开始。
	blocked := modelCreditTaskStartDecision(t, db, uid, "root-new")
	if blocked.Allowed || blocked.State != store.ModelCreditTaskPaused {
		t.Fatalf("new task started while in arrears: %+v", blocked)
	}
	if blocked.Reason != store.ModelCreditTaskReasonDebtOutstanding {
		t.Fatalf("blocked reason %q, want debt_outstanding", blocked.Reason)
	}
	// 保留任务结束：名额释放，但暂停的任务仍然暂停。
	if _, err := modelCreditTaskAction(t, db, uid, "root-later", store.ModelCreditTaskActionFinish); err != nil {
		t.Fatalf("finish retained task: %v", err)
	}
	if account, err := db.GetModelCreditAccount(ctx, uid); err != nil || account.RetainedTaskID != "" {
		t.Fatalf("retained slot after finish %v %+v", err, account)
	}
	if state, _ := modelCreditTaskState(t, db, uid, "root-earlier"); state != store.ModelCreditTaskPaused {
		t.Fatalf("finish revived a paused task: state=%q", state)
	}
}

// 补款先抵欠款，但不会自动恢复任务；resume 需要用户触发且条件齐备。
func TestPostgresModelCreditTaskTopUpRepaysDebtWithoutResuming(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "topup-paid")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-a")
	modelCreditTestTask(t, db, uid, "root-b")
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "a-1", RootTaskID: "root-a", Units: 10,
	}); err != nil {
		t.Fatal(err)
	}
	// 余额归零：root-b 是最近开始的，成为保留任务；root-a 暂停。
	if state, _ := modelCreditTaskState(t, db, uid, "root-a"); state != store.ModelCreditTaskPaused {
		t.Fatalf("root-a state=%q, want paused", state)
	}
	hold, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "b-1", RootTaskID: "root-b", Units: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hold.UnfundedUnits != 30 {
		t.Fatalf("overdraft hold %+v", hold)
	}
	if _, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "b-1", ActualUnits: 20}); err != nil || after.DebtUnits != 20 {
		t.Fatalf("overdraft settle %v %+v", err, after)
	}
	// 补款 50：先抵 20 欠款，剩下 30 进 paid；任务状态一律不动。
	account := modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 50, "topup-50")
	if account.DebtUnits != 0 || account.PaidUnits != 30 {
		t.Fatalf("top-up did not repay arrears first: %+v", account)
	}
	if state, _ := modelCreditTaskState(t, db, uid, "root-a"); state != store.ModelCreditTaskPaused {
		t.Fatalf("top-up auto-resumed root-a: state=%q", state)
	}
	// 状态查询只报告「需要手动恢复」，不会自己把任务拉起来。
	status, err := modelCreditTaskAction(t, db, uid, "root-a", store.ModelCreditTaskActionStatus)
	if err != nil {
		t.Fatal(err)
	}
	if status.Allowed || status.State != store.ModelCreditTaskPaused || status.Reason != store.ModelCreditTaskReasonResumeRequired {
		t.Fatalf("paused status %+v", status)
	}
	// 保留名额被还在跑的 root-b 占着：这时 resume root-a 必须被拒绝。
	resumed, err := modelCreditTaskAction(t, db, uid, "root-a", store.ModelCreditTaskActionResume)
	if err != nil {
		t.Fatalf("resume while another task is retained: %v", err)
	}
	if resumed.Allowed || resumed.Reason != store.ModelCreditTaskReasonRetainedOther {
		t.Fatalf("resume stole the overdraft slot: %+v", resumed)
	}
	// 保留任务结束后，用户可以手动恢复 root-a。
	if _, err := modelCreditTaskAction(t, db, uid, "root-b", store.ModelCreditTaskActionFinish); err != nil {
		t.Fatal(err)
	}
	resumed, err = modelCreditTaskAction(t, db, uid, "root-a", store.ModelCreditTaskActionResume)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.Allowed || resumed.State != store.ModelCreditTaskRunning {
		t.Fatalf("resume decision %+v", resumed)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "a-2", RootTaskID: "root-a", Units: 10,
	}); err != nil {
		t.Fatalf("reserve after resume: %v", err)
	}
}

// 已结束的任务不能被 resume 复活；finish 幂等。
func TestPostgresModelCreditTaskFinishIsIdempotent(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "finish-paid")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-finish")
	finished, err := modelCreditTaskAction(t, db, uid, "root-finish", store.ModelCreditTaskActionFinish)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != store.ModelCreditTaskCompleted || finished.Allowed || finished.Reason != store.ModelCreditTaskReasonTaskCompleted {
		t.Fatalf("finish decision %+v", finished)
	}
	replayed, err := modelCreditTaskAction(t, db, uid, "root-finish", store.ModelCreditTaskActionFinish)
	if err != nil || replayed.State != store.ModelCreditTaskCompleted {
		t.Fatalf("finish replay %v %+v", err, replayed)
	}
	resumed, err := modelCreditTaskAction(t, db, uid, "root-finish", store.ModelCreditTaskActionResume)
	if err != nil || resumed.Allowed || resumed.Reason != store.ModelCreditTaskReasonTaskCompleted {
		t.Fatalf("completed task resumed %v %+v", err, resumed)
	}
	if _, _, err := db.ReserveModelCredits(context.Background(), store.ModelCreditReserveInput{
		UID: uid, RequestID: "after-finish", RootTaskID: "root-finish", Units: 1,
	}); !errors.Is(err, store.ErrModelCreditTaskNotRunning) {
		t.Fatalf("reserve on a finished task accepted: %v", err)
	}
	if _, err := modelCreditTaskAction(t, db, uid, "root-missing", store.ModelCreditTaskActionFinish); !errors.Is(err, store.ErrModelCreditTaskNotFound) {
		t.Fatalf("finish of an unknown task accepted: %v", err)
	}
}

// 并发 start 只落一行一个序号：同一个 root 的并发重试不会拿到两个启动顺序。
func TestPostgresModelCreditTaskConcurrentStartKeepsOneRow(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "concurrent-paid")
	modelCreditTestPermission(t, db, uid)
	const workers = 8
	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := db.ApplyModelCreditTask(context.Background(), store.ModelCreditTaskInput{
				UID: uid, TaskID: "root-same", Action: store.ModelCreditTaskActionStart,
			})
			done <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent start: %v", err)
		}
	}
	var tasks, distinctSeq int
	if err := db.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT started_seq) FROM model_credit_tasks WHERE uid = $1`, uid).Scan(&tasks, &distinctSeq); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || distinctSeq != 1 {
		t.Fatalf("concurrent start rows=%d distinct sequences=%d, want 1/1", tasks, distinctSeq)
	}
}

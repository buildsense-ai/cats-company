package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/openchat/openchat/server/store"
)

// Relay opt-in 派发（X-CatsCo-Billing-Dispatch）在 Company 侧只新增了一个 INTERNAL
// action：dispatch。它只推进计费状态、绝不重放应用请求 —— root 缺失等同 start，
// running/completed 等同 status，paused 等同 resume，全部复用既有的 Apply... 函数。
// 下面这些用例锁的就是这件事：政策不变、存档不变、老 action 不变、不多写一行账。

// sameModelCreditTaskRow 比较存档行（started_at / started_seq / 档位 / 状态 / 原因），
// dispatch 不许改写其中任何一个。
func sameModelCreditTaskRow(a, b *store.ModelCreditTask) bool {
	return a != nil && b != nil && a.UID == b.UID && a.TaskID == b.TaskID && a.State == b.State &&
		a.Reason == b.Reason && a.PlanTier == b.PlanTier && a.StartedSeq == b.StartedSeq &&
		a.StartedAt.Equal(b.StartedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

// modelCreditDispatchWrites 数这个 uid 的账本行数：dispatch 不该新增预占、结算回执或
// 账本事件（它不是一次应用请求的重放）。
func modelCreditDispatchWrites(t *testing.T, db *Adapter, uid int64) (holds, events int) {
	t.Helper()
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_reservations WHERE uid = $1`, uid).Scan(&holds); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid = $1`, uid).Scan(&events); err != nil {
		t.Fatal(err)
	}
	return holds, events
}

// root 缺失时 dispatch 必须和 start 完全一致：同样的权限、额度、欠款和档位快照政策。
// 首笔 postpaid 预留再次检查准入条件，后续请求复用该轮的准入结果。
func TestPostgresModelCreditTaskDispatchAdmitsNewRootLikeStart(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-dispatch")
	modelCreditTestPermission(t, db, uid) // 邀请档位 1，不发月额度：只有 paid 桶算可花余额
	empty, err := modelCreditTaskAction(t, db, uid, "dispatch-empty", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("dispatch without balance: %v", err)
	}
	if !empty.Enabled || empty.Allowed || empty.State != store.ModelCreditTaskPaused ||
		empty.Reason != store.ModelCreditTaskReasonQuotaExhausted {
		t.Fatalf("dispatch without balance admitted credit: %+v", empty)
	}
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 5, "dispatch-paid")
	decision, err := modelCreditTaskAction(t, db, uid, "dispatch-new", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("dispatch new root: %v", err)
	}
	if !decision.Enabled || !decision.Allowed || decision.State != store.ModelCreditTaskRunning ||
		decision.Reason != store.ModelCreditTaskReasonOK || decision.PlanTier != 1 {
		t.Fatalf("dispatch new root decision %+v", decision)
	}
	stored, err := db.GetModelCreditTask(ctx, uid, "dispatch-new")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != store.ModelCreditTaskRunning || stored.PlanTier != 1 {
		t.Fatalf("stored root after dispatch %+v", stored)
	}
	// 首笔 postpaid 预留只留回执，不扣桶。
	hold, account, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "dispatch-hold-1", RootTaskID: "dispatch-new", Postpaid: true,
	})
	if err != nil || hold.ReservedUnits != 0 || account.SpendableUnits() != 5 {
		t.Fatalf("postpaid admission after dispatch: %v hold=%+v account=%+v", err, hold, account)
	}
	// dispatch 重放只回存档：started_at / started_seq / 档位快照都不许被重写。
	replay, err := modelCreditTaskAction(t, db, uid, "dispatch-new", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("dispatch replay: %v", err)
	}
	replayed, err := db.GetModelCreditTask(ctx, uid, "dispatch-new")
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Allowed || replay.State != store.ModelCreditTaskRunning || replay.PlanTier != 1 ||
		!sameModelCreditTaskRow(stored, replayed) {
		t.Fatalf("dispatch replay rewrote the root: decision=%+v before=%+v after=%+v", replay, stored, replayed)
	}
	// 已准入的 root 可以继续接收请求并保留原档位，直到 finish。
	if _, account, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{
		UID: uid, RequestID: "dispatch-hold-1", ActualUnits: 9,
	}); err != nil || account.DebtUnits != 4 || account.SpendableUnits() != 0 {
		t.Fatalf("postpaid settle %v %+v", err, account)
	}
	if receipt, account, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "dispatch-hold-2", RootTaskID: "dispatch-new", Postpaid: true,
	}); err != nil || receipt.ReservedUnits != 0 || account.DebtUnits != 4 {
		t.Fatalf("same turn must continue while in arrears: %v %+v %+v", err, receipt, account)
	}
	finished, err := modelCreditTaskAction(t, db, uid, "dispatch-new", store.ModelCreditTaskActionFinish)
	if err != nil || finished.State != store.ModelCreditTaskCompleted || finished.Allowed || finished.PlanTier != 1 {
		t.Fatalf("finish after dispatch %v %+v", err, finished)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "dispatch-hold-3", RootTaskID: "dispatch-new", Postpaid: true,
	}); !errors.Is(err, store.ErrModelCreditTaskNotRunning) {
		t.Fatalf("finished root admitted a new request: %v", err)
	}
}

// dispatch 打在 paused root 上等同 resume：余额恢复、透支名额空出来才会拉起；而「余额 0
// 或有欠款」的判定必须原样报 quota_exhausted / debt_outstanding，不能被改写成
// retained_task_other —— 那笔已经派发的调用还在结算，原因不能骗 Relay。旧 status 行为
// 保持不变（它仍然报 retained_task_other）。
func TestPostgresModelCreditTaskDispatchPausedRootFollowsCreditPolicy(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-dispatch")
	modelCreditTestPermission(t, db, uid)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "dispatch-paused-paid")
	modelCreditTestTask(t, db, uid, "dispatch-early")
	modelCreditTestTask(t, db, uid, "dispatch-late")
	// 把余额用到 0：最近开始的 dispatch-late 成为唯一保留任务，dispatch-early 暂停。
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "dispatch-drain", RootTaskID: "dispatch-late", Units: 10,
	}); err != nil {
		t.Fatalf("drain balance: %v", err)
	}
	if state, reason := modelCreditTaskState(t, db, uid, "dispatch-early"); state != store.ModelCreditTaskPaused ||
		reason != store.ModelCreditTaskReasonQuotaExhausted {
		t.Fatalf("early root state=%q reason=%q", state, reason)
	}
	paused, err := db.GetModelCreditTask(ctx, uid, "dispatch-early")
	if err != nil {
		t.Fatal(err)
	}
	holdsBefore, eventsBefore := modelCreditDispatchWrites(t, db, uid)
	blocked, err := modelCreditTaskAction(t, db, uid, "dispatch-early", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("dispatch on a paused root: %v", err)
	}
	if blocked.Allowed || blocked.State != store.ModelCreditTaskPaused ||
		blocked.Reason != store.ModelCreditTaskReasonQuotaExhausted || blocked.PlanTier != 1 {
		t.Fatalf("dispatch on a paused root at zero balance: %+v", blocked)
	}
	status, err := modelCreditTaskAction(t, db, uid, "dispatch-early", store.ModelCreditTaskActionStatus)
	if err != nil {
		t.Fatalf("status on a paused root: %v", err)
	}
	if status.Allowed || status.Reason != store.ModelCreditTaskReasonRetainedOther {
		t.Fatalf("old status reason changed: %+v", status)
	}
	if holds, events := modelCreditDispatchWrites(t, db, uid); holds != holdsBefore || events != eventsBefore {
		t.Fatalf("dispatch on a paused root wrote holds=%d events=%d", holds, events)
	}
	// 补款之后余额为正，但透支名额还被在跑的 dispatch-late 占着：仍然不能拉起。
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 5, "dispatch-paused-topup")
	kept, err := modelCreditTaskAction(t, db, uid, "dispatch-early", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("dispatch while another root is retained: %v", err)
	}
	if kept.Allowed || kept.State != store.ModelCreditTaskPaused || kept.Reason != store.ModelCreditTaskReasonRetainedOther {
		t.Fatalf("dispatch stole the overdraft slot: %+v", kept)
	}
	// 保留任务结束后，dispatch 才能把暂停的 root 拉回 running，且存档不变。
	if _, err := modelCreditTaskAction(t, db, uid, "dispatch-late", store.ModelCreditTaskActionFinish); err != nil {
		t.Fatalf("finish the retained root: %v", err)
	}
	resumed, err := modelCreditTaskAction(t, db, uid, "dispatch-early", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("dispatch resume: %v", err)
	}
	if !resumed.Allowed || resumed.State != store.ModelCreditTaskRunning ||
		resumed.Reason != store.ModelCreditTaskReasonOK || resumed.PlanTier != 1 {
		t.Fatalf("dispatch did not resume the paused root: %+v", resumed)
	}
	resumedTask, err := db.GetModelCreditTask(ctx, uid, "dispatch-early")
	if err != nil {
		t.Fatal(err)
	}
	if !resumedTask.StartedAt.Equal(paused.StartedAt) || resumedTask.StartedSeq != paused.StartedSeq ||
		resumedTask.PlanTier != paused.PlanTier || resumedTask.State != store.ModelCreditTaskRunning {
		t.Fatalf("dispatch resume rewrote the root: before=%+v after=%+v", paused, resumedTask)
	}
	if replay, err := modelCreditTaskAction(t, db, uid, "dispatch-early", store.ModelCreditTaskActionDispatch); err != nil ||
		!replay.Allowed || replay.State != store.ModelCreditTaskRunning || replay.PlanTier != 1 {
		t.Fatalf("dispatch replay on a running root %v %+v", err, replay)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "dispatch-resumed", RootTaskID: "dispatch-early", Postpaid: true,
	}); err != nil {
		t.Fatalf("postpaid admission after dispatch resume: %v", err)
	}
}

// 欠款状态下的 dispatch：原因必须是 debt_outstanding（不是 retained_task_other），而且
// 只改任务原因，不碰账本、不改 started_at / 档位 / 根 id。
func TestPostgresModelCreditTaskDispatchKeepsDebtOutstanding(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-dispatch")
	modelCreditTestPermission(t, db, uid)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 1, "dispatch-debt-paid")
	modelCreditTestTask(t, db, uid, "debt-early")
	modelCreditTestTask(t, db, uid, "debt-late")
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "debt-drain", RootTaskID: "debt-late", Units: 1,
	}); err != nil {
		t.Fatalf("drain balance: %v", err)
	}
	// 保留任务可以透支：这一笔落地成真实欠款。
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "debt-over", RootTaskID: "debt-late", Units: 5,
	}); err != nil {
		t.Fatalf("overdraft reserve: %v", err)
	}
	if _, account, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{
		UID: uid, RequestID: "debt-over", ActualUnits: 5,
	}); err != nil || account.DebtUnits != 5 {
		t.Fatalf("overdraft settle %v %+v", err, account)
	}
	before, err := db.GetModelCreditTask(ctx, uid, "debt-early")
	if err != nil {
		t.Fatal(err)
	}
	holdsBefore, eventsBefore := modelCreditDispatchWrites(t, db, uid)
	dispatch, err := modelCreditTaskAction(t, db, uid, "debt-early", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("dispatch in arrears: %v", err)
	}
	if dispatch.Allowed || dispatch.State != store.ModelCreditTaskPaused ||
		dispatch.Reason != store.ModelCreditTaskReasonDebtOutstanding || dispatch.PlanTier != 1 {
		t.Fatalf("dispatch in arrears = %+v, want paused/debt_outstanding", dispatch)
	}
	after, err := db.GetModelCreditTask(ctx, uid, "debt-early")
	if err != nil {
		t.Fatal(err)
	}
	if after.State != store.ModelCreditTaskPaused || after.Reason != store.ModelCreditTaskReasonDebtOutstanding ||
		!after.StartedAt.Equal(before.StartedAt) || after.StartedSeq != before.StartedSeq ||
		after.PlanTier != before.PlanTier || after.TaskID != before.TaskID {
		t.Fatalf("dispatch in arrears rewrote the root: before=%+v after=%+v", before, after)
	}
	if holds, events := modelCreditDispatchWrites(t, db, uid); holds != holdsBefore || events != eventsBefore {
		t.Fatalf("dispatch in arrears wrote holds=%d events=%d", holds, events)
	}
	// 旧 status 仍然报 retained_task_other：老 API 不变。
	status, err := modelCreditTaskAction(t, db, uid, "debt-early", store.ModelCreditTaskActionStatus)
	if err != nil || status.Allowed || status.Reason != store.ModelCreditTaskReasonRetainedOther {
		t.Fatalf("old status in arrears %v %+v", err, status)
	}
}

// dispatch 打在运行中或已完成的 root 上等同 status：纯读，一行都不写。
func TestPostgresModelCreditTaskDispatchIsReadOnlyForRunningAndCompletedRoots(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-dispatch")
	modelCreditTestPermission(t, db, uid)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 5, "dispatch-read-paid")
	running := modelCreditTestTask(t, db, uid, "dispatch-running")
	before, err := db.GetModelCreditTask(ctx, uid, "dispatch-running")
	if err != nil {
		t.Fatal(err)
	}
	holdsBefore, eventsBefore := modelCreditDispatchWrites(t, db, uid)
	decision, err := modelCreditTaskAction(t, db, uid, "dispatch-running", store.ModelCreditTaskActionDispatch)
	if err != nil || !decision.Allowed || decision.State != store.ModelCreditTaskRunning ||
		decision.PlanTier != running.PlanTier || decision.Reason != store.ModelCreditTaskReasonOK {
		t.Fatalf("dispatch on a running root %v %+v", err, decision)
	}
	after, err := db.GetModelCreditTask(ctx, uid, "dispatch-running")
	if err != nil {
		t.Fatal(err)
	}
	if !sameModelCreditTaskRow(before, after) {
		t.Fatalf("dispatch on a running root wrote the row: before=%+v after=%+v", before, after)
	}
	if holds, events := modelCreditDispatchWrites(t, db, uid); holds != holdsBefore || events != eventsBefore {
		t.Fatalf("dispatch on a running root wrote holds=%d events=%d", holds, events)
	}
	if _, err := modelCreditTaskAction(t, db, uid, "dispatch-running", store.ModelCreditTaskActionFinish); err != nil {
		t.Fatalf("finish: %v", err)
	}
	completed, err := db.GetModelCreditTask(ctx, uid, "dispatch-running")
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := modelCreditTaskAction(t, db, uid, "dispatch-running", store.ModelCreditTaskActionDispatch)
	if err != nil || dispatch.Allowed || dispatch.State != store.ModelCreditTaskCompleted ||
		dispatch.Reason != store.ModelCreditTaskReasonTaskCompleted || dispatch.PlanTier != running.PlanTier {
		t.Fatalf("dispatch on a completed root %v %+v", err, dispatch)
	}
	replayed, err := db.GetModelCreditTask(ctx, uid, "dispatch-running")
	if err != nil {
		t.Fatal(err)
	}
	if !sameModelCreditTaskRow(completed, replayed) {
		t.Fatalf("dispatch on a completed root wrote the row: before=%+v after=%+v", completed, replayed)
	}
	if holds, events := modelCreditDispatchWrites(t, db, uid); holds != holdsBefore || events != eventsBefore {
		t.Fatalf("dispatch on a completed root wrote holds=%d events=%d", holds, events)
	}
}

// 纯 legacy 账户（从没进过 credit 体系）：dispatch 和 start 一样只回 enabled=false +
// allowed=true + reason=legacy，账本、任务、回执一行都不写。
func TestPostgresModelCreditTaskDispatchLeavesLegacyAccountUntouched(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	uid := modelCreditTestUser(t, db, "credit-dispatch")
	decision, err := modelCreditTaskAction(t, db, uid, "legacy-dispatch", store.ModelCreditTaskActionDispatch)
	if err != nil {
		t.Fatalf("legacy dispatch: %v", err)
	}
	if decision.Enabled || !decision.Allowed || decision.State != store.ModelCreditTaskRunning ||
		decision.Reason != store.ModelCreditTaskReasonLegacy || decision.PlanTier != store.ModelCreditTaskPlanTierNone {
		t.Fatalf("legacy dispatch decision %+v", decision)
	}
	start, err := modelCreditTaskAction(t, db, uid, "legacy-start", store.ModelCreditTaskActionStart)
	if err != nil || start.Enabled || !start.Allowed || start.Reason != store.ModelCreditTaskReasonLegacy {
		t.Fatalf("legacy start contract changed: %v %+v", err, start)
	}
	var accounts, tasks, holds, events int
	for _, probe := range []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM model_credit_accounts WHERE uid = $1`, &accounts},
		{`SELECT COUNT(*) FROM model_credit_tasks WHERE uid = $1`, &tasks},
		{`SELECT COUNT(*) FROM model_credit_reservations WHERE uid = $1`, &holds},
		{`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid = $1`, &events},
	} {
		if err := db.db.QueryRow(probe.query, uid).Scan(probe.dest); err != nil {
			t.Fatal(err)
		}
	}
	if accounts != 0 || tasks != 0 || holds != 0 || events != 0 {
		t.Fatalf("legacy dispatch/start wrote accounts=%d tasks=%d holds=%d events=%d", accounts, tasks, holds, events)
	}
}

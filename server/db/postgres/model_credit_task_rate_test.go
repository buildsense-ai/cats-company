package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// root 任务的套餐档位快照（费率锁定）集成测试
// （docs/credit-plan-rules-20261109.md：任务运行中套餐到期，按到期前费率跑到结束）：
//   - 合法 start 从服务端有效套餐取 plan_tier(1..3) 存进任务行，start 重放 / status /
//     finish 都回同一份，跨 adapter 重启仍在；
//   - 运行中的任务套餐到期后仍然 allowed、仍然可以 reserve（按原档位计价）；
//   - resume 保留原档位（旧任务旧费率），不会刷成当前套餐；
//   - 服务端取不到 1..3 档时失败关闭（plan_tier_unavailable），绝不给过期任务猜档位；
//   - 档位只能由服务端写，请求体里没有、也拒绝这个字段（见 server 包的 HTTP 契约测试）。

// modelCreditTestPermissionTier 给 uid 一个指定档位的生效 credit 权限（source=invite，
// 不发月额度，不会改动用例里精确的余额）。
func modelCreditTestPermissionTier(t *testing.T, db *Adapter, uid int64, tier int) int64 {
	t.Helper()
	slug := fmt.Sprintf("credit-task-tier-%d", time.Now().UnixNano())
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             slug,
		Name:             "Credit task tier test",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         tier,
		ModelCreditUnits: 4_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create credit task tier plan: %v", err)
	}
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'invite', $3, 'active', $4, NULL)`,
		uid, planID, "INVITE-"+slug, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("insert credit task tier permission: %v", err)
	}
	return planID
}

func TestPostgresModelCreditTaskPlanTierSnapshotIsStable(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-tier")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 100, "tier-paid")
	modelCreditTestPermissionTier(t, db, uid, 2)

	started := modelCreditTaskStartDecision(t, db, uid, "tier-root")
	if !started.Allowed || started.State != store.ModelCreditTaskRunning {
		t.Fatalf("start decision %+v", started)
	}
	if started.PlanTier != 2 {
		t.Fatalf("start plan tier = %d, want the server-side plan tier 2", started.PlanTier)
	}

	stored, err := db.GetModelCreditTask(ctx, uid, "tier-root")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PlanTier != 2 {
		t.Fatalf("stored plan tier = %d, want 2", stored.PlanTier)
	}

	// start 重放、status、finish 都回同一份快照。
	replay := modelCreditTaskStartDecision(t, db, uid, "tier-root")
	if replay.PlanTier != 2 || !replay.Allowed {
		t.Fatalf("start replay %+v", replay)
	}
	status, err := modelCreditTaskAction(t, db, uid, "tier-root", store.ModelCreditTaskActionStatus)
	if err != nil || status.PlanTier != 2 {
		t.Fatalf("status %v %+v", err, status)
	}

	// 跨 adapter 重启：档位在库里，不靠内存。
	var schema string
	if err := db.db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	reopened := &Adapter{}
	if err := reopened.Open(dsnWithSearchPath(t, os.Getenv("CATS_PG_TEST_DSN"), schema)); err != nil {
		t.Fatalf("reopen adapter: %v", err)
	}
	defer reopened.Close()
	afterRestart, err := reopened.GetModelCreditTask(ctx, uid, "tier-root")
	if err != nil {
		t.Fatalf("read task after adapter restart: %v", err)
	}
	if afterRestart.PlanTier != 2 {
		t.Fatalf("plan tier after adapter restart = %d, want 2", afterRestart.PlanTier)
	}
	restartedStatus, err := reopened.ApplyModelCreditTask(ctx, store.ModelCreditTaskInput{
		UID: uid, TaskID: "tier-root", Action: store.ModelCreditTaskActionStatus,
	})
	if err != nil || restartedStatus.PlanTier != 2 {
		t.Fatalf("status after adapter restart %v %+v", err, restartedStatus)
	}

	finished, err := modelCreditTaskAction(t, db, uid, "tier-root", store.ModelCreditTaskActionFinish)
	if err != nil || finished.PlanTier != 2 || finished.State != store.ModelCreditTaskCompleted {
		t.Fatalf("finish %v %+v", err, finished)
	}
}

// 运行中套餐到期：任务继续跑（status 仍 allowed），reserve 仍然成功 —— 这就是「按到期前
// 费率跑到结束」的执行点；同一时刻新 root 开始、以及 paused 任务的 resume 都必须被拒。
func TestPostgresModelCreditTaskRunningSurvivesPlanExpiry(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-expiry")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 100, "expiry-paid")
	planID := modelCreditTestPermissionTier(t, db, uid, 1)

	started := modelCreditTaskStartDecision(t, db, uid, "expiry-root")
	if !started.Allowed || started.PlanTier != 1 {
		t.Fatalf("start %+v", started)
	}
	// 套餐过期：该 root 的档位快照不变，任务继续。
	if _, err := db.db.Exec(`
		UPDATE commercial_entitlements SET expires_at = $3
		WHERE uid = $1 AND plan_id = $2`, uid, planID, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	status, err := modelCreditTaskAction(t, db, uid, "expiry-root", store.ModelCreditTaskActionStatus)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Allowed || status.State != store.ModelCreditTaskRunning ||
		status.Reason != store.ModelCreditTaskReasonOK || status.PlanTier != 1 {
		t.Fatalf("running task after plan expiry = %+v, want allowed with the locked tier", status)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "expiry-reserve", RootTaskID: "expiry-root", Units: 10,
	}); err != nil {
		t.Fatalf("a running task must keep reserving after the plan expired: %v", err)
	}

	// 同一个用户此刻新建 root：权限已失效，必须被拒（不能靠新 root 绕过）。
	fresh := modelCreditTaskStartDecision(t, db, uid, "expiry-new-root")
	if fresh.Allowed || fresh.Reason != store.ModelCreditTaskReasonEntitlementExpired {
		t.Fatalf("new root after expiry = %+v, want denied entitlement_expired", fresh)
	}
	if fresh.PlanTier != 0 {
		t.Fatalf("denied start reported plan tier %d, want 0 (no trusted snapshot)", fresh.PlanTier)
	}
}

// resume 保留原任务档位（旧任务旧费率），即使此刻生效的是更高档套餐。
func TestPostgresModelCreditTaskResumeKeepsOriginalTier(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-resume-tier")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "resume-tier-paid")
	modelCreditTestPermissionTier(t, db, uid, 2)

	modelCreditTestTask(t, db, uid, "resume-root-a")
	modelCreditTestTask(t, db, uid, "resume-root-b")
	// 用光余额：后开始的 root-b 成为保留任务，root-a 被暂停。
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "resume-tier-a", RootTaskID: "resume-root-a", Units: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if state, _ := modelCreditTaskState(t, db, uid, "resume-root-a"); state != store.ModelCreditTaskPaused {
		t.Fatalf("root-a state=%q, want paused", state)
	}
	// 期间用户升级到 3 档并补款：resume 必须仍按 root-a 自己的 2 档计价。
	modelCreditTestPermissionTier(t, db, uid, 3)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 50, "resume-tier-topup")
	if _, err := modelCreditTaskAction(t, db, uid, "resume-root-b", store.ModelCreditTaskActionFinish); err != nil {
		t.Fatal(err)
	}
	resumed, err := modelCreditTaskAction(t, db, uid, "resume-root-a", store.ModelCreditTaskActionResume)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.Allowed || resumed.State != store.ModelCreditTaskRunning {
		t.Fatalf("resume decision %+v", resumed)
	}
	if resumed.PlanTier != 2 {
		t.Fatalf("resume re-priced the task at tier %d, want the original 2", resumed.PlanTier)
	}
	stored, err := db.GetModelCreditTask(ctx, uid, "resume-root-a")
	if err != nil || stored.PlanTier != 2 {
		t.Fatalf("stored tier after resume %v %+v", err, stored)
	}
}

// 服务端取不到 1..3 档（套餐 plan_tier=0）时失败关闭：不开始、不猜档位；之后即使补上
// 可信套餐也只在 resume 时锁一次快照，从不凭空给档位。
func TestPostgresModelCreditTaskWithoutTrustedTierFailsClosed(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-no-tier")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 100, "no-tier-paid")
	modelCreditTestPermissionTier(t, db, uid, 0)

	started := modelCreditTaskStartDecision(t, db, uid, "no-tier-root")
	if started.Allowed || started.State != store.ModelCreditTaskPaused {
		t.Fatalf("start without a trusted plan tier = %+v, want denied", started)
	}
	if started.Reason != store.ModelCreditTaskReasonPlanTierUnavailable || started.PlanTier != 0 {
		t.Fatalf("start denial = %+v, want plan_tier_unavailable with tier 0", started)
	}
	stored, err := db.GetModelCreditTask(ctx, uid, "no-tier-root")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PlanTier != 0 || stored.State != store.ModelCreditTaskPaused {
		t.Fatalf("persisted row %+v", stored)
	}
	// 暂停态下不报档位；resume 依然失败关闭（没有可信档位可用）。
	status, err := modelCreditTaskAction(t, db, uid, "no-tier-root", store.ModelCreditTaskActionStatus)
	if err != nil || status.PlanTier != 0 || status.Allowed {
		t.Fatalf("status %v %+v", err, status)
	}
	resumed, err := modelCreditTaskAction(t, db, uid, "no-tier-root", store.ModelCreditTaskActionResume)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Allowed || resumed.Reason != store.ModelCreditTaskReasonPlanTierUnavailable || resumed.PlanTier != 0 {
		t.Fatalf("resume without a trusted plan tier = %+v, want fail closed", resumed)
	}

	// 列上线前写下的旧行：running 但档位是 0（不可信）→ status 失败关闭、reserve 409
	// 语义的硬错误、finish 仍能收尾释放名额。
	if _, err := db.db.Exec(`INSERT INTO model_credit_accounts(uid) VALUES ($1) ON CONFLICT (uid) DO NOTHING`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`
		INSERT INTO model_credit_tasks(root_task_id, uid, state, reason, started_at, started_seq)
		VALUES ('legacy-running-root', $1, 'running', 'ok', CURRENT_TIMESTAMP, 99)`, uid); err != nil {
		t.Fatal(err)
	}
	legacyStatus, err := modelCreditTaskAction(t, db, uid, "legacy-running-root", store.ModelCreditTaskActionStatus)
	if err != nil {
		t.Fatal(err)
	}
	if legacyStatus.Allowed || legacyStatus.Reason != store.ModelCreditTaskReasonPlanTierUnavailable || legacyStatus.PlanTier != 0 {
		t.Fatalf("legacy running row status = %+v, want fail closed", legacyStatus)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "legacy-root-reserve", RootTaskID: "legacy-running-root", Units: 1,
	}); !errors.Is(err, store.ErrModelCreditTaskTierUnavailable) {
		t.Fatalf("reserve under a row without a trusted tier accepted: %v", err)
	}
	finished, err := modelCreditTaskAction(t, db, uid, "legacy-running-root", store.ModelCreditTaskActionFinish)
	if err != nil {
		t.Fatalf("finish must still release the slot: %v", err)
	}
	if finished.State != store.ModelCreditTaskCompleted || finished.PlanTier != 0 {
		t.Fatalf("finish of an untrusted row %+v", finished)
	}
}

// 快照跨启动 mirror 与编号迁移一致（DDL 重复执行安全，列只加一次）。
func TestPostgresModelCreditTaskPlanTierColumnIsIdempotent(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	var nullable string
	var typ string
	read := func() {
		if err := db.db.QueryRowContext(ctx, `
			SELECT is_nullable, data_type FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'model_credit_tasks'
			  AND column_name = 'plan_tier'`).Scan(&nullable, &typ); err != nil {
			t.Fatalf("read plan_tier column: %v", err)
		}
	}
	read()
	if nullable != "NO" || typ != "smallint" {
		t.Fatalf("plan_tier column = nullable=%q type=%q, want NOT NULL smallint", nullable, typ)
	}
	uid := modelCreditTestUser(t, db, "credit-tier-ddl")
	modelCreditTestPermissionTier(t, db, uid, 1)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "tier-ddl-paid")
	modelCreditTestTask(t, db, uid, "tier-ddl-root")
	// 再跑一次 CreateSchema（= 一次重启）：列已存在，不能被重建、已有快照不能丢。
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("re-run schema: %v", err)
	}
	read()
	stored, err := db.GetModelCreditTask(ctx, uid, "tier-ddl-root")
	if err != nil || stored.PlanTier != 1 {
		t.Fatalf("snapshot lost after schema re-run: %v %+v", err, stored)
	}
}

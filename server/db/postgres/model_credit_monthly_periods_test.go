package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 月额度周期对账的 PostgreSQL 集成测试（需要 CATS_PG_TEST_DSN）。
//
// 覆盖 docs/credit-plan-rules-20261109.md 里可自动验证的条款：
//   - P0-5 自然月对日：1/31 → 2/28 → 3/31，提前续费不重发；
//   - P0-2 月额度当期有效、不结转：跨期作废旧周期余额，并发/重复执行只发一次；
//   - P0-2 跨期预留不挪用：旧周期的预留释放不会塞回新周期；
//   - P1-1 邀请赠送 20 credit，且不覆盖已付 primary、不发月额度。
func TestPostgresModelCreditMonthlyPeriodReconcile(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()

	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             "credit-v2-monthly",
		Name:             "V2 日常",
		PriceFen:         7900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		State:            0,
		PlanTier:         2,
		ModelCreditUnits: 12_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create credit plan: %v", err)
	}
	uid := createCreditMonthlyTestUser(t, db, "monthly-reconcile")
	// 订阅起点相对当前时间取两个月前：ReserveModelCredits 用真实时钟对账，所以
	// 订阅必须已经生效，周期边界仍然由锚点日号决定（该月没有这一天就取月末）。
	anchor := time.Now().UTC().AddDate(0, -2, 0).Truncate(time.Second)
	insertCreditMonthlyTestEntitlement(t, db, uid, planID, "order", "ORDER-MONTHLY", anchor,
		anchor.AddDate(0, 6, 0), anchor)

	first, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, anchor.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if first.MonthlyUnits != 12_000*store.ModelCreditUnitsPerCredit {
		t.Fatalf("first period allowance = %d units, want 12000 credit", first.MonthlyUnits)
	}
	// 幂等：同一周期重复对账（后台扫描 + 预留并发）不能发第二份。
	again, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, anchor.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if again.MonthlyUnits != first.MonthlyUnits {
		t.Fatalf("repeat reconcile changed the allowance: %d -> %d", first.MonthlyUnits, again.MonthlyUnits)
	}

	// 本期预留 500 credit，跨到下一个周期后剩余额度作废、新周期重新发放。
	// 账本预留必须挂在已经 start 的 root 任务上（P0-3.9）。
	if decision, taskErr := db.ApplyModelCreditTask(ctx, store.ModelCreditTaskInput{
		UID: uid, TaskID: "root-monthly-period", Action: store.ModelCreditTaskActionStart,
	}); taskErr != nil || !decision.Allowed {
		t.Fatalf("start root task: %v %+v", taskErr, decision)
	}
	reservation, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
		UID: uid, RequestID: "run-period-1", RootTaskID: "root-monthly-period", Units: 500 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("reserve in first period: %v", err)
	}
	if held.MonthlyUnits != 11_500*store.ModelCreditUnitsPerCredit {
		t.Fatalf("after hold monthly = %d units, want 11500 credit", held.MonthlyUnits)
	}

	// 走到下一个周期：旧周期剩下的 11,500 作废，新周期重新发 12,000。
	second, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, time.Now().UTC().AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("second period reconcile: %v", err)
	}
	if second.MonthlyUnits != 12_000*store.ModelCreditUnitsPerCredit {
		t.Fatalf("second period allowance = %d units, want exactly 12000 (old period must not carry over)", second.MonthlyUnits)
	}

	// 模拟真实跨期：这笔预留是上个周期创建的（跨期时后台扫描已经换过期）。
	if _, err := db.db.ExecContext(ctx, `UPDATE model_credit_reservations SET created_at = $3
		WHERE uid = $1 AND request_id = $2`, uid, reservation.RequestID, anchor); err != nil {
		t.Fatalf("backdate reservation: %v", err)
	}
	// 旧周期的预留此时释放：未用的 500 credit 属于上个周期，必须作废而不是塞回本期。
	settled, afterSettle, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{
		UID: uid, RequestID: reservation.RequestID, ActualUnits: 0,
	})
	if err != nil {
		t.Fatalf("settle cross-period reservation: %v", err)
	}
	if settled.State != store.ModelCreditReservationSettled {
		t.Fatalf("reservation state = %q", settled.State)
	}
	if afterSettle.MonthlyUnits != 12_000*store.ModelCreditUnitsPerCredit {
		t.Fatalf("cross-period release leaked into the current period: monthly = %d units, want 12000 credit", afterSettle.MonthlyUnits)
	}
	var expireEvents int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_credit_ledger_events
		WHERE uid = $1 AND bucket = 'monthly' AND event_type = 'expire'`, uid).Scan(&expireEvents); err != nil {
		t.Fatalf("count expire events: %v", err)
	}
	if expireEvents < 2 {
		t.Fatalf("expected expiry events for the carried-over allowance and the stale hold, got %d", expireEvents)
	}
	var grants int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_credit_ledger_events
		WHERE uid = $1 AND bucket = 'monthly' AND event_type = 'grant'`, uid).Scan(&grants); err != nil {
		t.Fatalf("count monthly grants: %v", err)
	}
	if grants != 3 {
		t.Fatalf("monthly grants = %d, want exactly one per period (3)", grants)
	}
}

// 订阅结束后，剩下的月额度必须作废：额度不结转，也不能在无套餐状态下继续花。
func TestPostgresModelCreditMonthlyPeriodExpiresWithSubscription(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             "credit-v1-monthly",
		Name:             "V1 入门",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         1,
		ModelCreditUnits: 4_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create credit plan: %v", err)
	}
	uid := createCreditMonthlyTestUser(t, db, "monthly-expiry")
	anchor := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
	insertCreditMonthlyTestEntitlement(t, db, uid, planID, "order", "ORDER-EXPIRY", anchor,
		time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC), anchor)

	funded, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, anchor.Add(time.Hour))
	if err != nil {
		t.Fatalf("reconcile active period: %v", err)
	}
	if funded.MonthlyUnits != 4_000*store.ModelCreditUnitsPerCredit {
		t.Fatalf("active period allowance = %d units", funded.MonthlyUnits)
	}
	expired, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, time.Date(2027, time.March, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reconcile expired subscription: %v", err)
	}
	if expired.MonthlyUnits != 0 {
		t.Fatalf("expired subscription kept %d monthly units, want 0", expired.MonthlyUnits)
	}
}

// 邀请码赠送额度是邀请自己的 20 credit，不是套餐月额度；它不能覆盖已付 V2 primary，
// 也不能让邀请用户开始领月额度。
func TestPostgresCreditInviteGrantsTwentyCredit(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             "credit-v3-monthly",
		Name:             "V3 重度",
		PriceFen:         15900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         3,
		ModelCreditUnits: 22_500 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create credit plan: %v", err)
	}
	inviteID, err := db.CreateCommercialInviteCode(&types.CommercialInviteCode{
		CreateOnly:              true,
		Code:                    "CREDIT-INVITE-20",
		PlanID:                  planID,
		MaxRedemptions:          5,
		ModelCreditUnitsInherit: true,
	})
	if err != nil {
		t.Fatalf("create credit invite: %v", err)
	}
	if inviteID <= 0 {
		t.Fatal("invite id must be positive")
	}
	invites, err := db.ListCommercialInviteCodes(10)
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	var recorded int64
	for _, invite := range invites {
		if invite != nil && invite.Code == "CREDIT-INVITE-20" {
			recorded = invite.ModelCreditUnits
		}
	}
	if recorded != types.CommercialInviteDefaultCreditUnits {
		t.Fatalf("invite records %d units, want the default 20 credit (%d)", recorded, types.CommercialInviteDefaultCreditUnits)
	}

	// 这个用户已经付过 V2：邀请只能加 gift，不能把他降级或撤销付费权益。
	paidPlanID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             "credit-v2-paid",
		Name:             "V2 已付",
		PriceFen:         7900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         2,
		ModelCreditUnits: 12_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create paid plan: %v", err)
	}
	uid := createCreditMonthlyTestUser(t, db, "invite-redeemer")
	paidAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	insertCreditMonthlyTestEntitlement(t, db, uid, paidPlanID, "order", "ORDER-PAID-V2", paidAt,
		paidAt.AddDate(0, 1, 0), paidAt)

	if _, err := db.RedeemCommercialInvite(uid, "CREDIT-INVITE-20"); err != nil {
		t.Fatalf("redeem credit invite: %v", err)
	}
	account, err := db.GetModelCreditAccount(ctx, uid)
	if err != nil {
		t.Fatalf("account after redemption: %v", err)
	}
	if account.GiftUnits != types.CommercialInviteDefaultCreditUnits {
		t.Fatalf("gift credit = %d units, want 20 credit", account.GiftUnits)
	}
	if account.PaidUnits != 0 || account.MonthlyUnits != 0 {
		t.Fatalf("a credit invite must only credit the gift bucket, got paid=%d monthly=%d", account.PaidUnits, account.MonthlyUnits)
	}

	// 已付 V2 仍在有效期内，且未被撤销。
	var revoked int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commercial_entitlements
		WHERE uid = $1 AND plan_id = $2 AND state = 'active'`, uid, paidPlanID).Scan(&revoked); err != nil {
		t.Fatalf("count paid entitlements: %v", err)
	}
	if revoked != 1 {
		t.Fatalf("paid V2 entitlement was revoked by an invite redemption (active rows = %d)", revoked)
	}
	// 邀请码权限不设固定天数：credit 邀请的订阅没有到期时间。
	var inviteExpiry *time.Time
	var inviteSource string
	if err := db.db.QueryRowContext(ctx, `SELECT expires_at, source FROM commercial_entitlements
		WHERE uid = $1 AND source = 'invite' ORDER BY id DESC LIMIT 1`, uid).Scan(&inviteExpiry, &inviteSource); err != nil {
		t.Fatalf("load invite entitlement: %v", err)
	}
	if inviteExpiry != nil {
		t.Fatalf("credit invite entitlement expires at %s, want no fixed term", inviteExpiry)
	}
	// 邀请订阅不发月额度。
	enabled, tier, allowance, err := db.GetModelCreditEligibility(ctx, uid)
	if err != nil {
		t.Fatalf("eligibility after redemption: %v", err)
	}
	if !enabled || tier != 2 || allowance != 12_000*store.ModelCreditUnitsPerCredit {
		t.Fatalf("eligibility = (enabled=%t tier=%d allowance=%d), want the paid V2 primary", enabled, tier, allowance)
	}
	afterInvite, err := db.GetModelCreditAccount(ctx, uid)
	if err != nil {
		t.Fatalf("account after eligibility: %v", err)
	}
	if afterInvite.MonthlyUnits != 12_000*store.ModelCreditUnitsPerCredit {
		t.Fatalf("monthly allowance after invite = %d units, want only the paid V2 period allowance", afterInvite.MonthlyUnits)
	}
}

func openCreditMonthlyTestDB(t *testing.T) *Adapter {
	t.Helper()
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_credit_monthly_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		base.Close()
		t.Fatalf("create credit monthly test schema: %v", err)
	}
	t.Cleanup(func() {
		base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)
		base.Close()
	})
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open credit monthly postgres connection: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create credit monthly schema objects: %v", err)
	}
	return db
}

func createCreditMonthlyTestUser(t *testing.T, db *Adapter, prefix string) int64 {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	uid, err := db.CreateUser(&types.User{
		Username:    name,
		Email:       name + "@example.test",
		DisplayName: name,
		AccountType: types.AccountHuman,
		PassHash:    []byte(name + "-hash"),
	})
	if err != nil {
		t.Fatalf("create %s user: %v", prefix, err)
	}
	return uid
}

func insertCreditMonthlyTestEntitlement(t *testing.T, db *Adapter, uid, planID int64, source, sourceRef string, startsAt, expiresAt, anchor time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at, period_anchor_at)
		VALUES ($1, $2, $3, $4, 'active', $5, $6, $7)`,
		uid, planID, source, sourceRef, startsAt, expiresAt, anchor); err != nil {
		t.Fatalf("insert %s entitlement: %v", source, err)
	}
}

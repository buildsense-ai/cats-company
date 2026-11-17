package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// GetModelCreditEligibility 的充值补充规则（需要 CATS_PG_TEST_DSN）：
//   - 花钱买来的 paid 点数在没有套餐时也是一份权限（tier 用梯度默认第 1 档）；
//   - legacy 老用户不因为这次改动凭空获得 credit 权限；
//   - 只送不买的 gift 额度不构成权限；
//   - 过期订阅仍然是「有身份、没权限」。
func TestPostgresModelCreditEligibilityTopupOnly(t *testing.T) {
	db := newCreditTopupTestAdapter(t)
	ctx := context.Background()

	// 1. 从没进过 credit 体系的 legacy 用户：完全不变。
	legacyUID := createCreditTopupTestUser(t, db, "eligibility-legacy")
	enabled, tier, allowance, err := db.GetModelCreditEligibility(ctx, legacyUID)
	if err != nil {
		t.Fatalf("legacy eligibility: %v", err)
	}
	if enabled || tier != 0 || allowance != 0 {
		t.Fatalf("a legacy account must stay outside credit: enabled=%v tier=%d allowance=%d", enabled, tier, allowance)
	}
	if permitted, err := eligibilityTaskPermitted(t, db, legacyUID); err != nil || permitted {
		t.Fatalf("a legacy account must not be permitted to start a credit task: permitted=%v err=%v", permitted, err)
	}

	// 2. 只有赠送额度（邀请）：身份有，权限没有。
	giftUID := createCreditTopupTestUser(t, db, "eligibility-gift")
	if _, err := db.GrantModelCredits(ctx, store.ModelCreditGrantInput{
		UID: giftUID, Bucket: store.ModelCreditBucketGift,
		Units: 500 * CommercialTopupUnitsPerFen, IdempotencyKey: "topup:eligibility-gift",
	}); err != nil {
		t.Fatalf("grant gift credit: %v", err)
	}
	enabled, _, allowance, err = db.GetModelCreditEligibility(ctx, giftUID)
	if err != nil {
		t.Fatalf("gift eligibility: %v", err)
	}
	if !enabled || allowance != 0 {
		t.Fatalf("gift-only accounts have an identity but no permission: enabled=%v allowance=%d", enabled, allowance)
	}

	// 3. 只买了充值：没有套餐也能用，档位取梯度默认第 1 档。
	topupUID := createCreditTopupTestUser(t, db, "eligibility-topup")
	grantTopupCredits(t, db, topupUID, 1200, "topup:eligibility-topup")
	enabled, tier, allowance, err = db.GetModelCreditEligibility(ctx, topupUID)
	if err != nil {
		t.Fatalf("top-up eligibility: %v", err)
	}
	if !enabled || allowance != 1200*CommercialTopupUnitsPerFen {
		t.Fatalf("a paid top-up must be spendable without a package: enabled=%v allowance=%d", enabled, allowance)
	}
	if tier != store.ModelCreditTaskPlanTierMin {
		t.Fatalf("a top-up-only account prices at the base rung, got %d", tier)
	}
	if permitted, err := eligibilityTaskPermitted(t, db, topupUID); err != nil || !permitted {
		t.Fatalf("a top-up-only account must be able to start a task: permitted=%v err=%v", permitted, err)
	}

	// 4. 过期订阅：身份还在，权限没了。
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "eligibility-expired", Name: "Expired Credit Plan", DurationDays: 30,
		ModelCreditUnits: 800 * CommercialTopupUnitsPerFen, PlanTier: 2, PriceFen: 9900,
		Currency: "CNY", ModelBudgets: map[string]float64{},
	})
	if err != nil {
		t.Fatalf("create eligibility plan: %v", err)
	}
	expiredUID := createCreditTopupTestUser(t, db, "eligibility-expired")
	startsAt := time.Now().UTC().Add(-60 * 24 * time.Hour)
	expiresAt := time.Now().UTC().Add(-24 * time.Hour)
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', 'eligibility-expired-order', 'active', $3, $4)`,
		expiredUID, planID, startsAt, expiresAt); err != nil {
		t.Fatalf("seed expired entitlement: %v", err)
	}
	// 真实账户在订阅期里会被记账一次月额度：这才是「进过 credit 体系」的身份来源。
	if _, err := db.GrantModelCredits(ctx, store.ModelCreditGrantInput{
		UID: expiredUID, Bucket: store.ModelCreditBucketMonthly,
		Units: 800 * CommercialTopupUnitsPerFen, IdempotencyKey: "topup:eligibility-expired-monthly",
	}); err != nil {
		t.Fatalf("grant expired-period monthly credit: %v", err)
	}
	enabled, _, allowance, err = db.GetModelCreditEligibility(ctx, expiredUID)
	if err != nil {
		t.Fatalf("expired eligibility: %v", err)
	}
	if !enabled || allowance != 0 {
		t.Fatalf("an expired subscription keeps the identity but not the permission: enabled=%v allowance=%d", enabled, allowance)
	}
	if permitted, err := eligibilityTaskPermitted(t, db, expiredUID); err != nil || permitted {
		t.Fatalf("an expired subscription must not be permitted to start a credit task: permitted=%v err=%v", permitted, err)
	}

	// 5. 过期订阅 + 独立充值：权限来自充值，不是来自已经过期的套餐。
	grantTopupCredits(t, db, expiredUID, 300, "topup:eligibility-expired")
	enabled, tier, allowance, err = db.GetModelCreditEligibility(ctx, expiredUID)
	if err != nil {
		t.Fatalf("expired+top-up eligibility: %v", err)
	}
	if !enabled || allowance != 300*CommercialTopupUnitsPerFen || tier != store.ModelCreditTaskPlanTierMin {
		t.Fatalf("a top-up must restore spendable permission on a lapsed account: enabled=%v tier=%d allowance=%d", enabled, tier, allowance)
	}

	// 有效的旧套餐没有点数额度，不能遮住独立充值的基础档权限。
	legacyPlan, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "eligibility-legacy-active", Name: "Legacy", DurationDays: 30,
		PlanTier: 3, PriceFen: 9900, Currency: "CNY", ModelBudgets: map[string]float64{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO commercial_entitlements
		(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'admin', 'legacy-active', 'active', $3, $4)`,
		topupUID, legacyPlan, time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	enabled, tier, allowance, err = db.GetModelCreditEligibility(ctx, topupUID)
	if err != nil || !enabled || tier != store.ModelCreditTaskPlanTierMin || allowance != 1200*CommercialTopupUnitsPerFen {
		t.Fatalf("legacy package must not shadow paid balance: enabled=%v tier=%d allowance=%d err=%v", enabled, tier, allowance, err)
	}
	// 同一旧套餐来自真实订单时，零点数快照也不能遮住充值余额。
	insertCreditMonthlyTestOrderSnapshot(t, db, topupUID, legacyPlan, "legacy-active-order")
	if _, err := db.db.Exec(`UPDATE commercial_entitlements SET source = 'order', source_ref = 'legacy-active-order'
		WHERE uid = $1 AND plan_id = $2`, topupUID, legacyPlan); err != nil {
		t.Fatal(err)
	}
	enabled, tier, allowance, err = db.GetModelCreditEligibility(ctx, topupUID)
	if err != nil || !enabled || tier != store.ModelCreditTaskPlanTierMin || allowance != 1200*CommercialTopupUnitsPerFen {
		t.Fatalf("legacy order must not shadow paid balance: enabled=%v tier=%d allowance=%d err=%v", enabled, tier, allowance, err)
	}

	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	taskTier, err := modelCreditTaskPlanTier(ctx, tx, topupUID, time.Now().UTC())
	if err != nil || taskTier != tier {
		t.Fatalf("task and eligibility tiers diverged: task=%d eligibility=%d err=%v", taskTier, tier, err)
	}
}

// eligibilityTaskPermitted runs the start-time gate in a throwaway transaction.
func eligibilityTaskPermitted(t *testing.T, db *Adapter, uid int64) (bool, error) {
	t.Helper()
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatalf("begin task gate check: %v", err)
	}
	defer tx.Rollback()
	return modelCreditTaskPermitted(context.Background(), tx, uid, time.Now().UTC())
}

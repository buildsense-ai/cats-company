package postgres

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 月订阅的边界契约（需要 CATS_PG_TEST_DSN）：
//
//   - 同一档不同 plan row／slug 的续费接在付费链尾端，并继承原锚点；
//   - 连续多次提前续费逐段累计，不重启窗口、不提前发月额度；
//   - 月额度按订单快照，后台改额或清零不改写已付用户；
//   - credit 商品只按 30 天自然月售卖（建品／下单／履约都拒绝别的天数），legacy 不受影响；
//   - 邀请码没有订单快照：不发月额度，但权限/赠送额度不回归；
//   - 订单行缺失时 fail closed，绝不按套餐现值凭空授额。
func TestPostgresModelCreditSubscriptionRenewalChain(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	const allowance = 4_000 * store.ModelCreditUnitsPerCredit
	firstPlanID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-chain-v1-a", Name: "V1 月订阅 A", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1, ModelCreditUnits: allowance,
	})
	if err != nil {
		t.Fatalf("create first credit plan: %v", err)
	}
	// 重新上架的同档套餐：新 id、新 slug、额度也不同，但对买家仍是同一档。
	relistedPlanID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-chain-v1-b", Name: "V1 月订阅 B", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1, ModelCreditUnits: 5_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create relisted credit plan: %v", err)
	}

	t.Run("a relisted plan on the same tier renews the same chain", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-chain-rename")
		first := createAndFulfillCommercialTestOrder(t, db, uid, firstPlanID, "CCCHAIN01", "cc_chain_request_1", "cc-chain-event-1")
		firstStart, firstExpiry := creditSubscriptionEntitlementWindow(t, db, uid, first.OrderNo)
		firstAnchor := creditBoundaryEntitlementAnchor(t, db, uid, first.OrderNo)

		relisted := createAndFulfillCommercialTestOrder(t, db, uid, relistedPlanID, "CCCHAIN02", "cc_chain_request_2", "cc-chain-event-2")
		renewalStart, renewalExpiry := creditSubscriptionEntitlementWindow(t, db, uid, relisted.OrderNo)
		renewalAnchor := creditBoundaryEntitlementAnchor(t, db, uid, relisted.OrderNo)

		if !renewalStart.Equal(firstExpiry) || !renewalExpiry.After(firstExpiry) || !renewalStart.After(firstStart) {
			t.Fatalf("a same-tier plan on another slug did not append a period: first=[%s,%s) renewal=[%s,%s)",
				firstStart.Format(time.RFC3339Nano), firstExpiry.Format(time.RFC3339Nano),
				renewalStart.Format(time.RFC3339Nano), renewalExpiry.Format(time.RFC3339Nano))
		}
		if !renewalAnchor.Equal(firstAnchor) {
			t.Fatalf("renewal anchor = %s, want the inherited %s (a new plan row must not re-anchor the month)",
				renewalAnchor.Format(time.RFC3339Nano), firstAnchor.Format(time.RFC3339Nano))
		}
		account := creditSubscriptionAccount(t, db, ctx, uid)
		if account.MonthlyUnits != allowance || account.PaidUnits != 0 {
			t.Fatalf("renewing through another plan row topped up the balance: monthly=%d paid=%d, want monthly=%d paid=0",
				account.MonthlyUnits, account.PaidUnits, allowance)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
			t.Fatalf("renewal through another plan row granted the next period immediately: monthly grants=%d, want 1", grants)
		}
		if paid := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketPaid, ""); paid != 0 {
			t.Fatalf("a credit subscription wrote %d paid ledger events, want 0", paid)
		}
	})

	t.Run("stacked early renewals accumulate and keep the original anchor", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-chain-stack")
		first := createAndFulfillCommercialTestOrder(t, db, uid, firstPlanID, "CCSTACK01", "cc_stack_request_1", "cc-stack-event-1")
		firstStart, firstExpiry := creditSubscriptionEntitlementWindow(t, db, uid, first.OrderNo)
		firstAnchor := creditBoundaryEntitlementAnchor(t, db, uid, first.OrderNo)

		second := createAndFulfillCommercialTestOrder(t, db, uid, firstPlanID, "CCSTACK02", "cc_stack_request_2", "cc-stack-event-2")
		secondStart, secondExpiry := creditSubscriptionEntitlementWindow(t, db, uid, second.OrderNo)
		third := createAndFulfillCommercialTestOrder(t, db, uid, firstPlanID, "CCSTACK03", "cc_stack_request_3", "cc-stack-event-3")
		thirdStart, thirdExpiry := creditSubscriptionEntitlementWindow(t, db, uid, third.OrderNo)

		// 每段都接在前一段尾端：第三个窗口不能回到第一个到期日重新算。
		if !secondStart.Equal(firstExpiry) || !secondExpiry.After(firstExpiry) {
			t.Fatalf("second renewal [%s,%s) does not follow the first expiry %s",
				secondStart.Format(time.RFC3339Nano), secondExpiry.Format(time.RFC3339Nano),
				firstExpiry.Format(time.RFC3339Nano))
		}
		if !thirdStart.Equal(secondExpiry) || !thirdExpiry.After(secondExpiry) {
			t.Fatalf("third renewal [%s,%s) restarted from the first expiry instead of stacking after %s",
				thirdStart.Format(time.RFC3339Nano), thirdExpiry.Format(time.RFC3339Nano),
				secondExpiry.Format(time.RFC3339Nano))
		}
		if !thirdStart.After(firstStart) {
			t.Fatalf("stacked renewals produced an overlapping window: first start %s third start %s",
				firstStart.Format(time.RFC3339Nano), thirdStart.Format(time.RFC3339Nano))
		}
		for _, orderNo := range []string{second.OrderNo, third.OrderNo} {
			if anchor := creditBoundaryEntitlementAnchor(t, db, uid, orderNo); !anchor.Equal(firstAnchor) {
				t.Fatalf("renewal %s anchor = %s, want the original %s", orderNo,
					anchor.Format(time.RFC3339Nano), firstAnchor.Format(time.RFC3339Nano))
			}
		}
		account := creditSubscriptionAccount(t, db, ctx, uid)
		if account.MonthlyUnits != allowance || account.PaidUnits != 0 {
			t.Fatalf("stacked renewals changed the balance: monthly=%d paid=%d, want monthly=%d paid=0",
				account.MonthlyUnits, account.PaidUnits, allowance)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
			t.Fatalf("stacked renewals granted extra monthly allowances: grants=%d, want 1", grants)
		}
		// 同一个周期内再对账一次，也不能补发。
		if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, time.Now().UTC().Add(2*time.Hour)); err != nil {
			t.Fatalf("reconcile the current period after stacking: %v", err)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
			t.Fatalf("reconcile after stacked renewals granted again: grants=%d", grants)
		}
	})
}

// 后台改套餐或把套餐额度清零，都不能改写已经卖出去的订单快照。
func TestPostgresModelCreditSubscriptionUsesOrderSnapshot(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	// 订单卖出 4,000 credit，套餐现值之后被改成 9,000 再被清零。
	const soldUnits = 4_000 * store.ModelCreditUnitsPerCredit
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-snapshot-v1", Name: "V1 订单快照", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1, ModelCreditUnits: 9_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create snapshot plan: %v", err)
	}
	uid := createCreditMonthlyTestUser(t, db, "credit-snapshot")
	anchor := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC)
	seedCreditBoundaryOrderSnapshot(t, db, uid, planID, "CCSNAPSHOT", soldUnits, 30)
	seedCreditBoundaryOrderEntitlement(t, db, uid, planID, "CCSNAPSHOT", anchor, periodEnd, anchor)

	granted, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, anchor.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("reconcile the sold period: %v", err)
	}
	if granted.MonthlyUnits != soldUnits {
		t.Fatalf("period allowance = %d units, want the sold snapshot %d (not the live plan)", granted.MonthlyUnits, soldUnits)
	}

	if _, err := db.db.ExecContext(ctx, `UPDATE commercial_plans SET model_credit_units = 0 WHERE id = $1`, planID); err != nil {
		t.Fatalf("zero the plan allowance: %v", err)
	}
	after, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, anchor.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("reconcile after zeroing the plan: %v", err)
	}
	if after.MonthlyUnits != soldUnits {
		t.Fatalf("zeroing the plan rewrote the sold allowance: monthly=%d, want %d", after.MonthlyUnits, soldUnits)
	}
	if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
		t.Fatalf("zeroing the plan changed the ledger: monthly grants=%d, want 1", grants)
	}
	var snapshot int64
	if err := db.db.QueryRow(`SELECT plan_model_credit_units FROM commercial_orders WHERE order_no = 'CCSNAPSHOT'`).Scan(&snapshot); err != nil {
		t.Fatalf("read order snapshot: %v", err)
	}
	if snapshot != soldUnits {
		t.Fatalf("order snapshot = %d units, want the sold %d", snapshot, soldUnits)
	}

	// 真实支付路径上同样成立：读权限、读周期、任务放行都按快照，套餐清零后也不会掉成 legacy。
	livePlanID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-snapshot-live", Name: "V1 清零", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1, ModelCreditUnits: soldUnits,
	})
	if err != nil {
		t.Fatalf("create live snapshot plan: %v", err)
	}
	liveUID := createCreditMonthlyTestUser(t, db, "credit-snapshot-live")
	order := createAndFulfillCommercialTestOrder(t, db, liveUID, livePlanID, "CCSNAPLIVE", "cc_snap_live_request", "cc-snap-live-event")
	_, orderExpiry := creditSubscriptionEntitlementWindow(t, db, liveUID, order.OrderNo)
	if _, err := db.db.ExecContext(ctx, `UPDATE commercial_plans SET model_credit_units = 0 WHERE id = $1`, livePlanID); err != nil {
		t.Fatalf("zero the live plan allowance: %v", err)
	}
	enabled, tier, allowance, err := db.GetModelCreditEligibility(ctx, liveUID)
	if err != nil || !enabled || tier != 1 || allowance != soldUnits {
		t.Fatalf("zeroing the plan changed eligibility: (%v,%d,%d,%v), want (true,1,%d)",
			enabled, tier, allowance, err, soldUnits)
	}
	if decision := modelCreditTaskStartDecision(t, db, liveUID, "snapshot-root"); !decision.Allowed {
		t.Fatalf("a paid order lost its credit task permission after the plan was zeroed: %+v", decision)
	}
	period, err := db.GetModelCreditPeriod(ctx, liveUID, time.Now().UTC())
	if err != nil || period == nil {
		t.Fatalf("paid order period=%v err=%v, want one after the plan was zeroed", period, err)
	}
	if period.SubscriptionExpiresAt == nil || !period.SubscriptionExpiresAt.Equal(orderExpiry) {
		t.Fatalf("subscription_expires_at=%v, want %s", period.SubscriptionExpiresAt, orderExpiry)
	}
	if account := creditSubscriptionAccount(t, db, ctx, liveUID); account.MonthlyUnits != soldUnits || account.PaidUnits != 0 {
		t.Fatalf("monthly=%d paid=%d after zeroing the plan, want %d/0", account.MonthlyUnits, account.PaidUnits, soldUnits)
	}
}

// credit 商品只能按 30 天自然月售卖；legacy CNY/token 套餐的天数保持自由。
func TestPostgresModelCreditSubscriptionDurationGuard(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	const allowance = 4_000 * store.ModelCreditUnitsPerCredit

	t.Run("a credit plan with another duration is refused", func(t *testing.T) {
		if _, err := db.CreateCommercialPlan(&types.CommercialPlan{
			Slug: "credit-duration-90", Name: "V1 90 天", PriceFen: 3900, Currency: "CNY",
			SaleState: "hidden", DurationDays: 90, PlanTier: 1, ModelCreditUnits: allowance,
		}); err == nil || !strings.Contains(err.Error(), "30-day") {
			t.Fatalf("a 90-day credit plan was accepted: err=%v", err)
		}
		var rows int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_plans WHERE slug = 'credit-duration-90'`).Scan(&rows); err != nil {
			t.Fatalf("count refused plan: %v", err)
		}
		if rows != 0 {
			t.Fatalf("a refused credit plan was still written: rows=%d", rows)
		}
	})

	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-duration-30", Name: "V1 30 天", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1, ModelCreditUnits: allowance,
	})
	if err != nil {
		t.Fatalf("create 30-day credit plan: %v", err)
	}

	t.Run("an order for a credit plan sold for another duration is refused", func(t *testing.T) {
		if _, err := db.db.ExecContext(ctx, `UPDATE commercial_plans SET duration_days = 90 WHERE id = $1`, planID); err != nil {
			t.Fatalf("edit the plan duration behind the upsert: %v", err)
		}
		defer func() {
			if _, err := db.db.ExecContext(ctx, `UPDATE commercial_plans SET duration_days = 30 WHERE id = $1`, planID); err != nil {
				t.Fatalf("restore the plan duration: %v", err)
			}
		}()
		uid := createCreditMonthlyTestUser(t, db, "credit-duration-order")
		expiresAt := time.Now().UTC().Add(20 * time.Minute)
		_, err := db.CreateCommercialOrder(&types.CommercialOrder{
			OrderNo: "CCDURATION90", UID: uid, PlanID: planID, Channel: "test",
			ClientRequestID: "cc_duration_request", ExpiresAt: &expiresAt,
		})
		if err == nil || !strings.Contains(err.Error(), "30-day") {
			t.Fatalf("an order for a 90-day credit plan was accepted: err=%v", err)
		}
	})

	t.Run("fulfilling a credit order sold for another duration is refused", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-duration-fulfill")
		seedCreditBoundaryOrderSnapshot(t, db, uid, planID, "CCDURFULFILL", allowance, 90)
		// 订单快照停在「已支付待履约」，才能走到履约时的天数校验。
		if _, err := db.db.Exec(`UPDATE commercial_orders SET status = 'created' WHERE order_no = 'CCDURFULFILL'`); err != nil {
			t.Fatalf("reopen seeded order: %v", err)
		}
		_, _, err := db.FulfillCommercialOrder("CCDURFULFILL", &types.CommercialPaymentConfirmation{
			Channel: "test", EventID: "cc-duration-fulfill-event", ProviderTradeNo: "trade-cc-duration",
			AmountFen: 3900, Currency: "CNY", PaidAt: time.Now().UTC(), PayloadHash: strings.Repeat("d", 64),
		})
		if err == nil || !strings.Contains(err.Error(), "30-day") {
			t.Fatalf("a 90-day credit order was fulfilled: err=%v", err)
		}
		var entitlements, events int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid = $1`, uid).Scan(&entitlements); err != nil {
			t.Fatalf("count entitlements: %v", err)
		}
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_payment_events WHERE order_no = 'CCDURFULFILL'`).Scan(&events); err != nil {
			t.Fatalf("count payment events: %v", err)
		}
		if entitlements != 0 || events != 0 {
			t.Fatalf("a refused fulfilment left entitlements=%d payment events=%d behind", entitlements, events)
		}
	})

	t.Run("a legacy package keeps its free-form duration", func(t *testing.T) {
		legacyPlanID, err := db.CreateCommercialPlan(&types.CommercialPlan{
			Slug: "legacy-duration-90", Name: "老套餐 90 天", PriceFen: 100, Currency: "CNY",
			SaleState: "hidden", DurationDays: 90, PlanTier: 0,
			ModelBudgets: map[string]float64{"gpt-5.6-terra": 1},
		})
		if err != nil {
			t.Fatalf("legacy plan with a 90-day term was refused: %v", err)
		}
		uid := createCreditMonthlyTestUser(t, db, "legacy-duration")
		order := createAndFulfillCommercialTestOrder(t, db, uid, legacyPlanID, "CCLEGACY90", "cc_legacy_request", "cc-legacy-event")
		startsAt, expiresAt := creditSubscriptionEntitlementWindow(t, db, uid, order.OrderNo)
		if got := expiresAt.Sub(startsAt); got < 89*24*time.Hour || got > 91*24*time.Hour {
			t.Fatalf("legacy entitlement window = %s, want the sold 90 days", got)
		}
		if monthly := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, ""); monthly != 0 {
			t.Fatalf("a legacy package wrote %d monthly ledger events, want 0", monthly)
		}
	})
}

// 邀请码只是权限 + 赠送额度：没有月额度，也不能因此掉成 legacy。
func TestPostgresModelCreditInviteKeepsPermissionWithoutMonthly(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	const allowance = 4_000 * store.ModelCreditUnitsPerCredit
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-invite-permission", Name: "V1 邀请", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1, ModelCreditUnits: allowance,
	})
	if err != nil {
		t.Fatalf("create invite plan: %v", err)
	}
	uid := createCreditMonthlyTestUser(t, db, "credit-invite-permission")
	now := time.Now().UTC()
	insertCreditMonthlyTestEntitlement(t, db, uid, planID, "invite", "INVITE-PERMISSION",
		now.Add(-time.Hour), now.AddDate(0, 1, 0), now.Add(-time.Hour))

	enabled, tier, gotAllowance, err := db.GetModelCreditEligibility(ctx, uid)
	if err != nil || !enabled || tier != 1 || gotAllowance != allowance {
		t.Fatalf("invite eligibility = (%v,%d,%d,%v), want the package permission", enabled, tier, gotAllowance, err)
	}
	if taskEnabled, err := db.modelCreditTaskEnabled(ctx, uid); err != nil || !taskEnabled {
		t.Fatalf("an invite credit user fell out of the credit model: enabled=%t err=%v", taskEnabled, err)
	}
	if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, now); err != nil {
		t.Fatalf("reconcile an invite-only account: %v", err)
	}
	if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, ""); grants != 0 {
		t.Fatalf("an invite entitlement granted %d monthly ledger events, want 0", grants)
	}
	if account, err := db.GetModelCreditAccount(ctx, uid); err == nil && account != nil && account.MonthlyUnits != 0 {
		t.Fatalf("invite account monthly = %d units, want 0 (gift only)", account.MonthlyUnits)
	}
	if period, err := db.GetModelCreditPeriod(ctx, uid, now); err != nil || period != nil {
		t.Fatalf("invite account reported a monthly period %#v (err=%v), want none", period, err)
	}
}

// 订单行缺失（source_ref 指不到 commercial_orders）时 fail closed：不发额度。
func TestPostgresModelCreditOrderSnapshotMissingFailsClosed(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-orphan-order", Name: "V1 缺订单", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1,
		ModelCreditUnits: 7_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create orphan plan: %v", err)
	}
	uid := createCreditMonthlyTestUser(t, db, "credit-orphan")
	anchor := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC)
	seedCreditBoundaryOrderEntitlement(t, db, uid, planID, "CC-ORPHAN-MISSING", anchor, periodEnd, anchor)

	account, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, anchor.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("reconcile an order entitlement without an order row: %v", err)
	}
	if account != nil && account.MonthlyUnits != 0 {
		t.Fatalf("an order entitlement without an order row granted %d monthly units, want 0 (fail closed)",
			account.MonthlyUnits)
	}
	if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, ""); grants != 0 {
		t.Fatalf("fail closed path wrote %d monthly ledger events, want 0", grants)
	}
}

// creditBoundaryEntitlementAnchor 读某条订单权益的自然月锚点。
func creditBoundaryEntitlementAnchor(t *testing.T, db *Adapter, uid int64, orderNo string) time.Time {
	t.Helper()
	var anchor sql.NullTime
	if err := db.db.QueryRow(`SELECT period_anchor_at FROM commercial_entitlements
		WHERE uid = $1 AND source = 'order' AND source_ref = $2`, uid, orderNo).Scan(&anchor); err != nil {
		t.Fatalf("load period anchor of order %s: %v", orderNo, err)
	}
	if !anchor.Valid {
		t.Fatalf("entitlement of order %s has no period anchor", orderNo)
	}
	return anchor.Time.UTC()
}

// seedCreditBoundaryOrderSnapshot 按指定天数/额度落一条订单快照行，用来构造「卖出时
// 的套餐」与现在的套餐不一致（或不合规）的场景。
func seedCreditBoundaryOrderSnapshot(t *testing.T, db *Adapter, uid, planID int64, orderNo string, units int64, durationDays int) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_orders(order_no, uid, plan_id, plan_slug, plan_name,
			plan_duration_days, amount_fen, currency, channel, status, client_request_id,
			plan_model_credit_units)
		SELECT $1, $2, p.id, p.slug, p.name, $4, GREATEST(p.price_fen, 1),
		       p.currency, 'test', 'fulfilled', $1, $5
		FROM commercial_plans p WHERE p.id = $3`, orderNo, uid, planID, durationDays, units); err != nil {
		t.Fatalf("seed order snapshot %s: %v", orderNo, err)
	}
}

// seedCreditBoundaryOrderEntitlement 只插权益行（不再自动补快照订单），用来构造
// 「权益存在但订单行缺失」的 fail closed 场景。
func seedCreditBoundaryOrderEntitlement(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, startsAt, expiresAt, anchor time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at, period_anchor_at)
		VALUES ($1, $2, 'order', $3, 'active', $4, $5, $6)`,
		uid, planID, sourceRef, startsAt, expiresAt, anchor); err != nil {
		t.Fatalf("seed order entitlement %s: %v", sourceRef, err)
	}
}

// 同一用户的两笔订单同时履约时，必须串行读付费链尾：否则两笔都从同一个链尾续，
// 得到两个重叠的月份。这里用同一个 uid、两个不同的合法订单并发履约验收。
func TestPostgresModelCreditSubscriptionConcurrentFulfillment(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	const allowance = 4_000 * store.ModelCreditUnitsPerCredit
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug: "credit-concurrent-v1", Name: "V1 并发续费", PriceFen: 3900, Currency: "CNY",
		SaleState: "hidden", DurationDays: 30, PlanTier: 1, ModelCreditUnits: allowance,
	})
	if err != nil {
		t.Fatalf("create concurrent plan: %v", err)
	}
	uid := createCreditMonthlyTestUser(t, db, "credit-concurrent")
	// 月末锚点：1/31 起的两个月分别是 1/31→2/28、2/28→3/31，两笔履约都必须落在这条链上。
	paidAt := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
	orderNos := []string{"CCCONCURRENT1", "CCCONCURRENT2"}
	for _, orderNo := range orderNos {
		// 直接安排两张合法订单：同套餐的建单会合并成一个 pending 单，拿不到两笔。
		seedCreditBoundaryOrderSnapshot(t, db, uid, planID, orderNo, allowance, 30)
		if _, err := db.db.Exec(`UPDATE commercial_orders SET status = 'created' WHERE order_no = $1`, orderNo); err != nil {
			t.Fatalf("reopen seeded order %s: %v", orderNo, err)
		}
	}

	errs := make([]error, len(orderNos))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, orderNo := range orderNos {
		wg.Add(1)
		go func(i int, orderNo string) {
			defer wg.Done()
			<-start
			_, _, err := db.FulfillCommercialOrder(orderNo, &types.CommercialPaymentConfirmation{
				Channel: "test", EventID: "cc-concurrent-event-" + orderNo,
				ProviderTradeNo: "trade-" + orderNo,
				AmountFen:       3900, Currency: "CNY", PaidAt: paidAt,
				PayloadHash: strings.Repeat("e", 64),
			})
			errs[i] = err
		}(i, orderNo)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent fulfilment of %s failed: %v", orderNos[i], err)
		}
	}

	type entitlementWindow struct {
		startsAt, expiresAt, anchor time.Time
	}
	windows := make([]entitlementWindow, 0, len(orderNos))
	for _, orderNo := range orderNos {
		var window entitlementWindow
		if err := db.db.QueryRow(`
			SELECT starts_at, expires_at, period_anchor_at FROM commercial_entitlements
			WHERE uid = $1 AND source = 'order' AND source_ref = $2`, uid, orderNo).Scan(
			&window.startsAt, &window.expiresAt, &window.anchor); err != nil {
			t.Fatalf("load entitlement of %s: %v", orderNo, err)
		}
		windows = append(windows, window)
	}
	if windows[1].startsAt.Before(windows[0].startsAt) {
		windows[0], windows[1] = windows[1], windows[0]
	}
	firstPeriodEnd := time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC)
	secondPeriodEnd := time.Date(2027, time.March, 31, 10, 0, 0, 0, time.UTC)
	if !windows[0].startsAt.Equal(paidAt) || !windows[0].expiresAt.Equal(firstPeriodEnd) {
		t.Fatalf("first fulfilled period = [%s,%s), want [%s,%s)",
			windows[0].startsAt.Format(time.RFC3339Nano), windows[0].expiresAt.Format(time.RFC3339Nano),
			paidAt.Format(time.RFC3339Nano), firstPeriodEnd.Format(time.RFC3339Nano))
	}
	if !windows[1].startsAt.Equal(firstPeriodEnd) || !windows[1].expiresAt.Equal(secondPeriodEnd) {
		t.Fatalf("concurrent fulfilment produced overlapping/gapped periods: [%s,%s) and [%s,%s), want [%s,%s) then [%s,%s)",
			windows[0].startsAt.Format(time.RFC3339Nano), windows[0].expiresAt.Format(time.RFC3339Nano),
			windows[1].startsAt.Format(time.RFC3339Nano), windows[1].expiresAt.Format(time.RFC3339Nano),
			paidAt.Format(time.RFC3339Nano), firstPeriodEnd.Format(time.RFC3339Nano),
			firstPeriodEnd.Format(time.RFC3339Nano), secondPeriodEnd.Format(time.RFC3339Nano))
	}
	for _, window := range windows {
		if !window.anchor.Equal(paidAt) {
			t.Fatalf("concurrent fulfilment re-anchored a period at %s, want the original %s",
				window.anchor.Format(time.RFC3339Nano), paidAt.Format(time.RFC3339Nano))
		}
	}
	account := creditSubscriptionAccount(t, db, ctx, uid)
	if account.MonthlyUnits != allowance || account.PaidUnits != 0 {
		t.Fatalf("concurrent fulfilment monthly=%d paid=%d, want monthly=%d paid=0",
			account.MonthlyUnits, account.PaidUnits, allowance)
	}
	if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
		t.Fatalf("two concurrent orders granted the monthly allowance %d times, want exactly 1", grants)
	}
}

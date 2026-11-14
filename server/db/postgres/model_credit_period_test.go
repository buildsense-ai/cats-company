package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 月额度窗口（GET /api/relay/credits 的 monthly_period_* / subscription_expires_at /
// next_refresh_at）必须按订阅自己的锚点算，不能按「现在 + 1 个月」推算；没续费时
// 不给刷新点，未来才开始的续费也不能被当成当下有效（需要 CATS_PG_TEST_DSN）。
func TestPostgresModelCreditPeriodWindow(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             "credit-period-window",
		Name:             "V1 月订阅",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         1,
		ModelCreditUnits: 4_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create credit period plan: %v", err)
	}

	// 月末锚点：1/31 的订阅在 2 月只到 2/28，3 月回到 3/31；没有续费就没有刷新点。
	t.Run("month-end anchor clamps and waits for a renewal", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-period-monthend")
		anchor := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
		periodEnd := time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC)
		nextPeriodEnd := time.Date(2027, time.March, 31, 10, 0, 0, 0, time.UTC)
		insertCreditMonthlyTestEntitlement(t, db, uid, planID, "order", "ORDER-PERIOD-1", anchor, periodEnd, anchor)

		period := mustModelCreditPeriod(t, db, ctx, uid, time.Date(2027, time.February, 10, 0, 0, 0, 0, time.UTC))
		if !period.MonthlyPeriodStartsAt.Equal(anchor) || !period.MonthlyPeriodEndsAt.Equal(periodEnd) {
			t.Fatalf("month-end period = [%s,%s), want [%s,%s)",
				period.MonthlyPeriodStartsAt, period.MonthlyPeriodEndsAt, anchor, periodEnd)
		}
		if period.SubscriptionExpiresAt == nil || !period.SubscriptionExpiresAt.Equal(periodEnd) {
			t.Fatalf("subscription_expires_at=%v, want %s", period.SubscriptionExpiresAt, periodEnd)
		}
		if period.NextRefreshAt != nil {
			t.Fatalf("nothing covers the next period, so next_refresh_at must be absent, got %s", period.NextRefreshAt)
		}

		// 提前续费：新的一段 2/28 → 3/31 才开始，当前窗口和到期时间都不能被它改写。
		insertCreditMonthlyTestEntitlement(t, db, uid, planID, "order", "ORDER-PERIOD-2", periodEnd, nextPeriodEnd, anchor)
		renewed := mustModelCreditPeriod(t, db, ctx, uid, time.Date(2027, time.February, 10, 0, 0, 0, 0, time.UTC))
		if !renewed.MonthlyPeriodStartsAt.Equal(anchor) || !renewed.MonthlyPeriodEndsAt.Equal(periodEnd) {
			t.Fatalf("a future renewal changed the current period: [%s,%s)", renewed.MonthlyPeriodStartsAt, renewed.MonthlyPeriodEndsAt)
		}
		if renewed.SubscriptionExpiresAt == nil || !renewed.SubscriptionExpiresAt.Equal(periodEnd) {
			t.Fatalf("a future renewal was reported as the current subscription end: %v, want %s", renewed.SubscriptionExpiresAt, periodEnd)
		}
		if renewed.NextRefreshAt == nil || !renewed.NextRefreshAt.Equal(periodEnd) {
			t.Fatalf("next_refresh_at=%v, want the paid boundary %s", renewed.NextRefreshAt, periodEnd)
		}

		// 走到新周期：窗口才换成新的一段，而它的下一个边界又没人付钱了。
		next := mustModelCreditPeriod(t, db, ctx, uid, periodEnd.Add(time.Hour))
		if !next.MonthlyPeriodStartsAt.Equal(periodEnd) || !next.MonthlyPeriodEndsAt.Equal(nextPeriodEnd) {
			t.Fatalf("renewed period = [%s,%s), want [%s,%s)",
				next.MonthlyPeriodStartsAt, next.MonthlyPeriodEndsAt, periodEnd, nextPeriodEnd)
		}
		if next.SubscriptionExpiresAt == nil || !next.SubscriptionExpiresAt.Equal(nextPeriodEnd) {
			t.Fatalf("renewed subscription_expires_at=%v, want %s", next.SubscriptionExpiresAt, nextPeriodEnd)
		}
		if next.NextRefreshAt != nil {
			t.Fatalf("the renewed period ends at its own boundary, so no further refresh is scheduled: %s", next.NextRefreshAt)
		}
	})

	// 一条覆盖多期的订阅：当前窗口仍是本月，但下个边界确实已经付过，可以给刷新点。
	t.Run("a paid window longer than one period covers the next refresh", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-period-longwindow")
		anchor := time.Date(2027, time.January, 31, 10, 0, 0, 0, time.UTC)
		periodEnd := time.Date(2027, time.February, 28, 10, 0, 0, 0, time.UTC)
		subscriptionExpires := time.Date(2027, time.March, 31, 10, 0, 0, 0, time.UTC)
		insertCreditMonthlyTestEntitlement(t, db, uid, planID, "order", "ORDER-PERIOD-LONG", anchor, subscriptionExpires, anchor)

		period := mustModelCreditPeriod(t, db, ctx, uid, time.Date(2027, time.February, 10, 0, 0, 0, 0, time.UTC))
		if !period.MonthlyPeriodStartsAt.Equal(anchor) || !period.MonthlyPeriodEndsAt.Equal(periodEnd) {
			t.Fatalf("period = [%s,%s), want [%s,%s)",
				period.MonthlyPeriodStartsAt, period.MonthlyPeriodEndsAt, anchor, periodEnd)
		}
		if period.SubscriptionExpiresAt == nil || !period.SubscriptionExpiresAt.Equal(subscriptionExpires) {
			t.Fatalf("subscription_expires_at=%v, want %s", period.SubscriptionExpiresAt, subscriptionExpires)
		}
		if period.NextRefreshAt == nil || !period.NextRefreshAt.Equal(periodEnd) {
			t.Fatalf("next_refresh_at=%v, want %s", period.NextRefreshAt, periodEnd)
		}
	})

	// 邀请/免费/legacy：没有月订阅就不给周期，更不能编造刷新时间。
	t.Run("invite and legacy accounts have no monthly period", func(t *testing.T) {
		inviteUID := createCreditMonthlyTestUser(t, db, "credit-period-invite")
		insertCreditMonthlyTestEntitlement(t, db, inviteUID, planID, "invite", "INVITE-PERIOD",
			time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC))
		now := time.Date(2027, time.February, 10, 0, 0, 0, 0, time.UTC)
		period, err := db.GetModelCreditPeriod(ctx, inviteUID, now)
		if err != nil {
			t.Fatalf("load invite account period: %v", err)
		}
		if period != nil {
			t.Fatalf("an invite subscription is not a monthly grant source, got period %#v", period)
		}

		legacyUID := createCreditMonthlyTestUser(t, db, "credit-period-legacy")
		if period, err := db.GetModelCreditPeriod(ctx, legacyUID, now); err != nil || period != nil {
			t.Fatalf("legacy account period=%#v err=%v, want none", period, err)
		}
	})

	// 真实支付路径：首购后本期结束 == 订阅到期，没有刷新点；提前续费只把刷新点
	// 定下来，不改当前窗口、不改到期时间。读取本身是只读的。
	t.Run("early renewal only schedules the next refresh", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-period-renew")
		createAndFulfillCommercialTestOrder(t, db, uid, planID, "CCPERIOD1", "cc_period_request_1", "cc-period-event-1")
		_, firstExpiry := creditSubscriptionEntitlementWindow(t, db, uid, "CCPERIOD1")

		var eventsBefore, eventsAfter int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid = $1`, uid).Scan(&eventsBefore); err != nil {
			t.Fatalf("count ledger events before period read: %v", err)
		}
		now := time.Now().UTC()
		period := mustModelCreditPeriod(t, db, ctx, uid, now)
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid = $1`, uid).Scan(&eventsAfter); err != nil {
			t.Fatalf("count ledger events after period read: %v", err)
		}
		if eventsBefore != eventsAfter {
			t.Fatalf("reading the period wrote %d ledger events, want none", eventsAfter-eventsBefore)
		}
		if !period.MonthlyPeriodEndsAt.Equal(firstExpiry) {
			t.Fatalf("first period ends at %s, subscription expires at %s", period.MonthlyPeriodEndsAt, firstExpiry)
		}
		if period.SubscriptionExpiresAt == nil || !period.SubscriptionExpiresAt.Equal(firstExpiry) {
			t.Fatalf("subscription_expires_at=%v, want %s", period.SubscriptionExpiresAt, firstExpiry)
		}
		if period.NextRefreshAt != nil {
			t.Fatalf("nobody paid for the next period, so next_refresh_at must be absent, got %s", period.NextRefreshAt)
		}

		createAndFulfillCommercialTestOrder(t, db, uid, planID, "CCPERIOD2", "cc_period_request_2", "cc-period-event-2")
		_, renewalExpiry := creditSubscriptionEntitlementWindow(t, db, uid, "CCPERIOD2")
		if !renewalExpiry.After(firstExpiry) {
			t.Fatalf("renewal did not extend the paid window: first=%s renewal=%s", firstExpiry, renewalExpiry)
		}
		renewed := mustModelCreditPeriod(t, db, ctx, uid, now)
		if !renewed.MonthlyPeriodStartsAt.Equal(period.MonthlyPeriodStartsAt) || !renewed.MonthlyPeriodEndsAt.Equal(period.MonthlyPeriodEndsAt) {
			t.Fatalf("early renewal changed the current period: [%s,%s) -> [%s,%s)",
				period.MonthlyPeriodStartsAt, period.MonthlyPeriodEndsAt,
				renewed.MonthlyPeriodStartsAt, renewed.MonthlyPeriodEndsAt)
		}
		if renewed.SubscriptionExpiresAt == nil || !renewed.SubscriptionExpiresAt.Equal(firstExpiry) {
			t.Fatalf("early renewal was reported as the current subscription end: %v, want %s", renewed.SubscriptionExpiresAt, firstExpiry)
		}
		if renewed.NextRefreshAt == nil || !renewed.NextRefreshAt.Equal(firstExpiry) {
			t.Fatalf("next_refresh_at=%v, want the paid boundary %s", renewed.NextRefreshAt, firstExpiry)
		}
	})
}

func mustModelCreditPeriod(t *testing.T, db *Adapter, ctx context.Context, uid int64, now time.Time) *store.ModelCreditPeriod {
	t.Helper()
	period, err := db.GetModelCreditPeriod(ctx, uid, now)
	if err != nil {
		t.Fatalf("load model credit period of uid %d: %v", uid, err)
	}
	if period == nil {
		t.Fatalf("uid %d has no monthly credit period, want one", uid)
	}
	return period
}

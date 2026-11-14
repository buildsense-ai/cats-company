package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 套餐支付只发当期月额度，不再 paid + monthly 双发（需要 CATS_PG_TEST_DSN）。
//
// 背景：FulfillCommercialOrder 曾经把 order.PlanModelCreditUnits 无条件记进 paid
// 桶，而 reconcileModelCreditMonthlyPeriod 又按订阅发 monthly，于是每一次新购都拿到
// 两份额度；844/845/846 全是月订阅，没有充值商品。这里钉死修复后的账本不变量：
//   - 首购只发 1× monthly，paid 恒为 0；
//   - 支付回调重放不重复记账；
//   - 提前续费只顺延有效期，不补额、不增加 paid；
//   - 下一个自然周期只发一次；
//   - 新订单退款不再要求不存在的 paid grant，撤销权益后按剩余权限对账；
//   - 旧「已 paid 入账」订单仍走原来的保守 reverse。
func TestPostgresCreditSubscriptionPaymentGrantsMonthlyOnly(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	const allowance = 4_000 * store.ModelCreditUnitsPerCredit
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             "credit-subscription-monthly",
		Name:             "V1 月订阅",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "public",
		DurationDays:     30,
		PlanTier:         1,
		ModelCreditUnits: allowance,
	})
	if err != nil {
		t.Fatalf("create credit subscription plan: %v", err)
	}

	t.Run("first purchase books one monthly period and no paid credit", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-sub-first")
		first := createAndFulfillCommercialTestOrder(t, db, uid, planID, "CCSUB0001", "cc_sub_request_0001", "cc-sub-event-0001")
		account := creditSubscriptionAccount(t, db, ctx, uid)
		if account.MonthlyUnits != allowance || account.PaidUnits != 0 {
			t.Fatalf("first credit purchase booked monthly=%d paid=%d, want monthly=%d paid=0 (the same units must not be granted twice)",
				account.MonthlyUnits, account.PaidUnits, allowance)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
			t.Fatalf("first credit purchase recorded %d monthly grants, want exactly 1", grants)
		}
		if paid := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketPaid, ""); paid != 0 {
			t.Fatalf("first credit purchase wrote %d paid ledger events, want 0", paid)
		}

		// 支付回调重放：同一事件、同一订单重复回调必须是空操作。
		replay := &types.CommercialPaymentConfirmation{
			Channel:         first.Channel,
			EventID:         "cc-sub-event-0001",
			ProviderTradeNo: first.ProviderTradeNo,
			AmountFen:       first.AmountFen,
			Currency:        first.Currency,
			PaidAt:          first.PaidAt.UTC(),
		}
		again, changed, err := db.FulfillCommercialOrder(first.OrderNo, replay)
		if err != nil || changed || again.Status != "fulfilled" {
			t.Fatalf("replayed payment callback was not idempotent: order=%#v changed=%v err=%v", again, changed, err)
		}
		// 读路径/后台对账的重复 reconcile 也不能补第二份。
		if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, time.Now().UTC()); err != nil {
			t.Fatalf("reconcile after first purchase: %v", err)
		}
		account = creditSubscriptionAccount(t, db, ctx, uid)
		if account.MonthlyUnits != allowance || account.PaidUnits != 0 {
			t.Fatalf("replaying the callback changed the balance: monthly=%d paid=%d", account.MonthlyUnits, account.PaidUnits)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
			t.Fatalf("replay booked another monthly grant: grants=%d", grants)
		}
	})

	t.Run("early renewal only extends the window", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-sub-renew")
		first := createAndFulfillCommercialTestOrder(t, db, uid, planID, "CCSUBRENEW1", "cc_sub_renew_request_1", "cc-sub-renew-event-1")
		firstStart, firstExpiry := creditSubscriptionEntitlementWindow(t, db, uid, first.OrderNo)
		renewal := createAndFulfillCommercialTestOrder(t, db, uid, planID, "CCSUBRENEW2", "cc_sub_renew_request_2", "cc-sub-renew-event-2")
		renewalStart, renewalExpiry := creditSubscriptionEntitlementWindow(t, db, uid, renewal.OrderNo)
		if !renewalStart.Equal(firstExpiry) || !renewalExpiry.After(firstExpiry) || !renewalStart.After(firstStart) {
			t.Fatalf("early renewal did not simply append a period: first=[%s,%s) renewal=[%s,%s)",
				firstStart.Format(time.RFC3339Nano), firstExpiry.Format(time.RFC3339Nano),
				renewalStart.Format(time.RFC3339Nano), renewalExpiry.Format(time.RFC3339Nano))
		}
		account := creditSubscriptionAccount(t, db, ctx, uid)
		if account.MonthlyUnits != allowance || account.PaidUnits != 0 {
			t.Fatalf("early renewal topped up the balance: monthly=%d paid=%d, want monthly=%d paid=0",
				account.MonthlyUnits, account.PaidUnits, allowance)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 1 {
			t.Fatalf("early renewal granted the next period immediately: monthly grants=%d, want 1", grants)
		}

		// 下一个自然周期开始时只发一次：旧周期余额作废，新周期发一份。
		if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, renewalStart.Add(time.Hour)); err != nil {
			t.Fatalf("reconcile the renewed period: %v", err)
		}
		account = creditSubscriptionAccount(t, db, ctx, uid)
		if account.MonthlyUnits != allowance || account.PaidUnits != 0 {
			t.Fatalf("renewed period balance monthly=%d paid=%d, want monthly=%d paid=0",
				account.MonthlyUnits, account.PaidUnits, allowance)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 2 {
			t.Fatalf("renewed period monthly grants=%d, want exactly one per period (2)", grants)
		}
		if expires := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventExpire); expires != 1 {
			t.Fatalf("previous period allowance was not expired exactly once: expiries=%d", expires)
		}
		if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, renewalStart.Add(2*time.Hour)); err != nil {
			t.Fatalf("repeat reconcile of the renewed period: %v", err)
		}
		if grants := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); grants != 2 {
			t.Fatalf("repeat reconcile granted the same period twice: monthly grants=%d", grants)
		}
	})

	t.Run("refunding a monthly order does not demand a paid grant", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-sub-refund")
		order := createAndFulfillCommercialTestOrder(t, db, uid, planID, "CCSUBREFUND", "cc_sub_refund_request", "cc-sub-refund-event")
		if account := creditSubscriptionAccount(t, db, ctx, uid); account.MonthlyUnits != allowance || account.PaidUnits != 0 {
			t.Fatalf("refund fixture balance monthly=%d paid=%d", account.MonthlyUnits, account.PaidUnits)
		}
		refundRequestNo := "CCRF-" + order.OrderNo
		if _, claimed, err := db.BeginCommercialOrderRefund(order.OrderNo, refundRequestNo, time.Minute); err != nil || !claimed {
			t.Fatalf("claim refund: claimed=%v err=%v", claimed, err)
		}
		refunded, changed, err := db.CompleteCommercialOrderRefund(order.OrderNo, &types.CommercialRefundConfirmation{
			Channel:         order.Channel,
			EventID:         refundRequestNo,
			ProviderTradeNo: order.ProviderTradeNo,
			RefundRequestNo: refundRequestNo,
			AmountFen:       order.AmountFen,
			Currency:        order.Currency,
			RefundedAt:      time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("refund of a monthly subscription order must not look for a paid grant: %v", err)
		}
		if !changed || refunded.Status != "refunded" || refunded.RefundedAt == nil {
			t.Fatalf("refund did not complete: order=%#v changed=%v", refunded, changed)
		}
		// 权益撤销，当期未用额度作废，且没有凭空写出的 paid 反向事件。
		var state string
		if err := db.db.QueryRow(`SELECT state FROM commercial_entitlements WHERE uid = $1 AND source = 'order' AND source_ref = $2`,
			uid, order.OrderNo).Scan(&state); err != nil || state != "revoked" {
			t.Fatalf("refunded entitlement state=%q err=%v, want revoked", state, err)
		}
		account := creditSubscriptionAccount(t, db, ctx, uid)
		if account.MonthlyUnits != 0 || account.PaidUnits != 0 {
			t.Fatalf("refunded order kept credit: monthly=%d paid=%d, want 0/0", account.MonthlyUnits, account.PaidUnits)
		}
		if paidRefunds := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketPaid, store.ModelCreditEventRefund); paidRefunds != 0 {
			t.Fatalf("refund invented a paid reversal for an order that never booked paid credit: events=%d", paidRefunds)
		}
		if expiries := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventExpire); expiries != 1 {
			t.Fatalf("refunded period allowance was not expired exactly once: expiries=%d", expiries)
		}
	})

	t.Run("legacy paid order keeps the conservative reversal", func(t *testing.T) {
		uid := createCreditMonthlyTestUser(t, db, "credit-sub-legacy")
		expiresAt := time.Now().UTC().Add(20 * time.Minute)
		legacy, err := db.CreateCommercialOrder(&types.CommercialOrder{
			OrderNo: "CCSUBLEGACY", UID: uid, PlanID: planID, Channel: "test",
			ClientRequestID: "cc_sub_legacy_request", ExpiresAt: &expiresAt,
		})
		if err != nil {
			t.Fatalf("create legacy order: %v", err)
		}
		// 旧履约镜像：权益 + paid 入账（就是修复前 grantCommercialOrderModelCredit
		// 写进账本的那一行），没有 monthly 发放。
		paidAt := time.Now().UTC().Truncate(time.Second)
		if _, err := db.db.Exec(`
			INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at, period_anchor_at)
			VALUES ($1, $2, 'order', $3, 'active', $4, $5, $4)`,
			uid, planID, legacy.OrderNo, paidAt, paidAt.AddDate(0, 1, 0)); err != nil {
			t.Fatalf("seed legacy entitlement: %v", err)
		}
		if _, err := db.db.Exec(`
			UPDATE commercial_orders SET status = 'fulfilled', paid_at = $2, fulfilled_at = CURRENT_TIMESTAMP
			WHERE order_no = $1`, legacy.OrderNo, paidAt); err != nil {
			t.Fatalf("mark legacy order fulfilled: %v", err)
		}
		if _, err := db.GrantModelCredits(ctx, store.ModelCreditGrantInput{
			UID: uid, Bucket: store.ModelCreditBucketPaid, Units: allowance,
			IdempotencyKey: "order:" + legacy.OrderNo,
		}); err != nil {
			t.Fatalf("seed legacy paid credit: %v", err)
		}
		if account := creditSubscriptionAccount(t, db, ctx, uid); account.PaidUnits != allowance || account.MonthlyUnits != 0 {
			t.Fatalf("legacy fixture balance monthly=%d paid=%d", account.MonthlyUnits, account.PaidUnits)
		}
		refundRequestNo := "CCRF-" + legacy.OrderNo
		if _, claimed, err := db.BeginCommercialOrderRefund(legacy.OrderNo, refundRequestNo, time.Minute); err != nil || !claimed {
			t.Fatalf("claim legacy refund: claimed=%v err=%v", claimed, err)
		}
		refunded, changed, err := db.CompleteCommercialOrderRefund(legacy.OrderNo, &types.CommercialRefundConfirmation{
			Channel:         legacy.Channel,
			EventID:         refundRequestNo,
			ProviderTradeNo: "trade-legacy-" + legacy.OrderNo,
			RefundRequestNo: refundRequestNo,
			AmountFen:       legacy.AmountFen,
			Currency:        legacy.Currency,
			RefundedAt:      time.Now().UTC(),
		})
		if err != nil || !changed || refunded.Status != "refunded" {
			t.Fatalf("legacy paid refund: order=%#v changed=%v err=%v", refunded, changed, err)
		}
		account := creditSubscriptionAccount(t, db, ctx, uid)
		if account.PaidUnits != 0 || account.MonthlyUnits != 0 {
			t.Fatalf("legacy refund did not take the paid credit back: monthly=%d paid=%d", account.MonthlyUnits, account.PaidUnits)
		}
		if refunds := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketPaid, store.ModelCreditEventRefund); refunds != 1 {
			t.Fatalf("legacy refund reversal events=%d, want the original conservative reversal (1)", refunds)
		}
		// 旧订单不会被当成月订阅退款去作废 monthly（这条路径它从来没有过）。
		if monthly := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, ""); monthly != 0 {
			t.Fatalf("legacy refund touched the monthly bucket: events=%d", monthly)
		}
	})
}

func creditSubscriptionAccount(t *testing.T, db *Adapter, ctx context.Context, uid int64) *store.ModelCreditAccount {
	t.Helper()
	account, err := db.GetModelCreditAccount(ctx, uid)
	if err != nil {
		t.Fatalf("load model credit account of uid %d: %v", uid, err)
	}
	if account == nil {
		t.Fatalf("uid %d has no model credit account", uid)
	}
	return account
}

// creditSubscriptionEventCount 按桶/事件类型数账本行，eventType 为空表示不限。
func creditSubscriptionEventCount(t *testing.T, db *Adapter, uid int64, bucket, eventType string) int {
	t.Helper()
	var count int
	if err := db.db.QueryRow(`
		SELECT COUNT(*) FROM model_credit_ledger_events
		WHERE uid = $1 AND bucket = $2 AND ($3 = '' OR event_type = $3)`, uid, bucket, eventType).Scan(&count); err != nil {
		t.Fatalf("count %s/%s model credit events of uid %d: %v", bucket, eventType, uid, err)
	}
	return count
}

func creditSubscriptionEntitlementWindow(t *testing.T, db *Adapter, uid int64, orderNo string) (time.Time, time.Time) {
	t.Helper()
	var startsAt, expiresAt time.Time
	if err := db.db.QueryRow(`
		SELECT starts_at, expires_at FROM commercial_entitlements
		WHERE uid = $1 AND source = 'order' AND source_ref = $2`, uid, orderNo).Scan(&startsAt, &expiresAt); err != nil {
		t.Fatalf("load entitlement window of order %s: %v", orderNo, err)
	}
	return startsAt.UTC(), expiresAt.UTC()
}

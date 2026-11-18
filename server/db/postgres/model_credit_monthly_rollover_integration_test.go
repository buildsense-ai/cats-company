package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const rolloverAllowance = 4_000 * store.ModelCreditUnitsPerCredit

func TestPostgresModelCreditInviteThenPaidRollover(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	uid := createCreditMonthlyTestUser(t, db, "invite-paid-rollover")
	planID := seedInviteTermPlan(t, db, "invite-paid-rollover", rolloverAllowance, 1)
	mintInvite(t, db, "INVITE-PAID-ROLLOVER", planID, 20*store.ModelCreditUnitsPerCredit, false)
	if _, err := db.RedeemCommercialInvite(uid, "INVITE-PAID-ROLLOVER"); err != nil {
		t.Fatal(err)
	}
	_, _, inviteEnd, _ := readInviteSnapshotEntitlement(t, db, uid)
	modelCreditTestTask(t, db, uid, "invite-paid-task")
	const spent = 2_500 * store.ModelCreditUnitsPerCredit
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "invite-spent", RootTaskID: "invite-paid-task", Units: spent}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "invite-spent", ActualUnits: spent}); err != nil {
		t.Fatal(err)
	}
	order := createAndFulfillCommercialTestOrder(t, db, uid, planID, "ROLLOVERINVPAID", "invite-paid-order", "invite-paid-payment")
	start, end := creditSubscriptionEntitlementWindow(t, db, uid, order.OrderNo)
	if !inviteEnd.Valid || !start.Equal(inviteEnd.Time) || !end.Equal(store.ModelCreditMonthlyBoundary(start, 1)) {
		t.Fatalf("paid renewal must append a full month to the invite: invite end=%v, paid window=[%v,%v)", inviteEnd, start, end)
	}
	if account := creditSubscriptionAccount(t, db, ctx, uid); account.MonthlyUnits != rolloverAllowance-spent || account.GiftUnits != 20*store.ModelCreditUnitsPerCredit || account.PaidUnits != 0 {
		t.Fatalf("early payment must not issue the second allowance: %+v", account)
	}
	for i := 0; i < 2; i++ {
		account, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, start.Add(time.Hour))
		if err != nil || account.MonthlyUnits != 5_500*store.ModelCreditUnitsPerCredit || account.GiftUnits != 20*store.ModelCreditUnitsPerCredit || account.FrozenMonthlyUnits != 0 {
			t.Fatalf("rollover = 1500 + 4000 independently of gift: %+v, %v", account, err)
		}
	}
	if n := creditSubscriptionEventCount(t, db, uid, store.ModelCreditBucketMonthly, store.ModelCreditEventGrant); n != 2 {
		t.Fatalf("got %d first/renewal grants; want 2", n)
	}
}

func TestPostgresModelCreditRefundRevokesOnlyOrderLots(t *testing.T) {
	db := openCreditMonthlyTestDB(t)
	ctx := context.Background()
	uid := createCreditMonthlyTestUser(t, db, "refund-rollover-lots")
	planID := seedInviteTermPlan(t, db, "refund-rollover-lots", rolloverAllowance, 1)
	first := createAndFulfillCommercialTestOrder(t, db, uid, planID, "ROLLOVERREFUND1", "refund-order-one", "refund-payment-one")
	second := createAndFulfillCommercialTestOrder(t, db, uid, planID, "ROLLOVERREFUND2", "refund-order-two", "refund-payment-two")
	secondStart, secondEnd := creditSubscriptionEntitlementWindow(t, db, uid, second.OrderNo)
	if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, secondStart); err != nil {
		t.Fatal(err)
	}
	modelCreditTestTask(t, db, uid, "refund-lots-task")
	const fromSecond = 500 * store.ModelCreditUnitsPerCredit
	reserved, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "rollover-refund-hold", RootTaskID: "refund-lots-task", Units: rolloverAllowance + fromSecond})
	if err != nil || reserved.Split.Monthly != rolloverAllowance+fromSecond {
		t.Fatalf("reserve across order lots: %+v, %v", reserved, err)
	}
	if _, err := db.ReconcileModelCreditMonthlyPeriod(ctx, uid, secondEnd); err != nil {
		t.Fatal(err)
	}
	refundNo := "CCRF-" + first.OrderNo
	if _, changed, err := db.BeginCommercialOrderRefund(first.OrderNo, refundNo, time.Minute); err != nil || !changed {
		t.Fatalf("start refund: changed=%v, %v", changed, err)
	}
	if _, changed, err := db.CompleteCommercialOrderRefund(first.OrderNo, &types.CommercialRefundConfirmation{
		Channel: first.Channel, EventID: refundNo, RefundRequestNo: refundNo, ProviderTradeNo: first.ProviderTradeNo,
		AmountFen: first.AmountFen, Currency: first.Currency, RefundedAt: secondEnd,
	}); err != nil || !changed {
		t.Fatalf("finish refund: changed=%v, %v", changed, err)
	}
	if account := creditSubscriptionAccount(t, db, ctx, uid); account.FrozenMonthlyUnits != rolloverAllowance-fromSecond || account.MonthlyUnits != 0 {
		t.Fatalf("refund must preserve the unrelated frozen lot: %+v", account)
	}
	// Move the fixture windows into the past for the real-time settle path.
	if _, err := db.db.Exec(`UPDATE commercial_entitlements SET starts_at = CURRENT_TIMESTAMP - interval '2 months',
		expires_at = CURRENT_TIMESTAMP - interval '1 hour' WHERE uid = $1`, uid); err != nil {
		t.Fatal(err)
	}
	_, settled, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "rollover-refund-hold", ActualUnits: 100 * store.ModelCreditUnitsPerCredit})
	if err != nil || settled.MonthlyUnits != 0 || settled.FrozenMonthlyUnits != rolloverAllowance || settled.DebtUnits != 0 {
		t.Fatalf("settlement must discard unused refunded credit and return the other lot frozen: %+v, %v", settled, err)
	}
}

func TestPostgresModelCreditLegacyMonthlyRollover(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		hold                  int64
		refund, expire, spend bool
		delayed               bool
		want                  int64
	}{
		{name: "refunded-hold", hold: rolloverAllowance, refund: true},
		{name: "expired-idle-not-hold", hold: rolloverAllowance / 2, expire: true, want: rolloverAllowance / 4},
		{name: "spent-order-keeps-manual-grant", hold: rolloverAllowance, refund: true, spend: true, want: 90},
		{name: "delayed-grant-refund", refund: true, delayed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openCreditMonthlyTestDB(t)
			ctx := context.Background()
			uid := createCreditMonthlyTestUser(t, db, tc.name)
			planID := seedInviteTermPlan(t, db, tc.name, rolloverAllowance, 1)
			order := createAndFulfillCommercialTestOrder(t, db, uid, planID, "LEGACYROLL", "legacy-order", "legacy-paid")
			start, end := creditSubscriptionEntitlementWindow(t, db, uid, order.OrderNo)
			grantKey := store.ModelCreditMonthlyGrantKey(uid, planID, start)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			if tc.hold > 0 {
				modelCreditTestTask(t, db, uid, "legacy-task")
				if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
					UID: uid, RequestID: "legacy-hold", RootTaskID: "legacy-task", Units: tc.hold,
				}); err != nil {
					t.Fatal(err)
				}
			}
			settle := func(units int64) *store.ModelCreditAccount {
				t.Helper()
				_, account, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{
					UID: uid, RequestID: "legacy-hold", ActualUnits: units,
				})
				if err != nil {
					t.Fatal(err)
				}
				return account
			}
			if tc.spend {
				settle(tc.hold)
				if _, err := db.GrantModelCredits(ctx, store.ModelCreditGrantInput{
					UID: uid, Bucket: store.ModelCreditBucketMonthly, Units: tc.want, IdempotencyKey: "manual-after-order",
				}); err != nil {
					t.Fatal(err)
				}
			}
			exec(`DELETE FROM model_credit_monthly_hold_lots WHERE uid = $1`, uid)
			exec(`DELETE FROM model_credit_monthly_lots WHERE uid = $1`, uid)
			if tc.delayed {
				exec(`UPDATE model_credit_ledger_events SET created_at = $2 WHERE event_key = $1`, grantKey, end.Add(time.Minute))
			}
			if tc.expire {
				// Replicate the old expiry of idle units while the hold was in flight.
				exec(`UPDATE commercial_entitlements SET expires_at = $2 WHERE uid = $1`, uid, start.Add(time.Microsecond))
				exec(`UPDATE model_credit_accounts SET monthly_units = 0 WHERE uid = $1`, uid)
				exec(`INSERT INTO model_credit_ledger_events (event_key, uid, bucket, event_type, units,
					monthly_balance, gift_balance, paid_balance, debt_balance)
					VALUES ($1, $2, 'monthly', 'expire', $3, 0, 0, 0, 0)`,
					store.ModelCreditMonthlyExpireKey(uid, start), uid, rolloverAllowance-tc.hold)
			}
			if tc.refund {
				refundNo := "CCRF-" + order.OrderNo
				if _, changed, err := db.BeginCommercialOrderRefund(order.OrderNo, refundNo, time.Minute); err != nil || !changed {
					t.Fatalf("start refund: changed=%v, %v", changed, err)
				}
				if _, changed, err := db.CompleteCommercialOrderRefund(order.OrderNo, &types.CommercialRefundConfirmation{
					Channel: order.Channel, EventID: refundNo, RefundRequestNo: refundNo, ProviderTradeNo: order.ProviderTradeNo,
					AmountFen: order.AmountFen, Currency: order.Currency, RefundedAt: end.Add(2 * time.Minute),
				}); err != nil || !changed {
					t.Fatalf("finish refund: changed=%v, %v", changed, err)
				}
				if units, revoked, found := machineUpgradeLotState(t, db, grantKey); !found || !revoked || units != 0 {
					t.Fatalf("refunded order lot = %d, revoked=%v found=%v", units, revoked, found)
				}
			}
			if tc.hold > 0 && !tc.spend {
				if tc.expire {
					for i := 0; i < 2; i++ {
						if got := settle(tc.hold / 2); got.MonthlyUnits != 0 || got.FrozenMonthlyUnits != tc.want {
							t.Fatalf("settle legacy hold: %+v", got)
						}
					}
				} else {
					if _, err := db.MarkModelCreditNoUsage(ctx, store.ModelCreditNoUsageConfirmation{UID: uid, RequestID: "legacy-hold"}); err != nil {
						t.Fatal(err)
					}
					for i := 0; i < 2; i++ {
						if _, _, err := db.ReleaseModelCredits(ctx, store.ModelCreditReleaseInput{UID: uid, RequestID: "legacy-hold"}); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if got := creditSubscriptionAccount(t, db, ctx, uid); got.MonthlyUnits != 0 || got.FrozenMonthlyUnits != tc.want || got.DebtUnits != 0 {
				t.Fatalf("legacy reconciliation: %+v, want %d frozen", got, tc.want)
			}
		})
	}
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 退款安全的真实 PostgreSQL 回归（需要 CATS_PG_TEST_DSN，且必须指向测试库）。
//
// 修复前的缺陷：先调用渠道商退款，再撤回点数；撤回失败时把订单恢复成
// fulfilled，于是现金已退、点数还在。这里验证的顺序是反过来的：
//   - 认领订单与冻结可退点数在同一事务（订单行锁 + 账户行锁）里完成；
//   - 已消费、未决模型调用预占、机器升配预占、充值抵欠款一律在调用前拒绝；
//   - 冻结之后别的消费不影响退款完成；
//   - 重复 / stale / 并发认领都只冻结一次；
//   - 渠道结果未知时保持 refunding + hold，绝不恢复 fulfilled；
//   - 完成失败可以重试，且不会二次扣点；
//   - 旧版遗留的 refunding（无 hold）fail closed；
//   - migration down 在有历史时拒绝删表。

func newCommercialRefundSafetyAdapter(t *testing.T) *Adapter {
	t.Helper()
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_refund_safety_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	t.Cleanup(func() { base.Close() })
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create refund safety test schema: %v", err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`) })
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open refund safety schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create refund safety schema objects: %v", err)
	}
	// CreateSchema carries the hold table once it is registered in the schema
	// list; applying the constant as well proves it is idempotent either way.
	if _, err := db.db.Exec(createCommercialRefundHolds); err != nil {
		t.Fatalf("apply commercial refund hold table: %v", err)
	}
	testPostgresMigrationFiles(t, db)
	return db
}

// fulfilledRefundTestOrder buys paid credit with real money and fulfils it, which
// is the state a refund starts from.
func fulfilledRefundTestOrder(t *testing.T, db *Adapter, uid int64, requestID string, amountFen int64) *types.CommercialOrder {
	t.Helper()
	order, err := db.CreateCommercialTopupOrder(uid, amountFen, "alipay_page", requestID, nil)
	if err != nil {
		t.Fatalf("create refund test order: %v", err)
	}
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: "alipay_page", EventID: requestID + "-paid", ProviderTradeNo: "trade-" + requestID,
		AmountFen: amountFen, Currency: "CNY", PaidAt: time.Now().UTC(),
		PayloadHash: strings.Repeat("a", 64),
	}
	fulfilled, changed, err := db.FulfillCommercialOrder(order.OrderNo, confirmation)
	if err != nil || !changed || fulfilled.Status != "fulfilled" {
		t.Fatalf("fulfil refund test order: changed=%v order=%#v err=%v", changed, fulfilled, err)
	}
	return fulfilled
}

func refundTestConfirmation(order *types.CommercialOrder, requestNo, eventID string) *types.CommercialRefundConfirmation {
	return &types.CommercialRefundConfirmation{
		Channel: order.Channel, EventID: eventID, ProviderTradeNo: order.ProviderTradeNo,
		RefundRequestNo: requestNo, AmountFen: order.AmountFen, Currency: order.Currency,
		RefundedAt: time.Now().UTC(), PayloadHash: strings.Repeat("b", 64),
	}
}

func refundTestBalance(t *testing.T, db *Adapter, uid int64) store.ModelCreditAccount {
	t.Helper()
	account, err := db.GetModelCreditAccount(context.Background(), uid)
	if err != nil {
		t.Fatalf("read credit account %d: %v", uid, err)
	}
	return *account
}

func countRefundTestRows(t *testing.T, db *Adapter, query string, args ...interface{}) int {
	t.Helper()
	var count int
	if err := db.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows for %q: %v", query, err)
	}
	return count
}

// insertRefundTestLedgerEvent writes a ledger row the way another subsystem
// would have written it (a settle, a reserve, an arrears repayment).
func insertRefundTestLedgerEvent(t *testing.T, db *Adapter, key string, uid int64, bucket, eventType string, units int64, requestID *string) {
	t.Helper()
	account := refundTestBalance(t, db, uid)
	if _, err := db.db.Exec(`INSERT INTO model_credit_ledger_events
		(event_key, uid, bucket, event_type, units, request_id,
		 monthly_balance, gift_balance, paid_balance, debt_balance)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		key, uid, bucket, eventType, units, requestID,
		account.MonthlyUnits, account.GiftUnits, account.PaidUnits, account.DebtUnits); err != nil {
		t.Fatalf("insert ledger event %s: %v", key, err)
	}
}

// ageRefundClaim backdates commercial_orders.updated_at the way a crashed process
// would leave a stale claim. The updated_at trigger would overwrite the value, so
// it is suspended for the write.
func ageRefundClaim(t *testing.T, db *Adapter, orderNo string) {
	t.Helper()
	if _, err := db.db.Exec(`ALTER TABLE commercial_orders DISABLE TRIGGER trg_commercial_orders_updated_at`); err != nil {
		t.Fatalf("disable commercial order trigger: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE commercial_orders SET updated_at = CURRENT_TIMESTAMP - INTERVAL '10 minutes' WHERE order_no = $1`, orderNo); err != nil {
		t.Fatalf("age the refund claim: %v", err)
	}
	if _, err := db.db.Exec(`ALTER TABLE commercial_orders ENABLE TRIGGER trg_commercial_orders_updated_at`); err != nil {
		t.Fatalf("re-enable commercial order trigger: %v", err)
	}
}

// assertClaimRefused proves a refused refund never froze credit and never booked
// a reversal: no hold row, no refund event, order still 'fulfilled'.
func assertClaimRefused(t *testing.T, db *Adapter, orderNo string) {
	t.Helper()
	order, err := db.GetCommercialOrder(0, orderNo)
	if err != nil || order == nil {
		t.Fatalf("reload refused order %s: %#v %v", orderNo, order, err)
	}
	if order.Status != "fulfilled" {
		t.Fatalf("refused order %s changed status to %q", orderNo, order.Status)
	}
	if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_refund_holds WHERE order_no = $1`, orderNo); got != 0 {
		t.Fatalf("refused order %s wrote %d hold row(s)", orderNo, got)
	}
	if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM model_credit_ledger_events WHERE event_key = $1`,
		commercialOrderCreditRefundKey(orderNo)); got != 0 {
		t.Fatalf("refused order %s booked a refund event", orderNo)
	}
}

func TestPostgresCommercialRefundSafety(t *testing.T) {
	db := newCommercialRefundSafetyAdapter(t)

	t.Run("refuses spent paid credit before the claim", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-spent")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-spent-0001", 5)
		settleRequest := "settle-spent-0001"
		insertRefundTestLedgerEvent(t, db, "settle:"+settleRequest, uid, store.ModelCreditBucketPaid,
			store.ModelCreditEventSettle, 1, &settleRequest)
		before := refundTestBalance(t, db, uid)

		if _, _, err := db.BeginCommercialOrderRefund(order.OrderNo, commercialRefundRequestNoOf(order.OrderNo), time.Minute); err == nil {
			t.Fatal("a refund of already consumed paid credit was claimed")
		} else if !errors.Is(err, ErrCommercialOrderCreditSpent) {
			t.Fatalf("refusal reason=%v, want spent credit", err)
		}
		assertClaimRefused(t, db, order.OrderNo)
		if after := refundTestBalance(t, db, uid); after.PaidUnits != before.PaidUnits {
			t.Fatalf("a refused claim moved paid units from %d to %d", before.PaidUnits, after.PaidUnits)
		}
	})

	t.Run("refuses an open model call reservation", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-reserved")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-reserved-0001", 5)
		if _, err := db.db.Exec(`INSERT INTO model_credit_reservations
			(uid, request_id, root_task_id, reserved_units, monthly_units, gift_units, paid_units,
			 unfunded_units, state, created_at, lease_expires_at)
			VALUES ($1, $2, NULL, 1, 0, 0, 1, 0, 'reserved', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 minutes')`,
			uid, "reserve-open-0001"); err != nil {
			t.Fatalf("seed open paid reservation: %v", err)
		}
		if _, _, err := db.BeginCommercialOrderRefund(order.OrderNo, commercialRefundRequestNoOf(order.OrderNo), time.Minute); err == nil {
			t.Fatal("a refund while a model call still holds paid credit was claimed")
		} else if !errors.Is(err, ErrCommercialOrderCreditSpent) {
			t.Fatalf("refusal reason=%v, want spent credit", err)
		}
		assertClaimRefused(t, db, order.OrderNo)
	})

	t.Run("refuses an open machine upgrade hold", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-upgrade")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-upgrade-0001", 5)
		operationID := "mu-refund-safety"
		if _, err := db.db.Exec(`INSERT INTO cloud_worker_machine_upgrades
			(operation_id, uid, tenant_name, lifecycle_id, from_plan, to_plan, cost_units, status,
			 held, settled, client_request_id, quote_id, attempts, machine_expires_at, message)
			VALUES ($1, $2, 'bot-refund-safety', 0, 'light', 'standard', 1, 'running',
			        TRUE, FALSE, 'req-refund-safety', '', 0, CURRENT_TIMESTAMP + INTERVAL '1 day', '')`,
			operationID, uid); err != nil {
			t.Fatalf("seed open machine upgrade: %v", err)
		}
		insertRefundTestLedgerEvent(t, db,
			store.ModelCreditEventKey(store.ModelCreditEventReserve, uid, operationID, store.ModelCreditBucketPaid),
			uid, store.ModelCreditBucketPaid, store.ModelCreditEventReserve, 1, &operationID)

		if _, _, err := db.BeginCommercialOrderRefund(order.OrderNo, commercialRefundRequestNoOf(order.OrderNo), time.Minute); err == nil {
			t.Fatal("a refund while a machine upgrade still holds paid credit was claimed")
		} else if !errors.Is(err, ErrCommercialOrderCreditSpent) {
			t.Fatalf("refusal reason=%v, want spent credit", err)
		}
		assertClaimRefused(t, db, order.OrderNo)
	})

	t.Run("refuses a top-up that repaid arrears", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-arrears")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-arrears-0001", 5)
		insertRefundTestLedgerEvent(t, db, commercialOrderArrearsRepaymentKey(order.OrderNo), uid,
			store.ModelCreditBucketDebt, store.ModelCreditEventDebtRepaid, 1, nil)

		if _, _, err := db.BeginCommercialOrderRefund(order.OrderNo, commercialRefundRequestNoOf(order.OrderNo), time.Minute); err == nil {
			t.Fatal("a refund of a top-up that already repaid arrears was claimed")
		} else if !errors.Is(err, ErrCommercialOrderCreditSpent) {
			t.Fatalf("refusal reason=%v, want spent credit", err)
		}
		assertClaimRefused(t, db, order.OrderNo)
	})

	t.Run("freezes credit and completes after other consumption", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-freeze")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-freeze-0001", 5)
		units := order.PlanModelCreditUnits
		if units <= 0 {
			t.Fatalf("test order sold %d paid units", units)
		}
		requestNo := commercialRefundRequestNoOf(order.OrderNo)
		claimed, ok, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute)
		if err != nil || !ok || claimed.Status != "refunding" {
			t.Fatalf("claim refund: claimed=%v order=%#v err=%v", ok, claimed, err)
		}
		// The freeze really left the spendable bucket, and it is recorded twice:
		// once as the hold row, once as a paid reserve in the ledger.
		if got := refundTestBalance(t, db, uid).PaidUnits; got != 0 {
			t.Fatalf("paid bucket after the freeze=%d, want 0", got)
		}
		var holdUnits int64
		if err := db.db.QueryRow(`SELECT units FROM commercial_refund_holds WHERE order_no = $1 AND state = 'held'`,
			order.OrderNo).Scan(&holdUnits); err != nil {
			t.Fatalf("read refund hold: %v", err)
		}
		if holdUnits != units {
			t.Fatalf("hold units=%d, want %d", holdUnits, units)
		}
		reserveKey := store.ModelCreditEventKey(store.ModelCreditEventReserve, uid, requestNo, store.ModelCreditBucketPaid)
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM model_credit_ledger_events
			WHERE event_key = $1 AND event_type = 'reserve' AND units = $2`, reserveKey, units); got != 1 {
			t.Fatalf("frozen credit has %d reserve event(s), want 1", got)
		}

		// Another consumer spends credit while the channel call is in flight. The
		// old completion refused to finish here and left the order refunding with
		// the cash already gone; the hold makes this harmless.
		otherRequest := "settle-during-0001"
		insertRefundTestLedgerEvent(t, db, "settle:"+otherRequest, uid, store.ModelCreditBucketPaid,
			store.ModelCreditEventSettle, 1, &otherRequest)

		refunded, changed, err := db.CompleteCommercialOrderRefund(order.OrderNo,
			refundTestConfirmation(order, requestNo, "refund-freeze-event-0001"))
		if err != nil || !changed || refunded.Status != "refunded" {
			t.Fatalf("complete refund: changed=%v order=%#v err=%v", changed, refunded, err)
		}
		// Net effect: the units left exactly once, and the refund event keeps the
		// shape the ledger always had.
		if got := refundTestBalance(t, db, uid).PaidUnits; got != 0 {
			t.Fatalf("paid bucket after the refund=%d, want 0", got)
		}
		refundKey := commercialOrderCreditRefundKey(order.OrderNo)
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM model_credit_ledger_events WHERE event_key = $1`, refundKey); got != 1 {
			t.Fatalf("refund booked %d refund event(s), want 1", got)
		}
		var refundUnits int64
		if err := db.db.QueryRow(`SELECT units FROM model_credit_ledger_events WHERE event_key = $1`, refundKey).Scan(&refundUnits); err != nil {
			t.Fatalf("read refund event: %v", err)
		}
		if refundUnits != -units {
			t.Fatalf("refund event units=%d, want %d", refundUnits, -units)
		}
		releaseKey := store.ModelCreditEventKey(store.ModelCreditEventRelease, uid, requestNo, store.ModelCreditBucketPaid)
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM model_credit_ledger_events
			WHERE event_key = $1 AND event_type = 'release' AND units = $2`, releaseKey, units); got != 1 {
			t.Fatalf("hold released %d time(s), want 1", got)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_refund_holds
			WHERE order_no = $1 AND state = 'consumed' AND consumed_at IS NOT NULL`, order.OrderNo); got != 1 {
			t.Fatalf("hold was not consumed exactly once (%d)", got)
		}
	})

	t.Run("duplicate and stale claim never freezes twice", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-stale")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-stale-0001", 5)
		units := order.PlanModelCreditUnits
		requestNo := commercialRefundRequestNoOf(order.OrderNo)
		reserveKey := store.ModelCreditEventKey(store.ModelCreditEventReserve, uid, requestNo, store.ModelCreditBucketPaid)

		if _, ok, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute); err != nil || !ok {
			t.Fatalf("first claim: ok=%v err=%v", ok, err)
		}
		if _, ok, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute); err != nil || ok {
			t.Fatalf("a live claim must not be taken again: ok=%v err=%v", ok, err)
		}
		ageRefundClaim(t, db, order.OrderNo)
		reclaimed, ok, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute)
		if err != nil || !ok || reclaimed.Status != "refunding" {
			t.Fatalf("stale claim was not refreshed: ok=%v order=%#v err=%v", ok, reclaimed, err)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_refund_holds WHERE order_no = $1`, order.OrderNo); got != 1 {
			t.Fatalf("stale retry wrote %d hold rows, want 1", got)
		}
		if got := refundTestBalance(t, db, uid).PaidUnits; got != 0 {
			t.Fatalf("stale retry moved paid units: bucket=%d", got)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM model_credit_ledger_events
			WHERE event_key = $1 AND units = $2`, reserveKey, units); got != 1 {
			t.Fatalf("stale retry booked %d reserve events, want 1", got)
		}

		// Concurrent claims of one order serialize on the order row: exactly one of
		// them may win, and none may freeze a second time.
		ageRefundClaim(t, db, order.OrderNo)
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, claimed, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute)
				if err != nil {
					return
				}
				if claimed {
					mu.Lock()
					won++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Fatalf("%d concurrent claims won, want exactly 1", won)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_refund_holds WHERE order_no = $1`, order.OrderNo); got != 1 {
			t.Fatalf("concurrent claims wrote %d hold rows, want 1", got)
		}
	})

	t.Run("legacy refunding without a hold fails closed", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-legacy")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-legacy-0001", 5)
		requestNo := commercialRefundRequestNoOf(order.OrderNo)
		// The old build flipped the status before it touched the credit.
		if _, err := db.db.Exec(`UPDATE commercial_orders SET status = 'refunding', refund_request_no = $2 WHERE order_no = $1`,
			order.OrderNo, requestNo); err != nil {
			t.Fatalf("seed legacy refunding order: %v", err)
		}
		if _, _, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute); err == nil {
			t.Fatal("a legacy refunding order without a hold was claimed")
		} else if !errors.Is(err, errCommercialRefundHoldMissing) {
			t.Fatalf("legacy claim refusal=%v, want a missing-hold refusal", err)
		}
		if _, _, err := db.CompleteCommercialOrderRefund(order.OrderNo,
			refundTestConfirmation(order, requestNo, "refund-legacy-event-0001")); err == nil {
			t.Fatal("a legacy refunding order without a hold was completed")
		}
		stored, err := db.GetCommercialOrder(0, order.OrderNo)
		if err != nil || stored == nil || stored.Status != "refunding" || stored.RefundedAt != nil {
			t.Fatalf("legacy claim changed: %#v %v", stored, err)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_payment_events WHERE order_no = $1 AND event_type = 'refund_success'`, order.OrderNo); got != 0 {
			t.Fatalf("legacy claim wrote %d payment event(s)", got)
		}
		if got := refundTestBalance(t, db, uid).PaidUnits; got != order.PlanModelCreditUnits {
			t.Fatalf("legacy claim moved paid units: bucket=%d want %d", got, order.PlanModelCreditUnits)
		}
	})

	t.Run("unknown provider outcome keeps refunding and frozen", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-unknown")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-unknown-0001", 5)
		requestNo := commercialRefundRequestNoOf(order.OrderNo)
		if _, ok, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute); err != nil || !ok {
			t.Fatalf("claim refund: ok=%v err=%v", ok, err)
		}
		// A timeout from the channel proves nothing: the money may already be gone.
		if err := db.FailCommercialOrderRefund(order.OrderNo, requestNo, "alipay trade.refund timeout"); err != nil {
			t.Fatalf("record provider failure: %v", err)
		}
		stored, err := db.GetCommercialOrder(0, order.OrderNo)
		if err != nil || stored == nil {
			t.Fatalf("reload order after failure: %#v %v", stored, err)
		}
		if stored.Status != "refunding" || !strings.Contains(stored.LastError, "timeout") {
			t.Fatalf("a failed refund must stay refunding with its cause, got %#v", stored)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_refund_holds WHERE order_no = $1 AND state = 'held'`, order.OrderNo); got != 1 {
			t.Fatalf("failed refund released its hold (%d held)", got)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_payment_events WHERE order_no = $1 AND event_type = 'refund_success'`, order.OrderNo); got != 0 {
			t.Fatalf("failed refund booked a refund_success event")
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM model_credit_ledger_events WHERE event_key = $1`,
			commercialOrderCreditRefundKey(order.OrderNo)); got != 0 {
			t.Fatalf("failed refund booked a refund ledger event")
		}
		// Completing with another claim number must be refused: only the claim that
		// froze the credit authorises the local reversal.
		if _, _, err := db.CompleteCommercialOrderRefund(order.OrderNo,
			refundTestConfirmation(order, "CCRF-OTHER", "refund-unknown-event-0001")); err == nil {
			t.Fatal("a completion with a foreign claim number was accepted")
		}
		if got := refundTestBalance(t, db, uid).PaidUnits; got != 0 {
			t.Fatalf("frozen credit came back before completion: bucket=%d", got)
		}
	})

	t.Run("completion failure retries without a second charge", func(t *testing.T) {
		uid := createCreditTopupTestUser(t, db, "refund-safety-retry")
		order := fulfilledRefundTestOrder(t, db, uid, "refund-retry-0001", 5)
		requestNo := commercialRefundRequestNoOf(order.OrderNo)
		if _, ok, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute); err != nil || !ok {
			t.Fatalf("claim refund: ok=%v err=%v", ok, err)
		}
		// The provider refunded, but the local completion is rejected. A wrong
		// amount is the cheapest way to make the transaction fail after the claim.
		bad := refundTestConfirmation(order, requestNo, "refund-retry-event-0001")
		bad.AmountFen = order.AmountFen + 1
		if _, _, err := db.CompleteCommercialOrderRefund(order.OrderNo, bad); err == nil {
			t.Fatal("a mismatched refund confirmation was accepted")
		}
		stored, err := db.GetCommercialOrder(0, order.OrderNo)
		if err != nil || stored == nil || stored.Status != "refunding" {
			t.Fatalf("failed completion did not keep the claim: %#v %v", stored, err)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_refund_holds WHERE order_no = $1 AND state = 'held'`, order.OrderNo); got != 1 {
			t.Fatalf("failed completion consumed the hold (%d held)", got)
		}
		if got := refundTestBalance(t, db, uid).PaidUnits; got != 0 {
			t.Fatalf("failed completion restored paid units: bucket=%d want 0", got)
		}
		// Retry with the right confirmation: one refund event, one release, one
		// consumed hold, and the balance untouched after the retry.
		refunded, changed, err := db.CompleteCommercialOrderRefund(order.OrderNo,
			refundTestConfirmation(order, requestNo, "refund-retry-event-0001"))
		if err != nil || !changed || refunded.Status != "refunded" {
			t.Fatalf("retried completion: changed=%v order=%#v err=%v", changed, refunded, err)
		}
		if got := refundTestBalance(t, db, uid).PaidUnits; got != 0 {
			t.Fatalf("paid bucket after the retried refund=%d, want 0", got)
		}
		if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM model_credit_ledger_events WHERE event_key = $1`,
			commercialOrderCreditRefundKey(order.OrderNo)); got != 1 {
			t.Fatalf("retried refund booked %d refund events, want 1", got)
		}
		again, againChanged, err := db.CompleteCommercialOrderRefund(order.OrderNo,
			refundTestConfirmation(order, requestNo, "refund-retry-event-0001"))
		if err != nil || againChanged || again == nil || again.Status != "refunded" {
			t.Fatalf("replayed completion: changed=%v order=%#v err=%v", againChanged, again, err)
		}
		if got := refundTestBalance(t, db, uid).PaidUnits; got != 0 {
			t.Fatalf("replayed completion moved the balance to %d", got)
		}
	})
}

// The down migration must refuse to drop a table that still records refunds:
// those rows are the only local evidence of money paid out against credit that
// was pulled back.
func TestPostgresCommercialRefundHoldRollbackKeepsHistory(t *testing.T) {
	db := newCommercialRefundSafetyAdapter(t)
	down, err := os.ReadFile("../migrations/postgres/000075_commercial_refund_holds.down.sql")
	if err != nil {
		t.Fatalf("read commercial refund hold down migration: %v", err)
	}
	conn, err := db.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("take rollback connection: %v", err)
	}
	defer conn.Close()

	// No history: the rollback is allowed and the table goes away.
	if _, err := conn.ExecContext(context.Background(), string(down)); err != nil {
		t.Fatalf("empty commercial_refund_holds should roll back: %v", err)
	}
	var exists bool
	if err := conn.QueryRowContext(context.Background(),
		`SELECT to_regclass('commercial_refund_holds') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("inspect rolled back table: %v", err)
	}
	if exists {
		t.Fatal("empty rollback left commercial_refund_holds behind")
	}

	// History: the rollback must fail and keep the row.
	if _, err := conn.ExecContext(context.Background(), createCommercialRefundHolds); err != nil {
		t.Fatalf("recreate commercial_refund_holds: %v", err)
	}
	uid := createCreditTopupTestUser(t, db, "refund-safety-rollback")
	order := fulfilledRefundTestOrder(t, db, uid, "refund-rollback-0001", 5)
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO commercial_refund_holds
		(order_no, uid, state, units, refund_request_no) VALUES ($1, $2, 'held', 1, $3)`,
		order.OrderNo, uid, commercialRefundRequestNoOf(order.OrderNo)); err != nil {
		t.Fatalf("seed refund hold history: %v", err)
	}
	_, downErr := conn.ExecContext(context.Background(), string(down))
	if downErr != nil {
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Fatalf("clear aborted rollback transaction: %v", err)
		}
	}
	if downErr == nil || !strings.Contains(downErr.Error(), "prevents rollback") {
		t.Fatalf("history must block the rollback, got %v", downErr)
	}
	if got := countRefundTestRows(t, db, `SELECT COUNT(*) FROM commercial_refund_holds WHERE order_no = $1`, order.OrderNo); got != 1 {
		t.Fatalf("blocked rollback deleted the hold row (%d left)", got)
	}
}

// commercialRefundRequestNoOf mirrors the handler's deterministic provider
// request number (CCRF-<orderNo>).
func commercialRefundRequestNoOf(orderNo string) string {
	return "CCRF-" + strings.TrimSpace(orderNo)
}

func TestPostgresCommercialRefundSafetyCompletesAtPaidBalanceLimit(t *testing.T) {
	db := newCommercialRefundSafetyAdapter(t)
	uid := createCreditTopupTestUser(t, db, "refund-balance-limit")
	order := fulfilledRefundTestOrder(t, db, uid, "refund-limit-order", 100)
	requestNo := "CCRF-" + order.OrderNo
	if _, claimed, err := db.BeginCommercialOrderRefund(order.OrderNo, requestNo, time.Minute); err != nil || !claimed {
		t.Fatalf("freeze refund: claimed=%v err=%v", claimed, err)
	}
	const maxPaid int64 = 1<<63 - 1
	// Simulate a subsequent independent grant while the provider is in flight.
	if _, err := db.db.Exec(`UPDATE model_credit_accounts SET paid_units=$2 WHERE uid=$1`, uid, maxPaid); err != nil {
		t.Fatal(err)
	}
	insertRefundTestLedgerEvent(t, db, "grant:refund-limit-after-hold", uid, "paid", "grant", maxPaid, nil)
	refunded, changed, err := db.CompleteCommercialOrderRefund(order.OrderNo, refundTestConfirmation(order, requestNo, requestNo))
	if err != nil || !changed || refunded == nil || refunded.Status != "refunded" {
		t.Fatalf("refund must not overflow an intermediate balance: changed=%v order=%#v err=%v", changed, refunded, err)
	}
	if got := refundTestBalance(t, db, uid).PaidUnits; got != maxPaid {
		t.Fatalf("completion moved already-frozen credit again: paid=%d", got)
	}
	var snapshots int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid=$1 AND event_type IN ('release','refund') AND paid_balance=$2`, uid, maxPaid).Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatalf("both events must snapshot the final account: rows=%d err=%v", snapshots, err)
	}
}

func TestPostgresCommercialRefundSafetyConsumesZeroUnitSubscriptionHold(t *testing.T) {
	db := commercialPolicyTestDB(t)
	plan := seedSharedMachinePlan(t, db, "refund-zero-hold", "Refund zero hold", 30)
	uid := createGLM53MigrationUser(t, db, "refund-zero-hold")
	order := buyMachinePackage(t, db, uid, plan, "REFUNDZEROHOLD")
	refundPackage(t, db, order)
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_refund_holds
		WHERE order_no=$1 AND units=0 AND state='consumed' AND consumed_at IS NOT NULL`, order.OrderNo).Scan(&count); err != nil || count != 1 {
		t.Fatalf("zero-unit subscription hold must be consumed: count=%d err=%v", count, err)
	}
}

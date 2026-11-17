package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 点数充值的真实 PostgreSQL 契约（需要 CATS_PG_TEST_DSN，且必须是测试库）：
//   - 充值订单没有商品行、金额与点数写在订单上；
//   - 付款回调幂等（重复回调 / 换 event id 重放都不二次入账）；
//   - 金额对不上（伪金额）一律拒绝；
//   - 没有套餐的用户也能拿充值点数用模型（资格查询 + 任务门禁）；
//   - 退款沿用「已消费不可退」的既有规则；
//   - 充值不产生任何订阅、赠送或机器权益。

func newCreditTopupTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_topup_test_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	t.Cleanup(func() { base.Close() })
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create credit top-up test schema: %v", err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`) })
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open credit top-up test schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create credit top-up schema objects: %v", err)
	}
	testPostgresMigrationFiles(t, db)
	return db
}

func createCreditTopupTestUser(t *testing.T, db *Adapter, name string) int64 {
	t.Helper()
	uid, err := db.CreateUser(&types.User{
		Username:    name,
		Email:       name + "@example.test",
		DisplayName: name,
		AccountType: types.AccountHuman,
		PassHash:    []byte(name + "-hash"),
	})
	if err != nil {
		t.Fatalf("create credit top-up user %s: %v", name, err)
	}
	return uid
}

func paidBucketUnits(t *testing.T, db *Adapter, uid int64) int64 {
	t.Helper()
	account, err := db.GetModelCreditAccount(context.Background(), uid)
	if errors.Is(err, store.ErrModelCreditNotFound) {
		return 0
	}
	if err != nil {
		t.Fatalf("read credit account %d: %v", uid, err)
	}
	return account.PaidUnits
}

func TestPostgresCreditTopupContract(t *testing.T) {
	db := newCreditTopupTestAdapter(t)
	ctx := context.Background()
	uid := createCreditTopupTestUser(t, db, "topup-buyer")

	// 1. 下单：没有商品行，点数按 1 分 = 1 点写进订单快照。
	order, err := db.CreateCommercialTopupOrder(uid, 3000, "alipay_page", "topup-request-0001", nil)
	if err != nil {
		t.Fatalf("create credit top-up order: %v", err)
	}
	if order.Kind != types.CommercialOrderKindCreditTopup || order.PlanID != 0 || order.AmountFen != 3000 {
		t.Fatalf("top-up order snapshot is wrong: %#v", order)
	}
	if order.PlanModelCreditUnits != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("units snapshot=%d want %d", order.PlanModelCreditUnits, 3000*CommercialTopupUnitsPerFen)
	}
	if order.PlanSlug != CommercialTopupOrderSlug || order.PlanDurationDays != 0 || order.PlanMonthlyBudget != 0 {
		t.Fatalf("a top-up must not look like a subscription: %#v", order)
	}
	var topupPlans int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_plans WHERE slug = $1`, CommercialTopupOrderSlug).Scan(&topupPlans); err != nil {
		t.Fatalf("count top-up plans: %v", err)
	}
	if topupPlans != 0 {
		t.Fatalf("a top-up must not create a product row, found %d", topupPlans)
	}

	// 2. 同一个 client_request_id 重放：同一笔订单。
	replayed, err := db.CreateCommercialTopupOrder(uid, 3000, "alipay_page", "topup-request-0001", nil)
	if err != nil || replayed.OrderNo != order.OrderNo {
		t.Fatalf("replay must return the same order: %#v err=%v", replayed, err)
	}
	// 3. 同一个 id、不同金额：直接拒绝。
	if _, err := db.CreateCommercialTopupOrder(uid, 10000, "alipay_page", "topup-request-0001", nil); err == nil {
		t.Fatal("the same request id with another amount must be refused")
	}

	// 4. 已支付订单进入 paid 桶，账本记 grant:order:<orderNo>。
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: "alipay_page", EventID: "topup-event-0001", ProviderTradeNo: "TOPUP-TRADE-1",
		AmountFen: 3000, Currency: "CNY", PaidAt: time.Now().UTC(), PayloadHash: strings.Repeat("a", 64),
	}
	fulfilled, changed, err := db.FulfillCommercialOrder(order.OrderNo, confirmation)
	if err != nil || !changed || fulfilled.Status != "fulfilled" {
		t.Fatalf("fulfil credit top-up: changed=%v order=%#v err=%v", changed, fulfilled, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("paid bucket=%d want %d", got, 3000*CommercialTopupUnitsPerFen)
	}
	var grantKey string
	if err := db.db.QueryRow(`SELECT event_key FROM model_credit_ledger_events WHERE uid = $1 AND event_type = 'grant'`, uid).Scan(&grantKey); err != nil {
		t.Fatalf("read top-up grant event: %v", err)
	}
	if grantKey != commercialOrderCreditGrantKey(order.OrderNo) {
		t.Fatalf("grant key=%q want %q", grantKey, commercialOrderCreditGrantKey(order.OrderNo))
	}

	// 5. 充值绝不产生订阅、赠送或机器权益。
	var entitlements, grants, workerCredits int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_entitlements WHERE uid = $1`, uid).Scan(&entitlements); err != nil {
		t.Fatalf("count entitlements: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_grants WHERE uid = $1`, uid).Scan(&grants); err != nil {
		t.Fatalf("count quota grants: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM cloud_worker_credits WHERE uid = $1`, uid).Scan(&workerCredits); err != nil {
		t.Fatalf("count worker credits: %v", err)
	}
	if entitlements != 0 || grants != 0 || workerCredits != 0 {
		t.Fatalf("a top-up created entitlements=%d grants=%d workerCredits=%d", entitlements, grants, workerCredits)
	}

	// 6. 重复回调：订单已 fulfilled 时是幂等 no-op，余额不变。
	replayedOrder, replayChanged, err := db.FulfillCommercialOrder(order.OrderNo, confirmation)
	if err != nil || replayChanged || replayedOrder == nil || replayedOrder.Status != "fulfilled" {
		t.Fatalf("a replayed callback must be an idempotent no-op: changed=%v order=%#v err=%v", replayChanged, replayedOrder, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("a replayed callback changed the balance to %d", got)
	}
	// 同一个 event id 被另一笔订单再用：必须拒绝（一行付款事件只能付一笔单）。
	reused, err := db.CreateCommercialTopupOrder(uid, 800, "alipay_page", "topup-request-0003", nil)
	if err != nil {
		t.Fatalf("create reuse-check top-up order: %v", err)
	}
	reusedConfirmation := *confirmation
	reusedConfirmation.AmountFen = 800
	if _, _, err := db.FulfillCommercialOrder(reused.OrderNo, &reusedConfirmation); err == nil {
		t.Fatal("a payment event id already used by another order must be refused")
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("a reused payment event changed the balance to %d", got)
	}
	// 换一个 event id 再投一次「已支付」：订单已 fulfilled，返回同一状态且不入账。
	second := *confirmation
	second.EventID = "topup-event-0002"
	again, againChanged, err := db.FulfillCommercialOrder(order.OrderNo, &second)
	if err != nil || againChanged || again == nil || again.Status != "fulfilled" {
		t.Fatalf("a second delivery of a fulfilled order must be a no-op: changed=%v err=%v", againChanged, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("a second delivery changed the balance to %d", got)
	}

	// 7. 伪金额：金额对不上的回调不能把这笔订单付掉。
	other, err := db.CreateCommercialTopupOrder(uid, 5000, "alipay_page", "topup-request-0002", nil)
	if err != nil {
		t.Fatalf("create second top-up order: %v", err)
	}
	fake := &types.CommercialPaymentConfirmation{
		Channel: "alipay_page", EventID: "topup-event-0003", ProviderTradeNo: "TOPUP-TRADE-2",
		AmountFen: 100, Currency: "CNY", PaidAt: time.Now().UTC(), PayloadHash: strings.Repeat("b", 64),
	}
	if _, _, err := db.FulfillCommercialOrder(other.OrderNo, fake); err == nil {
		t.Fatal("a callback whose amount does not match the order must be refused")
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("a forged amount changed the balance to %d", got)
	}

	// 8. 独立充值在没有套餐时也能用模型：资格查询与任务门禁都要放行。
	enabled, tier, allowance, err := db.GetModelCreditEligibility(ctx, uid)
	if err != nil {
		t.Fatalf("credit eligibility: %v", err)
	}
	if !enabled || allowance != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("a top-up-only account must be allowed: enabled=%v tier=%d allowance=%d", enabled, tier, allowance)
	}
	if !store.ModelCreditTaskPlanTierTrusted(tier) || tier != store.ModelCreditTaskPlanTierMin {
		t.Fatalf("top-up-only accounts price at the base rung, got tier %d", tier)
	}
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatalf("begin task permission check: %v", err)
	}
	permitted, permitErr := modelCreditTaskPermitted(ctx, tx, uid, time.Now().UTC())
	taskTier, tierErr := modelCreditTaskPlanTier(ctx, tx, uid, time.Now().UTC())
	tx.Rollback()
	if permitErr != nil || !permitted {
		t.Fatalf("a top-up-only account must be permitted to start a task: %v", permitErr)
	}
	if tierErr != nil || taskTier != store.ModelCreditTaskPlanTierMin {
		t.Fatalf("task plan tier=%d err=%v want %d", taskTier, tierErr, store.ModelCreditTaskPlanTierMin)
	}

	// 9. 退款：未消费可以退，退回后 paid 桶归零，订单变 refunded。
	if _, _, err := db.BeginCommercialOrderRefund(order.OrderNo, "refund-topup-1", time.Minute); err != nil {
		t.Fatalf("claim top-up refund: %v", err)
	}
	refunded, refundChanged, err := db.CompleteCommercialOrderRefund(order.OrderNo, &types.CommercialRefundConfirmation{
		Channel: "alipay_page", EventID: "topup-refund-0001", ProviderTradeNo: "TOPUP-TRADE-1",
		RefundRequestNo: "refund-topup-1", AmountFen: 3000, Currency: "CNY",
		RefundedAt: time.Now().UTC(), PayloadHash: strings.Repeat("c", 64),
	})
	if err != nil || !refundChanged || refunded.Status != "refunded" {
		t.Fatalf("complete top-up refund: changed=%v order=%#v err=%v", refundChanged, refunded, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 0 {
		t.Fatalf("a refund must reverse the paid bucket, got %d", got)
	}
}

func TestPostgresCreditTopupRefundRefusesConsumedCredit(t *testing.T) {
	db := newCreditTopupTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "topup-spender")
	order, err := db.CreateCommercialTopupOrder(uid, 2000, "alipay_page", "topup-request-1000", nil)
	if err != nil {
		t.Fatalf("create credit top-up order: %v", err)
	}
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: "alipay_page", EventID: "topup-event-1000", ProviderTradeNo: "TOPUP-TRADE-1000",
		AmountFen: 2000, Currency: "CNY", PaidAt: time.Now().UTC(), PayloadHash: strings.Repeat("d", 64),
	}
	if _, _, err := db.FulfillCommercialOrder(order.OrderNo, confirmation); err != nil {
		t.Fatalf("fulfil credit top-up: %v", err)
	}
	// 花掉一部分：记一笔 paid 桶的 settle，正是「已消费」的证据。
	if _, err := db.db.Exec(`
		INSERT INTO model_credit_ledger_events(event_key, uid, bucket, event_type, units, request_id,
			monthly_balance, gift_balance, paid_balance, debt_balance)
		VALUES ('settle:' || $1::bigint::text || ':spent:paid', $1, 'paid', 'settle', 1000, 'spent',
		        0, 0, $2, 0)`, uid, 2000*CommercialTopupUnitsPerFen-1000); err != nil {
		t.Fatalf("record consumption: %v", err)
	}
	_, claimed, err := db.BeginCommercialOrderRefund(order.OrderNo, "refund-topup-2", time.Minute)
	if claimed {
		t.Fatal("consumed credit must be refused before a provider refund can be claimed")
	}
	if err == nil {
		t.Fatal("refunding consumed credit must be refused")
	}
	if !strings.Contains(err.Error(), "already consumed") && !strings.Contains(err.Error(), "lower than the purchased amount") {
		t.Fatalf("unexpected refusal reason: %v", err)
	}
	order, err = db.GetCommercialOrder(0, order.OrderNo)
	if err != nil || order == nil || order.Status != "fulfilled" {
		t.Fatalf("a refused refund must not settle the order: %#v err=%v", order, err)
	}
}

// TestPostgresCreditTopupRefundRefusesArrearsRepayment：充值到账先拿去抵欠款，抵掉的
// 那部分就是已经花掉的消费（钱变成了「不欠账」）。这种订单退款必须明确拒绝，既不
// 退钱也不恢复欠款——恢复欠款等于凭空给用户一笔免息信用。
func TestPostgresCreditTopupRefundRefusesArrearsRepayment(t *testing.T) {
	db := newCreditTopupTestAdapter(t)
	unit := CommercialTopupUnitsPerFen
	uid := createCreditTopupTestUser(t, db, "topup-arrears-refund")
	grantTopupCredits(t, db, uid, 1, "topup:arrears-bootstrap")
	// 账户已经透支 3000 点、paid 桶是空的。
	if _, err := db.db.Exec(`UPDATE model_credit_accounts SET paid_units = 0, debt_units = $2 WHERE uid = $1`,
		uid, 3000*unit); err != nil {
		t.Fatalf("seed arrears: %v", err)
	}
	order, err := db.CreateCommercialTopupOrder(uid, 2000, "alipay_page", "topup-request-arrears-1", nil)
	if err != nil {
		t.Fatalf("create credit top-up order: %v", err)
	}
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: "alipay_page", EventID: "topup-event-arrears-1", ProviderTradeNo: "TOPUP-ARREARS-1",
		AmountFen: 2000, Currency: "CNY", PaidAt: time.Now().UTC(), PayloadHash: strings.Repeat("7", 64),
	}
	if _, _, err := db.FulfillCommercialOrder(order.OrderNo, confirmation); err != nil {
		t.Fatalf("fulfil credit top-up: %v", err)
	}
	account, err := db.GetModelCreditAccount(context.Background(), uid)
	if err != nil {
		t.Fatalf("read credit account: %v", err)
	}
	// 2000 点全部抵了欠款：paid 归零，欠款剩 1000 点。
	if account.PaidUnits != 0 || account.DebtUnits != 1000*unit {
		t.Fatalf("the top-up must repay arrears first: paid=%d debt=%d", account.PaidUnits, account.DebtUnits)
	}
	_, claimed, err := db.BeginCommercialOrderRefund(order.OrderNo, "refund-arrears-1", time.Minute)
	if claimed {
		t.Fatal("arrears repayment must be refused before a provider refund can be claimed")
	}
	if !errors.Is(err, ErrCommercialOrderCreditSpent) {
		t.Fatalf("refunding credit that repaid arrears must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "arrears") {
		t.Fatalf("the refusal must name the arrears repayment, got %v", err)
	}
	// 拒绝之后余额、欠款和订单状态都不许变。
	account, err = db.GetModelCreditAccount(context.Background(), uid)
	if err != nil {
		t.Fatalf("re-read credit account: %v", err)
	}
	if account.PaidUnits != 0 || account.DebtUnits != 1000*unit {
		t.Fatalf("a refused refund must not restore arrears or move credit: paid=%d debt=%d",
			account.PaidUnits, account.DebtUnits)
	}
	stored, err := db.GetCommercialOrder(0, order.OrderNo)
	if err != nil || stored == nil || stored.Status != "fulfilled" {
		t.Fatalf("a refused refund must not settle the order: %#v err=%v", stored, err)
	}
}

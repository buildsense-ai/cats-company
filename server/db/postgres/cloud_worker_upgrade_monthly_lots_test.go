package postgres

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// 云机器点数升配 × 月额度批次（lot）的 PostgreSQL 契约（需要 CATS_PG_TEST_DSN）：
//   - 报价先对账月额度，过期/退回的 frozen 月额度不计入可花余额，也不能下单；
//   - 预占按批次扣月额度（老周期先花），并写下这笔钱来自哪一批；
//   - 结算把这笔预占消费掉，不退任何批次；
//   - 释放按批次退回：作废（revoked）的批次绝不复活，跨期批次退回 frozen；
//   - 改动之前预占的老操作（没有 hold_lots 行）仍按历史发放归属退回。
//
// gift 永远不参与升配：这批用例里没有任何 gift 桶，报价与扣费口径必须是
// monthly -> paid。

func seedMachineUpgradeMonthlyLot(t *testing.T, db *Adapter, uid int64, key string, period time.Time, units int64) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO model_credit_monthly_lots (grant_key, uid, period_start, units)
		VALUES ($1, $2, $3, $4)`, key, uid, period.UTC(), units); err != nil {
		t.Fatalf("seed monthly lot %s: %v", key, err)
	}
}

// machineUpgradeLotState reports one lot's remaining units and whether it was
// refunded (revoked). A revoked lot is kept on purpose so a hold can never
// revive refunded credit.
func machineUpgradeLotState(t *testing.T, db *Adapter, key string) (int64, bool, bool) {
	t.Helper()
	var units int64
	var revoked bool
	err := db.db.QueryRow(`SELECT units, revoked FROM model_credit_monthly_lots WHERE grant_key = $1`, key).
		Scan(&units, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false
	}
	if err != nil {
		t.Fatalf("read monthly lot %s: %v", key, err)
	}
	return units, revoked, true
}

type machineUpgradeHoldLot struct {
	key   string
	units int64
}

func machineUpgradeHoldLots(t *testing.T, db *Adapter, uid int64, requestID string) []machineUpgradeHoldLot {
	t.Helper()
	rows, err := db.db.Query(`
		SELECT grant_key, units FROM model_credit_monthly_hold_lots
		WHERE uid = $1 AND request_id = $2 ORDER BY ordinal`, uid, requestID)
	if err != nil {
		t.Fatalf("read monthly hold lots: %v", err)
	}
	defer rows.Close()
	out := make([]machineUpgradeHoldLot, 0, 2)
	for rows.Next() {
		var part machineUpgradeHoldLot
		if err := rows.Scan(&part.key, &part.units); err != nil {
			t.Fatalf("scan monthly hold lot: %v", err)
		}
		out = append(out, part)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate monthly hold lots: %v", err)
	}
	return out
}

// seedMachineUpgradeMonthlyGrantEvent books the historical monthly grant the old
// ledger carried. Its key is also the lot key, exactly what reconciliation would
// have written on the day it granted.
func seedMachineUpgradeMonthlyGrantEvent(t *testing.T, db *Adapter, uid int64, key string, units int64, createdAt time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO model_credit_ledger_events
			(event_key, uid, bucket, event_type, units, monthly_balance, gift_balance,
			 paid_balance, debt_balance, created_at)
		VALUES ($1, $2, 'monthly', 'grant', $3, $3, 0, 0, 0, $4)`,
		key, uid, units, createdAt.UTC()); err != nil {
		t.Fatalf("seed monthly grant event %s: %v", key, err)
	}
}

func countMachineUpgradeLedgerEvents(t *testing.T, db *Adapter, uid int64, requestID, eventType, bucket string) int {
	t.Helper()
	var count int
	if err := db.db.QueryRow(`
		SELECT COUNT(*) FROM model_credit_ledger_events
		WHERE uid = $1 AND request_id = $2 AND event_type = $3 AND bucket = $4`,
		uid, requestID, eventType, bucket).Scan(&count); err != nil {
		t.Fatalf("count %s %s ledger events: %v", eventType, bucket, err)
	}
	return count
}

// TestPostgresMachineUpgradeQuoteReconcilesAndExcludesFrozen：报价必须先对账，而且
// 过期月额度（frozen）既不进可花余额，也不能拿来下单——否则用户会看到一个自己
// 根本花不出去的余额。
func TestPostgresMachineUpgradeQuoteReconcilesAndExcludesFrozen(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	unit := CommercialTopupUnitsPerFen
	uid := createCreditTopupTestUser(t, db, "mu-lot-frozen")
	grantMachineUpgradeBucket(t, db, uid, store.ModelCreditBucketPaid, 1000, "topup:mu-lot-frozen")
	grantMachineUpgradeBucket(t, db, uid, store.ModelCreditBucketMonthly, 600, "monthly:mu-lot-frozen")

	// 上一个周期的发放还在，但订阅已经结束：对账会把 600 冻结。
	period := time.Now().UTC().AddDate(0, -1, 0).Truncate(time.Second)
	grantKey := store.ModelCreditMonthlyGrantKey(uid, 4242, period)
	seedMachineUpgradeMonthlyGrantEvent(t, db, uid, grantKey, 600*unit, period)
	seedMachineUpgradeMonthlyLot(t, db, uid, grantKey, period, 600*unit)

	units, arrears, err := db.MachineUpgradeCreditBalance(uid)
	if err != nil || arrears || units != 1000*unit {
		t.Fatalf("quote = %d units arrears=%v err=%v, want paid only %d", units, arrears, err, 1000*unit)
	}
	account := machineUpgradeAccountSnapshot(t, db, uid)
	if account.MonthlyUnits != 0 || account.FrozenMonthlyUnits != 600*unit {
		t.Fatalf("expired monthly must be frozen instead of spendable: %#v", account)
	}
	// 幂等：再报一次价不会把冻结的月额度解冻，也不会重复发放。
	if again, _, err := db.MachineUpgradeCreditBalance(uid); err != nil || again != units {
		t.Fatalf("a repeated quote changed the balance: %d -> %d err=%v", units, again, err)
	}
	if frozen := machineUpgradeAccountSnapshot(t, db, uid).FrozenMonthlyUnits; frozen != 600*unit {
		t.Fatalf("a repeated quote thawed frozen monthly credit: frozen=%d", frozen)
	}

	// 报价里够、冻结里不够：预占必须按可花余额拒绝，冻结额度一分不动。
	seedMachineUpgradeLifecycle(t, db, uid, "bot-lot-frozen", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-lot-frozen-1", "bot-lot-frozen", "upgrade-lot-frozen-1", 1600)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, _, err := db.StartMachineUpgradeCharge(op.OperationID, false); !errors.Is(err, store.ErrModelCreditInsufficient) {
		t.Fatalf("frozen monthly credit must not pay for an upgrade, got %v", err)
	}
	after := machineUpgradeAccountSnapshot(t, db, uid)
	if after.MonthlyUnits != 0 || after.FrozenMonthlyUnits != 600*unit || after.PaidUnits != 1000*unit {
		t.Fatalf("a refused charge must leave every bucket alone: %#v", after)
	}
	if units, _, found := machineUpgradeLotState(t, db, grantKey); !found || units != 600*unit {
		t.Fatalf("a refused charge must not touch the monthly lot: units=%d found=%v", units, found)
	}
}

// TestPostgresMachineUpgradeReserveAllocatesMonthlyLotsAndReleaseReturnsThem：
// 预占按批次扣（老周期先花）并留下归属，明确失败释放时按批次原样退回。
func TestPostgresMachineUpgradeReserveAllocatesMonthlyLotsAndReleaseReturnsThem(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	unit := CommercialTopupUnitsPerFen
	uid := createCreditTopupTestUser(t, db, "mu-lot-release")
	older := time.Now().UTC().AddDate(0, -2, 0).Truncate(time.Second)
	newer := older.AddDate(0, 1, 0)
	olderKey := store.ModelCreditMonthlyGrantKey(uid, 91, older)
	newerKey := store.ModelCreditMonthlyGrantKey(uid, 92, newer)
	grantMachineUpgradeBucket(t, db, uid, store.ModelCreditBucketMonthly, 60, "monthly:mu-lot-release")
	seedMachineUpgradeMonthlyLot(t, db, uid, olderKey, older, 30*unit)
	seedMachineUpgradeMonthlyLot(t, db, uid, newerKey, newer, 30*unit)

	seedMachineUpgradeLifecycle(t, db, uid, "bot-lot-release", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-lot-release-1", "bot-lot-release", "upgrade-lot-release-1", 40)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("start machine upgrade: proceed=%v err=%v", proceed, err)
	}

	if units, revoked, found := machineUpgradeLotState(t, db, olderKey); !found || revoked || units != 0 {
		t.Fatalf("the oldest lot must be spent first: units=%d revoked=%v found=%v", units, revoked, found)
	}
	if units, revoked, found := machineUpgradeLotState(t, db, newerKey); !found || revoked || units != 20*unit {
		t.Fatalf("the newer lot must cover only the remainder: units=%d revoked=%v found=%v", units, revoked, found)
	}
	holds := machineUpgradeHoldLots(t, db, uid, op.OperationID)
	if len(holds) != 2 || holds[0].key != olderKey || holds[0].units != 30*unit ||
		holds[1].key != newerKey || holds[1].units != 10*unit {
		t.Fatalf("the hold must record which lot each unit came from: %#v", holds)
	}
	if got := machineUpgradeAccountSnapshot(t, db, uid).MonthlyUnits; got != 20*unit {
		t.Fatalf("the hold must leave only the unspent monthly units, monthly=%d", got)
	}

	// 明确失败 → 释放：两个批次都回到原来的位置。
	if failed, err := db.FailMachineUpgrade(op.OperationID, "provider refused"); err != nil ||
		failed.Status != types.MachineUpgradeStatusFailed {
		t.Fatalf("fail machine upgrade: %#v err=%v", failed, err)
	}
	if units, _, found := machineUpgradeLotState(t, db, olderKey); !found || units != 30*unit {
		t.Fatalf("the oldest lot was not returned: units=%d found=%v", units, found)
	}
	if units, _, found := machineUpgradeLotState(t, db, newerKey); !found || units != 30*unit {
		t.Fatalf("the newer lot was not returned: units=%d found=%v", units, found)
	}
	released := machineUpgradeAccountSnapshot(t, db, uid)
	if released.MonthlyUnits != 60*unit || released.FrozenMonthlyUnits != 0 || released.PaidUnits != 0 {
		t.Fatalf("a released hold must return the monthly units where they came from: %#v", released)
	}
	if holds := machineUpgradeHoldLots(t, db, uid, op.OperationID); len(holds) != 0 {
		t.Fatalf("a released hold must not leave provenance rows behind: %#v", holds)
	}
	if events := countMachineUpgradeLedgerEvents(t, db, uid, op.OperationID,
		store.ModelCreditEventRelease, store.ModelCreditBucketMonthly); events != 1 {
		t.Fatalf("a release must book exactly one monthly release event, got %d", events)
	}
}

// TestPostgresMachineUpgradeSettleConsumesMonthlyLots：结算把这笔预占消费掉，
// 批次不回收、hold_lots 不残留。
func TestPostgresMachineUpgradeSettleConsumesMonthlyLots(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	unit := CommercialTopupUnitsPerFen
	uid := createCreditTopupTestUser(t, db, "mu-lot-settle")
	period := time.Now().UTC().AddDate(0, -2, 0).Truncate(time.Second)
	lotKey := store.ModelCreditMonthlyGrantKey(uid, 51, period)
	grantMachineUpgradeBucket(t, db, uid, store.ModelCreditBucketMonthly, 40, "monthly:mu-lot-settle")
	seedMachineUpgradeMonthlyLot(t, db, uid, lotKey, period, 40*unit)

	seedMachineUpgradeLifecycle(t, db, uid, "bot-lot-settle", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-lot-settle-1", "bot-lot-settle", "upgrade-lot-settle-1", 40)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("start machine upgrade: proceed=%v err=%v", proceed, err)
	}
	if completed, err := db.CompleteMachineUpgrade(op.OperationID, types.MachinePlanPro,
		types.MachinePlanLight, op.MachineExpiresAt); err != nil ||
		completed.Status != types.MachineUpgradeStatusCompleted {
		t.Fatalf("complete machine upgrade: %#v err=%v", completed, err)
	}
	if units, revoked, found := machineUpgradeLotState(t, db, lotKey); !found || revoked || units != 0 {
		t.Fatalf("a settled charge must consume the lot: units=%d revoked=%v found=%v", units, revoked, found)
	}
	account := machineUpgradeAccountSnapshot(t, db, uid)
	if account.MonthlyUnits != 0 || account.FrozenMonthlyUnits != 0 || account.PaidUnits != 0 {
		t.Fatalf("a settled charge must not refund anything: %#v", account)
	}
	if holds := machineUpgradeHoldLots(t, db, uid, op.OperationID); len(holds) != 0 {
		t.Fatalf("a settled charge must clear its provenance rows: %#v", holds)
	}
	if events := countMachineUpgradeLedgerEvents(t, db, uid, op.OperationID,
		store.ModelCreditEventSettle, store.ModelCreditBucketMonthly); events != 1 {
		t.Fatalf("a settle must book exactly one monthly settle event, got %d", events)
	}
}

// TestPostgresMachineUpgradeReleaseNeverRevivesRevokedMonthlyLots：订单退款作废的
// 批次在释放时绝不复活，也不写一笔假的退款事件。
func TestPostgresMachineUpgradeReleaseNeverRevivesRevokedMonthlyLots(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	unit := CommercialTopupUnitsPerFen
	uid := createCreditTopupTestUser(t, db, "mu-lot-revoked")
	period := time.Now().UTC().AddDate(0, -2, 0).Truncate(time.Second)
	lotKey := store.ModelCreditMonthlyGrantKey(uid, 61, period)
	grantMachineUpgradeBucket(t, db, uid, store.ModelCreditBucketMonthly, 40, "monthly:mu-lot-revoked")
	seedMachineUpgradeMonthlyLot(t, db, uid, lotKey, period, 40*unit)

	seedMachineUpgradeLifecycle(t, db, uid, "bot-lot-revoked", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-lot-revoked-1", "bot-lot-revoked", "upgrade-lot-revoked-1", 40)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("start machine upgrade: proceed=%v err=%v", proceed, err)
	}
	// 订单退款把这一批作废：批次行保留、额度归零（这样未完成的预占也不会复活它）。
	if _, err := db.db.Exec(`UPDATE model_credit_monthly_lots SET units = 0, revoked = TRUE
		WHERE grant_key = $1`, lotKey); err != nil {
		t.Fatalf("revoke monthly lot: %v", err)
	}

	if failed, err := db.FailMachineUpgrade(op.OperationID, "provider refused"); err != nil ||
		failed.Status != types.MachineUpgradeStatusFailed {
		t.Fatalf("fail machine upgrade: %#v err=%v", failed, err)
	}
	if units, revoked, found := machineUpgradeLotState(t, db, lotKey); !found || !revoked || units != 0 {
		t.Fatalf("a revoked lot must stay revoked: units=%d revoked=%v found=%v", units, revoked, found)
	}
	account := machineUpgradeAccountSnapshot(t, db, uid)
	if account.MonthlyUnits != 0 || account.FrozenMonthlyUnits != 0 {
		t.Fatalf("a revoked lot must not revive any monthly credit: %#v", account)
	}
	if events := countMachineUpgradeLedgerEvents(t, db, uid, op.OperationID,
		store.ModelCreditEventRelease, store.ModelCreditBucketMonthly); events != 0 {
		t.Fatalf("a revoked lot must not book a monthly release event, got %d", events)
	}
}

// TestPostgresMachineUpgradeReleaseReturnsLegacyMonthlyHoldFrozen：改动之前预占的
// 老操作没有 hold_lots 行，释放仍然按「预占前最近一次发放」的批次退回；那一期
// 已经结束，所以退回的是 frozen 月额度，看得见但不可花。
func TestPostgresMachineUpgradeReleaseReturnsLegacyMonthlyHoldFrozen(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	unit := CommercialTopupUnitsPerFen
	uid := createCreditTopupTestUser(t, db, "mu-lot-legacy")
	period := time.Now().UTC().AddDate(0, -2, 0).Truncate(time.Second)
	lotKey := store.ModelCreditMonthlyGrantKey(uid, 71, period)
	grantMachineUpgradeBucket(t, db, uid, store.ModelCreditBucketMonthly, 40, "monthly:mu-lot-legacy")
	seedMachineUpgradeMonthlyLot(t, db, uid, lotKey, period, 40*unit)

	seedMachineUpgradeLifecycle(t, db, uid, "bot-lot-legacy", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-lot-legacy-1", "bot-lot-legacy", "upgrade-lot-legacy-1", 40)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE cloud_worker_machine_upgrades SET created_at = $2 WHERE operation_id = $1`,
		op.OperationID, period.Add(-time.Hour)); err != nil {
		t.Fatalf("simulate opening the order before its funding period: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("start machine upgrade: proceed=%v err=%v", proceed, err)
	}
	// 老操作：预占发生在批次表上线之前，没有 hold_lots 行；账本里只有当年那笔发放。
	if _, err := db.db.Exec(`DELETE FROM model_credit_monthly_hold_lots
		WHERE uid = $1 AND request_id = $2`, uid, op.OperationID); err != nil {
		t.Fatalf("simulate a legacy hold: %v", err)
	}
	seedMachineUpgradeMonthlyGrantEvent(t, db, uid, lotKey, 40*unit, time.Now().UTC().Add(-2*time.Hour))

	if failed, err := db.FailMachineUpgrade(op.OperationID, "provider refused"); err != nil ||
		failed.Status != types.MachineUpgradeStatusFailed {
		t.Fatalf("fail machine upgrade: %#v err=%v", failed, err)
	}
	if units, revoked, found := machineUpgradeLotState(t, db, lotKey); !found || revoked || units != 40*unit {
		t.Fatalf("the legacy hold must return into its own lot: units=%d revoked=%v found=%v", units, revoked, found)
	}
	account := machineUpgradeAccountSnapshot(t, db, uid)
	if account.MonthlyUnits != 0 || account.FrozenMonthlyUnits != 40*unit {
		t.Fatalf("an ended period must return frozen, not spendable: %#v", account)
	}
	if events := countMachineUpgradeLedgerEvents(t, db, uid, op.OperationID,
		store.ModelCreditEventRelease, store.ModelCreditBucketMonthly); events != 1 {
		t.Fatalf("a legacy release must still book one monthly release event, got %d", events)
	}
	// 退回的冻结额度不再可花：报价不会把它算进来。
	if units, _, err := db.MachineUpgradeCreditBalance(uid); err != nil || units != 0 {
		t.Fatalf("frozen monthly credit must not be quoted: units=%d err=%v", units, err)
	}
}

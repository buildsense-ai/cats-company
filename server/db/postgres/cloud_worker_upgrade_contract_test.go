package postgres

import (
	"context"
	"database/sql"
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

// 云机器点数升配的真实 PostgreSQL 契约（需要 CATS_PG_TEST_DSN）：
//   - 预占 → 结算/释放三件事在一个事务里，脚本没确认成功就不会有终态扣费；
//   - 并发/重复申请不会二次扣费；
//   - 余额不足、欠债账户明确不扣；
//   - 失败可退、结果未知保留预占供 reconcile；
//   - 升配权益独立于套餐，任何后续写入都不能把它降档。

func newMachineUpgradeTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_machine_upgrade_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	t.Cleanup(func() { base.Close() })
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create machine upgrade test schema: %v", err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`) })
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open machine upgrade test schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create machine upgrade schema objects: %v", err)
	}
	testPostgresMigrationFiles(t, db)
	return db
}

// grantTopupCredits books spendable credit the way a paid top-up does, so the
// credit being spent in these tests is the same money-backed balance.
func grantTopupCredits(t *testing.T, db *Adapter, uid int64, credits int64, key string) {
	t.Helper()
	if _, err := db.GrantModelCredits(context.Background(), store.ModelCreditGrantInput{
		UID: uid, Bucket: store.ModelCreditBucketPaid,
		Units: credits * CommercialTopupUnitsPerFen, IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("grant top-up credit: %v", err)
	}
}

// seedMachineUpgradeLifecycle 建一条「卖出这台机器」的 lifecycle 行（独立的 bot
// 用户当 worker，真实链路就是这样）。升配权益只认这一行：id + owner + active +
// 套餐未到期，所以任何要结算升配的测试都必须先把它建出来。
func seedMachineUpgradeLifecycle(t *testing.T, db *Adapter, owner int64, tenant string, packageExpiresAt time.Time) int64 {
	t.Helper()
	worker := createCreditTopupTestUser(t, db, fmt.Sprintf("mw-%d", time.Now().UnixNano()))
	var id int64
	if err := db.db.QueryRow(`
		INSERT INTO cloud_worker_lifecycles(worker_uid, owner_uid, tenant_name, package_expires_at, delete_after, state)
		VALUES ($1, $2, $3, $4, $5, 'active')
		RETURNING id`,
		worker, owner, tenant, packageExpiresAt.UTC(), packageExpiresAt.UTC().Add(7*24*time.Hour)).Scan(&id); err != nil {
		t.Fatalf("seed lifecycle %s: %v", tenant, err)
	}
	return id
}

// rollbackTx opens a throwaway transaction for writes that must not commit (a
// refused term write still has to leave the database untouched).
func rollbackTx(t *testing.T, db *Adapter) *sql.Tx {
	t.Helper()
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatalf("begin rollback tx: %v", err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// machineUpgradeOperationForTest binds the operation to the lifecycle that owns
// the tenant; the remaining term is that lifecycle's package expiry, exactly what
// a real quote would snapshot.
func machineUpgradeOperationForTest(t *testing.T, db *Adapter, uid int64, operationID, tenant, requestID string, costCredits int64) *types.MachineUpgradeOperation {
	t.Helper()
	var lifecycleID int64
	var expiresAt time.Time
	if err := db.db.QueryRow(`
		SELECT id, package_expires_at FROM cloud_worker_lifecycles
		WHERE tenant_name = $1 AND owner_uid = $2 AND state = 'active'`, tenant, uid).
		Scan(&lifecycleID, &expiresAt); err != nil {
		t.Fatalf("machine upgrade %s has no active lifecycle for %s: %v", operationID, tenant, err)
	}
	return &types.MachineUpgradeOperation{
		OperationID: operationID, UID: uid, TenantName: tenant, LifecycleID: lifecycleID,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanPro,
		CostUnits:        costCredits * CommercialTopupUnitsPerFen,
		ClientRequestID:  requestID,
		QuoteID:          "mq-" + operationID,
		MachineExpiresAt: expiresAt.UTC(),
	}
}

func TestPostgresMachineUpgradeChargeSettleAndRelease(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-buyer")
	grantTopupCredits(t, db, uid, 10000, "topup:machine-upgrade-1")

	expires := time.Now().UTC().Add(10 * 24 * time.Hour)
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-1", expires)
	op := machineUpgradeOperationForTest(t, db, uid, "mu-charge-settle-1", "bot-upgrade-1", "upgrade-request-1", 7000)
	stored, created, err := db.OpenMachineUpgrade(op)
	if err != nil || !created || stored.Status != types.MachineUpgradeStatusPending {
		t.Fatalf("open machine upgrade: created=%v op=%#v err=%v", created, stored, err)
	}

	charged, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false)
	if err != nil || !proceed || charged.Status != types.MachineUpgradeStatusRunning || !charged.Held {
		t.Fatalf("start machine upgrade charge: proceed=%v op=%#v err=%v", proceed, charged, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("hold must take the cost out of the balance, paid=%d", got)
	}
	// 重复调用：不再扣第二次。
	again, againProceed, err := db.StartMachineUpgradeCharge(op.OperationID, false)
	if err != nil || againProceed || again.Status != types.MachineUpgradeStatusRunning {
		t.Fatalf("a second start must not charge again: proceed=%v err=%v", againProceed, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("a second start changed the balance to %d", got)
	}

	completed, err := db.CompleteMachineUpgrade(op.OperationID, types.MachinePlanPro, types.MachinePlanLight, op.MachineExpiresAt)
	if err != nil || completed.Status != types.MachineUpgradeStatusCompleted || !completed.Settled || completed.Held {
		t.Fatalf("complete machine upgrade: op=%#v err=%v", completed, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("settling must not refund the charge, paid=%d", got)
	}
	term, err := db.MachinePlanTerm("bot-upgrade-1")
	if err != nil || term == nil || term.Plan != types.MachinePlanPro || term.BasePlan != types.MachinePlanLight {
		t.Fatalf("upgrade entitlement was not recorded: %#v err=%v", term, err)
	}
	// 重复完成：幂等。
	repeated, err := db.CompleteMachineUpgrade(op.OperationID, types.MachinePlanPro, types.MachinePlanLight, op.MachineExpiresAt)
	if err != nil || repeated.Status != types.MachineUpgradeStatusCompleted {
		t.Fatalf("repeated completion must be a no-op: %#v err=%v", repeated, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("repeated completion refunded a charge, paid=%d", got)
	}
}

func TestPostgresMachineUpgradeRejectsInsufficientAndArrears(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-poor")
	grantTopupCredits(t, db, uid, 100, "topup:machine-upgrade-poor")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-poor", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-poor-1", "bot-upgrade-poor", "upgrade-poor-1", 7000)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, _, err := db.StartMachineUpgradeCharge(op.OperationID, false); !errors.Is(err, store.ErrModelCreditInsufficient) {
		t.Fatalf("an insufficient balance must be reported as such, got %v", err)
	}
	if got := paidBucketUnits(t, db, uid); got != 100*CommercialTopupUnitsPerFen {
		t.Fatalf("an insufficient charge must not move the balance, paid=%d", got)
	}

	// 欠债账户：即使余额够也不许升配（欠款不能变成新资源）。
	arrearsUID := createCreditTopupTestUser(t, db, "machine-upgrade-arrears")
	grantTopupCredits(t, db, arrearsUID, 20000, "topup:machine-upgrade-arrears")
	if _, err := db.db.Exec(`
		UPDATE model_credit_accounts SET debt_units = $2 WHERE uid = $1`,
		arrearsUID, 500*CommercialTopupUnitsPerFen); err != nil {
		t.Fatalf("seed arrears: %v", err)
	}
	seedMachineUpgradeLifecycle(t, db, arrearsUID, "bot-upgrade-arrears", time.Now().UTC().Add(10*24*time.Hour))
	arrearsOp := machineUpgradeOperationForTest(t, db, arrearsUID, "mu-arrears-1", "bot-upgrade-arrears", "upgrade-arrears-1", 7000)
	if _, _, err := db.OpenMachineUpgrade(arrearsOp); err != nil {
		t.Fatalf("open arrears machine upgrade: %v", err)
	}
	if _, _, err := db.StartMachineUpgradeCharge(arrearsOp.OperationID, false); !errors.Is(err, store.ErrModelCreditInsufficient) {
		t.Fatalf("an account in arrears must be refused, got %v", err)
	}
}

func TestPostgresMachineUpgradeIdempotencyAndOpenTenantConflict(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-idem")
	grantTopupCredits(t, db, uid, 30000, "topup:machine-upgrade-idem")

	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-idem", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-idem-1", "bot-upgrade-idem", "upgrade-idem-1", 7000)
	first, created, err := db.OpenMachineUpgrade(op)
	if err != nil || !created {
		t.Fatalf("open machine upgrade: created=%v err=%v", created, err)
	}
	// 同一个 client_request_id：返回同一条操作，不新建。
	replay, replayCreated, err := db.OpenMachineUpgrade(machineUpgradeOperationForTest(t, db, uid, "mu-idem-2", "bot-upgrade-idem", "upgrade-idem-1", 7000))
	if err != nil || replayCreated || replay.OperationID != first.OperationID {
		t.Fatalf("a replay must return the stored operation: created=%v op=%#v err=%v", replayCreated, replay, err)
	}
	// 同一个 id、不同费用：冲突，绝不能当成第二笔。
	if _, _, err := db.OpenMachineUpgrade(machineUpgradeOperationForTest(t, db, uid, "mu-idem-3", "bot-upgrade-idem", "upgrade-idem-1", 9000)); !errors.Is(err, store.ErrMachineUpgradeConflict) {
		t.Fatalf("the same request id with another cost must conflict, got %v", err)
	}
	// 同一台机器上另一笔未完成的操作：冲突。
	if _, _, err := db.OpenMachineUpgrade(machineUpgradeOperationForTest(t, db, uid, "mu-idem-4", "bot-upgrade-idem", "upgrade-idem-2", 7000)); !errors.Is(err, store.ErrMachineUpgradeConflict) {
		t.Fatalf("a second open operation on the same machine must conflict, got %v", err)
	}
	if open, err := db.MachineUpgradeOpenForTenant("bot-upgrade-idem"); err != nil || !open {
		t.Fatalf("open-for-tenant=%v err=%v want true", open, err)
	}

	// 并发申请同一个优先操作：只能有一个人真正扣费。
	grantTopupCredits(t, db, uid, 30000, "topup:machine-upgrade-concurrent")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-concurrent", time.Now().UTC().Add(10*24*time.Hour))
	concurrent := machineUpgradeOperationForTest(t, db, uid, "mu-concurrent-1", "bot-upgrade-concurrent", "upgrade-concurrent-1", 7000)
	if _, _, err := db.OpenMachineUpgrade(concurrent); err != nil {
		t.Fatalf("open concurrent machine upgrade: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	proceeds := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, proceed, err := db.StartMachineUpgradeCharge(concurrent.OperationID, false)
			if err == nil && proceed {
				mu.Lock()
				proceeds++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if proceeds != 1 {
		t.Fatalf("exactly one concurrent claim may charge, got %d", proceeds)
	}
}

func TestPostgresMachineUpgradeReconcileKeepsHoldUntilKnown(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-reconcile")
	grantTopupCredits(t, db, uid, 10000, "topup:machine-upgrade-reconcile")

	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-reconcile", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-reconcile-1", "bot-upgrade-reconcile", "upgrade-reconcile-1", 7000)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("start machine upgrade: proceed=%v err=%v", proceed, err)
	}
	// 结果未知：保留预占，进入 reconciling。
	if _, err := db.MarkMachineUpgradeReconciling(op.OperationID, "script timed out"); err != nil {
		t.Fatalf("mark reconciling: %v", err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("an unknown result must keep the hold, paid=%d", got)
	}
	// 让这一行看起来「已经很久没动」，reconcile 才会挑到它。
	if _, err := db.db.Exec(`
		UPDATE cloud_worker_machine_upgrades SET updated_at = CURRENT_TIMESTAMP - INTERVAL '10 minutes'
		WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("backdate machine upgrade: %v", err)
	}
	candidates, err := db.ListMachineUpgradesForReconcile(2*time.Minute, 10)
	if err != nil || len(candidates) != 1 || candidates[0].OperationID != op.OperationID {
		t.Fatalf("reconcile must pick the stale operation: %#v err=%v", candidates, err)
	}
	// 重试时允许续跑（预占已在），成功后结算。
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, true); err != nil || !proceed {
		t.Fatalf("reconcile retry must resume the hold: proceed=%v err=%v", proceed, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("resuming must not charge twice, paid=%d", got)
	}
	completed, err := db.CompleteMachineUpgrade(op.OperationID, types.MachinePlanPro, types.MachinePlanLight, op.MachineExpiresAt)
	if err != nil || completed.Status != types.MachineUpgradeStatusCompleted {
		t.Fatalf("reconciled completion: %#v err=%v", completed, err)
	}

	// 明确失败：退预占、进 failed，且绝不写权益。
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-reconcile-2", time.Now().UTC().Add(10*24*time.Hour))
	failedOp := machineUpgradeOperationForTest(t, db, uid, "mu-reconcile-2", "bot-upgrade-reconcile-2", "upgrade-reconcile-2", 1000)
	if _, _, err := db.OpenMachineUpgrade(failedOp); err != nil {
		t.Fatalf("open failing machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(failedOp.OperationID, false); err != nil || !proceed {
		t.Fatalf("start failing machine upgrade: proceed=%v err=%v", proceed, err)
	}
	before := paidBucketUnits(t, db, uid)
	failed, err := db.FailMachineUpgrade(failedOp.OperationID, "provider refused")
	if err != nil || failed.Status != types.MachineUpgradeStatusFailed || failed.Held {
		t.Fatalf("fail machine upgrade: %#v err=%v", failed, err)
	}
	if got := paidBucketUnits(t, db, uid); got != before+1000*CommercialTopupUnitsPerFen {
		t.Fatalf("a failure must release the hold: %d -> %d", before, got)
	}
	if term, err := db.MachinePlanTerm("bot-upgrade-reconcile-2"); err != nil || term != nil {
		t.Fatalf("a failed upgrade must not write an entitlement: %#v err=%v", term, err)
	}
}

func TestPostgresMachinePlanTermNeverDowngrades(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-term")
	expires := time.Now().UTC().Add(10 * 24 * time.Hour)
	lifecycleID := seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-term", expires)
	upgraded := &types.MachineUpgradeOperation{
		OperationID: "mu-term-1", UID: uid, TenantName: "bot-upgrade-term", LifecycleID: lifecycleID,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanPro, CostUnits: 1,
		ClientRequestID: "upgrade-term-1", MachineExpiresAt: expires,
	}
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatalf("begin term write: %v", err)
	}
	if err := upsertMachinePlanTerm(tx, upgraded, types.MachinePlanPro, types.MachinePlanLight, expires, time.Now().UTC()); err != nil {
		tx.Rollback()
		t.Fatalf("write pro term: %v", err)
	}
	// 后续一次「套餐按 light 重新同步」的写入不能把档位降回去。
	lowered := &types.MachineUpgradeOperation{
		OperationID: "mu-term-2", UID: uid, TenantName: "bot-upgrade-term", LifecycleID: lifecycleID,
		FromPlan: types.MachinePlanPro, ToPlan: types.MachinePlanLight, CostUnits: 1,
		ClientRequestID: "upgrade-term-2", MachineExpiresAt: expires,
	}
	if err := upsertMachinePlanTerm(tx, lowered, types.MachinePlanLight, "", expires, time.Now().UTC()); err != nil {
		tx.Rollback()
		t.Fatalf("write light term: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit term writes: %v", err)
	}
	term, err := db.MachinePlanTerm("bot-upgrade-term")
	if err != nil || term == nil || term.Plan != types.MachinePlanPro {
		t.Fatalf("the bought tier must survive a smaller write: %#v err=%v", term, err)
	}
	if term.LifecycleID != lifecycleID || term.OwnerUID != uid {
		t.Fatalf("the term must stay bound to its lifecycle and owner: %#v", term)
	}
	// 没有绑在任何 lifecycle 上的写入（比如旧数据、手工写歪的行）一律不许落库：
	// 否则一次结算就能把档位挂到已经不存在的机器上。
	stray := &types.MachineUpgradeOperation{
		OperationID: "mu-term-3", UID: uid, TenantName: "bot-upgrade-term-stray", LifecycleID: lifecycleID,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanPro, CostUnits: 1,
		ClientRequestID: "upgrade-term-3", MachineExpiresAt: expires,
	}
	if err := upsertMachinePlanTerm(rollbackTx(t, db), stray, types.MachinePlanPro, types.MachinePlanLight, expires, time.Now().UTC()); !errors.Is(err, store.ErrMachineUpgradeConflict) {
		t.Fatalf("a term whose tenant does not match the lifecycle must be refused, got %v", err)
	}
}

// machineUpgradeAccountSnapshot reads every bucket so a test can assert exactly
// which buckets a charge may touch.
func machineUpgradeAccountSnapshot(t *testing.T, db *Adapter, uid int64) *store.ModelCreditAccount {
	t.Helper()
	account, err := db.GetModelCreditAccount(context.Background(), uid)
	if err != nil {
		t.Fatalf("read machine upgrade account %d: %v", uid, err)
	}
	return account
}

func grantMachineUpgradeBucket(t *testing.T, db *Adapter, uid int64, bucket string, credits int64, key string) {
	t.Helper()
	if _, err := db.GrantModelCredits(context.Background(), store.ModelCreditGrantInput{
		UID: uid, Bucket: bucket, Units: credits * CommercialTopupUnitsPerFen, IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("grant %s credit: %v", bucket, err)
	}
}

// TestPostgresMachineUpgradeNeverSpendsGift：升配只花 monthly -> paid。
// gift 是邀请奖励，只能用于模型调用；报价余额同样排除它，否则用户会看到
// 一个自己根本花不出去的余额。
func TestPostgresMachineUpgradeNeverSpendsGift(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	unit := CommercialTopupUnitsPerFen

	// 1. 只有 gift 的账户：报价余额为 0，扣费直接拒绝，gift 一分不动。
	giftUID := createCreditTopupTestUser(t, db, "machine-upgrade-gift-only")
	grantMachineUpgradeBucket(t, db, giftUID, store.ModelCreditBucketGift, 10000, "gift:machine-upgrade-only")
	if units, arrears, err := db.MachineUpgradeCreditBalance(giftUID); err != nil || units != 0 || arrears {
		t.Fatalf("a gift-only balance must not quote as spendable: units=%d arrears=%v err=%v", units, arrears, err)
	}
	seedMachineUpgradeLifecycle(t, db, giftUID, "bot-upgrade-gift-1", time.Now().UTC().Add(10*24*time.Hour))
	giftOp := machineUpgradeOperationForTest(t, db, giftUID, "mu-gift-1", "bot-upgrade-gift-1", "upgrade-gift-1", 7000)
	if _, _, err := db.OpenMachineUpgrade(giftOp); err != nil {
		t.Fatalf("open gift-only machine upgrade: %v", err)
	}
	if _, _, err := db.StartMachineUpgradeCharge(giftOp.OperationID, false); !errors.Is(err, store.ErrModelCreditInsufficient) {
		t.Fatalf("a gift-only balance must be refused, got %v", err)
	}
	if account := machineUpgradeAccountSnapshot(t, db, giftUID); account.GiftUnits != 10000*unit ||
		account.PaidUnits != 0 || account.MonthlyUnits != 0 {
		t.Fatalf("a refused charge must leave every bucket alone: %#v", account)
	}

	// 2. gift + paid 混合但 paid 不够：仍然拒绝，gift 不能补差额，
	//    拒绝之后原来的桶也一个都不能被释放/移动。
	shortUID := createCreditTopupTestUser(t, db, "machine-upgrade-gift-short")
	grantMachineUpgradeBucket(t, db, shortUID, store.ModelCreditBucketPaid, 3000, "topup:machine-upgrade-gift-short")
	grantMachineUpgradeBucket(t, db, shortUID, store.ModelCreditBucketGift, 10000, "gift:machine-upgrade-gift-short")
	if units, _, err := db.MachineUpgradeCreditBalance(shortUID); err != nil || units != 3000*unit {
		t.Fatalf("the quote must exclude gift: units=%d want %d err=%v", units, 3000*unit, err)
	}
	seedMachineUpgradeLifecycle(t, db, shortUID, "bot-upgrade-gift-2", time.Now().UTC().Add(10*24*time.Hour))
	shortOp := machineUpgradeOperationForTest(t, db, shortUID, "mu-gift-2", "bot-upgrade-gift-2", "upgrade-gift-2", 7000)
	if _, _, err := db.OpenMachineUpgrade(shortOp); err != nil {
		t.Fatalf("open mixed machine upgrade: %v", err)
	}
	if _, _, err := db.StartMachineUpgradeCharge(shortOp.OperationID, false); !errors.Is(err, store.ErrModelCreditInsufficient) {
		t.Fatalf("gift must not top up a paid shortfall, got %v", err)
	}
	if account := machineUpgradeAccountSnapshot(t, db, shortUID); account.GiftUnits != 10000*unit ||
		account.PaidUnits != 3000*unit || account.MonthlyUnits != 0 {
		t.Fatalf("a refused charge must not release or move any bucket: %#v", account)
	}

	// 3. monthly + paid 够：按 monthly -> paid 扣，gift 一动不动，结算也不碰它。
	mixedUID := createCreditTopupTestUser(t, db, "machine-upgrade-gift-mixed")
	grantMachineUpgradeBucket(t, db, mixedUID, store.ModelCreditBucketMonthly, 2000, "monthly:machine-upgrade-mixed")
	grantMachineUpgradeBucket(t, db, mixedUID, store.ModelCreditBucketPaid, 6000, "topup:machine-upgrade-mixed")
	grantMachineUpgradeBucket(t, db, mixedUID, store.ModelCreditBucketGift, 5000, "gift:machine-upgrade-mixed")
	seedMachineUpgradeLifecycle(t, db, mixedUID, "bot-upgrade-gift-3", time.Now().UTC().Add(10*24*time.Hour))
	mixedOp := machineUpgradeOperationForTest(t, db, mixedUID, "mu-gift-3", "bot-upgrade-gift-3", "upgrade-gift-3", 7000)
	if _, _, err := db.OpenMachineUpgrade(mixedOp); err != nil {
		t.Fatalf("open mixed machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(mixedOp.OperationID, false); err != nil || !proceed {
		t.Fatalf("start mixed machine upgrade: proceed=%v err=%v", proceed, err)
	}
	account := machineUpgradeAccountSnapshot(t, db, mixedUID)
	if account.MonthlyUnits != 0 || account.PaidUnits != 1000*unit || account.GiftUnits != 5000*unit {
		t.Fatalf("the hold must drain monthly then paid and never gift: %#v", account)
	}
	var giftEvents int
	if err := db.db.QueryRow(`
		SELECT COUNT(*) FROM model_credit_ledger_events
		WHERE uid = $1 AND request_id = $2 AND bucket = 'gift'`,
		mixedUID, mixedOp.OperationID).Scan(&giftEvents); err != nil {
		t.Fatalf("count gift ledger events: %v", err)
	}
	if giftEvents != 0 {
		t.Fatalf("a machine upgrade must never book a gift ledger event, got %d", giftEvents)
	}
	if completed, err := db.CompleteMachineUpgrade(mixedOp.OperationID, types.MachinePlanPro,
		types.MachinePlanLight, mixedOp.MachineExpiresAt); err != nil ||
		completed.Status != types.MachineUpgradeStatusCompleted {
		t.Fatalf("complete mixed machine upgrade: %#v err=%v", completed, err)
	}
	if account := machineUpgradeAccountSnapshot(t, db, mixedUID); account.GiftUnits != 5000*unit ||
		account.PaidUnits != 1000*unit || account.MonthlyUnits != 0 {
		t.Fatalf("settling must not refund or spend gift: %#v", account)
	}
}

// TestPostgresMachineUpgradeReconcileClaimIsLeased：allowRetry 只属于 reconciler，
// 而且必须在行锁之后确认 updated_at 已过保护期，否则两个 reconciler 会同时
// 跑同一个改档脚本（以及重复结算）。
func TestPostgresMachineUpgradeReconcileClaimIsLeased(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-lease")
	grantTopupCredits(t, db, uid, 10000, "topup:machine-upgrade-lease")
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-lease", time.Now().UTC().Add(10*24*time.Hour))
	op := machineUpgradeOperationForTest(t, db, uid, "mu-lease-1", "bot-upgrade-lease", "upgrade-lease-1", 7000)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	started, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false)
	if err != nil || !proceed || started.Attempts != 1 {
		t.Fatalf("start machine upgrade: proceed=%v op=%#v err=%v", proceed, started, err)
	}
	// 刚认领完（updated_at 还在保护期内）：另一个 reconciler 必须放手。
	if again, retryProceed, err := db.StartMachineUpgradeCharge(op.OperationID, true); err != nil || retryProceed {
		t.Fatalf("a fresh claim must not be retried: proceed=%v op=%#v err=%v", retryProceed, again, err)
	}
	current, err := db.GetMachineUpgrade(uid, op.OperationID)
	if err != nil || current == nil || current.Attempts != 1 || current.Status != types.MachineUpgradeStatusRunning {
		t.Fatalf("a refused retry must not touch the row: %#v err=%v", current, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("a refused retry must not move the balance, paid=%d", got)
	}

	// 保护期过了：并发的两个 reconciler 只能有一个认领成功。
	if _, err := db.db.Exec(`
		UPDATE cloud_worker_machine_upgrades SET updated_at = CURRENT_TIMESTAMP - INTERVAL '10 minutes'
		WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("backdate machine upgrade: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, true)
			if err == nil && proceed {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claims != 1 {
		t.Fatalf("exactly one reconciler may claim a stale hold, got %d", claims)
	}
	if current, err := db.GetMachineUpgrade(uid, op.OperationID); err != nil || current == nil || current.Attempts != 2 {
		t.Fatalf("the claim must be counted exactly once: %#v err=%v", current, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 3000*CommercialTopupUnitsPerFen {
		t.Fatalf("resuming must not charge twice, paid=%d", got)
	}
	if completed, err := db.CompleteMachineUpgrade(op.OperationID, types.MachinePlanPro,
		types.MachinePlanLight, op.MachineExpiresAt); err != nil ||
		completed.Status != types.MachineUpgradeStatusCompleted {
		t.Fatalf("complete leased machine upgrade: %#v err=%v", completed, err)
	}
}

// TestPostgresMachineUpgradeTerminalStatesAreFinal：pending 从没付过钱、failed 的
// 预占已经退回去了，两者都不能被结算成免费升配；终态读回必须按 operation id
// 拿到真实行，而不是「uid=0 查不到」。
func TestPostgresMachineUpgradeTerminalStatesAreFinal(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-terminal")
	grantTopupCredits(t, db, uid, 20000, "topup:machine-upgrade-terminal")
	unit := CommercialTopupUnitsPerFen

	// pending：allowRetry 不许直接扣款，reconcile 必须能列出它交给取消逻辑。
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-terminal-1", time.Now().UTC().Add(10*24*time.Hour))
	pendingOp := machineUpgradeOperationForTest(t, db, uid, "mu-terminal-1", "bot-upgrade-terminal-1", "upgrade-terminal-1", 7000)
	if _, _, err := db.OpenMachineUpgrade(pendingOp); err != nil {
		t.Fatalf("open pending machine upgrade: %v", err)
	}
	if _, err := db.db.Exec(`
		UPDATE cloud_worker_machine_upgrades SET updated_at = CURRENT_TIMESTAMP - INTERVAL '10 minutes'
		WHERE operation_id = $1`, pendingOp.OperationID); err != nil {
		t.Fatalf("backdate pending machine upgrade: %v", err)
	}
	candidates, err := db.ListMachineUpgradesForReconcile(2*time.Minute, 10)
	if err != nil {
		t.Fatalf("list machine upgrades for reconcile: %v", err)
	}
	listedPending := false
	for _, candidate := range candidates {
		if candidate.OperationID == pendingOp.OperationID && candidate.Status == types.MachineUpgradeStatusPending {
			listedPending = true
		}
	}
	if !listedPending {
		t.Fatalf("reconcile must list the stale pending operation: %#v", candidates)
	}
	if retried, proceed, err := db.StartMachineUpgradeCharge(pendingOp.OperationID, true); err != nil || proceed ||
		retried.Status != types.MachineUpgradeStatusPending {
		t.Fatalf("allowRetry must not charge a pending operation: proceed=%v op=%#v err=%v", proceed, retried, err)
	}
	if _, err := db.CompleteMachineUpgrade(pendingOp.OperationID, types.MachinePlanPro,
		types.MachinePlanLight, pendingOp.MachineExpiresAt); err == nil {
		t.Fatal("a pending operation must not be settled into a free upgrade")
	}
	if got := paidBucketUnits(t, db, uid); got != 20000*unit {
		t.Fatalf("a pending operation must not move the balance, paid=%d", got)
	}
	if term, err := db.MachinePlanTerm("bot-upgrade-terminal-1"); err != nil || term != nil {
		t.Fatalf("a pending operation must not write an entitlement: %#v err=%v", term, err)
	}

	// failed：预占已退回，结算必须被拒；读回仍要拿到 failed 行本身。
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-terminal-2", time.Now().UTC().Add(10*24*time.Hour))
	failedOp := machineUpgradeOperationForTest(t, db, uid, "mu-terminal-2", "bot-upgrade-terminal-2", "upgrade-terminal-2", 1000)
	if _, _, err := db.OpenMachineUpgrade(failedOp); err != nil {
		t.Fatalf("open failing machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(failedOp.OperationID, false); err != nil || !proceed {
		t.Fatalf("start failing machine upgrade: proceed=%v err=%v", proceed, err)
	}
	if failed, err := db.FailMachineUpgrade(failedOp.OperationID, "provider refused"); err != nil ||
		failed.Status != types.MachineUpgradeStatusFailed {
		t.Fatalf("fail machine upgrade: %#v err=%v", failed, err)
	}
	if _, err := db.CompleteMachineUpgrade(failedOp.OperationID, types.MachinePlanPro,
		types.MachinePlanLight, failedOp.MachineExpiresAt); err == nil {
		t.Fatal("a failed operation must not be settled")
	}
	if term, err := db.MachinePlanTerm("bot-upgrade-terminal-2"); err != nil || term != nil {
		t.Fatalf("a failed operation must not write an entitlement: %#v err=%v", term, err)
	}
	readBack, err := db.MarkMachineUpgradeReconciling(failedOp.OperationID, "late sweep")
	if err != nil || readBack == nil || readBack.Status != types.MachineUpgradeStatusFailed {
		t.Fatalf("a terminal read-back must return the failed row: %#v err=%v", readBack, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 20000*unit {
		t.Fatalf("terminal handling must not refund or charge twice, paid=%d", got)
	}
}

// TestPostgresMachinePlanTermStopsAtPackageExpiry：花钱买到的升配权益只能活到套餐
// 到期时间。套餐缩期（退款）后权益跟着缩短，套餐过期后立刻不在效——不能让一次
// 升配换来一段比套餐还长的档位。
func TestPostgresMachinePlanTermStopsAtPackageExpiry(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-shrink")
	grantTopupCredits(t, db, uid, 10000, "topup:machine-upgrade-shrink")
	packageExpiry := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Second)
	lifecycleID := seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-shrink", packageExpiry)

	op := &types.MachineUpgradeOperation{
		OperationID: "mu-shrink-1", UID: uid, TenantName: "bot-upgrade-shrink", LifecycleID: lifecycleID,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanPro,
		CostUnits: 1000 * CommercialTopupUnitsPerFen, ClientRequestID: "upgrade-shrink-1",
		QuoteID: "mq-mu-shrink-1",
		// 报价上的机器 term 比套餐还长（脏数据或还没落地的续费）：权益不能用它。
		MachineExpiresAt: packageExpiry.Add(20 * 24 * time.Hour),
	}
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("start machine upgrade: proceed=%v err=%v", proceed, err)
	}
	if _, err := db.CompleteMachineUpgrade(op.OperationID, types.MachinePlanPro, types.MachinePlanLight, op.MachineExpiresAt); err != nil {
		t.Fatalf("complete machine upgrade: %v", err)
	}
	term, err := db.MachinePlanTerm("bot-upgrade-shrink")
	if err != nil || term == nil || term.Plan != types.MachinePlanPro {
		t.Fatalf("the upgrade must be recorded: %#v err=%v", term, err)
	}
	if !term.ExpiresAt.Equal(packageExpiry) {
		t.Fatalf("term expires at %s, want the package expiry %s", term.ExpiresAt.UTC(), packageExpiry)
	}

	// 套餐缩期（退款）：lifecycle 的 package_expires_at 被改短，权益必须跟着缩短。
	shortened := time.Now().UTC().Add(2 * 24 * time.Hour).Truncate(time.Second)
	if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET package_expires_at = $2 WHERE id = $1`,
		lifecycleID, shortened); err != nil {
		t.Fatalf("shorten package: %v", err)
	}
	term, err = db.MachinePlanTerm("bot-upgrade-shrink")
	if err != nil || term == nil {
		t.Fatalf("a shortened package must still answer with the capped term: %#v err=%v", term, err)
	}
	if !term.ExpiresAt.Equal(shortened) {
		t.Fatalf("term expires at %s after the package was shortened to %s", term.ExpiresAt.UTC(), shortened)
	}

	// 套餐过期：权益不再在效，谁也不能拿它当「当前档位」。
	if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET package_expires_at = CURRENT_TIMESTAMP - INTERVAL '1 minute' WHERE id = $1`,
		lifecycleID); err != nil {
		t.Fatalf("expire package: %v", err)
	}
	if term, err := db.MachinePlanTerm("bot-upgrade-shrink"); err != nil || term != nil {
		t.Fatalf("an expired package must not keep the upgrade in force: %#v err=%v", term, err)
	}
}

// TestPostgresMachinePlanTermNotInheritedByReusedTenant：删机、或者 tenant 名被另一条
// lifecycle 复用之后，老机器花钱买到的档位不能被新机器/新主人继承。
func TestPostgresMachinePlanTermNotInheritedByReusedTenant(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	oldOwner := createCreditTopupTestUser(t, db, "machine-reuse-old-owner")
	grantTopupCredits(t, db, oldOwner, 20000, "topup:machine-reuse-old")
	oldExpiry := time.Now().UTC().Add(20 * 24 * time.Hour).Truncate(time.Second)
	oldLifecycle := seedMachineUpgradeLifecycle(t, db, oldOwner, "bot-reuse-term", oldExpiry)

	oldOp := &types.MachineUpgradeOperation{
		OperationID: "mu-reuse-1", UID: oldOwner, TenantName: "bot-reuse-term", LifecycleID: oldLifecycle,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanPro,
		CostUnits: 1000 * CommercialTopupUnitsPerFen, ClientRequestID: "upgrade-reuse-1",
		QuoteID: "mq-mu-reuse-1", MachineExpiresAt: oldExpiry,
	}
	if _, _, err := db.OpenMachineUpgrade(oldOp); err != nil {
		t.Fatalf("open old machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(oldOp.OperationID, false); err != nil || !proceed {
		t.Fatalf("start old machine upgrade: proceed=%v err=%v", proceed, err)
	}
	if _, err := db.CompleteMachineUpgrade(oldOp.OperationID, types.MachinePlanPro, types.MachinePlanLight,
		oldExpiry); err != nil {
		t.Fatalf("complete old machine upgrade: %v", err)
	}
	if term, err := db.MachinePlanTerm("bot-reuse-term"); err != nil || term == nil || term.Plan != types.MachinePlanPro {
		t.Fatalf("the old owner's upgrade must be in force: %#v err=%v", term, err)
	}

	// 删机：lifecycle 进入 deleted，权益立刻不在效。
	if _, err := db.db.Exec(`UPDATE cloud_worker_lifecycles SET state = 'deleted' WHERE id = $1`, oldLifecycle); err != nil {
		t.Fatalf("delete old lifecycle: %v", err)
	}
	if term, err := db.MachinePlanTerm("bot-reuse-term"); err != nil || term != nil {
		t.Fatalf("a deleted machine must not keep its entitlement: %#v err=%v", term, err)
	}

	// tenant 名复用：老 lifecycle 行被清掉，同一个 tenant 名换了一台新机器、一个新主人。
	if _, err := db.db.Exec(`DELETE FROM cloud_worker_lifecycles WHERE id = $1`, oldLifecycle); err != nil {
		t.Fatalf("purge old lifecycle: %v", err)
	}
	newOwner := createCreditTopupTestUser(t, db, "machine-reuse-new-owner")
	grantTopupCredits(t, db, newOwner, 20000, "topup:machine-reuse-new")
	newExpiry := time.Now().UTC().Add(5 * 24 * time.Hour).Truncate(time.Second)
	newLifecycle := seedMachineUpgradeLifecycle(t, db, newOwner, "bot-reuse-term", newExpiry)
	if term, err := db.MachinePlanTerm("bot-reuse-term"); err != nil || term != nil {
		t.Fatalf("a reused tenant name must not inherit the old tier: %#v err=%v", term, err)
	}
	if term, err := db.MachinePlanTermForOwner(newOwner, "bot-reuse-term"); err != nil || term != nil {
		t.Fatalf("the new owner must not inherit the old tier: %#v err=%v", term, err)
	}
	if term, err := db.MachinePlanTermForOwner(oldOwner, "bot-reuse-term"); err != nil || term != nil {
		t.Fatalf("the old owner must not read a machine it no longer has: %#v err=%v", term, err)
	}

	// 新主人自己买一档：写进去的必须是这次买的档和这次租期，不是旧的 pro / 20 天。
	newOp := &types.MachineUpgradeOperation{
		OperationID: "mu-reuse-2", UID: newOwner, TenantName: "bot-reuse-term", LifecycleID: newLifecycle,
		FromPlan: types.MachinePlanLight, ToPlan: types.MachinePlanStandard,
		CostUnits: 1000 * CommercialTopupUnitsPerFen, ClientRequestID: "upgrade-reuse-2",
		QuoteID: "mq-mu-reuse-2", MachineExpiresAt: newExpiry,
	}
	if _, _, err := db.OpenMachineUpgrade(newOp); err != nil {
		t.Fatalf("open new owner machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(newOp.OperationID, false); err != nil || !proceed {
		t.Fatalf("start new owner machine upgrade: proceed=%v err=%v", proceed, err)
	}
	if _, err := db.CompleteMachineUpgrade(newOp.OperationID, types.MachinePlanStandard,
		types.MachinePlanLight, newExpiry); err != nil {
		t.Fatalf("complete new owner machine upgrade: %v", err)
	}
	term, err := db.MachinePlanTermForOwner(newOwner, "bot-reuse-term")
	if err != nil || term == nil {
		t.Fatalf("the new owner's own upgrade must be in force: %#v err=%v", term, err)
	}
	if term.Plan != types.MachinePlanStandard || term.LifecycleID != newLifecycle ||
		term.OwnerUID != newOwner || !term.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("the reused tenant inherited something from the old machine: %#v", term)
	}
}

// TestPostgresCreditTopupRefundBlockedByMachineUpgradeHold：进行中的机器升配在 paid
// 桶里预占着点数，这笔钱正在花，退款必须被拒；升配明确失败释放预占之后才能退。
func TestPostgresCreditTopupRefundBlockedByMachineUpgradeHold(t *testing.T) {
	db := newMachineUpgradeTestAdapter(t)
	unit := CommercialTopupUnitsPerFen
	uid := createCreditTopupTestUser(t, db, "machine-upgrade-refund")
	order, err := db.CreateCommercialTopupOrder(uid, 5000, "alipay_page", "topup-request-upgrade-refund-1", nil)
	if err != nil {
		t.Fatalf("create credit top-up order: %v", err)
	}
	confirmation := &types.CommercialPaymentConfirmation{
		Channel: "alipay_page", EventID: "topup-event-upgrade-refund-1", ProviderTradeNo: "TOPUP-UPGRADE-REFUND-1",
		AmountFen: 5000, Currency: "CNY", PaidAt: time.Now().UTC(), PayloadHash: strings.Repeat("f", 64),
	}
	if _, _, err := db.FulfillCommercialOrder(order.OrderNo, confirmation); err != nil {
		t.Fatalf("fulfil credit top-up: %v", err)
	}
	expires := time.Now().UTC().Add(10 * 24 * time.Hour)
	seedMachineUpgradeLifecycle(t, db, uid, "bot-upgrade-refund", expires)
	op := machineUpgradeOperationForTest(t, db, uid, "mu-refund-1", "bot-upgrade-refund", "upgrade-refund-1", 3000)
	if _, _, err := db.OpenMachineUpgrade(op); err != nil {
		t.Fatalf("open machine upgrade: %v", err)
	}
	if _, proceed, err := db.StartMachineUpgradeCharge(op.OperationID, false); err != nil || !proceed {
		t.Fatalf("start machine upgrade: proceed=%v err=%v", proceed, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 2000*unit {
		t.Fatalf("the open upgrade must hold its cost out of the balance, paid=%d", got)
	}

	// 预占还在：退款被明确拒绝，钱一分不动。
	if _, _, err := db.BeginCommercialOrderRefund(order.OrderNo, "refund-upgrade-1", time.Minute); err != nil {
		t.Fatalf("claim refund: %v", err)
	}
	refundConfirmation := &types.CommercialRefundConfirmation{
		Channel: "alipay_page", EventID: "topup-refund-upgrade-1", ProviderTradeNo: "TOPUP-UPGRADE-REFUND-1",
		RefundRequestNo: "refund-upgrade-1", AmountFen: 5000, Currency: "CNY",
		RefundedAt: time.Now().UTC(), PayloadHash: strings.Repeat("1", 64),
	}
	_, _, err = db.CompleteCommercialOrderRefund(order.OrderNo, refundConfirmation)
	if !errors.Is(err, ErrCommercialOrderCreditSpent) {
		t.Fatalf("a refund must be refused while an upgrade holds paid credit, got %v", err)
	}
	if got := paidBucketUnits(t, db, uid); got != 2000*unit {
		t.Fatalf("a refused refund must not move the balance, paid=%d", got)
	}
	if stored, err := db.GetCommercialOrder(0, order.OrderNo); err != nil || stored == nil || stored.Status == "refunded" {
		t.Fatalf("a refused refund must not settle the order: %#v err=%v", stored, err)
	}

	// 升配明确失败 → 预占退回 → 同一笔订单现在可以退。
	if failed, err := db.FailMachineUpgrade(op.OperationID, "provider refused"); err != nil ||
		failed.Status != types.MachineUpgradeStatusFailed {
		t.Fatalf("fail machine upgrade: %#v err=%v", failed, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 5000*unit {
		t.Fatalf("a released hold must give the credit back, paid=%d", got)
	}
	refundConfirmation.EventID = "topup-refund-upgrade-2"
	refunded, changed, err := db.CompleteCommercialOrderRefund(order.OrderNo, refundConfirmation)
	if err != nil || !changed || refunded == nil || refunded.Status != "refunded" {
		t.Fatalf("a refund must work once the hold is released: changed=%v order=%#v err=%v",
			changed, refunded, err)
	}
	if got := paidBucketUnits(t, db, uid); got != 0 {
		t.Fatalf("a completed refund must reverse the paid bucket, paid=%d", got)
	}
}

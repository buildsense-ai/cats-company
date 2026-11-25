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
)

// Cats 算力任务的契约：
//   - 报价是写一次就不能改的快照，重放同一条款是幂等的，换条款是冲突；
//   - 确认才预占（整笔上限，monthly -> paid，gift 不动，欠债拒绝），重放不二次扣费；
//   - 领任务给唯一租约，没有可领的任务返回 (nil, nil)；
//   - 保存/结束都被租约 + 期限 fence 住，旧 worker 改不动别人的任务；
//   - 结束按实际用量结算、未用部分按批次退回；逐字相同的终态重放不动钱，冲突重放被拒。

// computeTaskMigrationSQL 读指定方向的迁移脚本。
func computeTaskMigrationSQL(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000083_compute_tasks." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stripSQLComments drops `--` lines so the numbered migration can carry a header
// the startup constant does not repeat.
func stripSQLComments(script string) string {
	lines := strings.Split(script, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(strings.Fields(strings.Join(kept, "\n")), " ")
}

// The startup constant and the numbered migration must stay one script: a fresh
// CreateSchema and an adopted database must end up with the same tables.
func TestComputeTaskMigrationMatchesStartupSchema(t *testing.T) {
	up := stripSQLComments(computeTaskMigrationSQL(t, "up"))
	want := stripSQLComments(createComputeTaskTables)
	if up != want {
		t.Fatalf("000083_compute_tasks.up.sql differs from createComputeTaskTables:\n got: %s\nwant: %s", up, want)
	}
	down := computeTaskMigrationSQL(t, "down")
	for _, dropped := range []string{
		"DROP TABLE IF EXISTS compute_tasks;",
		"DROP TABLE IF EXISTS compute_task_quotes;",
		"DROP FUNCTION IF EXISTS guard_compute_task_quote();",
	} {
		if !strings.Contains(down, dropped) {
			t.Fatalf("down migration is missing %q", dropped)
		}
	}
	tasks, quotes := strings.Index(down, "compute_tasks"), strings.Index(down, "compute_task_quotes")
	if tasks < 0 || quotes < 0 || tasks > quotes {
		t.Fatal("down migration must drop compute_tasks before compute_task_quotes")
	}
	source, err := os.ReadFile("schema.go")
	if err != nil {
		t.Fatal(err)
	}
	statements := string(source)
	start := strings.Index(statements, "statements := []string{")
	if start < 0 {
		t.Fatal("schema.go has no CreateSchema statement list")
	}
	list := statements[start:]
	end := strings.Index(list, "\n\t}\n")
	if end < 0 || !strings.Contains(list[:end], "\t\tcreateComputeTaskTables,") {
		t.Fatal("CreateSchema does not run createComputeTaskTables")
	}
}

// TestComputeTaskQuoteValidation keeps the offer that never reaches the database
// from being silently different from the offer that does.
func TestComputeTaskQuoteValidation(t *testing.T) {
	quote := testComputeTaskQuote("hpc_0123456789abcdef0123456789abcdef", 300*store.ModelCreditUnitsPerCredit)
	if _, err := encodeComputeTaskQuote(1, quote); err != nil {
		t.Fatalf("a well formed quote must encode: %v", err)
	}
	for name, mutate := range map[string]func(*store.ComputeTaskQuote){
		"short id":     func(q *store.ComputeTaskQuote) { q.ID = "hpc_1" },
		"no SKU":       func(q *store.ComputeTaskQuote) { q.SKU = "  " },
		"no wall time": func(q *store.ComputeTaskQuote) { q.Spec.WallSeconds = 0 },
		"no increment": func(q *store.ComputeTaskQuote) { q.Rate.IncrementSeconds = 0 },
		"no ceiling":   func(q *store.ComputeTaskQuote) { q.MaxUnits = 0 },
		"no expiry":    func(q *store.ComputeTaskQuote) { q.ExpiresAt = time.Time{} },
	} {
		broken := quote
		mutate(&broken)
		if _, err := encodeComputeTaskQuote(1, broken); !errors.Is(err, store.ErrComputeTaskConflict) {
			t.Fatalf("%s must be refused, got %v", name, err)
		}
	}
	if _, err := encodeComputeTaskQuote(0, quote); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a quote without an owner must be refused, got %v", err)
	}
	for _, token := range []string{"hpc_0123456789abcdef0123456789abcdef", strings.Repeat("a", 64)} {
		if !validComputeTaskToken(token) {
			t.Fatalf("%q must be a valid compute task token", token)
		}
	}
	for _, token := range []string{"", "short", "hpc 1", "../etc/passwd", strings.Repeat("a", 65), "-hpc_0123456789abcdef"} {
		if validComputeTaskToken(token) {
			t.Fatalf("%q must not be a valid compute task token", token)
		}
	}
}

func testComputeTaskQuote(id string, maxUnits int64) store.ComputeTaskQuote {
	return store.ComputeTaskQuote{
		ID:      id,
		Spec:    store.ComputeTaskSpec{Nodes: 1, LogicalCPUs: 2, MemoryMiB: 2048, WallSeconds: 120},
		SKU:     "cats-cpu-2x4",
		SKUCPUs: 2, SKUMemoryMiB: 4096,
		Rate: store.ComputeTaskRate{
			UnitsPerNodePeriod: store.ModelCreditUnitsPerCredit,
			PeriodSeconds:      3600,
			IncrementSeconds:   60,
		},
		MaxUnits:       maxUnits,
		MaxNodeSeconds: 1400,
		ExpiresAt:      time.Now().UTC().Add(5 * time.Minute),
	}
}

func newComputeTaskTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_compute_task_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	t.Cleanup(func() { base.Close() })
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create compute task test schema: %v", err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`) })
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open compute task test schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create compute task schema objects: %v", err)
	}
	testPostgresMigrationFiles(t, db)
	return db
}

func grantComputeTaskCredits(t *testing.T, db *Adapter, uid int64, bucket string, credits int64, key string) {
	t.Helper()
	if _, err := db.GrantModelCredits(context.Background(), store.ModelCreditGrantInput{
		UID: uid, Bucket: bucket, Units: credits * store.ModelCreditUnitsPerCredit, IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("grant %s credit: %v", bucket, err)
	}
}

func computeTaskAccount(t *testing.T, db *Adapter, uid int64) *store.ModelCreditAccount {
	t.Helper()
	account, err := db.GetModelCreditAccount(context.Background(), uid)
	if err != nil {
		t.Fatalf("read compute task account %d: %v", uid, err)
	}
	return account
}

func countComputeTaskLedgerEvents(t *testing.T, db *Adapter, uid int64, requestID, eventType, bucket string) int {
	t.Helper()
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_ledger_events
		WHERE uid = $1 AND request_id = $2 AND event_type = $3 AND bucket = $4`,
		uid, requestID, eventType, bucket).Scan(&count); err != nil {
		t.Fatalf("count compute task ledger events: %v", err)
	}
	return count
}

// TestPostgresComputeTaskReserveSettleAndRelease 走完整条链路：报价 -> 确认（整笔
// 预占）-> 领取 -> 保存 -> 结束（按实际用量结算 + 按批次退回）。
func TestPostgresComputeTaskReserveSettleAndRelease(t *testing.T) {
	db := newComputeTaskTestAdapter(t)
	ctx := context.Background()
	unit := store.ModelCreditUnitsPerCredit
	uid := createCreditTopupTestUser(t, db, "compute-task-buyer")
	other := createCreditTopupTestUser(t, db, "compute-task-stranger")
	grantComputeTaskCredits(t, db, uid, store.ModelCreditBucketPaid, 1000, "topup:compute-task")
	grantComputeTaskCredits(t, db, uid, store.ModelCreditBucketMonthly, 200, "monthly:compute-task")
	grantComputeTaskCredits(t, db, uid, store.ModelCreditBucketGift, 50, "gift:compute-task")

	quote := testComputeTaskQuote("hpc_0123456789abcdef0123456789abcdef", 300*unit)
	if err := db.CreateComputeTaskQuote(ctx, uid, quote); err != nil {
		t.Fatalf("create compute task quote: %v", err)
	}
	// 同一条款重放是幂等的；换一个上限就是另一笔交易。
	if err := db.CreateComputeTaskQuote(ctx, uid, quote); err != nil {
		t.Fatalf("replaying the same quote must be idempotent: %v", err)
	}
	repriced := quote
	repriced.MaxUnits = quote.MaxUnits - unit
	if err := db.CreateComputeTaskQuote(ctx, uid, repriced); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("repricing a stored quote must be refused, got %v", err)
	}

	// 读取是按 uid 限定的：别人拿到同一个 id 什么也读不到。
	if run, err := db.GetComputeTask(ctx, other, quote.ID); err != nil || run != nil {
		t.Fatalf("another account must not read the quote: run=%#v err=%v", run, err)
	}
	quoted, err := db.GetComputeTask(ctx, uid, quote.ID)
	if err != nil || quoted == nil {
		t.Fatalf("read quoted task: %#v %v", quoted, err)
	}
	if quoted.State != store.ComputeTaskQuoted || quoted.ConfirmedAt != nil || quoted.ID != quote.ID ||
		quoted.Spec != quote.Spec || quoted.MaxUnits != quote.MaxUnits || quoted.Rate != quote.Rate {
		t.Fatalf("quoted run does not echo the offer: %#v", quoted)
	}
	if claimed, err := db.ClaimComputeTask(ctx, "hpc_33333333333333333333333333333333", time.Now(), time.Now().Add(time.Minute)); err != nil || claimed != nil {
		t.Fatalf("an unconfirmed quote must not be claimable: %#v %v", claimed, err)
	}
	if _, err := db.ConfirmComputeTask(ctx, other, quote.ID, quote.MaxUnits, time.Now()); !errors.Is(err, store.ErrComputeTaskNotFound) {
		t.Fatalf("another account must not confirm the quote: %v", err)
	}

	// 回显的上限必须和报价逐字相等。
	if _, err := db.ConfirmComputeTask(ctx, uid, quote.ID, quote.MaxUnits-unit, time.Now().UTC()); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a mismatched confirmed ceiling must be refused, got %v", err)
	}
	if account := computeTaskAccount(t, db, uid); account.PaidUnits != 1000*unit || account.MonthlyUnits != 200*unit {
		t.Fatalf("a refused confirmation must not touch the balance: %#v", account)
	}

	now := time.Now().UTC()
	queued, err := db.ConfirmComputeTask(ctx, uid, quote.ID, quote.MaxUnits, now)
	if err != nil || queued == nil {
		t.Fatalf("confirm compute task: %#v %v", queued, err)
	}
	if queued.State != store.ComputeTaskQueued || queued.ConfirmedAt == nil {
		t.Fatalf("confirmed task must be queued with a timestamp: %#v", queued)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, uid); err == nil || !strings.Contains(err.Error(), "compute task must settle before deletion") {
		t.Fatalf("deleting the account must preserve a queued task for worker cleanup: %v", err)
	}
	// 整笔上限预占：monthly 200 先扣光，再扣 100 的 paid；gift 永远不动。
	if account := computeTaskAccount(t, db, uid); account.MonthlyUnits != 0 || account.PaidUnits != 900*unit || account.GiftUnits != 50*unit {
		t.Fatalf("confirmation must hold monthly then paid and leave gift alone: %#v", account)
	}
	// 重放确认不二次扣费。
	if replay, err := db.ConfirmComputeTask(ctx, uid, quote.ID, quote.MaxUnits, now.Add(time.Minute)); err != nil ||
		replay == nil || replay.State != store.ComputeTaskQueued {
		t.Fatalf("replayed confirmation must return the same task: %#v %v", replay, err)
	}
	if account := computeTaskAccount(t, db, uid); account.MonthlyUnits != 0 || account.PaidUnits != 900*unit {
		t.Fatalf("replayed confirmation must not reserve twice: %#v", account)
	}
	if events := countComputeTaskLedgerEvents(t, db, uid, quote.ID, store.ModelCreditEventReserve, store.ModelCreditBucketMonthly); events != 1 {
		t.Fatalf("confirmation must book exactly one monthly reserve event, got %d", events)
	}

	// 领取：只有 queued 的任务可以被领走，租约写进返回行。
	lease := "hpc_ffffffffffffffffffffffffffffffff"
	active, err := db.ClaimComputeTask(ctx, lease, now, now.Add(45*time.Second))
	if err != nil || active == nil {
		t.Fatalf("claim compute task: %#v %v", active, err)
	}
	if active.State != store.ComputeTaskActive || active.Lease != lease || active.ID != quote.ID {
		t.Fatalf("claimed task must carry the lease: %#v", active)
	}
	if idle, err := db.ClaimComputeTask(ctx, "hpc_11111111111111111111111111111111", now, now.Add(45*time.Second)); err != nil || idle != nil {
		t.Fatalf("nothing else is claimable: run=%#v err=%v", idle, err)
	}

	// 保存进度被租约 fence：别人的租约改不动这一行。
	if err := db.SaveComputeTaskProgress(ctx, quote.ID, "hpc_22222222222222222222222222222222",
		store.ComputeTaskProgress{Phase: "stolen"}); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a foreign lease must not save progress, got %v", err)
	}
	if err := db.SaveComputeTaskProgress(ctx, quote.ID, lease,
		store.ComputeTaskProgress{Phase: "provisioning", SlurmJobID: "job-7"}); err != nil {
		t.Fatalf("save progress: %v", err)
	}
	if run, err := db.GetComputeTask(ctx, uid, quote.ID); err != nil || run.Progress.SlurmJobID != "job-7" {
		t.Fatalf("saved progress must be readable: %#v %v", run, err)
	}

	// 结束：别人的租约被拒，真正的租约按实际用量结算。
	final := store.ComputeTaskProgress{Phase: "released", SlurmJobID: "job-7", SlurmExitCode: "0:0", ReleasedAt: &now}
	if err := db.FinishComputeTask(ctx, quote.ID, "hpc_22222222222222222222222222222222",
		final, 120*unit, true, now); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a foreign lease must not finish the task, got %v", err)
	}
	if err := db.FinishComputeTask(ctx, quote.ID, lease, final, quote.MaxUnits+unit, true, now); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a charge above the ceiling must be refused, got %v", err)
	}
	if err := db.FinishComputeTask(ctx, quote.ID, lease, final, 120*unit, true, now); err != nil {
		t.Fatalf("finish compute task: %v", err)
	}
	// 消费 120：monthly 付 120，退回 80 monthly 与 100 paid，余额 = 1200 - 120。
	if account := computeTaskAccount(t, db, uid); account.MonthlyUnits != 80*unit || account.PaidUnits != 1000*unit ||
		account.GiftUnits != 50*unit || account.DebtUnits != 0 {
		t.Fatalf("finish must charge the measured units and return the rest: %#v", account)
	}
	done, err := db.GetComputeTask(ctx, uid, quote.ID)
	if err != nil || done == nil || done.State != store.ComputeTaskCompleted ||
		done.ChargedUnits == nil || *done.ChargedUnits != 120*unit || done.Lease != "" {
		t.Fatalf("finished task must carry the charge and drop the lease: %#v %v", done, err)
	}
	if events := countComputeTaskLedgerEvents(t, db, uid, quote.ID, store.ModelCreditEventSettle, store.ModelCreditBucketMonthly); events != 1 {
		t.Fatalf("finish must book one monthly settle event, got %d", events)
	}
	if events := countComputeTaskLedgerEvents(t, db, uid, quote.ID, store.ModelCreditEventRelease, store.ModelCreditBucketMonthly); events != 1 {
		t.Fatalf("finish must return the unused monthly hold, got %d release events", events)
	}

	// 逐字相同的终态重放：成功回执，不动钱。
	before := computeTaskAccount(t, db, uid)
	if err := db.FinishComputeTask(ctx, quote.ID, lease, final, 120*unit, true, now.Add(time.Minute)); err != nil {
		t.Fatalf("an identical terminal replay must succeed: %v", err)
	}
	if after := computeTaskAccount(t, db, uid); *after != *before {
		t.Fatalf("an identical terminal replay must not move money: before=%#v after=%#v", before, after)
	}
	if events := countComputeTaskLedgerEvents(t, db, uid, quote.ID, store.ModelCreditEventSettle, store.ModelCreditBucketMonthly); events != 1 {
		t.Fatalf("a replayed finish must not book a second settle, got %d", events)
	}
	// 冲突的终态重放：换金额或换结果都拒绝。
	if err := db.FinishComputeTask(ctx, quote.ID, lease, final, 100*unit, true, now); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a conflicting terminal replay must be refused, got %v", err)
	}
	if err := db.FinishComputeTask(ctx, quote.ID, lease, final, 120*unit, false, now); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a different terminal state must be refused, got %v", err)
	}
}

// TestPostgresComputeTaskExpiryAndLeaseFence 覆盖过期报价、过期租约和租约唯一性：
// 报价过期不能再确认，租约过期的任务可以被重新领走，旧 worker 从此写不动它。
func TestPostgresComputeTaskExpiryAndLeaseFence(t *testing.T) {
	db := newComputeTaskTestAdapter(t)
	ctx := context.Background()
	unit := store.ModelCreditUnitsPerCredit
	uid := createCreditTopupTestUser(t, db, "compute-task-lease")
	grantComputeTaskCredits(t, db, uid, store.ModelCreditBucketPaid, 1000, "topup:compute-task-lease")

	expired := testComputeTaskQuote("hpc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 60*unit)
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := db.CreateComputeTaskQuote(ctx, uid, expired); err != nil {
		t.Fatalf("create expired quote: %v", err)
	}
	if _, err := db.ConfirmComputeTask(ctx, uid, expired.ID, expired.MaxUnits, time.Now().UTC()); !errors.Is(err, store.ErrComputeQuoteExpired) {
		t.Fatalf("an expired quote must not be confirmed, got %v", err)
	}
	if account := computeTaskAccount(t, db, uid); account.PaidUnits != 1000*unit {
		t.Fatalf("a refused expired confirmation must not hold anything: %#v", account)
	}

	quote := testComputeTaskQuote("hpc_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 60*unit)
	if err := db.CreateComputeTaskQuote(ctx, uid, quote); err != nil {
		t.Fatalf("create compute task quote: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.ConfirmComputeTask(ctx, uid, quote.ID, quote.MaxUnits, now); err != nil {
		t.Fatalf("confirm compute task: %v", err)
	}

	// 领取一个一秒钟后就到期的租约，模拟 worker 掉线。
	dead := "hpc_cccccccccccccccccccccccccccccccc"
	if run, err := db.ClaimComputeTask(ctx, dead, now, now.Add(time.Second)); err != nil || run == nil {
		t.Fatalf("claim compute task: %#v %v", run, err)
	}
	// 同一个租约不能再挂到第二个任务上。
	second := testComputeTaskQuote("hpc_dddddddddddddddddddddddddddddddd", 60*unit)
	if err := db.CreateComputeTaskQuote(ctx, uid, second); err != nil {
		t.Fatalf("create second quote: %v", err)
	}
	if _, err := db.ConfirmComputeTask(ctx, uid, second.ID, second.MaxUnits, now); err != nil {
		t.Fatalf("confirm second quote: %v", err)
	}
	if _, err := db.ClaimComputeTask(ctx, dead, now, now.Add(time.Minute)); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a lease already in use must be refused, got %v", err)
	}
	if err := db.SaveComputeTaskProgress(ctx, quote.ID, dead, store.ComputeTaskProgress{Phase: "working"}); err != nil {
		t.Fatalf("renew active lease: %v", err)
	}
	secondLease := "hpc_22222222222222222222222222222222"
	secondRun, err := db.ClaimComputeTask(ctx, secondLease, now, now.Add(store.ComputeTaskLeaseTTL))
	if err != nil || secondRun == nil || secondRun.ID != second.ID {
		t.Fatalf("claim second task: %#v %v", secondRun, err)
	}
	if err := db.FinishComputeTask(ctx, second.ID, secondLease, store.ComputeTaskProgress{}, 0, false, now); err != nil {
		t.Fatalf("release second task: %v", err)
	}
	if run, err := db.ClaimComputeTask(ctx, secondLease, now.Add(2*time.Second), now.Add(time.Minute)); err != nil || run != nil {
		t.Fatalf("heartbeat must prevent early reclaim: %#v %v", run, err)
	}
	var leaseDeadline time.Time
	if err := db.db.QueryRow(`SELECT lease_expires_at FROM compute_tasks WHERE quote_id = $1`, quote.ID).Scan(&leaseDeadline); err != nil {
		t.Fatal(err)
	}

	// 到期后重新领取：换租约，旧租约的保存和结束都被 fence 掉。
	later := leaseDeadline.Add(time.Second)
	fresh := "hpc_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	reclaimed, err := db.ClaimComputeTask(ctx, fresh, later, later.Add(45*time.Second))
	if err != nil || reclaimed == nil || reclaimed.Lease != fresh || reclaimed.ID != quote.ID {
		t.Fatalf("an expired lease must be re-claimable: %#v %v", reclaimed, err)
	}
	if err := db.SaveComputeTaskProgress(ctx, quote.ID, dead, store.ComputeTaskProgress{Phase: "stale"}); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a replaced worker must not save progress, got %v", err)
	}
	final := store.ComputeTaskProgress{Phase: "released", ReleasedAt: &later}
	if err := db.FinishComputeTask(ctx, quote.ID, dead, final, 0, true, later); !errors.Is(err, store.ErrComputeTaskConflict) {
		t.Fatalf("a replaced worker must not finish the task, got %v", err)
	}
	// 零用量结束：整笔预占退回，余额回到预占前。
	if err := db.FinishComputeTask(ctx, quote.ID, fresh, final, 0, false, later); err != nil {
		t.Fatalf("finish with zero measured usage: %v", err)
	}
	if account := computeTaskAccount(t, db, uid); account.PaidUnits != 1000*unit {
		t.Fatalf("a zero-usage finish must return the whole hold: %#v", account)
	}
	failed, err := db.GetComputeTask(ctx, uid, quote.ID)
	if err != nil || failed == nil || failed.State != store.ComputeTaskFailed ||
		failed.ChargedUnits == nil || *failed.ChargedUnits != 0 {
		t.Fatalf("a failed run must keep its zero charge: %#v %v", failed, err)
	}
}

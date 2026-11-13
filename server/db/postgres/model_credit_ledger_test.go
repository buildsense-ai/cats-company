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

func modelCreditMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000068_model_credit_ledger." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func modelCreditTaskMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000071_model_credit_tasks." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The startup constant and the numbered migration must stay one script, and the
// rollback must drop the tables in reverse dependency order.
func TestModelCreditLedgerMigrationMatchesStartup(t *testing.T) {
	up := strings.Join(strings.Fields(modelCreditMigration(t, "up")), " ")
	if up != strings.Join(strings.Fields(createModelCreditLedgerTables), " ") {
		t.Fatal("000068_model_credit_ledger.up.sql differs from createModelCreditLedgerTables")
	}
	down := modelCreditMigration(t, "down")
	for _, want := range []string{
		"DROP TABLE IF EXISTS model_credit_reservations;",
		"DROP TABLE IF EXISTS model_credit_ledger_events;",
		"DROP TABLE IF EXISTS model_credit_accounts;",
	} {
		if !strings.Contains(down, want) {
			t.Fatalf("down migration is missing %q", want)
		}
	}
	if reservations, events, accounts := strings.Index(down, "model_credit_reservations"),
		strings.Index(down, "model_credit_ledger_events"), strings.Index(down, "model_credit_accounts"); !(reservations < events && events < accounts) {
		t.Fatal("down migration does not drop dependents before their parents")
	}
	source, err := os.ReadFile("schema.go")
	if err != nil {
		t.Fatal(err)
	}
	statements := string(source)
	start := strings.Index(statements, "statements := []string{")
	end := strings.Index(statements[start:], "\n\t}")
	if start < 0 || end < 0 || !strings.Contains(statements[start:start+end], "\t\tcreateModelCreditLedgerTables,") {
		t.Fatal("CreateSchema does not run createModelCreditLedgerTables")
	}
	if !strings.Contains(statements[start:start+end], "\t\tmigrateModelCreditTasks,") {
		t.Fatal("CreateSchema does not run migrateModelCreditTasks (migration 000071)")
	}
}

// 000071 的启动副本必须和编号迁移一致，并且回滚要能真的把 root 任务表与两列删掉。
func TestModelCreditTaskMigrationMatchesStartup(t *testing.T) {
	up := strings.Join(strings.Fields(modelCreditTaskMigration(t, "up")), " ")
	if up != strings.Join(strings.Fields(migrateModelCreditTasks), " ") {
		t.Fatal("000071_model_credit_tasks.up.sql differs from migrateModelCreditTasks")
	}
	down := modelCreditTaskMigration(t, "down")
	for _, want := range []string{
		"DROP TABLE IF EXISTS model_credit_tasks;",
		"DROP COLUMN IF EXISTS retained_task_id;",
		"DROP COLUMN IF EXISTS root_task_id;",
		"DROP COLUMN IF EXISTS unfunded_units;",
		"DROP CONSTRAINT IF EXISTS chk_model_credit_reservations_bucket_total;",
	} {
		if !strings.Contains(down, want) {
			t.Fatalf("000071 down migration is missing %q", want)
		}
	}
}

func modelCreditDatabase(t *testing.T) *Adapter {
	t.Helper()
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for model credit ledger integration tests")
	}
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("cats_model_credit_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, dsn, schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// The production initializer must register the ledger tables.
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	return db
}

func modelCreditFund(t *testing.T, db *Adapter, uid int64, bucket string, units int64, key string) *store.ModelCreditAccount {
	t.Helper()
	account, err := db.GrantModelCredits(context.Background(), store.ModelCreditGrantInput{
		UID: uid, Bucket: bucket, Units: units, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("grant %s %d units: %v", bucket, units, err)
	}
	return account
}

// modelCreditTestPermission 给 uid 一个有效的模型权限，来源是邀请（invite）：
// 邀请订阅不发月额度，所以不会像 order/admin 订阅那样把用例里精确的月额度余额改掉，
// 但它足以让任务门禁认为「权限有效」。
func modelCreditTestPermission(t *testing.T, db *Adapter, uid int64) {
	t.Helper()
	slug := fmt.Sprintf("credit-task-test-%d", time.Now().UnixNano())
	planID, err := db.CreateCommercialPlan(&types.CommercialPlan{
		Slug:             slug,
		Name:             "Credit task test",
		PriceFen:         3900,
		Currency:         "CNY",
		SaleState:        "hidden",
		DurationDays:     30,
		PlanTier:         1,
		ModelCreditUnits: 4_000 * store.ModelCreditUnitsPerCredit,
	})
	if err != nil {
		t.Fatalf("create credit task test plan: %v", err)
	}
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'invite', $3, 'active', $4, NULL)`,
		uid, planID, "INVITE-"+slug, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("insert credit task permission: %v", err)
	}
}

// modelCreditTestTask 启动一个原始任务，并确认它当下是 allowed 的。账本预留必须挂在
// 这样一个任务上（缺少 root 或任务已暂停都会被拒绝）。
func modelCreditTestTask(t *testing.T, db *Adapter, uid int64, taskID string) *store.ModelCreditTaskDecision {
	t.Helper()
	decision, err := db.ApplyModelCreditTask(context.Background(), store.ModelCreditTaskInput{
		UID: uid, TaskID: taskID, Action: store.ModelCreditTaskActionStart,
	})
	if err != nil {
		t.Fatalf("start root task %s: %v", taskID, err)
	}
	if !decision.Enabled || !decision.Allowed || decision.State != store.ModelCreditTaskRunning {
		t.Fatalf("root task %s was not allowed to start: %+v", taskID, decision)
	}
	return decision
}

func sameModelCreditBuckets(a, b *store.ModelCreditAccount) bool {
	return a != nil && b != nil && a.UID == b.UID && a.MonthlyUnits == b.MonthlyUnits &&
		a.GiftUnits == b.GiftUnits && a.PaidUnits == b.PaidUnits && a.DebtUnits == b.DebtUnits
}

func sameModelCreditHold(a, b *store.ModelCreditReservation) bool {
	if a == nil || b == nil || a.UID != b.UID || a.RequestID != b.RequestID ||
		a.ReservedUnits != b.ReservedUnits || a.Split != b.Split || a.State != b.State ||
		a.RootTaskID != b.RootTaskID || a.UnfundedUnits != b.UnfundedUnits {
		return false
	}
	if (a.SettledUnits == nil) != (b.SettledUnits == nil) {
		return false
	}
	return a.SettledUnits == nil || *a.SettledUnits == *b.SettledUnits
}

func TestPostgresModelCreditReserveSettleIsIdempotent(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketMonthly, 5, "seed-monthly")
	modelCreditFund(t, db, uid, store.ModelCreditBucketGift, 3, "seed-gift")
	seeded := modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 2, "seed-paid")
	if seeded.MonthlyUnits != 5 || seeded.GiftUnits != 3 || seeded.PaidUnits != 2 || seeded.DebtUnits != 0 {
		t.Fatalf("seeded buckets %+v", seeded)
	}
	if replay := modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 2, "seed-paid"); replay.PaidUnits != 2 {
		t.Fatalf("replayed grant credited twice: %+v", replay)
	}
	if _, err := db.GrantModelCredits(ctx, store.ModelCreditGrantInput{
		UID: uid, Bucket: store.ModelCreditBucketPaid, Units: 3, IdempotencyKey: "seed-paid",
	}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("conflicting grant key accepted: %v", err)
	}
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-idempotent")
	hold, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", RootTaskID: "root-idempotent", Units: 7})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Split != (store.ModelCreditSplit{Monthly: 5, Gift: 2}) || hold.State != store.ModelCreditReservationReserved ||
		hold.SettledUnits != nil || hold.RootTaskID != "root-idempotent" || hold.UnfundedUnits != 0 {
		t.Fatalf("hold %+v", hold)
	}
	if held.MonthlyUnits != 0 || held.GiftUnits != 1 || held.PaidUnits != 2 {
		t.Fatalf("balance after hold %+v", held)
	}
	replayedHold, replayedBalance, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", RootTaskID: "root-idempotent", Units: 7})
	if err != nil || !sameModelCreditHold(replayedHold, hold) || !sameModelCreditBuckets(replayedBalance, held) {
		t.Fatalf("reserve replay held twice: %v %+v", err, replayedBalance)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", RootTaskID: "root-idempotent", Units: 6}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("conflicting reserve accepted: %v", err)
	}
	// 同一个 request id 换个 root 重放也必须冲突：一笔调用不能同时属于两个任务。
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", RootTaskID: "root-other", Units: 7}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("reserve replay under another root accepted: %v", err)
	}
	settled, afterSettle, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "req-1", ActualUnits: 6})
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != store.ModelCreditReservationSettled || settled.SettledUnits == nil || *settled.SettledUnits != 6 {
		t.Fatalf("settled reservation %+v", settled)
	}
	// Six of the seven held units were used: the unused gift unit goes back to
	// the gift bucket it came from.
	if afterSettle.MonthlyUnits != 0 || afterSettle.GiftUnits != 2 || afterSettle.PaidUnits != 2 || afterSettle.DebtUnits != 0 {
		t.Fatalf("balance after settle %+v", afterSettle)
	}
	replayedSettled, replayedAfter, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "req-1", ActualUnits: 6})
	if err != nil || !sameModelCreditHold(replayedSettled, settled) || !sameModelCreditBuckets(replayedAfter, afterSettle) {
		t.Fatalf("settle replay charged twice: %v %+v", err, replayedAfter)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", RootTaskID: "root-idempotent", Units: 7}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("reserve replay after settlement accepted: %v", err)
	}
	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "req-1", ActualUnits: 5}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("conflicting settle accepted: %v", err)
	}

	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "req-missing", ActualUnits: 1}); !errors.Is(err, store.ErrModelCreditNotFound) {
		t.Fatalf("settle without a hold accepted: %v", err)
	}
	account, err := db.GetModelCreditAccount(ctx, uid)
	if err != nil || account.DebtUnits != 0 {
		t.Fatalf("settle created debt: %v %+v", err, account)
	}
	var events int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM model_credit_ledger_events WHERE uid = $1`, uid).Scan(&events); err != nil {
		t.Fatal(err)
	}
	// Three grants, two reserve rows (monthly+gift), two settle rows
	// (monthly+gift) and one release row (gift).
	if events != 8 {
		t.Fatalf("ledger rows %d, want 8", events)
	}
	got, err := db.GetModelCreditReservation(ctx, uid, "req-1")
	if err != nil || !sameModelCreditHold(got, settled) {
		t.Fatalf("reservation read %v %+v", err, got)
	}
}

// 预留阶段不许超支（除了唯一的保留任务），但已经派发出去的调用在结算时超出预占/余额
// 必须落成真实欠款，而且重复结算不能再记一次（P0-2.3/P0-3.2）。
func TestPostgresModelCreditSettleOverageBooksRealDebt(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "seed-paid")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-overage")
	if _, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "over-1", RootTaskID: "root-overage", Units: 4}); err != nil || held.PaidUnits != 6 {
		t.Fatalf("hold %v %+v", err, held)
	}
	if _, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "over-1", ActualUnits: 6}); err != nil || after.PaidUnits != 4 || after.DebtUnits != 0 {
		t.Fatalf("covered overage %v %+v", err, after)
	}
	if _, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "over-2", RootTaskID: "root-overage", Units: 4}); err != nil || held.PaidUnits != 0 {
		t.Fatalf("second hold %v %+v", err, held)
	}
	// 余额 0 时结算 10：预占的 4 从 hold 扣，剩下 6 一分钱都收不到，只能记欠款。
	settled, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "over-2", ActualUnits: 10})
	if err != nil {
		t.Fatalf("dispatched overage must settle instead of failing: %v", err)
	}
	if settled.State != store.ModelCreditReservationSettled || after.PaidUnits != 0 || after.DebtUnits != 6 {
		t.Fatalf("overage debt %+v %+v", settled, after)
	}
	replayedSettled, replayedAfter, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "over-2", ActualUnits: 10})
	if err != nil || !sameModelCreditHold(replayedSettled, settled) || !sameModelCreditBuckets(replayedAfter, after) {
		t.Fatalf("settle replay booked debt twice: %v %+v", err, replayedAfter)
	}
	var debtEvents int
	var debtUnits int64
	if err := db.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(units), 0) FROM model_credit_ledger_events
		WHERE uid = $1 AND event_type = 'debt_accrued'`, uid).Scan(&debtEvents, &debtUnits); err != nil {
		t.Fatal(err)
	}
	if debtEvents != 1 || debtUnits != 6 {
		t.Fatalf("debt ledger rows=%d units=%d, want one row of 6", debtEvents, debtUnits)
	}
}

// 有欠款时赠送额度既不能抵债，也不能被花掉；结算超支只会让欠款变大。
func TestPostgresModelCreditGiftNeverCoversArrears(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketGift, 5, "seed-gift")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 5, "seed-paid")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-arrears")
	if _, err := db.db.Exec(`UPDATE model_credit_accounts SET debt_units = 2 WHERE uid = $1`, uid); err != nil {
		t.Fatal(err)
	}
	hold, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "debt-1", RootTaskID: "root-arrears", Units: 4})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Split.Gift != 0 || hold.Split.Paid != 4 {
		t.Fatalf("gift covered arrears: %+v", hold.Split)
	}
	if held.GiftUnits != 5 || held.PaidUnits != 1 || held.DebtUnits != 2 {
		t.Fatalf("balance after hold %+v", held)
	}
	if _, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "debt-2", RootTaskID: "root-arrears", Units: 1}); err != nil || held.PaidUnits != 0 {
		t.Fatalf("draining the paid bucket %v %+v", err, held)
	}
	settled, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "debt-1", ActualUnits: 6})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	// 预占 4 全部来自 paid；多出的 2 想在 gift 里找钱，但有欠款时 gift 不可用，
	// 于是这 2 变成新的欠款。
	if settled.State != store.ModelCreditReservationSettled {
		t.Fatalf("reservation state %q", settled.State)
	}
	if after.GiftUnits != 5 || after.PaidUnits != 0 || after.DebtUnits != 4 {
		t.Fatalf("arrears or gift changed: %+v", after)
	}
}

func TestPostgresModelCreditConcurrentReserveNeverOverdraws(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 250, "seed-paid")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-race")
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	start := make(chan struct{})
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := db.ReserveModelCredits(context.Background(), store.ModelCreditReserveInput{
				UID: uid, RequestID: fmt.Sprintf("race-%d", i), RootTaskID: "root-race", Units: 100,
			})
			switch {
			case err == nil:
				mu.Lock()
				successes++
				mu.Unlock()
			case errors.Is(err, store.ErrModelCreditInsufficient):
			default:
				t.Errorf("concurrent reserve: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if successes != 2 {
		t.Fatalf("%d concurrent holds won, want 2", successes)
	}
	account, err := db.GetModelCreditAccount(context.Background(), uid)
	if err != nil || account.PaidUnits != 50 {
		t.Fatalf("balance after the race %v %+v", err, account)
	}
	var heldUnits int64
	if err := db.db.QueryRow(`SELECT COALESCE(SUM(reserved_units), 0) FROM model_credit_reservations WHERE uid = $1`, uid).Scan(&heldUnits); err != nil || heldUnits != 200 {
		t.Fatalf("held units %d: %v", heldUnits, err)
	}
}

func TestPostgresModelCreditReleaseRestoresReservationIdempotently(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketMonthly, 3, "release-monthly")
	modelCreditFund(t, db, uid, store.ModelCreditBucketGift, 2, "release-gift")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 4, "release-paid")
	// 人工补发的月额度没有订阅周期，释放时必须原样退回，不能被跨期规则吃掉。
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-release")
	hold, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "release-1", RootTaskID: "root-release", Units: 7})
	if err != nil || hold.Split != (store.ModelCreditSplit{Monthly: 3, Gift: 2, Paid: 2}) {
		t.Fatalf("reserve before release: %v %+v", err, hold)
	}
	if _, _, err := db.ReleaseModelCredits(ctx, store.ModelCreditReleaseInput{UID: uid, RequestID: "release-1"}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("unconfirmed usage must retain the hold: %v", err)
	}
	if _, err := db.MarkModelCreditNoUsage(ctx, store.ModelCreditNoUsageConfirmation{UID: uid, RequestID: "release-1"}); err != nil {
		t.Fatalf("confirm zero usage before release: %v", err)
	}
	released, account, err := db.ReleaseModelCredits(ctx, store.ModelCreditReleaseInput{UID: uid, RequestID: "release-1"})
	if err != nil {
		t.Fatal(err)
	}
	if released.State != store.ModelCreditReservationReleased || released.ReleasedAt == nil {
		t.Fatalf("released reservation %+v", released)
	}
	if account.MonthlyUnits != 3 || account.GiftUnits != 2 || account.PaidUnits != 4 {
		t.Fatalf("release did not restore exact buckets: %+v", account)
	}
	replayed, replayedAccount, err := db.ReleaseModelCredits(ctx, store.ModelCreditReleaseInput{UID: uid, RequestID: "release-1"})
	if err != nil || !sameModelCreditHold(replayed, released) || !sameModelCreditBuckets(replayedAccount, account) {
		t.Fatalf("release replay changed balance: %v %+v", err, replayedAccount)
	}
	if confirmed, err := db.MarkModelCreditNoUsage(ctx, store.ModelCreditNoUsageConfirmation{UID: uid, RequestID: "release-1"}); err != nil || confirmed.State != store.ModelCreditReservationReleased {
		t.Fatalf("no-usage replay after release: %v %+v", err, confirmed)
	}
	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "release-1", ActualUnits: 0}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("settle after release accepted: %v", err)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "release-1", RootTaskID: "root-release", Units: 7}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("reserve replay after release accepted: %v", err)
	}
}

func TestPostgresModelCreditRequestIDsAreScopedPerUser(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uids := []int64{
		modelCreditTestUser(t, db, "credit-scope-a"),
		modelCreditTestUser(t, db, "credit-scope-b"),
	}
	for _, uid := range uids {
		modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, fmt.Sprintf("seed-paid-%d", uid))
		modelCreditTestPermission(t, db, uid)
		modelCreditTestTask(t, db, uid, fmt.Sprintf("root-shared-%d", uid))
		if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{
			UID: uid, RequestID: "shared-request", RootTaskID: fmt.Sprintf("root-shared-%d", uid), Units: 4,
		}); err != nil {
			t.Fatalf("reserve for uid %d: %v", uid, err)
		}
	}
	for _, uid := range uids {
		if _, account, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "shared-request", ActualUnits: 3}); err != nil || account.PaidUnits != 7 {
			t.Fatalf("settle for uid %d: %v %+v", uid, err, account)
		}
	}
}

func TestPostgresModelCreditLedgerDatabaseGuards(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 5, "seed-paid")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-guard")
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "guard-1", RootTaskID: "root-guard", Units: 5}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE model_credit_accounts SET paid_units = -1 WHERE uid = $1`,
		// A debt event may not be booked on a spending bucket, and a gift bucket
		// may never carry a debt event.
		`INSERT INTO model_credit_ledger_events (event_key, uid, bucket, event_type, units, monthly_balance, gift_balance, paid_balance, debt_balance) VALUES ('grant:bad-bucket', $1, 'debt', 'grant', 1, 0, 0, 0, 0)`,
		`INSERT INTO model_credit_ledger_events (event_key, uid, bucket, event_type, units, request_id, monthly_balance, gift_balance, paid_balance, debt_balance) VALUES ('debt_accrued:guard-1:gift', $1, 'gift', 'debt_accrued', 1, 'guard-1', 0, 0, 0, 0)`,
		// Reserve/settle/release rows must name their request.
		`INSERT INTO model_credit_ledger_events (event_key, uid, bucket, event_type, units, monthly_balance, gift_balance, paid_balance, debt_balance) VALUES ('reserve:guard-1:paid', $1, 'paid', 'reserve', 1, 0, 0, 0, 0)`,
		`INSERT INTO model_credit_ledger_events (event_key, uid, bucket, event_type, units, monthly_balance, gift_balance, paid_balance, debt_balance) VALUES ('grant:bad key', $1, 'paid', 'grant', 1, 0, 0, 0, 0)`,
		`INSERT INTO model_credit_reservations (uid, request_id, reserved_units, monthly_units, gift_units, paid_units) VALUES ($1, 'bad request', 1, 1, 0, 0)`,
		// 透支预占必须等于「预占 - 三个消费桶」，不能凭空多出来。
		`INSERT INTO model_credit_reservations (uid, request_id, reserved_units, monthly_units, gift_units, paid_units, unfunded_units, lease_expires_at) VALUES ($1, 'bad-unfunded', 5, 1, 0, 0, 0, CURRENT_TIMESTAMP + interval '1 hour')`,
		`INSERT INTO model_credit_tasks (root_task_id, uid, state, started_at, started_seq) VALUES ('bad task', $1, 'running', CURRENT_TIMESTAMP, 99)`,
		`UPDATE model_credit_accounts SET retained_task_id = 'bad task id!' WHERE uid = $1`,
		`DELETE FROM model_credit_accounts WHERE uid = $1`,
	} {
		if _, err := db.db.Exec(query, uid); err == nil {
			t.Fatalf("SQL guard accepted %q", query)
		}
	}
	// 000071 的 root 任务表依赖账户表，回滚必须先撤掉它（部署时也是倒序执行）。
	if _, err := db.db.Exec(modelCreditTaskMigration(t, "down")); err != nil {
		t.Fatalf("task down migration: %v", err)
	}
	if _, err := db.db.Exec(modelCreditMigration(t, "down")); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	for _, table := range []string{"model_credit_reservations", "model_credit_ledger_events", "model_credit_accounts"} {
		var present bool
		if err := db.db.QueryRow(`SELECT to_regclass('` + table + `') IS NOT NULL`).Scan(&present); err != nil || present {
			t.Fatalf("down migration left %s: %v", table, err)
		}
	}
	if _, err := db.db.Exec(modelCreditMigration(t, "up")); err != nil {
		t.Fatalf("up migration after rollback: %v", err)
	}
	if _, err := db.db.Exec(modelCreditTaskMigration(t, "up")); err != nil {
		t.Fatalf("task up migration after rollback: %v", err)
	}
}

func TestPostgresModelCreditExpiryRequiresNoUsageAndPreservesLateSettle(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "credit-ledger")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 20, "expiry-seed")
	modelCreditTestPermission(t, db, uid)
	modelCreditTestTask(t, db, uid, "root-expiry")
	now := time.Now().UTC()

	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "expiry-unconfirmed", RootTaskID: "root-expiry", Units: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE model_credit_reservations SET created_at = $3, lease_expires_at = $4 WHERE uid = $1 AND request_id = $2`, uid, "expiry-unconfirmed", now.Add(-store.ModelCreditReservationLease-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if count, err := db.ReleaseExpiredModelCredits(ctx, now, 20); err != nil || count != 0 {
		t.Fatalf("unconfirmed expiry released %d rows: %v", count, err)
	}

	if _, err := db.MarkModelCreditNoUsage(ctx, store.ModelCreditNoUsageConfirmation{UID: uid, RequestID: "expiry-unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	if count, err := db.ReleaseExpiredModelCredits(ctx, now, 20); err != nil || count != 1 {
		t.Fatalf("confirmed failed request released %d rows: %v", count, err)
	}
	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "expiry-unconfirmed", ActualUnits: 1}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("settle after expiry release accepted: %v", err)
	}

	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "expiry-late-usage", RootTaskID: "root-expiry", Units: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE model_credit_reservations SET created_at = $3, lease_expires_at = $4 WHERE uid = $1 AND request_id = $2`, uid, "expiry-late-usage", now.Add(-store.ModelCreditReservationLease-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkModelCreditNoUsage(ctx, store.ModelCreditNoUsageConfirmation{UID: uid, RequestID: "expiry-late-usage"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "expiry-late-usage", ActualUnits: 3}); err != nil {
		t.Fatalf("late usage settle before sweep: %v", err)
	}
	if count, err := db.ReleaseExpiredModelCredits(ctx, now, 20); err != nil || count != 0 {
		t.Fatalf("sweep released settled request (%d): %v", count, err)
	}
}

func TestPostgresModelCreditLedgerListingPaginatesAndScopesByUID(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	uid := modelCreditTestUser(t, db, "ledger-page")
	otherUID := modelCreditTestUser(t, db, "ledger-page-other")
	for _, key := range []string{"ledger-a", "ledger-b", "ledger-c"} {
		modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 1, key)
	}
	modelCreditFund(t, db, otherUID, store.ModelCreditBucketPaid, 1, "ledger-other")
	// Equal timestamps exercise the deterministic event_key tie-breaker.
	createdAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.db.Exec(`UPDATE model_credit_ledger_events SET created_at = $3 WHERE uid IN ($1, $2)`, uid, otherUID, createdAt); err != nil {
		t.Fatal(err)
	}

	firstPage, err := db.ListModelCreditLedgerEvents(ctx, uid, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || firstPage[0].EventKey != "grant:ledger-c" || firstPage[1].EventKey != "grant:ledger-b" {
		t.Fatalf("first page has wrong ordering or UID scope: %+v", firstPage)
	}
	secondPage, err := db.ListModelCreditLedgerEvents(ctx, uid, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 2 || secondPage[0].EventKey != "grant:ledger-b" || secondPage[1].EventKey != "grant:ledger-a" {
		t.Fatalf("second page has wrong offset or ordering: %+v", secondPage)
	}
	if secondPage[0].EventKey != firstPage[1].EventKey {
		t.Fatal("adjacent pages skipped or duplicated an event")
	}
}

// modelCreditTestUser 建一个真实用户：commercial_entitlements.uid 有外键，所以权限
// 必须挂在真实用户上，不能用裸 uid 造。
func modelCreditTestUser(t *testing.T, db *Adapter, prefix string) int64 {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	uid, err := db.CreateUser(&types.User{
		Username:    name,
		Email:       name + "@example.test",
		DisplayName: name,
		AccountType: types.AccountHuman,
		PassHash:    []byte(name + "-hash"),
	})
	if err != nil {
		t.Fatalf("create %s user: %v", prefix, err)
	}
	return uid
}

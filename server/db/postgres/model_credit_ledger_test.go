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
)

func modelCreditMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000068_model_credit_ledger." + direction + ".sql")
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

func sameModelCreditBuckets(a, b *store.ModelCreditAccount) bool {
	return a != nil && b != nil && a.UID == b.UID && a.MonthlyUnits == b.MonthlyUnits &&
		a.GiftUnits == b.GiftUnits && a.PaidUnits == b.PaidUnits && a.DebtUnits == b.DebtUnits
}

func sameModelCreditHold(a, b *store.ModelCreditReservation) bool {
	if a == nil || b == nil || a.UID != b.UID || a.RequestID != b.RequestID ||
		a.ReservedUnits != b.ReservedUnits || a.Split != b.Split || a.State != b.State {
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
	ctx, uid := context.Background(), int64(900001)
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
	hold, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", Units: 7})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Split != (store.ModelCreditSplit{Monthly: 5, Gift: 2}) || hold.State != store.ModelCreditReservationReserved || hold.SettledUnits != nil {
		t.Fatalf("hold %+v", hold)
	}
	if held.MonthlyUnits != 0 || held.GiftUnits != 1 || held.PaidUnits != 2 {
		t.Fatalf("balance after hold %+v", held)
	}
	replayedHold, replayedBalance, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", Units: 7})
	if err != nil || !sameModelCreditHold(replayedHold, hold) || !sameModelCreditBuckets(replayedBalance, held) {
		t.Fatalf("reserve replay held twice: %v %+v", err, replayedBalance)
	}
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", Units: 6}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("conflicting reserve accepted: %v", err)
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
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "req-1", Units: 7}); !errors.Is(err, store.ErrModelCreditConflict) {
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

func TestPostgresModelCreditSettleOverageNeverOverdraws(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx, uid := context.Background(), int64(900002)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, "seed-paid")
	if _, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "over-1", Units: 4}); err != nil || held.PaidUnits != 6 {
		t.Fatalf("hold %v %+v", err, held)
	}
	if _, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "over-1", ActualUnits: 6}); err != nil || after.PaidUnits != 4 {
		t.Fatalf("covered overage %v %+v", err, after)
	}
	if _, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "over-2", Units: 4}); err != nil || held.PaidUnits != 0 {
		t.Fatalf("second hold %v %+v", err, held)
	}
	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "over-2", ActualUnits: 10}); !errors.Is(err, store.ErrModelCreditInsufficient) {
		t.Fatalf("uncovered overage accepted: %v", err)
	}
	account, err := db.GetModelCreditAccount(ctx, uid)
	if err != nil || account.PaidUnits != 0 || account.DebtUnits != 0 {
		t.Fatalf("failed settle changed the account: %v %+v", err, account)
	}
	hold, err := db.GetModelCreditReservation(ctx, uid, "over-2")
	if err != nil || hold.State != store.ModelCreditReservationReserved || hold.SettledUnits != nil {
		t.Fatalf("failed settle changed the hold: %v %+v", err, hold)
	}
	if _, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "over-2", ActualUnits: 4}); err != nil || after.PaidUnits != 0 {
		t.Fatalf("retry under the hold %v %+v", err, after)
	}
}

func TestPostgresModelCreditGiftNeverCoversArrears(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx, uid := context.Background(), int64(900003)
	modelCreditFund(t, db, uid, store.ModelCreditBucketGift, 5, "seed-gift")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 5, "seed-paid")
	if _, err := db.db.Exec(`UPDATE model_credit_accounts SET debt_units = 2 WHERE uid = $1`, uid); err != nil {
		t.Fatal(err)
	}
	hold, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "debt-1", Units: 4})
	if err != nil {
		t.Fatal(err)
	}
	if hold.Split.Gift != 0 || hold.Split.Paid != 4 {
		t.Fatalf("gift covered arrears: %+v", hold.Split)
	}
	if held.GiftUnits != 5 || held.PaidUnits != 1 || held.DebtUnits != 2 {
		t.Fatalf("balance after hold %+v", held)
	}
	if _, held, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "debt-2", Units: 1}); err != nil || held.PaidUnits != 0 {
		t.Fatalf("draining the paid bucket %v %+v", err, held)
	}
	if _, _, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "debt-1", ActualUnits: 6}); !errors.Is(err, store.ErrModelCreditInsufficient) {
		t.Fatalf("gift was spent while the account owes arrears: %v", err)
	}
	account, err := db.GetModelCreditAccount(ctx, uid)
	if err != nil || account.GiftUnits != 5 || account.DebtUnits != 2 {
		t.Fatalf("arrears or gift changed: %v %+v", err, account)
	}
	if _, after, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "debt-1", ActualUnits: 4}); err != nil || after.GiftUnits != 5 || after.DebtUnits != 2 {
		t.Fatalf("settle within the hold %v %+v", err, after)
	}
}

func TestPostgresModelCreditConcurrentReserveNeverOverdraws(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	uid := int64(900004)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 250, "seed-paid")
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
				UID: uid, RequestID: fmt.Sprintf("race-%d", i), Units: 100,
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
	ctx, uid := context.Background(), int64(900008)
	modelCreditFund(t, db, uid, store.ModelCreditBucketMonthly, 3, "release-monthly")
	modelCreditFund(t, db, uid, store.ModelCreditBucketGift, 2, "release-gift")
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 4, "release-paid")
	hold, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "release-1", Units: 7})
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
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "release-1", Units: 7}); !errors.Is(err, store.ErrModelCreditConflict) {
		t.Fatalf("reserve replay after release accepted: %v", err)
	}
}
func TestPostgresModelCreditRequestIDsAreScopedPerUser(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx := context.Background()
	for _, uid := range []int64{900006, 900007} {
		modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 10, fmt.Sprintf("seed-paid-%d", uid))
		if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "shared-request", Units: 4}); err != nil {
			t.Fatalf("reserve for uid %d: %v", uid, err)
		}
	}
	for _, uid := range []int64{900006, 900007} {
		if _, account, err := db.SettleModelCredits(ctx, store.ModelCreditSettleInput{UID: uid, RequestID: "shared-request", ActualUnits: 3}); err != nil || account.PaidUnits != 7 {
			t.Fatalf("settle for uid %d: %v %+v", uid, err, account)
		}
	}
}
func TestPostgresModelCreditLedgerDatabaseGuards(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx, uid := context.Background(), int64(900005)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 5, "seed-paid")
	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "guard-1", Units: 5}); err != nil {
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
		`DELETE FROM model_credit_accounts WHERE uid = $1`,
	} {
		if _, err := db.db.Exec(query, uid); err == nil {
			t.Fatalf("SQL guard accepted %q", query)
		}
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
}

func TestPostgresModelCreditExpiryRequiresNoUsageAndPreservesLateSettle(t *testing.T) {
	t.Parallel()
	db := modelCreditDatabase(t)
	ctx, uid := context.Background(), int64(900009)
	modelCreditFund(t, db, uid, store.ModelCreditBucketPaid, 20, "expiry-seed")
	now := time.Now().UTC()

	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "expiry-unconfirmed", Units: 5}); err != nil {
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

	if _, _, err := db.ReserveModelCredits(ctx, store.ModelCreditReserveInput{UID: uid, RequestID: "expiry-late-usage", Units: 5}); err != nil {
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
	const uid, otherUID = int64(900010), int64(900011)
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

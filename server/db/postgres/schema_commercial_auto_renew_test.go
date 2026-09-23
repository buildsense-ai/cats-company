package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestAutoRenewStartupMigrationMatchesFile(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000025_commercial_auto_renew.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(up), "\r\n", "\n")) != strings.TrimSpace(migrateCommercialAutoRenew) {
		t.Fatal("startup migration diverged from numbered SQL migration (up)")
	}
	down, err := os.ReadFile("../migrations/postgres/000025_commercial_auto_renew.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(down), "\r\n", "\n")) != strings.TrimSpace(rollbackCommercialAutoRenew) {
		t.Fatal("startup migration diverged from numbered SQL migration (down)")
	}
}

func TestPostgresCommercialAutoRenewConfigAndRuns(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_auto_renew_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create auto renew test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open auto renew test postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create current schema: %v", err)
	}

	uid := createFlashMigrationUser(t, db, "auto-renew")
	planID := seedFlashMigrationPlan(t, db, "catsco-pro", "Pro", `{"MiniMax-M2.7":5250}`)
	now := time.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(3 * 24 * time.Hour)
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'operator', 'auto-renew-test', 'active', $3, $4)`,
		uid, planID, now.Add(-27*24*time.Hour), expiresAt); err != nil {
		t.Fatalf("seed auto renew entitlement: %v", err)
	}

	config, err := db.SetCommercialAutoRenewConfig(uid, true, "内部车队")
	if err != nil {
		t.Fatalf("set auto renew config: %v", err)
	}
	if config == nil || !config.Enabled || config.Note != "内部车队" {
		t.Fatalf("saved config: %#v", config)
	}
	loaded, err := db.GetCommercialAutoRenewConfig(uid)
	if err != nil || loaded == nil || !loaded.Enabled {
		t.Fatalf("load auto renew config: %#v %v", loaded, err)
	}

	due, err := db.ListDueCommercialAutoRenew(now, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("list due auto renew: %v", err)
	}
	if len(due) != 1 || due[0].UID != uid || !due[0].ExpiresAt.UTC().Truncate(time.Second).Equal(expiresAt) {
		t.Fatalf("due candidates: %#v want uid=%d expiry=%v", due, uid, expiresAt)
	}

	if short, err := db.ListDueCommercialAutoRenew(now, time.Hour); err != nil || len(short) != 0 {
		t.Fatalf("accounts outside the lead time must not be due: %#v %v", short, err)
	}

	// An extension segment pushes the latest expiry well past the lead time.
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'operator', 'auto-renew-test-ext', 'active', $3, $4)`,
		uid, planID, expiresAt, expiresAt.AddDate(0, 0, 30)); err != nil {
		t.Fatalf("seed extension segment: %v", err)
	}
	if extended, err := db.ListDueCommercialAutoRenew(now, 7*24*time.Hour); err != nil || len(extended) != 0 {
		t.Fatalf("an already extended chain must not be renewed twice: %#v %v", extended, err)
	}

	// Disabling the switch removes the account from the schedule entirely.
	if _, err := db.SetCommercialAutoRenewConfig(uid, false, ""); err != nil {
		t.Fatalf("disable auto renew: %v", err)
	}
	if disabled, err := db.ListDueCommercialAutoRenew(now, 60*24*time.Hour); err != nil || len(disabled) != 0 {
		t.Fatalf("disabled accounts must not be due: %#v %v", disabled, err)
	}

	previous := expiresAt
	next := expiresAt.AddDate(0, 0, 30)
	if err := db.RecordCommercialAutoRenewRun(&types.CommercialAutoRenewRun{
		UID: uid, Action: "extend", Status: "applied",
		PreviousExpiry: &previous, NewExpiry: &next,
		Message: "test run", OperationID: "auto-renew-test-1",
	}); err != nil {
		t.Fatalf("record auto renew run: %v", err)
	}
	runs, err := db.ListCommercialAutoRenewRuns(uid, 10)
	if err != nil {
		t.Fatalf("list auto renew runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("run log: %#v", runs)
	}
	run := runs[0]
	if run.UID != uid || run.Status != "applied" || run.Action != "extend" || run.OperationID != "auto-renew-test-1" {
		t.Fatalf("run record fields: %#v", run)
	}
	if run.PreviousExpiry == nil || !run.PreviousExpiry.UTC().Truncate(time.Second).Equal(previous) {
		t.Fatalf("run previous expiry: %#v want %v", run.PreviousExpiry, previous)
	}
	if run.NewExpiry == nil || !run.NewExpiry.UTC().Truncate(time.Second).Equal(next) {
		t.Fatalf("run new expiry: %#v want %v", run.NewExpiry, next)
	}
}

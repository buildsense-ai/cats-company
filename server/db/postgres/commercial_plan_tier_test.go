package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The upgrade ladder used to be a slug→rung table in Go:
//
//	var commercialOfficialPlanTiers = []struct{ slug string; tier int }{
//		{"catsco-basic", 1}, {"catsco-personal", 2}, {"catsco-pro", 3},
//	}
//
// That shape broke every time a package line changed: a renamed package lost
// its "续费 / 已包含 / 升级至" behaviour, and a new package could never join.
// The rung is a column now, so these tests assert the two things a column can
// get wrong: what the migration leaves behind, and the ordering the purchase
// guard depends on.

// The rung is opt-in. A package that never set one stays at 0, which every
// caller reads as "no ladder rules apply".
//
// In particular the migration must not guess a rung for a retired package: the
// public catalog stops listing it, the webapp therefore cannot compute the
// buyer's rung from it, and a backend rung would then reject purchases the UI
// still offered. That mismatch is the failure this test pins down.
func TestCommercialPlanTierMigrationLeavesPackagesOffLadder(t *testing.T) {
	ddl := migrateCommercialPlansAddTier

	if !strings.Contains(ddl, "ADD COLUMN IF NOT EXISTS plan_tier") {
		t.Fatal("the migration must add plan_tier idempotently")
	}
	if !strings.Contains(ddl, "DEFAULT 0") {
		t.Fatal("a package must default to off-ladder; defaulting to 1 would make every new package the cheapest tier")
	}
	if !strings.Contains(ddl, "CHECK (plan_tier >= 0)") {
		t.Fatal("a negative rung would sort below every package and disable the downgrade guard")
	}
	if strings.Contains(ddl, "UPDATE commercial_plans") {
		t.Fatal("the migration must not assign rungs: a retired package with a backend rung disagrees with the catalog-driven webapp")
	}
}

// An upgrade must supersede every cheaper package, not just the one directly
// below it. Otherwise a cheap package's unspent provisioning credit survives the
// upgrade and the buyer ends up with two machines.
//
// This reads the column, so it needs a database.
func TestCommercialPlanIDsBelowSupersedesEveryLowerRung(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_plan_tier_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create plan tier test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open plan tier schema: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create plan tier schema objects: %v", err)
	}

	// Three rungs plus one package that is not on the ladder at all.
	seed := []struct {
		slug string
		tier int
	}{
		{"tier-entry", 1},
		{"tier-middle", 2},
		{"tier-top", 3},
		{"internal-off-ladder", 0},
	}
	for _, s := range seed {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_plans(slug, name, price_fen, sale_state, duration_days, plan_tier, model_budgets)
			VALUES ($1, $1, 1, 'hidden', 30, $2, '{"MiniMax-M3":1}'::jsonb)`, s.slug, s.tier); err != nil {
			t.Fatalf("seed %s: %v", s.slug, err)
		}
	}

	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	below, err := commercialPlanIDsBelow(tx, 3)
	if err != nil {
		t.Fatalf("list plans below rung 3: %v", err)
	}
	if len(below) != 2 {
		t.Fatalf("rung 3 must supersede rungs 1 and 2, got %d plans", len(below))
	}

	// Rung 1 supersedes nothing: buying the cheapest package must not revoke
	// anything.
	if got, err := commercialPlanIDsBelow(tx, 1); err != nil || len(got) != 0 {
		t.Fatalf("the entry rung supersedes nothing, got %v err=%v", got, err)
	}
	// Off-ladder plans are never superseded by a purchase: a licence package
	// this account bought separately must survive an upgrade.
	if got, err := commercialPlanIDsBelow(tx, 99); err != nil || len(got) != 3 {
		t.Fatalf("only ladder plans are superseded, got %d err=%v", len(got), err)
	}
}

// A plan that is not on the ladder reports 0, which every caller reads as "no
// upgrade rules apply" — the same answer the old slug table gave for an unknown
// slug. Price is deliberately not used as a proxy: a discounted plan would then
// silently change rungs.
func TestPlanTierForPlanIDDefaultsToOffLadder(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	schemaName := fmt.Sprintf("cats_plan_tier_lookup_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create plan tier lookup schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open plan tier lookup schema: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create plan tier lookup schema objects: %v", err)
	}

	var planID int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_plans(slug, name, price_fen, sale_state, duration_days, model_budgets)
		VALUES ('off-ladder', 'off-ladder', 9900, 'hidden', 30, '{"MiniMax-M3":1}'::jsonb)
		RETURNING id`).Scan(&planID); err != nil {
		t.Fatalf("seed off-ladder plan: %v", err)
	}
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if got, err := planTierForPlanID(tx, planID); err != nil || got != 0 {
		t.Fatalf("a plan created without a rung must stay off the ladder, got %d err=%v", got, err)
	}
	if got, err := planTierForPlanID(tx, 999999); err != nil || got != 0 {
		t.Fatalf("a missing plan must report 0 rather than erroring, got %d err=%v", got, err)
	}
}

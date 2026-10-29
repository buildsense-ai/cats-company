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
// get wrong: the migration's one-time backfill, and the ordering the purchase
// guard depends on.

// The backfill is the only reason an existing buyer keeps their behaviour across
// the deploy. It must run exactly once — re-asserting it on every boot would
// undo an operator who deliberately took a retired package off the ladder — so
// the assertion is on the guarded shape, not merely on the values.
func TestCommercialPlanTierMigrationBackfillsLegacyRungs(t *testing.T) {
	ddl := migrateCommercialPlansAddTier

	if !strings.Contains(ddl, "ADD COLUMN IF NOT EXISTS plan_tier") {
		t.Fatal("the migration must add plan_tier idempotently")
	}
	// Rung 2 / 3 belong to the two packages that held them before the column
	// existed. Rung 1 is new: the ¥59 package had no historical occupant.
	for _, want := range []string{
		"UPDATE commercial_plans SET plan_tier = 2 WHERE slug = 'catsco-personal' AND plan_tier = 0;",
		"UPDATE commercial_plans SET plan_tier = 3 WHERE slug = 'catsco-pro'      AND plan_tier = 0;",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("backfill missing %q", want)
		}
	}
	// `AND plan_tier = 0` is the guard. Without it every restart re-asserts the
	// rung and silently undoes an operator's decision — the exact bug the
	// cloud_worker_included backfill had.
	if strings.Contains(ddl, "SET plan_tier = 2 WHERE slug = 'catsco-personal';") {
		t.Fatal("the backfill must be guarded on plan_tier = 0; an unguarded UPDATE runs on every boot")
	}
	if !strings.Contains(ddl, "CHECK (plan_tier >= 0)") {
		t.Fatal("a negative rung would sort below every package and disable the downgrade guard")
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

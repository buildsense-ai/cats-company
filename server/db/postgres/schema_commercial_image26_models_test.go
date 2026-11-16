package postgres

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	// Plan shapes before 000073: nine models, no 2.6 image family.
	image26FreeBudgets     = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-flash":100,"glm-5.3-flash":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}`
	image26PersonalBudgets = `{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}`
	image26ProBudgets      = `{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}`
	image26InternalBudgets = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-flash":100,"glm-5.3-flash":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}`
)

// image26Models are the three models 000073 adds, at 100 CNY each.
var image26Models = []string{"gpt-image-2.6", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst"}

func TestImage26ModelsStartupMigrationMatchesFile(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000073_commercial_image_26_models.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(up), "\r\n", "\n")) != strings.TrimSpace(migrateCommercialPlansImage26Models) {
		t.Fatal("startup migration diverged from numbered SQL migration (up)")
	}
	down, err := os.ReadFile("../migrations/postgres/000073_commercial_image_26_models.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(down), "\r\n", "\n")) != strings.TrimSpace(rollbackCommercialPlansImage26Models) {
		t.Fatal("startup migration diverged from numbered SQL migration (down)")
	}
}

// TestPostgresCommercialImage26ModelsFillPlansAndRollBack covers 000073 end to
// end: the three 2.6 image models join the Free pool and the paid packages at
// 100 CNY each, every other budget key survives, grants land once per package
// (on the grant type the package already carries), a second run changes
// nothing, and the down migration removes exactly what the up migration wrote.
func TestPostgresCommercialImage26ModelsFillPlansAndRollBack(t *testing.T) {
	db := commercialPolicyTestDB(t)

	freePlanID := seedFlashMigrationPlan(t, db, "catsco-free", "Free", image26FreeBudgets)
	personalPlanID := seedFlashMigrationPlan(t, db, "catsco-personal", "Personal", image26PersonalBudgets)
	proPlanID := seedFlashMigrationPlan(t, db, "catsco-pro", "Pro", image26ProBudgets)
	internalPlanID := seedFlashMigrationPlan(t, db, "catsco-internal-all-models-50k", "Internal", image26InternalBudgets)

	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	expiresAt := now.Add(30 * 24 * time.Hour)

	freeUID := createFlashMigrationUser(t, db, "image26-free")
	seedFreeImageBaseline(t, db, freeUID, freePlanID, "free-26-baseline", now, expiresAt)
	personalUID := createFlashMigrationUser(t, db, "image26-personal")
	seedFlashPaidPackage(t, db, personalUID, personalPlanID, "personal-26", 2100, now, expiresAt)
	proUID := createFlashMigrationUser(t, db, "image26-pro")
	seedFlashPaidPackage(t, db, proUID, proPlanID, "pro-26", 6300, now, expiresAt)
	internalUID := createFlashMigrationUser(t, db, "image26-internal")
	seedFlashPaidPackage(t, db, internalUID, internalPlanID, "internal-26", 100, now, expiresAt)

	if _, err := db.db.Exec(migrateCommercialPlansImage26Models); err != nil {
		t.Fatalf("run image 2.6 models migration: %v", err)
	}
	assertImage26Up(t, db, freeUID, personalUID, proUID, internalUID)

	// The migration must be repeatable on every run.
	if _, err := db.db.Exec(migrateCommercialPlansImage26Models); err != nil {
		t.Fatalf("image 2.6 models migration should be idempotent: %v", err)
	}
	assertImage26Up(t, db, freeUID, personalUID, proUID, internalUID)

	execFlashMigrationFile(t, db, "000073_commercial_image_26_models.down.sql")
	assertImage26Down(t, db, freeUID, personalUID, proUID, internalUID)
}

func assertImage26Up(t *testing.T, db *Adapter, freeUID, personalUID, proUID, internalUID int64) {
	t.Helper()
	assertImage26PlanBudgets(t, db, "catsco-free", image26FreeBudgets)
	assertImage26PlanBudgets(t, db, "catsco-personal", image26PersonalBudgets)
	assertImage26PlanBudgets(t, db, "catsco-pro", image26ProBudgets)
	assertImage26PlanBudgets(t, db, "catsco-internal-all-models-50k", image26InternalBudgets)

	// Free keeps its five chat grants plus one free grant per new image model.
	assertFlashActivePackage(t, db, "free-26-baseline", 8, 2100, 1000, false)
	assertFlashLedger(t, db, freeUID, "free", 3, 300)

	// Paid packages keep their five chat grants and gain one grant per model at
	// 100 CNY, on the grant type the package already carried.
	for _, paid := range []struct {
		sourceRef string
		uid       int64
		base      float64
		amount    float64
	}{
		{"personal-26", personalUID, 5 * 2100, 2100},
		{"pro-26", proUID, 5 * 6300, 6300},
		{"internal-26", internalUID, 5 * 100, 100},
	} {
		assertFlashActivePackage(t, db, paid.sourceRef, 8, paid.base+300, paid.amount, false)
		var mirrored int
		if err := db.db.QueryRow(`
			SELECT COUNT(*) FROM commercial_quota_grants
			WHERE source_ref = $1 AND grant_type = 'order'
			  AND model IN ('gpt-image-2.6', 'gpt-image-2.6-flare', 'gpt-image-2.6-sunburst')
			  AND amount_cny = 100 AND revoked_at IS NULL`, paid.sourceRef).Scan(&mirrored); err != nil {
			t.Fatalf("count image 2.6 grants for %s: %v", paid.sourceRef, err)
		}
		if mirrored != 3 {
			t.Fatalf("%s image 2.6 grants = %d, want 3", paid.sourceRef, mirrored)
		}
		assertFlashLedger(t, db, paid.uid, "image_models_v1", 3, 300)
	}
}

func assertImage26Down(t *testing.T, db *Adapter, freeUID, personalUID, proUID, internalUID int64) {
	t.Helper()
	// Only the three new keys are removed; everything else stays as it was.
	assertImage26PlanBudgets(t, db, "catsco-free", image26FreeBudgets)
	assertImage26PlanBudgets(t, db, "catsco-personal", image26PersonalBudgets)
	assertImage26PlanBudgets(t, db, "catsco-pro", image26ProBudgets)
	assertImage26PlanBudgets(t, db, "catsco-internal-all-models-50k", image26InternalBudgets)

	assertFlashActivePackage(t, db, "free-26-baseline", 5, 1800, 1000, false)
	assertFlashLedger(t, db, freeUID, "plan_model_migration_rollback", 3, -300)
	for _, paid := range []struct {
		sourceRef string
		uid       int64
		base      float64
		amount    float64
	}{
		{"personal-26", personalUID, 5 * 2100, 2100},
		{"pro-26", proUID, 5 * 6300, 6300},
		{"internal-26", internalUID, 5 * 100, 100},
	} {
		assertFlashActivePackage(t, db, paid.sourceRef, 5, paid.base, paid.amount, false)
		assertFlashLedger(t, db, paid.uid, "plan_model_migration_rollback", 3, -300)
	}
}

// assertImage26PlanBudgets compares a plan's stored budgets with the pre-2.6
// shape plus the three new keys, so a migration that rewrote the whole map
// (dropping an operator's edit) fails here.
func assertImage26PlanBudgets(t *testing.T, db *Adapter, slug, previous string) {
	t.Helper()
	want := decodeModelBudgets([]byte(previous))
	for _, model := range image26Models {
		want[model] = 100
	}
	var raw []byte
	if err := db.db.QueryRow(`SELECT model_budgets FROM commercial_plans WHERE slug = $1`, slug).Scan(&raw); err != nil {
		t.Fatalf("query plan budgets %s: %v", slug, err)
	}
	if got := decodeModelBudgets(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan %s budgets = %v, want %v", slug, got, want)
	}
}

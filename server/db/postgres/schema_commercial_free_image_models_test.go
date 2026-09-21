package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	// Free plan state after 000022, before this migration.
	freeImageFiveModelBudgets = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"deepseek-flash":100,"glm-5.3-flash":100}`
	freeImageTenModelBudgets  = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"deepseek-flash":100,"glm-5.3-flash":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}`
)

func TestPostgresCommercialFreeImageModelsMigrationGrantsAndRollsBack(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_free_image_migration_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create free image migration test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open free image migration test postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create current schema: %v", err)
	}

	freeUID := createFlashMigrationUser(t, db, "free-image")
	paidPlanID := seedFlashMigrationPlan(t, db, "catsco-personal", "Personal", flashPersonalSixModelBudgets)
	freePlanID := seedFlashMigrationPlan(t, db, "catsco-free", "Free", freeImageFiveModelBudgets)
	_ = paidPlanID

	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	expiresAt := now.Add(30 * 24 * time.Hour)
	seedFreeImageBaseline(t, db, freeUID, freePlanID, "free-baseline", now, expiresAt)

	// Exercise this migration independently of newer startup policy.
	if _, err := db.db.Exec(migrateCommercialPlansFreeImageModels); err != nil {
		t.Fatalf("run free image models migration: %v", err)
	}
	assertFreeImageUp(t, db, freeUID, 5, 500)

	// The migration must be repeatable on every startup.
	if _, err := db.db.Exec(migrateCommercialPlansFreeImageModels); err != nil {
		t.Fatalf("free image models migration should be idempotent: %v", err)
	}
	assertFreeImageUp(t, db, freeUID, 5, 500)

	execFreeImageMigrationFile(t, db, "000024_commercial_free_image_models.down.sql")
	assertFreeImageDown(t, db, freeUID)

	execFreeImageMigrationFile(t, db, "000024_commercial_free_image_models.up.sql")
	assertFreeImageUp(t, db, freeUID, 10, 1000)
}

func TestFreeImageModelsStartupMigrationMatchesFile(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000024_commercial_free_image_models.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(up), "\r\n", "\n")) != strings.TrimSpace(migrateCommercialPlansFreeImageModels) {
		t.Fatal("startup migration diverged from numbered SQL migration (up)")
	}
	down, err := os.ReadFile("../migrations/postgres/000024_commercial_free_image_models.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(down), "\r\n", "\n")) != strings.TrimSpace(rollbackCommercialPlansFreeImageModels) {
		t.Fatal("startup migration diverged from numbered SQL migration (down)")
	}
}

func seedFreeImageBaseline(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, startsAt, expiresAt time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'free', $3, 'active', $4, $5)`, uid, planID, sourceRef, startsAt, expiresAt); err != nil {
		t.Fatalf("seed free entitlement: %v", err)
	}
	for model, amount := range map[string]float64{
		"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-v4-flash": 100,
		"deepseek-flash": 100, "glm-5.3-flash": 100,
	} {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, 'free', $3, $4, '1M', $5, $6, $7, 'free image fixture')`,
			uid, planID, model, amount, startsAt, expiresAt, sourceRef); err != nil {
			t.Fatalf("seed free grant %s: %v", model, err)
		}
	}
}

func assertFreeImageUp(t *testing.T, db *Adapter, freeUID int64, ledgerCount int, ledgerTotal float64) {
	t.Helper()
	assertFlashPlanBudgets(t, db, "catsco-free", freeImageTenModelBudgets)
	assertFlashPlanBudgets(t, db, "catsco-personal", flashPersonalSixModelBudgets)
	assertFlashActivePackage(t, db, "free-baseline", 10, 2300, 1000, true)
	assertFreeImageLedger(t, db, freeUID, "free", ledgerCount, ledgerTotal)
}

func assertFreeImageDown(t *testing.T, db *Adapter, freeUID int64) {
	t.Helper()
	assertFlashPlanBudgets(t, db, "catsco-free", freeImageFiveModelBudgets)
	assertFlashPlanBudgets(t, db, "catsco-personal", flashPersonalSixModelBudgets)
	assertFlashActivePackage(t, db, "free-baseline", 5, 1800, 1000, true)
	assertFreeImageLedger(t, db, freeUID, "plan_model_migration_rollback", 5, -500)
}

func assertFreeImageLedger(t *testing.T, db *Adapter, uid int64, sourceType string, wantCount int, wantTotal float64) {
	t.Helper()
	assertFlashLedger(t, db, uid, sourceType, wantCount, wantTotal)
}

func execFreeImageMigrationFile(t *testing.T, db *Adapter, name string) {
	t.Helper()
	execFlashMigrationFile(t, db, name)
}

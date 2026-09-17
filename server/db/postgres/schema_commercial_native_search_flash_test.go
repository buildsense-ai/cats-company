package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	flashPersonalFiveModelBudgets = `{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-v4-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}`
	flashPersonalSixModelBudgets  = `{"MiniMax-M2.7":1750,"MiniMax-M3":1750,"deepseek-v4-flash":1750,"deepseek-flash":1750,"glm-5.3-flash":1750,"gpt-5.6-terra":1750,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}`
	flashProFiveModelBudgets      = `{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-v4-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}`
	flashProSixModelBudgets       = `{"MiniMax-M2.7":5250,"MiniMax-M3":5250,"deepseek-v4-flash":5250,"deepseek-flash":5250,"glm-5.3-flash":5250,"gpt-5.6-terra":5250,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}`
	flashFreeFourModelBudgets     = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"glm-5.3-flash":100}`
	flashFreeFiveModelBudgets     = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"deepseek-flash":100,"glm-5.3-flash":100}`
)

var flashFivePaidModels = []string{
	"MiniMax-M2.7", "MiniMax-M3", "deepseek-v4-flash", "glm-5.3-flash", "gpt-5.6-terra",
}

var flashSixPaidModels = []string{
	"MiniMax-M2.7", "MiniMax-M3", "deepseek-v4-flash", "deepseek-flash",
	"glm-5.3-flash", "gpt-5.6-terra",
}

var flashImageModels = []string{
	"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "chatgpt-image-latest",
}

func TestPostgresCommercialNativeSearchFlashMigrationPreservesManualQuotaAndRollsBack(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_native_flash_migration_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create native search flash test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open native search flash test postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create current schema: %v", err)
	}

	personalUID := createFlashMigrationUser(t, db, "personal")
	proUID := createFlashMigrationUser(t, db, "pro")
	freeUID := createFlashMigrationUser(t, db, "free")
	personalPlanID := seedFlashMigrationPlan(t, db, "catsco-personal", "Personal", flashPersonalFiveModelBudgets)
	proPlanID := seedFlashMigrationPlan(t, db, "catsco-pro", "Pro", flashProFiveModelBudgets)
	freePlanID := seedFlashMigrationPlan(t, db, "catsco-free", "Free", flashFreeFourModelBudgets)

	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	expiresAt := now.Add(30 * 24 * time.Hour)
	seedFlashPaidPackage(t, db, personalUID, personalPlanID, "personal-order", 2100, now, expiresAt)
	seedFlashPaidPackage(t, db, proUID, proPlanID, "pro-order", 6300, now, expiresAt)
	seedFlashImageAddOn(t, db, personalUID, personalPlanID, "personal-order", 100, now, expiresAt)
	seedFlashImageAddOn(t, db, proUID, proPlanID, "pro-order", 300, now, expiresAt)
	seedFlashFreePackage(t, db, freeUID, freePlanID, "free-baseline", now, expiresAt)
	if _, err := db.db.Exec(`
		INSERT INTO commercial_quota_grants(
			uid, plan_id, grant_type, model, amount_cny, reset_duration,
			effective_at, expires_at, source_ref, note
		) VALUES ($1, $2, 'manual', 'gpt-5.6-terra', 17, '1M', $3, $4, 'manual-extra', 'must survive model migrations')`,
		personalUID, personalPlanID, now, expiresAt); err != nil {
		t.Fatalf("seed manual quota: %v", err)
	}
	seedFlashOrderSnapshots(t, db, personalUID, personalPlanID)

	// Exercise this historical migration independently of newer startup policy.
	if _, err := db.db.Exec(migrateCommercialPlansNativeSearchFlash); err != nil {
		t.Fatalf("run native search flash migration through CreateSchema: %v", err)
	}
	assertFlashMigrationUp(t, db, personalUID, proUID, freeUID, 1)
	assertFlashGrantRowCount(t, db, "personal-order", 16)
	if _, err := db.db.Exec(migrateCommercialPlansNativeSearchFlash); err != nil {
		t.Fatalf("native search flash startup migration should be idempotent: %v", err)
	}
	assertFlashMigrationUp(t, db, personalUID, proUID, freeUID, 1)
	assertFlashGrantRowCount(t, db, "personal-order", 16)

	execFlashMigrationFile(t, db, "000022_commercial_native_search_flash.down.sql")
	assertFlashMigrationDown(t, db, personalUID, proUID, freeUID)
	execFlashMigrationFile(t, db, "000022_commercial_native_search_flash.up.sql")
	assertFlashMigrationUp(t, db, personalUID, proUID, freeUID, 2)
}

func createFlashMigrationUser(t *testing.T, db *Adapter, suffix string) int64 {
	t.Helper()
	uid, err := db.CreateUser(&types.User{
		Username:    "native-flash-migration-" + suffix,
		Email:       "native-flash-migration-" + suffix + "@example.test",
		DisplayName: "Native search flash migration " + suffix,
		AccountType: types.AccountHuman,
		PassHash:    []byte("native-flash-migration-hash"),
	})
	if err != nil {
		t.Fatalf("create %s migration user: %v", suffix, err)
	}
	return uid
}

func seedFlashMigrationPlan(t *testing.T, db *Adapter, slug, name, budgets string) int64 {
	t.Helper()
	var planID int64
	if err := db.db.QueryRow(`
		INSERT INTO commercial_plans(
			slug, name, price_fen, currency, sale_state, monthly_budget_cny,
			model_budgets, duration_days, state, sort_order
		) VALUES ($1, $2, 1, 'CNY', 'public', 0, $3::jsonb, 30, 0, 100)
		RETURNING id`, slug, name, budgets).Scan(&planID); err != nil {
		t.Fatalf("seed plan %s: %v", slug, err)
	}
	return planID
}

func seedFlashPaidPackage(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, amount float64, startsAt, expiresAt time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', $3, 'active', $4, $5)`, uid, planID, sourceRef, startsAt, expiresAt); err != nil {
		t.Fatalf("seed paid entitlement %s: %v", sourceRef, err)
	}
	for _, model := range flashFivePaidModels {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, 'order', $3, $4, '1M', $5, $6, $7, 'five-model fixture')`,
			uid, planID, model, amount, startsAt, expiresAt, sourceRef); err != nil {
			t.Fatalf("seed paid grant %s/%s: %v", sourceRef, model, err)
		}
	}
}

func seedFlashImageAddOn(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, amount float64, startsAt, expiresAt time.Time) {
	t.Helper()
	for _, model := range flashImageModels {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, 'order', $3, $4, '1M', $5, $6, $7, 'image add-on fixture')`,
			uid, planID, model, amount, startsAt, expiresAt, sourceRef); err != nil {
			t.Fatalf("seed image add-on grant %s/%s: %v", sourceRef, model, err)
		}
	}
}

func seedFlashFreePackage(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, startsAt, expiresAt time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'free', $3, 'active', $4, $5)`, uid, planID, sourceRef, startsAt, expiresAt); err != nil {
		t.Fatalf("seed free entitlement: %v", err)
	}
	for model, amount := range map[string]float64{
		"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-v4-flash": 100, "glm-5.3-flash": 100,
	} {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, 'free', $3, $4, '1M', $5, $6, $7, 'free fixture')`,
			uid, planID, model, amount, startsAt, expiresAt, sourceRef); err != nil {
			t.Fatalf("seed free grant %s: %v", model, err)
		}
	}
}

func seedFlashOrderSnapshots(t *testing.T, db *Adapter, uid, planID int64) {
	t.Helper()
	for _, status := range []string{"created", "fulfilled"} {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_orders(
				order_no, uid, plan_id, plan_slug, plan_name, plan_duration_days,
				plan_model_budgets, amount_fen, channel, status, client_request_id
			) VALUES ($1, $2, $3, 'catsco-personal', 'Personal', 30, $4::jsonb, 1, 'test', $5, $6)`,
			"flash-"+status, uid, planID, flashPersonalFiveModelBudgets, status, "flash-request-"+status); err != nil {
			t.Fatalf("seed %s order snapshot: %v", status, err)
		}
	}
}

func assertFlashMigrationUp(t *testing.T, db *Adapter, personalUID, proUID, freeUID int64, migrationRuns int) {
	t.Helper()
	assertFlashPlanBudgets(t, db, "catsco-personal", flashPersonalSixModelBudgets)
	assertFlashPlanBudgets(t, db, "catsco-pro", flashProSixModelBudgets)
	assertFlashPlanBudgets(t, db, "catsco-free", flashFreeFiveModelBudgets)
	assertFlashActivePackage(t, db, "personal-order", 11, 11000, 1750, true)
	assertFlashActivePackage(t, db, "pro-order", 11, 33000, 5250, true)
	assertFlashActivePackage(t, db, "free-baseline", 5, 1800, 1000, true)
	assertFlashManualQuota(t, db, personalUID)
	assertFlashOrderSnapshot(t, db, "flash-created", flashPersonalSixModelBudgets)
	assertFlashOrderSnapshot(t, db, "flash-fulfilled", flashPersonalFiveModelBudgets)
	assertFlashLedger(t, db, personalUID, "plan_model_migration", 11*migrationRuns, 0)
	assertFlashLedger(t, db, proUID, "plan_model_migration", 11*migrationRuns, 0)
	assertFlashLedger(t, db, freeUID, "free", migrationRuns, float64(100*migrationRuns))
}

func assertFlashMigrationDown(t *testing.T, db *Adapter, personalUID, proUID, freeUID int64) {
	t.Helper()
	assertFlashPlanBudgets(t, db, "catsco-personal", flashPersonalFiveModelBudgets)
	assertFlashPlanBudgets(t, db, "catsco-pro", flashProFiveModelBudgets)
	assertFlashPlanBudgets(t, db, "catsco-free", flashFreeFourModelBudgets)
	assertFlashActivePackage(t, db, "personal-order", 10, 11000, 2100, false)
	assertFlashActivePackage(t, db, "pro-order", 10, 33000, 6300, false)
	assertFlashActivePackage(t, db, "free-baseline", 4, 1700, 1000, false)
	assertFlashManualQuota(t, db, personalUID)
	assertFlashOrderSnapshot(t, db, "flash-created", flashPersonalFiveModelBudgets)
	assertFlashOrderSnapshot(t, db, "flash-fulfilled", flashPersonalFiveModelBudgets)
	assertFlashLedger(t, db, personalUID, "plan_model_migration_rollback", 11, 0)
	assertFlashLedger(t, db, proUID, "plan_model_migration_rollback", 11, 0)
	assertFlashLedger(t, db, freeUID, "plan_model_migration_rollback", 1, -100)
}

func assertFlashPlanBudgets(t *testing.T, db *Adapter, slug, expected string) {
	t.Helper()
	var matches bool
	if err := db.db.QueryRow(`SELECT model_budgets = $2::jsonb FROM commercial_plans WHERE slug = $1`, slug, expected).Scan(&matches); err != nil {
		t.Fatalf("query plan budgets %s: %v", slug, err)
	}
	if !matches {
		t.Fatalf("plan %s budgets do not match %s", slug, expected)
	}
}

func assertFlashActivePackage(t *testing.T, db *Adapter, sourceRef string, wantCount int, wantTotal, wantMax float64, wantFlash bool) {
	t.Helper()
	var count, distinctCount, flashCount int
	var total, maxAmount float64
	if err := db.db.QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT model), COALESCE(SUM(amount_cny), 0)::float8,
		       COALESCE(MAX(amount_cny), 0)::float8,
		       COUNT(*) FILTER (WHERE model = 'deepseek-flash')
		FROM commercial_quota_grants
		WHERE source_ref = $1 AND revoked_at IS NULL`, sourceRef).
		Scan(&count, &distinctCount, &total, &maxAmount, &flashCount); err != nil {
		t.Fatalf("query active package %s: %v", sourceRef, err)
	}
	wantFlashCount := 0
	if wantFlash {
		wantFlashCount = 1
	}
	if count != wantCount || distinctCount != wantCount || total != wantTotal || maxAmount != wantMax || flashCount != wantFlashCount {
		t.Fatalf("active package %s = count:%d distinct:%d total:%v max:%v flash:%d", sourceRef, count, distinctCount, total, maxAmount, flashCount)
	}
}

func assertFlashManualQuota(t *testing.T, db *Adapter, uid int64) {
	t.Helper()
	var count int
	var amount float64
	if err := db.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(amount_cny), 0)::float8
		FROM commercial_quota_grants
		WHERE uid = $1 AND source_ref = 'manual-extra' AND grant_type = 'manual' AND revoked_at IS NULL`, uid).
		Scan(&count, &amount); err != nil {
		t.Fatalf("query manual quota: %v", err)
	}
	if count != 1 || amount != 17 {
		t.Fatalf("manual quota changed: count=%d amount=%v", count, amount)
	}
}

func assertFlashOrderSnapshot(t *testing.T, db *Adapter, orderNo, expected string) {
	t.Helper()
	var matches bool
	if err := db.db.QueryRow(`SELECT plan_model_budgets = $2::jsonb FROM commercial_orders WHERE order_no = $1`, orderNo, expected).Scan(&matches); err != nil {
		t.Fatalf("query order snapshot %s: %v", orderNo, err)
	}
	if !matches {
		t.Fatalf("order snapshot %s does not match expected budgets", orderNo)
	}
}

func assertFlashLedger(t *testing.T, db *Adapter, uid int64, sourceType string, wantCount int, wantTotal float64) {
	t.Helper()
	var count int
	var total float64
	if err := db.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(amount_cny), 0)::float8
		FROM commercial_quota_ledger WHERE uid = $1 AND source_type = $2`, uid, sourceType).
		Scan(&count, &total); err != nil {
		t.Fatalf("query ledger %s for uid %d: %v", sourceType, uid, err)
	}
	if count != wantCount || total != wantTotal {
		t.Fatalf("ledger %s for uid %d = count:%d total:%v, want count:%d total:%v", sourceType, uid, count, total, wantCount, wantTotal)
	}
}

func assertFlashGrantRowCount(t *testing.T, db *Adapter, sourceRef string, want int) {
	t.Helper()
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_grants WHERE source_ref = $1`, sourceRef).Scan(&count); err != nil {
		t.Fatalf("count grants for %s: %v", sourceRef, err)
	}
	if count != want {
		t.Fatalf("grant rows for %s = %d, want %d", sourceRef, count, want)
	}
}

func execFlashMigrationFile(t *testing.T, db *Adapter, name string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "migrations", "postgres", name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	if _, err := db.db.Exec(string(contents)); err != nil {
		t.Fatalf("execute migration %s: %v", name, err)
	}
}

func TestNativeSearchFlashStartupMigrationMatchesFile(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000022_commercial_native_search_flash.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(up), "\r\n", "\n")) != strings.TrimSpace(migrateCommercialPlansNativeSearchFlash) {
		t.Fatal("startup migration diverged from numbered SQL migration (up)")
	}
	down, err := os.ReadFile("../migrations/postgres/000022_commercial_native_search_flash.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(down), "\r\n", "\n")) != strings.TrimSpace(rollbackCommercialPlansNativeSearchFlash) {
		t.Fatal("startup migration diverged from numbered SQL migration (down)")
	}
}

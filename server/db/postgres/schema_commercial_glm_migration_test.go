package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	personalSixModelBudgets   = `{"MiniMax-M2.7":1750,"MiniMax-M3":1750,"deepseek-v4-flash":1750,"gpt-5.6-terra":1750,"gpt-5.6-sol":1750,"gpt-5.6-luna":1750}`
	personalSevenModelBudgets = `{"MiniMax-M2.7":1500,"MiniMax-M3":1500,"deepseek-v4-flash":1500,"glm-5.3-flash":1500,"gpt-5.6-terra":1500,"gpt-5.6-sol":1500,"gpt-5.6-luna":1500}`
	proSixModelBudgets        = `{"MiniMax-M2.7":5250,"MiniMax-M3":5250,"deepseek-v4-flash":5250,"gpt-5.6-terra":5250,"gpt-5.6-sol":5250,"gpt-5.6-luna":5250}`
	proSevenModelBudgets      = `{"MiniMax-M2.7":4500,"MiniMax-M3":4500,"deepseek-v4-flash":4500,"glm-5.3-flash":4500,"gpt-5.6-terra":4500,"gpt-5.6-sol":4500,"gpt-5.6-luna":4500}`
	freeThreeModelBudgets     = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100}`
	freeFourModelBudgets      = `{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"glm-5.3-flash":100}`
)

var sixPaidModels = []string{
	"MiniMax-M2.7", "MiniMax-M3", "deepseek-v4-flash",
	"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna",
}

func TestPostgresCommercialGLM53MigrationPreservesManualQuotaAndRollsBack(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_glm53_migration_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create GLM migration test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open GLM migration test postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create current schema: %v", err)
	}

	personalUID := createGLM53MigrationUser(t, db, "personal")
	proUID := createGLM53MigrationUser(t, db, "pro")
	freeUID := createGLM53MigrationUser(t, db, "free")
	personalPlanID := seedGLM53MigrationPlan(t, db, "catsco-personal", "Personal", personalSixModelBudgets)
	proPlanID := seedGLM53MigrationPlan(t, db, "catsco-pro", "Pro", proSixModelBudgets)
	freePlanID := seedGLM53MigrationPlan(t, db, "catsco-free", "Free", freeThreeModelBudgets)

	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	expiresAt := now.Add(30 * 24 * time.Hour)
	seedGLM53PaidPackage(t, db, personalUID, personalPlanID, "personal-order", 1750, now, expiresAt)
	seedGLM53PaidPackage(t, db, proUID, proPlanID, "pro-order", 5250, now, expiresAt)
	seedGLM53FreePackage(t, db, freeUID, freePlanID, "free-baseline", now, expiresAt)
	if _, err := db.db.Exec(`
		INSERT INTO commercial_quota_grants(
			uid, plan_id, grant_type, model, amount_cny, reset_duration,
			effective_at, expires_at, source_ref, note
		) VALUES ($1, $2, 'manual', 'gpt-5.6-terra', 17, '1M', $3, $4, 'manual-extra', 'must survive model migrations')`,
		personalUID, personalPlanID, now, expiresAt); err != nil {
		t.Fatalf("seed manual quota: %v", err)
	}
	seedGLM53OrderSnapshots(t, db, personalUID, personalPlanID)

	// This is the production startup path. It must migrate old active packages
	// and remain safe when every server restart executes CreateSchema again.
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("run GLM migration through CreateSchema: %v", err)
	}
	assertGLM53MigrationUp(t, db, personalUID, proUID, freeUID, 1)
	assertGLM53GrantRowCount(t, db, "personal-order", 13)
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("GLM startup migration should be idempotent: %v", err)
	}
	assertGLM53MigrationUp(t, db, personalUID, proUID, freeUID, 1)
	assertGLM53GrantRowCount(t, db, "personal-order", 13)

	execGLM53MigrationFile(t, db, "000017_commercial_glm_53_flash.down.sql")
	assertGLM53MigrationDown(t, db, personalUID, proUID, freeUID)
	execGLM53MigrationFile(t, db, "000017_commercial_glm_53_flash.up.sql")
	assertGLM53MigrationUp(t, db, personalUID, proUID, freeUID, 2)
}

func createGLM53MigrationUser(t *testing.T, db *Adapter, suffix string) int64 {
	t.Helper()
	uid, err := db.CreateUser(&types.User{
		Username:    "glm53-migration-" + suffix,
		Email:       "glm53-migration-" + suffix + "@example.test",
		DisplayName: "GLM migration " + suffix,
		AccountType: types.AccountHuman,
		PassHash:    []byte("glm53-migration-hash"),
	})
	if err != nil {
		t.Fatalf("create %s migration user: %v", suffix, err)
	}
	return uid
}

func seedGLM53MigrationPlan(t *testing.T, db *Adapter, slug, name, budgets string) int64 {
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

func seedGLM53PaidPackage(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, amount float64, startsAt, expiresAt time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', $3, 'active', $4, $5)`, uid, planID, sourceRef, startsAt, expiresAt); err != nil {
		t.Fatalf("seed paid entitlement %s: %v", sourceRef, err)
	}
	for _, model := range sixPaidModels {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, 'order', $3, $4, '1M', $5, $6, $7, 'six-model fixture')`,
			uid, planID, model, amount, startsAt, expiresAt, sourceRef); err != nil {
			t.Fatalf("seed paid grant %s/%s: %v", sourceRef, model, err)
		}
	}
}

func seedGLM53FreePackage(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, startsAt, expiresAt time.Time) {
	t.Helper()
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'free', $3, 'active', $4, $5)`, uid, planID, sourceRef, startsAt, expiresAt); err != nil {
		t.Fatalf("seed free entitlement: %v", err)
	}
	for model, amount := range map[string]float64{"MiniMax-M2.7": 1000, "MiniMax-M3": 500, "deepseek-v4-flash": 100} {
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

func seedGLM53OrderSnapshots(t *testing.T, db *Adapter, uid, planID int64) {
	t.Helper()
	for _, status := range []string{"created", "fulfilled"} {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_orders(
				order_no, uid, plan_id, plan_slug, plan_name, plan_duration_days,
				plan_model_budgets, amount_fen, channel, status, client_request_id
			) VALUES ($1, $2, $3, 'catsco-personal', 'Personal', 30, $4::jsonb, 1, 'test', $5, $6)`,
			"glm53-"+status, uid, planID, personalSixModelBudgets, status, "glm53-request-"+status); err != nil {
			t.Fatalf("seed %s order snapshot: %v", status, err)
		}
	}
}

func assertGLM53MigrationUp(t *testing.T, db *Adapter, personalUID, proUID, freeUID int64, migrationRuns int) {
	t.Helper()
	assertGLM53PlanBudgets(t, db, "catsco-personal", personalSevenModelBudgets)
	assertGLM53PlanBudgets(t, db, "catsco-pro", proSevenModelBudgets)
	assertGLM53PlanBudgets(t, db, "catsco-free", freeFourModelBudgets)
	assertGLM53ActivePackage(t, db, "personal-order", 7, 10500, 1500, true)
	assertGLM53ActivePackage(t, db, "pro-order", 7, 31500, 4500, true)
	assertGLM53ActivePackage(t, db, "free-baseline", 4, 1700, 1000, true)
	assertGLM53ManualQuota(t, db, personalUID)
	assertGLM53OrderSnapshot(t, db, "glm53-created", personalSevenModelBudgets)
	assertGLM53OrderSnapshot(t, db, "glm53-fulfilled", personalSixModelBudgets)
	assertGLM53Ledger(t, db, personalUID, "plan_model_migration", 13*migrationRuns, 0)
	assertGLM53Ledger(t, db, proUID, "plan_model_migration", 13*migrationRuns, 0)
	assertGLM53Ledger(t, db, freeUID, "free", migrationRuns, float64(100*migrationRuns))
}

func assertGLM53MigrationDown(t *testing.T, db *Adapter, personalUID, proUID, freeUID int64) {
	t.Helper()
	assertGLM53PlanBudgets(t, db, "catsco-personal", personalSixModelBudgets)
	assertGLM53PlanBudgets(t, db, "catsco-pro", proSixModelBudgets)
	assertGLM53PlanBudgets(t, db, "catsco-free", freeThreeModelBudgets)
	assertGLM53ActivePackage(t, db, "personal-order", 6, 10500, 1750, false)
	assertGLM53ActivePackage(t, db, "pro-order", 6, 31500, 5250, false)
	assertGLM53ActivePackage(t, db, "free-baseline", 3, 1600, 1000, false)
	assertGLM53ManualQuota(t, db, personalUID)
	assertGLM53OrderSnapshot(t, db, "glm53-created", personalSixModelBudgets)
	assertGLM53OrderSnapshot(t, db, "glm53-fulfilled", personalSixModelBudgets)
	assertGLM53Ledger(t, db, personalUID, "plan_model_migration_rollback", 13, 0)
	assertGLM53Ledger(t, db, proUID, "plan_model_migration_rollback", 13, 0)
	assertGLM53Ledger(t, db, freeUID, "plan_model_migration_rollback", 1, -100)
}

func assertGLM53PlanBudgets(t *testing.T, db *Adapter, slug, expected string) {
	t.Helper()
	var matches bool
	if err := db.db.QueryRow(`SELECT model_budgets = $2::jsonb FROM commercial_plans WHERE slug = $1`, slug, expected).Scan(&matches); err != nil {
		t.Fatalf("query plan budgets %s: %v", slug, err)
	}
	if !matches {
		t.Fatalf("plan %s budgets do not match %s", slug, expected)
	}
}

func assertGLM53ActivePackage(t *testing.T, db *Adapter, sourceRef string, wantCount int, wantTotal, wantMax float64, wantGLM bool) {
	t.Helper()
	var count, distinctCount, glmCount int
	var total, maxAmount float64
	if err := db.db.QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT model), COALESCE(SUM(amount_cny), 0)::float8,
		       COALESCE(MAX(amount_cny), 0)::float8,
		       COUNT(*) FILTER (WHERE model = 'glm-5.3-flash')
		FROM commercial_quota_grants
		WHERE source_ref = $1 AND revoked_at IS NULL`, sourceRef).
		Scan(&count, &distinctCount, &total, &maxAmount, &glmCount); err != nil {
		t.Fatalf("query active package %s: %v", sourceRef, err)
	}
	wantGLMCount := 0
	if wantGLM {
		wantGLMCount = 1
	}
	if count != wantCount || distinctCount != wantCount || total != wantTotal || maxAmount != wantMax || glmCount != wantGLMCount {
		t.Fatalf("active package %s = count:%d distinct:%d total:%v max:%v glm:%d", sourceRef, count, distinctCount, total, maxAmount, glmCount)
	}
}

func assertGLM53ManualQuota(t *testing.T, db *Adapter, uid int64) {
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

func assertGLM53OrderSnapshot(t *testing.T, db *Adapter, orderNo, expected string) {
	t.Helper()
	var matches bool
	if err := db.db.QueryRow(`SELECT plan_model_budgets = $2::jsonb FROM commercial_orders WHERE order_no = $1`, orderNo, expected).Scan(&matches); err != nil {
		t.Fatalf("query order snapshot %s: %v", orderNo, err)
	}
	if !matches {
		t.Fatalf("order snapshot %s does not match expected budgets", orderNo)
	}
}

func assertGLM53Ledger(t *testing.T, db *Adapter, uid int64, sourceType string, wantCount int, wantTotal float64) {
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

func assertGLM53GrantRowCount(t *testing.T, db *Adapter, sourceRef string, want int) {
	t.Helper()
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_grants WHERE source_ref = $1`, sourceRef).Scan(&count); err != nil {
		t.Fatalf("count grants for %s: %v", sourceRef, err)
	}
	if count != want {
		t.Fatalf("grant rows for %s = %d, want %d", sourceRef, count, want)
	}
}

func execGLM53MigrationFile(t *testing.T, db *Adapter, name string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "migrations", "postgres", name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	if _, err := db.db.Exec(string(contents)); err != nil {
		t.Fatalf("execute migration %s: %v", name, err)
	}
}

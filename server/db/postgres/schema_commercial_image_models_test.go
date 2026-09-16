package postgres

import (
	"os"
	"strings"
	"testing"
	"time"
)

const (
	personalPublicModelBudgets = `{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-v4-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100}`
	proPublicModelBudgets      = `{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-v4-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300}`
	personalImageModelBudgets  = `{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-v4-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}`
	proImageModelBudgets       = `{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-v4-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}`
)

func seedImageMigrationPackage(t *testing.T, db *Adapter, uid, planID int64, sourceRef string, perModel float64, startsAt time.Time) {
	t.Helper()
	expiresAt := startsAt.Add(30 * 24 * time.Hour)
	if _, err := db.db.Exec(`
		INSERT INTO commercial_entitlements(uid, plan_id, source, source_ref, state, starts_at, expires_at)
		VALUES ($1, $2, 'order', $3, 'active', $4, $5)`, uid, planID, sourceRef, startsAt, expiresAt); err != nil {
		t.Fatalf("seed image package entitlement %s: %v", sourceRef, err)
	}
	for _, model := range []string{"MiniMax-M2.7", "MiniMax-M3", "deepseek-v4-flash", "glm-5.3-flash", "gpt-5.6-terra"} {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_quota_grants(
				uid, plan_id, grant_type, model, amount_cny, reset_duration,
				effective_at, expires_at, source_ref, note
			) VALUES ($1, $2, 'order', $3, $4, '1M', $5, $6, $7, 'image add-on fixture')`,
			uid, planID, model, perModel, startsAt, expiresAt, sourceRef); err != nil {
			t.Fatalf("seed image package grant %s/%s: %v", sourceRef, model, err)
		}
	}
}

func seedImageMigrationOrders(t *testing.T, db *Adapter, uid, planID int64) {
	t.Helper()
	for _, status := range []string{"created", "fulfilled"} {
		if _, err := db.db.Exec(`
			INSERT INTO commercial_orders(
				order_no, uid, plan_id, plan_slug, plan_name, plan_duration_days,
				plan_model_budgets, amount_fen, channel, status, client_request_id
			) VALUES ($1, $2, $3, 'catsco-personal', 'Personal', 30, $4::jsonb, 1, 'test', $5, $6)`,
			"image-"+status, uid, planID, personalPublicModelBudgets, status, "image-request-"+status); err != nil {
			t.Fatalf("seed %s order snapshot: %v", status, err)
		}
	}
}

func assertImageMigrationPackage(t *testing.T, db *Adapter, sourceRef string, wantCount, wantImageCount int, wantTotal, wantImageTotal float64, wantStart time.Time) {
	t.Helper()
	var count, imageCount int
	var total, imageTotal float64
	var start, end time.Time
	if err := db.db.QueryRow(`
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE model IN (
		           'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare', 'gpt-image-2.5-sunburst', 'chatgpt-image-latest')),
		       COALESCE(SUM(amount_cny), 0)::float8,
		       COALESCE(SUM(amount_cny) FILTER (WHERE model IN (
		           'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare', 'gpt-image-2.5-sunburst', 'chatgpt-image-latest')), 0)::float8,
		       MIN(effective_at), MAX(expires_at)
		FROM commercial_quota_grants
		WHERE source_ref = $1 AND revoked_at IS NULL`, sourceRef).
		Scan(&count, &imageCount, &total, &imageTotal, &start, &end); err != nil {
		t.Fatalf("query package %s: %v", sourceRef, err)
	}
	if count != wantCount || imageCount != wantImageCount || total != wantTotal || imageTotal != wantImageTotal ||
		!start.Equal(wantStart) || !end.Equal(wantStart.Add(30*24*time.Hour)) {
		t.Fatalf("package %s = count:%d image:%d total:%v imageTotal:%v start:%v end:%v",
			sourceRef, count, imageCount, total, imageTotal, start, end)
	}
}

func TestPostgresCommercialImageModelsAddOnPreservesRenewalAndCustomQuota(t *testing.T) {
	db := commercialPolicyTestDB(t)
	personalUID := createGLM53MigrationUser(t, db, "image-personal")
	proUID := createGLM53MigrationUser(t, db, "image-pro")
	freeUID := createGLM53MigrationUser(t, db, "image-free")
	personalPlanID := seedGLM53MigrationPlan(t, db, "catsco-personal", "Personal", personalPublicModelBudgets)
	proPlanID := seedGLM53MigrationPlan(t, db, "catsco-pro", "Pro", proPublicModelBudgets)
	freePlanID := seedGLM53MigrationPlan(t, db, "catsco-free", "Free", freeFourModelBudgets)
	internalID := seedGLM53MigrationPlan(t, db, "internal-test", "Internal", personalPublicModelBudgets)

	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for index, ref := range []string{"current", "renewal"} {
		start := now.Add(time.Duration(index) * 30 * 24 * time.Hour)
		seedImageMigrationPackage(t, db, personalUID, personalPlanID, ref, 2100, start)
	}
	seedImageMigrationPackage(t, db, proUID, proPlanID, "pro-order", 6300, now)
	seedGLM53FreePackage(t, db, freeUID, freePlanID, "free-baseline", now, now.Add(30*24*time.Hour))
	seedGLM53PaidPackage(t, db, personalUID, internalID, "internal", 1750, now, now.Add(30*24*time.Hour))
	if _, err := db.db.Exec(`INSERT INTO commercial_quota_grants(uid, plan_id, grant_type, model, amount_cny, reset_duration, effective_at, expires_at, source_ref)
		VALUES ($1, $2, 'manual', 'gpt-5.6-sol', 17, '1M', $3, $4, 'manual')`, personalUID, personalPlanID, now, now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	seedImageMigrationOrders(t, db, personalUID, personalPlanID)

	for i := 0; i < 2; i++ {
		if _, err := db.db.Exec(migrateCommercialImageModels); err != nil {
			t.Fatalf("run image migration: %v", err)
		}
	}
	assertGLM53PlanBudgets(t, db, "catsco-personal", personalImageModelBudgets)
	assertGLM53PlanBudgets(t, db, "catsco-pro", proImageModelBudgets)
	assertGLM53PlanBudgets(t, db, "catsco-free", freeFourModelBudgets)
	for index, ref := range []string{"current", "renewal"} {
		assertImageMigrationPackage(t, db, ref, 10, 5, 11000, 500, now.Add(time.Duration(index)*30*24*time.Hour))
	}
	assertImageMigrationPackage(t, db, "pro-order", 10, 5, 33000, 1500, now)
	var preserved int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_grants WHERE revoked_at IS NULL AND source_ref IN ('internal','manual')`).Scan(&preserved); err != nil || preserved != 7 {
		t.Fatalf("custom grants changed: %v %d", err, preserved)
	}
	assertGLM53OrderSnapshot(t, db, "image-created", personalImageModelBudgets)
	assertGLM53OrderSnapshot(t, db, "image-fulfilled", personalPublicModelBudgets)
	assertGLM53Ledger(t, db, personalUID, "image_models_v1", 10, 1000)
	assertGLM53Ledger(t, db, proUID, "image_models_v1", 5, 1500)

	execGLM53MigrationFile(t, db, "000021_commercial_image_models.down.sql")
	execGLM53MigrationFile(t, db, "000021_commercial_image_models.down.sql")
	assertGLM53PlanBudgets(t, db, "catsco-personal", personalPublicModelBudgets)
	assertGLM53PlanBudgets(t, db, "catsco-pro", proPublicModelBudgets)
	for index, ref := range []string{"current", "renewal"} {
		assertImageMigrationPackage(t, db, ref, 5, 0, 10500, 0, now.Add(time.Duration(index)*30*24*time.Hour))
	}
	assertImageMigrationPackage(t, db, "pro-order", 5, 0, 31500, 0, now)
	assertGLM53Ledger(t, db, personalUID, "image_models_v1_rollback", 10, -1000)
	assertGLM53Ledger(t, db, proUID, "image_models_v1_rollback", 5, -1500)

	execGLM53MigrationFile(t, db, "000021_commercial_image_models.up.sql")
	for index, ref := range []string{"current", "renewal"} {
		assertImageMigrationPackage(t, db, ref, 10, 5, 11000, 500, now.Add(time.Duration(index)*30*24*time.Hour))
	}
	assertImageMigrationPackage(t, db, "pro-order", 10, 5, 33000, 1500, now)
}

func TestImageModelsStartupMigrationMatchesFile(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000021_commercial_image_models.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(up), "\r\n", "\n")) != strings.TrimSpace(migrateCommercialImageModels) {
		t.Fatal("startup migration diverged from numbered SQL migration")
	}
}

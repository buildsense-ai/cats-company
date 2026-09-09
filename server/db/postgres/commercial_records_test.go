package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func commercialPolicyTestDB(t *testing.T) *Adapter {
	t.Helper()
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for PostgreSQL integration tests")
	}
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("cats_plan_policy_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(name)); err != nil {
		t.Fatal(err)
	}
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, dsn, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); base.db.Exec(`DROP SCHEMA ` + quoteIdent(name) + ` CASCADE`); base.Close() })
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPostgresCommercialRecordsBeyondFirstHundred(t *testing.T) {
	db := commercialPolicyTestDB(t)
	uid := createGLM53MigrationUser(t, db, "records")
	planID := seedGLM53MigrationPlan(t, db, "records-plan", "Records", `{}`)
	invite := &types.CommercialInviteCode{Code: "TEST-GENERATED-INVITE", PlanID: planID, MaxRedemptions: 3, CreateOnly: true}
	if _, err := db.CreateCommercialInviteCode(invite); err != nil {
		t.Fatal(err)
	}
	invite.MaxRedemptions = 99
	if _, err := db.CreateCommercialInviteCode(invite); err == nil {
		t.Fatal("create-only collision overwrote existing invite")
	}
	var remaining int
	if err := db.db.QueryRow(`SELECT max_redemptions FROM commercial_invite_codes WHERE code=$1`, invite.Code).Scan(&remaining); err != nil || remaining != 3 {
		t.Fatalf("invite collision mutated original: %v %d", err, remaining)
	}
	invite.CreateOnly = false
	if _, err := db.CreateCommercialInviteCode(invite); err != nil {
		t.Fatal(err)
	}
	_, err := db.db.Exec(`INSERT INTO commercial_orders(order_no, uid, plan_id, plan_slug, plan_name,
		plan_duration_days, plan_model_budgets, amount_fen, channel, status, client_request_id)
		SELECT 'page-' || n, $1, $2, 'records-plan', 'Records', 30, '{}'::jsonb, 1, 'test',
		CASE WHEN n % 2 = 0 THEN 'created' ELSE 'fulfilled' END, 'private-request-' || n
		FROM generate_series(1,205) n`, uid, planID)
	if err != nil {
		t.Fatal(err)
	}
	q := types.CommercialRecordsQuery{Kind: "orders", UID: uid, Limit: 20, Offset: 200}
	page, err := db.ListCommercialRecords(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(page.Records, &rows); err != nil {
		t.Fatal(err)
	}
	if page.Total != 205 || page.HasMore || len(rows) != 5 || rows[0]["order_no"] != "page-5" {
		t.Fatalf("bad last page: %#v %s", page, page.Records)
	}
	if strings.Contains(string(page.Records), "private-request") || strings.Contains(string(page.Records), "client_request_id") {
		t.Fatal("private order metadata leaked")
	}
	q.Offset = 0
	q.Status = "created"
	page, err = db.ListCommercialRecords(context.Background(), q)
	if err != nil || page.Total != 102 || !page.HasMore {
		t.Fatalf("filter/count: %v %#v", err, page)
	}
	q.Offset = 400
	page, err = db.ListCommercialRecords(context.Background(), q)
	if err != nil || page.Total != 102 || string(page.Records) != "[]" {
		t.Fatalf("empty page lost count: %v %#v", err, page)
	}
	q.Offset = 0
	q.Status = ""
	q.Search = "page-205"
	page, err = db.ListCommercialRecords(context.Background(), q)
	if err != nil || page.Total != 1 {
		t.Fatalf("search: %v %#v", err, page)
	}
	for _, kind := range []string{"invites", "payment-events", "entitlements", "operator-events"} {
		_, err := db.ListCommercialRecords(context.Background(), types.CommercialRecordsQuery{Kind: kind, Limit: 20})
		if err != nil {
			t.Fatalf("%s projection: %v", kind, err)
		}
	}
}

func TestPostgresCommercialPublicModelsPreserveRenewalAndCustomQuota(t *testing.T) {
	db := commercialPolicyTestDB(t)
	uid := createGLM53MigrationUser(t, db, "migration")
	planID := seedGLM53MigrationPlan(t, db, "catsco-personal", "Pro", personalSevenModelBudgets)
	internalID := seedGLM53MigrationPlan(t, db, "internal-test", "Internal", personalSevenModelBudgets)
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for index, ref := range []string{"current", "renewal"} {
		start := now.Add(time.Duration(index) * 30 * 24 * time.Hour)
		seedGLM53PaidPackage(t, db, uid, planID, ref, 1750, start, start.Add(30*24*time.Hour))
	}
	seedGLM53PaidPackage(t, db, uid, internalID, "internal", 1750, now, now.Add(30*24*time.Hour))
	seedGLM53OrderSnapshots(t, db, uid, planID)
	if _, err := db.db.Exec(`INSERT INTO commercial_quota_grants(uid, plan_id, grant_type, model, amount_cny, reset_duration, effective_at, source_ref)
		VALUES ($1,$2,'manual','gpt-5.6-sol',17,'1M',$3,'manual')`, uid, planID, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.CreateSchema(); err != nil {
			t.Fatal(err)
		}
	}
	for index, ref := range []string{"current", "renewal"} {
		var count, restricted int
		var total float64
		var start, end time.Time
		err := db.db.QueryRow(`SELECT COUNT(*), SUM(amount_cny), MIN(effective_at), MAX(expires_at),
			COUNT(*) FILTER (WHERE model IN ('gpt-5.6-sol','gpt-5.6-luna')) FROM commercial_quota_grants WHERE source_ref=$1 AND revoked_at IS NULL`, ref).Scan(&count, &total, &start, &end, &restricted)
		wantStart := now.Add(time.Duration(index) * 30 * 24 * time.Hour)
		if err != nil || count != 5 || total != 10500 || restricted != 0 || !start.Equal(wantStart) || !end.Equal(wantStart.Add(30*24*time.Hour)) {
			t.Fatalf("%s changed interval/quota: %v %d %f %v %v", ref, err, count, total, start, end)
		}
	}
	var preserved int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM commercial_quota_grants WHERE revoked_at IS NULL AND source_ref IN ('internal','manual')`).Scan(&preserved); err != nil || preserved != 7 {
		t.Fatalf("custom grants changed: %v %d", err, preserved)
	}
	assertGLM53OrderSnapshot(t, db, "glm53-fulfilled", personalSixModelBudgets)
	assertGLM53OrderSnapshot(t, db, "glm53-created", `{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-v4-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100}`)
	var balance float64
	if err := db.db.QueryRow(`SELECT SUM(amount_cny) FROM commercial_quota_ledger WHERE source_type='public_models_v2'`).Scan(&balance); err != nil || balance != 0 {
		t.Fatalf("migration ledger unbalanced: %v %f", err, balance)
	}
	down, err := os.ReadFile("../migrations/postgres/000020_commercial_public_models.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.db.Exec(string(down)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var total float64
	if err := db.db.QueryRow(`SELECT COUNT(*),SUM(amount_cny) FROM commercial_quota_grants WHERE source_ref='renewal' AND revoked_at IS NULL`).Scan(&count, &total); err != nil || count != 7 || total != 10500 {
		t.Fatalf("rollback changed renewal total: %v %d %f", err, count, total)
	}
	if _, err := db.db.Exec(migrateCommercialPublicModels); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*),SUM(amount_cny) FROM commercial_quota_grants WHERE source_ref='renewal' AND revoked_at IS NULL`).Scan(&count, &total); err != nil || count != 5 || total != 10500 {
		t.Fatalf("reapply changed renewal total: %v %d %f", err, count, total)
	}
}

func TestPublicModelsStartupMigrationMatchesFile(t *testing.T) {
	up, err := os.ReadFile("../migrations/postgres/000020_commercial_public_models.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.ReplaceAll(string(up), "\r\n", "\n")) != strings.TrimSpace(migrateCommercialPublicModels) {
		t.Fatal("startup migration diverged from numbered SQL migration")
	}
}

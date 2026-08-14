package postgres

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestCreateSchemaMigratesLegacyCommercialRefundColumns(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_refund_migration_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open schema postgres connection: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create current schema: %v", err)
	}

	_, err := db.db.Exec(`
		DROP INDEX IF EXISTS idx_commercial_quota_grants_source;
		DROP INDEX IF EXISTS uk_commercial_refund_ledger_grant;
		ALTER TABLE commercial_quota_grants
			DROP COLUMN source_ref,
			DROP COLUMN revoked_at;
		ALTER TABLE commercial_orders
			DROP COLUMN refund_request_no,
			DROP COLUMN refunded_at;
	`)
	if err != nil {
		t.Fatalf("prepare legacy commercial schema: %v", err)
	}

	var uid int64
	if err := db.db.QueryRow(
		`INSERT INTO users (username, pass_hash) VALUES ($1, $2) RETURNING id`,
		"legacy-refund-user", []byte("hash"),
	).Scan(&uid); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	if _, err := db.db.Exec(`
		INSERT INTO commercial_quota_grants (uid, grant_type, model, amount_cny, note)
		VALUES ($1, 'order', 'gpt-5.6-terra', 1, 'order CC-LEGACY-1')
	`, uid); err != nil {
		t.Fatalf("insert legacy grant: %v", err)
	}

	if err := db.CreateSchema(); err != nil {
		t.Fatalf("migrate legacy commercial schema: %v", err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("commercial migration should be idempotent: %v", err)
	}

	var sourceRef string
	var revoked bool
	if err := db.db.QueryRow(`
		SELECT source_ref, revoked_at IS NOT NULL
		FROM commercial_quota_grants
		WHERE uid = $1
	`, uid).Scan(&sourceRef, &revoked); err != nil {
		t.Fatalf("read migrated grant: %v", err)
	}
	if sourceRef != "CC-LEGACY-1" || revoked {
		t.Fatalf("legacy grant was not migrated: source_ref=%q revoked=%v", sourceRef, revoked)
	}

	var orderColumns int
	if err := db.db.QueryRow(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'commercial_orders'
		  AND column_name IN ('refund_request_no', 'refunded_at')
	`).Scan(&orderColumns); err != nil {
		t.Fatalf("inspect migrated order columns: %v", err)
	}
	if orderColumns != 2 {
		t.Fatalf("expected both refund order columns, got %d", orderColumns)
	}
}

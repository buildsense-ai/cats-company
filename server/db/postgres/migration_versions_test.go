package postgres

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type migrationVersionFiles struct {
	name string
	up   bool
	down bool
}

func TestPostgresMigrationVersionsAreUniqueAndPaired(t *testing.T) {
	const migrationsDir = "../migrations/postgres"
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read PostgreSQL migrations: %v", err)
	}

	versions := make(map[uint64]migrationVersionFiles)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}

		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			t.Fatalf("migration filename must start with a numeric version: %s", entry.Name())
		}
		version, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			t.Fatalf("parse migration version from %s: %v", entry.Name(), err)
		}

		direction := ""
		switch {
		case strings.HasSuffix(entry.Name(), ".up.sql"):
			direction = ".up.sql"
		case strings.HasSuffix(entry.Name(), ".down.sql"):
			direction = ".down.sql"
		default:
			t.Fatalf("migration filename must end in .up.sql or .down.sql: %s", entry.Name())
		}

		name := strings.TrimSuffix(entry.Name(), direction)
		files := versions[version]
		if files.name != "" && files.name != name {
			t.Fatalf("migration version %d is used by both %s and %s", version, files.name, name)
		}
		files.name = name
		if direction == ".up.sql" {
			if files.up {
				t.Fatalf("migration version %d has multiple up files", version)
			}
			files.up = true
		} else {
			if files.down {
				t.Fatalf("migration version %d has multiple down files", version)
			}
			files.down = true
		}
		versions[version] = files
	}

	for version, files := range versions {
		if !files.up || !files.down {
			t.Errorf("migration version %d must have one up and one down file: %#v", version, files)
		}
	}
}

func TestVersionTwoReconciliationRestoresCommercialSchema(t *testing.T) {
	migration, err := os.ReadFile("../migrations/postgres/000007_reconcile_commercial_relay_foundation.up.sql")
	if err != nil {
		t.Fatalf("read version-two reconciliation migration: %v", err)
	}

	sql := string(migration)
	for _, table := range []string{
		"commercial_plans",
		"commercial_invite_codes",
		"commercial_entitlements",
		"commercial_quota_grants",
		"commercial_quota_ledger",
	} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("reconciliation migration does not restore %s", table)
		}
	}
}

func TestHistoricalMigrationReconciliationDownsAreNonDestructive(t *testing.T) {
	for _, name := range []string{
		"000006_weixin_clawbot_tokens.down.sql",
		"000007_reconcile_commercial_relay_foundation.down.sql",
	} {
		migration, err := os.ReadFile(filepath.Join("../migrations/postgres", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(strings.ToUpper(string(migration)), "DROP TABLE") {
			t.Errorf("%s must not delete historically ambiguous schema", name)
		}
	}
}

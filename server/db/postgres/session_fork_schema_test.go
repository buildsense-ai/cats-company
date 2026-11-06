package postgres

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The startup schema is the concatenation of every fork migration, because it
// runs on each boot while numbered migrations run once. Comparing against the
// pair keeps a new column from being added to one side only.
func readMigration(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../migrations/postgres/" + name + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSessionForkPostgresMigrationMatchesStartup(t *testing.T) {
	expected := readMigration(t, "000042_session_forks") + readMigration(t, "000063_session_fork_turn_count")
	for _, eol := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("EOL=%q", eol), func(t *testing.T) {
			migration := strings.ReplaceAll(strings.ReplaceAll(expected, "\r\n", "\n"), "\n", eol)
			if strings.TrimSpace(strings.ReplaceAll(migration, "\r\n", "\n")) != strings.TrimSpace(strings.ReplaceAll(migrateSessionForks, "\r\n", "\n")) {
				t.Fatal("session fork startup migration differs from numbered migrations")
			}
		})
	}
}

// A migration that cannot be rolled back is not a migration. The column is an
// addition, so its down direction must remove it.
func TestSessionForkTurnCountMigrationIsReversible(t *testing.T) {
	down, err := os.ReadFile("../migrations/postgres/000063_session_fork_turn_count.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), "DROP COLUMN IF EXISTS source_turn_count") {
		t.Fatalf("down migration does not remove the column: %s", down)
	}
}

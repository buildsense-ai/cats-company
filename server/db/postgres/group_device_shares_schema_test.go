package postgres

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGroupDeviceSharingMigrationMatchesRuntimeSchema(t *testing.T) {
	migration, err := os.ReadFile("../migrations/postgres/000082_group_device_sharing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	runtime := strings.TrimSpace(createGroupDeviceShares + "\n" + upgradeConversationDeviceSelectionOwner)
	if runtime != strings.TrimSpace(string(migration)) {
		t.Fatal("group sharing migration and startup schema differ")
	}
}

// golang-migrate identifies files by numeric version, even when git can merge
// two different filenames without a conflict. Guard that deployment contract.
func TestPostgresMigrationVersionsUnique(t *testing.T) {
	for _, direction := range []string{"up", "down"} {
		files, err := filepath.Glob("../migrations/postgres/*." + direction + ".sql")
		if err != nil || len(files) == 0 {
			t.Fatal("migration files unavailable", err)
		}
		seen := map[uint64]string{}
		for _, file := range files {
			name := filepath.Base(file)
			versionText, _, found := strings.Cut(name, "_")
			if !found {
				t.Fatal("migration filename lacks version", name)
			}
			version, err := strconv.ParseUint(versionText, 10, 64)
			if err != nil {
				t.Fatal("invalid migration version", name)
			}
			if previous, ok := seen[version]; ok {
				t.Fatalf("duplicate migration version %d: %s and %s", version, previous, name)
			}
			seen[version] = name
		}
	}
}

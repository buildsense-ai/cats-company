package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestGroupDeviceSharingMigrationMatchesRuntimeSchema(t *testing.T) {
	migration, err := os.ReadFile("../migrations/postgres/000081_group_device_sharing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	runtime := strings.TrimSpace(createGroupDeviceShares + "\n" + upgradeConversationDeviceSelectionOwner)
	if runtime != strings.TrimSpace(string(migration)) {
		t.Fatal("group sharing migration and startup schema differ")
	}
}

package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestPushSubscriptionsSchemaKeepsUserRelationshipAndUpdatedAtTrigger(t *testing.T) {
	const foreignKey = "CONSTRAINT fk_push_subscriptions_uid FOREIGN KEY (uid) REFERENCES users(id) ON DELETE CASCADE"
	const trigger = "CREATE OR REPLACE TRIGGER trg_push_subscriptions_updated_at"

	if !strings.Contains(createPushSubscriptionsTable, foreignKey) {
		t.Fatalf("push subscriptions must be deleted with their user; schema=%s", createPushSubscriptionsTable)
	}
	if !strings.Contains(createUpdatedAtTriggers, trigger) {
		t.Fatalf("push subscriptions must maintain updated_at; triggers=%s", createUpdatedAtTriggers)
	}

	migration, err := os.ReadFile("../migrations/postgres/000005_push_subscriptions.up.sql")
	if err != nil {
		t.Fatalf("read push subscriptions migration: %v", err)
	}
	if !strings.Contains(string(migration), foreignKey) {
		t.Fatalf("push subscription migration must match the schema foreign key; migration=%s", migration)
	}
	if !strings.Contains(string(migration), trigger) {
		t.Fatalf("push subscription migration must install the updated_at trigger; migration=%s", migration)
	}
}

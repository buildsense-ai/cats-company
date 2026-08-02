package postgres

import (
	"strings"
	"testing"
)

func TestPushSubscriptionsSchemaKeepsUserRelationshipAndUpdatedAtTrigger(t *testing.T) {
	const foreignKey = "CONSTRAINT fk_push_subscriptions_uid FOREIGN KEY (uid) REFERENCES users(id) ON DELETE CASCADE"
	const trigger = "CREATE OR REPLACE TRIGGER trg_push_subscriptions_updated_at"
	const registrationID = "registration_id VARCHAR(64) NOT NULL DEFAULT ''"

	if !strings.Contains(createPushSubscriptionsTable, foreignKey) {
		t.Fatalf("push subscriptions must be deleted with their user; schema=%s", createPushSubscriptionsTable)
	}
	if !strings.Contains(createUpdatedAtTriggers, trigger) {
		t.Fatalf("push subscriptions must maintain updated_at; triggers=%s", createUpdatedAtTriggers)
	}
	if !strings.Contains(createPushSubscriptionsTable, registrationID) {
		t.Fatalf("push subscriptions must create registration_id; schema=%s", createPushSubscriptionsTable)
	}
}

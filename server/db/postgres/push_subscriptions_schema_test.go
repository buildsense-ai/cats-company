package postgres

import (
	"strings"
	"testing"
)

func TestPushSubscriptionsSchemaKeepsUserRelationshipAndUpdatedAtTrigger(t *testing.T) {
	const foreignKey = "CONSTRAINT fk_push_subscriptions_uid FOREIGN KEY (uid) REFERENCES users(id) ON DELETE CASCADE"
	const trigger = "CREATE OR REPLACE TRIGGER trg_push_subscriptions_updated_at"
	const registrationID = "ADD COLUMN IF NOT EXISTS registration_id VARCHAR(64) NOT NULL DEFAULT ''"
	const foreignKeyName = "ADD CONSTRAINT fk_push_subscriptions_uid"
	const foreignKeyReference = "FOREIGN KEY (uid) REFERENCES users(id) ON DELETE CASCADE"

	if !strings.Contains(createPushSubscriptionsTable, foreignKey) {
		t.Fatalf("push subscriptions must be deleted with their user; schema=%s", createPushSubscriptionsTable)
	}
	if !strings.Contains(createUpdatedAtTriggers, trigger) {
		t.Fatalf("push subscriptions must maintain updated_at; triggers=%s", createUpdatedAtTriggers)
	}
	if !strings.Contains(migratePushSubscriptionsAddRegistrationID, registrationID) {
		t.Fatalf("push subscriptions must add registration_id during schema upgrades; migration=%s", migratePushSubscriptionsAddRegistrationID)
	}
	if !strings.Contains(migratePushSubscriptionsAddUserForeignKey, foreignKeyName) ||
		!strings.Contains(migratePushSubscriptionsAddUserForeignKey, foreignKeyReference) {
		t.Fatalf("push subscriptions must add a missing user foreign key during schema upgrades; migration=%s", migratePushSubscriptionsAddUserForeignKey)
	}
}

package postgres

import (
	"strings"
	"testing"
)

func TestConversationNotificationMutesBelongToUserWithoutRequiringTopicRow(t *testing.T) {
	const userForeignKey = "user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE"
	if !strings.Contains(createConversationNotificationMutesTable, userForeignKey) {
		t.Fatalf("conversation notification mutes must be deleted with their user; schema=%s", createConversationNotificationMutesTable)
	}
	if strings.Contains(createConversationNotificationMutesTable, "topic_id VARCHAR(64) NOT NULL REFERENCES topics(id)") {
		t.Fatalf("conversation notification mutes must support visible P2P conversations before their topic row exists; schema=%s", createConversationNotificationMutesTable)
	}
}

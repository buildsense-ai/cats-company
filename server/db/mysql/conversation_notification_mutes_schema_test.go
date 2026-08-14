package mysql

import (
	"strings"
	"testing"
)

func TestConversationNotificationMutesBelongToUserWithoutRequiringTopicRow(t *testing.T) {
	const userForeignKey = "FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE"
	if !strings.Contains(createConversationNotificationMutesTable, userForeignKey) {
		t.Fatalf("conversation notification mutes must be deleted with their user; schema=%s", createConversationNotificationMutesTable)
	}
	if strings.Contains(createConversationNotificationMutesTable, "FOREIGN KEY (topic_id)") {
		t.Fatalf("conversation notification mutes must support visible P2P conversations before their topic row exists; schema=%s", createConversationNotificationMutesTable)
	}
}

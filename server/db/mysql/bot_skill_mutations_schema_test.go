package mysql

import (
	"strings"
	"testing"
)

func TestBotSkillMutationSchemaEnforcesSingleActiveAndIdempotentRequest(t *testing.T) {
	for _, required := range []string{
		"UNIQUE KEY uk_bot_skill_mutations_request (actor_user_uid, bot_uid, client_request_id)",
		"UNIQUE KEY uk_bot_skill_mutations_active (bot_uid, active_slot)",
		"lease_generation BIGINT NOT NULL DEFAULT 1",
	} {
		if !strings.Contains(createBotSkillMutationsTable, required) {
			t.Fatalf("schema missing %q", required)
		}
	}
}

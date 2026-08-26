package postgres

import (
	"strings"
	"testing"
)

func TestBotSkillMutationSchemaEnforcesSingleActiveAndIdempotentRequest(t *testing.T) {
	for _, required := range []string{
		"uk_bot_skill_mutations_request UNIQUE (actor_user_uid, bot_uid, client_request_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS uk_bot_skill_mutations_active",
		"lease_generation BIGINT NOT NULL DEFAULT 1",
	} {
		if !strings.Contains(createBotSkillMutationsTable+createBotSkillMutationsActiveIndex, required) {
			t.Fatalf("schema missing %q", required)
		}
	}
	for _, required := range []string{
		"activation_definition_revision",
		"activation_skill_set_hash",
		"activation_runtime_body_id",
		"activation_installation_id",
	} {
		if !strings.Contains(migrateBotSkillMutationsAddActivationFacts, required) {
			t.Fatalf("activation fact migration missing %q", required)
		}
	}
}

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
	for _, migration := range []string{
		migrateBotSkillMutationsAddActivationDefinitionRevision,
		migrateBotSkillMutationsAddActivationSkillSetHash,
		migrateBotSkillMutationsAddActivationRuntimeBodyID,
		migrateBotSkillMutationsAddActivationInstallationID,
	} {
		if !strings.Contains(migration, "ALTER TABLE bot_skill_mutations ADD COLUMN activation_") {
			t.Fatalf("activation fact migration is not additive: %q", migration)
		}
	}
}

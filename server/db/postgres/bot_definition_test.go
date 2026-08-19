package postgres

import (
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestInitializeBotDefinitionIfAbsentPreservesExistingEmptySkills(t *testing.T) {
	record := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "42",
			Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "existing-model"},
			Prompt: &types.BotPromptDefinition{Selected: "existing-prompt"},
			Skills: []types.BotSkillRef{},
		},
		Exists: true,
	}
	initial := types.BotDefinition{
		Schema: types.BotDefinitionSchema,
		BotID:  "42",
		Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "incoming-model"},
		Prompt: &types.BotPromptDefinition{Selected: "incoming-prompt"},
		Skills: []types.BotSkillRef{{
			Source:      "skillhub",
			SkillID:     "legacy-skill",
			Version:     "1.0.0",
			ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
	}

	initializeBotDefinitionIfAbsent(record, initial)

	if len(record.Definition.Skills) != 0 {
		t.Fatalf("existing explicit empty skills must remain empty, got %#v", record.Definition.Skills)
	}
}

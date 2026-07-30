package store

import (
	"encoding/json"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestEncodeBotDefinitionJSONPreservesUnrelatedConfiguration(t *testing.T) {
	raw := []byte(`{"channel":"feishu","nested":{"keep":true}}`)
	record := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "43",
			Model: types.BotDefinitionModel{
				Kind:    "catalog",
				ModelID: "minimax-m3",
			},
			Prompt: &types.BotPromptDefinition{Selected: "custom", CustomSystemPrompt: "Stay concise."},
			Skills: []types.BotSkillRef{{
				Source: "skillhub", SkillID: "lin/review", Version: "1.2.0",
				ContentHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}},
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 3},
		Exists:  true,
	}

	next, err := EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(next, &root); err != nil {
		t.Fatal(err)
	}
	if string(root["channel"]) != `"feishu"` || len(root["nested"]) == 0 {
		t.Fatalf("unrelated config was not preserved: %s", next)
	}
	decoded, err := DecodeBotDefinitionJSON(next, 43)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Exists ||
		decoded.Definition.Model.ModelID != "minimax-m3" ||
		decoded.Definition.Prompt == nil ||
		decoded.Definition.Prompt.Selected != "custom" ||
		decoded.Definition.Prompt.CustomSystemPrompt != "Stay concise." ||
		len(decoded.Definition.Skills) != 1 ||
		decoded.Definition.Skills[0].SkillID != "lin/review" ||
		decoded.Runtime.DesiredRevision != 3 {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestEncodeBotDefinitionJSONRemovesLegacyIndependentSkills(t *testing.T) {
	raw := []byte(`{"bot_skills":{"revision":9},"channel":"feishu"}`)
	record := defaultBotDefinitionRecord(43)
	next, err := EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(next, &root); err != nil {
		t.Fatal(err)
	}
	if _, exists := root[legacyBotSkillsJSONKey]; exists {
		t.Fatalf("legacy bot_skills remained: %s", next)
	}
	if string(root["channel"]) != `"feishu"` {
		t.Fatalf("unrelated config changed: %s", next)
	}
}

func TestDecodeBotDefinitionJSONUsesLegacyCloudModelOnlyAsMigrationSource(t *testing.T) {
	raw := []byte(`{
		"cloud_model": {
			"kind": "catalog",
			"model_id": "gpt-5.6-sol",
			"reasoning_effort": "high",
			"revision": 7,
			"last_error": "old error"
		}
	}`)

	record, err := DecodeBotDefinitionJSON(raw, 43)
	if err != nil {
		t.Fatal(err)
	}
	if record.Exists {
		t.Fatal("legacy cloud_model must remain an unpersisted migration source")
	}
	if record.Definition.Schema != types.BotDefinitionSchema ||
		record.Definition.BotID != "43" ||
		record.Definition.Model.Kind != "catalog" ||
		record.Definition.Model.ModelID != "gpt-5.6-sol" ||
		record.Definition.Model.ReasoningEffort != "high" ||
		record.Definition.Prompt == nil ||
		record.Definition.Prompt.Selected != "default" ||
		record.Runtime.DesiredRevision != 7 ||
		record.Runtime.LastError != "old error" {
		t.Fatalf("record=%+v", record)
	}
}

func TestLegacyModelAdapterWritesCanonicalDefinitionWithoutDroppingPrompt(t *testing.T) {
	raw, err := EncodeBotDefinitionJSON(nil, &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "43",
			Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
			Prompt: &types.BotPromptDefinition{Selected: "custom", CustomSystemPrompt: "Keep this prompt."},
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 2},
		Exists:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	next, err := EncodeBotModelConfigJSON(raw, &types.BotModelConfig{
		Kind:            "catalog",
		ModelID:         "gpt-5.6-terra",
		ReasoningEffort: "medium",
		Revision:        3,
	}, 43)
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeBotDefinitionJSON(next, 43)
	if err != nil {
		t.Fatal(err)
	}
	if record.Definition.Model.ModelID != "gpt-5.6-terra" ||
		record.Definition.Prompt == nil ||
		record.Definition.Prompt.CustomSystemPrompt != "Keep this prompt." ||
		record.Runtime.DesiredRevision != 3 {
		t.Fatalf("record=%+v", record)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(next, &root); err != nil {
		t.Fatal(err)
	}
	if _, exists := root[botModelConfigJSONKey]; exists {
		t.Fatalf("legacy cloud_model remained writable: %s", next)
	}
}

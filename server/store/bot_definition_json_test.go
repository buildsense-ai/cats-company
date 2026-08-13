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

func TestBotDefinitionJSONRoundTripsPromptSharingState(t *testing.T) {
	record := defaultBotDefinitionRecord(43)
	record.PromptVisibility = types.BotPromptFriends
	record.DefaultPrompt = &types.BotDefaultPromptSnapshot{
		Content: "bundled prompt", ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		XiaoBaVersion: "1.2.3", RuntimeVersion: "node-24", ReportedAt: "2026-08-13T00:00:00Z",
	}
	raw, err := EncodeBotDefinitionJSON([]byte(`{"unrelated":true}`), record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBotDefinitionJSON(raw, 43)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PromptVisibility != types.BotPromptFriends || decoded.DefaultPrompt == nil ||
		decoded.DefaultPrompt.Content != "bundled prompt" || decoded.DefaultPrompt.XiaoBaVersion != "1.2.3" {
		t.Fatalf("decoded=%+v", decoded)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || string(root["unrelated"]) != "true" {
		t.Fatalf("unrelated configuration was not preserved: %s", raw)
	}
}

func TestBotDefinitionJSONDefaultsPromptVisibilityToOwner(t *testing.T) {
	record, err := DecodeBotDefinitionJSON([]byte(`{"bot_definition":{"schema":"xiaoba.bot-definition.v1","botId":"43","model":{"kind":"local"}}}`), 43)
	if err != nil {
		t.Fatal(err)
	}
	if record.PromptVisibility != types.BotPromptOwner {
		t.Fatalf("visibility=%q", record.PromptVisibility)
	}
}

func TestBotDefinitionJSONPreservesSavedCustomModelAcrossCatalogSwitch(t *testing.T) {
	record := &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "43",
			Model: types.BotDefinitionModel{
				Kind:             "custom",
				Model:            "private-model",
				APIKeyCiphertext: "encrypted-custom-profile",
			},
		},
		Exists: true,
	}
	RememberBotDefinitionCustomModel(record, record.Definition.Model)
	record.Definition.Model = types.BotDefinitionModel{Kind: "catalog", ModelID: "gpt-5.6-sol"}

	raw, err := EncodeBotDefinitionJSON(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBotDefinitionJSON(raw, 43)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Definition.Model.Kind != "catalog" ||
		decoded.SavedCustomModel == nil ||
		decoded.SavedCustomModel.APIKeyCiphertext != "encrypted-custom-profile" {
		t.Fatalf("decoded=%+v", decoded)
	}
	legacy := legacyModelConfigFromRecord(decoded)
	if legacy.CustomCiphertext != "encrypted-custom-profile" {
		t.Fatalf("legacy custom ciphertext=%q", legacy.CustomCiphertext)
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

func TestDecodeLegacyCatalogModelRetainsAlternateCustomCiphertext(t *testing.T) {
	raw := []byte(`{
		"cloud_model": {
			"kind": "catalog",
			"model_id": "minimax-m3",
			"custom_ciphertext": "encrypted-custom-profile",
			"revision": 7
		}
	}`)
	record, err := DecodeBotDefinitionJSON(raw, 43)
	if err != nil {
		t.Fatal(err)
	}
	if record.SavedCustomModel == nil ||
		record.SavedCustomModel.APIKeyCiphertext != "encrypted-custom-profile" {
		t.Fatalf("record=%+v", record)
	}
	if record.Definition.Model.APIKeyCiphertext != "" {
		t.Fatalf("catalog definition retained custom ciphertext: %+v", record.Definition.Model)
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
func TestLegacyEmptySelectionWithRevisionMigratesToExplicitLocal(t *testing.T) {
	legacyJSON, err := json.Marshal(map[string]any{
		botModelConfigJSONKey: types.BotModelConfig{
			Revision:            4,
			RuntimeProtocol:     "bot-definition.v1",
			RuntimeProtocolSeen: "2026-08-03T08:00:00Z",
			AppliedKind:         "local",
			AppliedModelID:      "local",
			AppliedRevision:     4,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	record, err := DecodeBotDefinitionJSON(legacyJSON, 43)
	if err != nil {
		t.Fatal(err)
	}
	if record.Definition.Model.Kind != "local" || record.Definition.Model.ModelID != "local" ||
		record.Runtime.DesiredRevision != 4 || record.Runtime.RuntimeProtocol != "bot-definition.v1" ||
		record.Runtime.AppliedKind != "local" || record.Runtime.AppliedModelID != "local" {
		t.Fatalf("migrated=%+v", record)
	}

	canonical, err := EncodeBotDefinitionJSON(legacyJSON, record)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := DecodeBotDefinitionJSON(canonical, 43)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Definition.Model.Kind != "local" || reloaded.Definition.Model.ModelID != "local" ||
		reloaded.Runtime.DesiredRevision != 4 || reloaded.Runtime.RuntimeProtocol != "bot-definition.v1" ||
		reloaded.Runtime.AppliedRevision != 4 {
		t.Fatalf("reloaded=%+v", reloaded)
	}
}

func TestLegacyEmptySelectionWithoutHistoryDoesNotBecomeLocal(t *testing.T) {
	legacyJSON, err := json.Marshal(map[string]any{botModelConfigJSONKey: types.BotModelConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeBotDefinitionJSON(legacyJSON, 43)
	if err != nil {
		t.Fatal(err)
	}
	if record.Definition.Model.Kind == "local" || record.Definition.Model.ModelID == "local" {
		t.Fatalf("empty legacy record became local: %+v", record)
	}
}

func TestCanonicalEmptySelectionWithRuntimeHistoryNormalizesToExplicitLocal(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		botDefinitionJSONKey: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "43",
			Prompt: &types.BotPromptDefinition{Selected: "default"},
		},
		botDefinitionRuntimeJSONKey: types.BotDefinitionRuntime{
			DesiredRevision: 3,
			AppliedKind:     "local",
			AppliedModelID:  "local",
			AppliedRevision: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	record, err := DecodeBotDefinitionJSON(raw, 43)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Exists || record.Definition.Model.Kind != "local" || record.Definition.Model.ModelID != "local" {
		t.Fatalf("record=%+v", record)
	}

	encoded, err := EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := DecodeBotDefinitionJSON(encoded, 43)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Definition.Model.Kind != "local" || reloaded.Definition.Model.ModelID != "local" {
		t.Fatalf("reloaded=%+v", reloaded)
	}
}

func TestLegacyModelAdapterPreservesExplicitLocalAcrossJSONRoundTrip(t *testing.T) {
	initial, err := EncodeBotDefinitionJSON(nil, &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  "43",
			Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 2},
		Exists:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	next, err := EncodeBotModelConfigJSON(initial, &types.BotModelConfig{
		Kind:     "local",
		ModelID:  "local",
		Revision: 3,
	}, 43)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := DecodeBotDefinitionJSON(next, 43)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Definition.Model.Kind != "local" ||
		reloaded.Definition.Model.ModelID != "local" ||
		reloaded.Runtime.DesiredRevision != 3 {
		t.Fatalf("reloaded=%+v", reloaded)
	}
	legacy := legacyModelConfigFromRecord(reloaded)
	if legacy.Kind != "local" || legacy.ModelID != "local" || legacy.Revision != 3 {
		t.Fatalf("legacy=%+v", legacy)
	}
}

func TestDecodeBotDefinitionPreservesLegacyCiphertextWhenCanonicalExists(t *testing.T) {
	// canonical bot_definition 已存在（catalog，未携带密文），同时遗留
	// cloud_model 节点仍带 custom_ciphertext。防御性迁移应把 legacy 密文
	// 保留到 SavedCustomModel，避免后续 encode 时被直接删除而丢失。
	raw := []byte(`{
		"bot_definition": {
			"schema": "` + types.BotDefinitionSchema + `",
			"bot_id": "43",
			"model": {"kind": "catalog", "model_id": "minimax-m3"}
		},
		"cloud_model": {
			"kind": "custom",
			"model_id": "private-model",
			"custom_ciphertext": "legacy-ciphertext"
		}
	}`)
	record, err := DecodeBotDefinitionJSON(raw, 43)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Exists {
		t.Fatal("canonical definition should be present")
	}
	if record.SavedCustomModel == nil || record.SavedCustomModel.APIKeyCiphertext != "legacy-ciphertext" {
		t.Fatalf("legacy ciphertext should be preserved, got %#v", record.SavedCustomModel)
	}

	encoded, err := EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBotDefinitionJSON(encoded, 43)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SavedCustomModel == nil || decoded.SavedCustomModel.APIKeyCiphertext != "legacy-ciphertext" {
		t.Fatalf("legacy ciphertext lost after round-trip, got %#v", decoded.SavedCustomModel)
	}
}

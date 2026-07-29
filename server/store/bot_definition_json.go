package store

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

const (
	botDefinitionJSONKey        = "bot_definition"
	botDefinitionRuntimeJSONKey = "bot_definition_runtime"
	legacyBotSkillsJSONKey      = "bot_skills"
)

// DecodeBotDefinitionJSON reads the canonical definition nodes while
// preserving all unrelated bot_config.config fields for their owning features.
// A legacy cloud_model node is exposed as a migration source when the canonical
// node does not exist.
func DecodeBotDefinitionJSON(raw []byte, botUID int64) (*types.BotDefinitionRecord, error) {
	root, err := decodeBotConfigRoot(raw)
	if err != nil {
		return nil, err
	}
	record := &types.BotDefinitionRecord{}
	if value := root[botDefinitionJSONKey]; len(value) > 0 {
		if err := json.Unmarshal(value, &record.Definition); err != nil {
			return nil, err
		}
		record.Exists = true
	}
	if value := root[botDefinitionRuntimeJSONKey]; len(value) > 0 {
		if err := json.Unmarshal(value, &record.Runtime); err != nil {
			return nil, err
		}
	}
	if !record.Exists {
		if value := root[botModelConfigJSONKey]; len(value) > 0 {
			var legacy types.BotModelConfig
			if err := json.Unmarshal(value, &legacy); err != nil {
				return nil, err
			}
			if strings.TrimSpace(legacy.ModelID) != "" {
				record.Definition = definitionFromLegacyModelConfig(botUID, &legacy)
				record.Runtime = runtimeFromLegacyModelConfig(&legacy)
			}
		}
	}
	normalizeDefinitionRecord(record, botUID)
	return record, nil
}

// EncodeBotDefinitionJSON replaces only the canonical definition nodes.
func EncodeBotDefinitionJSON(raw []byte, record *types.BotDefinitionRecord) ([]byte, error) {
	root, err := decodeBotConfigRoot(raw)
	if err != nil {
		return nil, err
	}
	definition, err := json.Marshal(record.Definition)
	if err != nil {
		return nil, err
	}
	runtime, err := json.Marshal(record.Runtime)
	if err != nil {
		return nil, err
	}
	root[botDefinitionJSONKey] = definition
	root[botDefinitionRuntimeJSONKey] = runtime
	delete(root, botModelConfigJSONKey)
	delete(root, legacyBotSkillsJSONKey)
	return json.Marshal(root)
}

func DefaultBotDefinitionJSON(botUID int64) ([]byte, error) {
	record := defaultBotDefinitionRecord(botUID)
	return EncodeBotDefinitionJSON(nil, record)
}

func defaultBotDefinitionRecord(botUID int64) *types.BotDefinitionRecord {
	return &types.BotDefinitionRecord{
		Definition: types.BotDefinition{
			Schema: types.BotDefinitionSchema,
			BotID:  strconv.FormatInt(botUID, 10),
			Model: types.BotDefinitionModel{
				Kind:    "catalog",
				ModelID: "minimax-m3",
			},
			Prompt: &types.BotPromptDefinition{Selected: "default"},
			Skills: []types.BotSkillRef{},
		},
		Exists: true,
	}
}

func decodeBotConfigRoot(raw []byte) (map[string]json.RawMessage, error) {
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}
	return root, nil
}

func normalizeDefinitionRecord(record *types.BotDefinitionRecord, botUID int64) {
	if record.Definition.Schema == "" {
		record.Definition.Schema = types.BotDefinitionSchema
	}
	if record.Definition.BotID == "" {
		record.Definition.BotID = strconv.FormatInt(botUID, 10)
	}
	if record.Definition.Model.Kind == "" && record.Definition.Model.ModelID != "" {
		record.Definition.Model.Kind = "catalog"
	}
	if record.Definition.Skills == nil {
		record.Definition.Skills = []types.BotSkillRef{}
	}
}

func definitionFromLegacyModelConfig(botUID int64, config *types.BotModelConfig) types.BotDefinition {
	model := types.BotDefinitionModel{
		Kind:             config.Kind,
		ModelID:          config.ModelID,
		ReasoningEffort:  config.ReasoningEffort,
		APIKeyCiphertext: config.CustomCiphertext,
	}
	if model.Kind == "" {
		model.Kind = "catalog"
	}
	return types.BotDefinition{
		Schema: types.BotDefinitionSchema,
		BotID:  strconv.FormatInt(botUID, 10),
		Model:  model,
		Prompt: &types.BotPromptDefinition{Selected: "default"},
		Skills: []types.BotSkillRef{},
	}
}

func runtimeFromLegacyModelConfig(config *types.BotModelConfig) types.BotDefinitionRuntime {
	return types.BotDefinitionRuntime{
		DesiredRevision:     config.Revision,
		UpdatedAt:           config.UpdatedAt,
		RuntimeProtocol:     config.RuntimeProtocol,
		RuntimeProtocolSeen: config.RuntimeProtocolSeen,
		AppliedKind:         config.AppliedKind,
		AppliedModelID:      config.AppliedModelID,
		AppliedReasoning:    config.AppliedReasoning,
		AppliedRevision:     config.AppliedRevision,
		AppliedAt:           config.AppliedAt,
		LastAttemptRevision: config.LastAttemptRevision,
		LastAttemptAt:       config.LastAttemptAt,
		LastError:           config.LastError,
	}
}

func legacyModelConfigFromRecord(record *types.BotDefinitionRecord) *types.BotModelConfig {
	model := record.Definition.Model
	runtime := record.Runtime
	return &types.BotModelConfig{
		Kind:                model.Kind,
		ModelID:             firstNonEmpty(model.ModelID, model.Model),
		ReasoningEffort:     model.ReasoningEffort,
		CustomCiphertext:    model.APIKeyCiphertext,
		RuntimeProtocol:     runtime.RuntimeProtocol,
		RuntimeProtocolSeen: runtime.RuntimeProtocolSeen,
		Revision:            runtime.DesiredRevision,
		UpdatedAt:           runtime.UpdatedAt,
		AppliedKind:         runtime.AppliedKind,
		AppliedModelID:      runtime.AppliedModelID,
		AppliedReasoning:    runtime.AppliedReasoning,
		AppliedRevision:     runtime.AppliedRevision,
		AppliedAt:           runtime.AppliedAt,
		LastAttemptRevision: runtime.LastAttemptRevision,
		LastAttemptAt:       runtime.LastAttemptAt,
		LastError:           runtime.LastError,
	}
}

func applyLegacyModelConfig(record *types.BotDefinitionRecord, config *types.BotModelConfig) {
	switch config.Kind {
	case "catalog":
		record.Definition.Model = types.BotDefinitionModel{
			Kind:            config.Kind,
			ModelID:         config.ModelID,
			ReasoningEffort: config.ReasoningEffort,
		}
	case "custom":
		model := record.Definition.Model
		if model.Kind != "custom" {
			model = types.BotDefinitionModel{}
		}
		model.Kind = config.Kind
		model.ModelID = ""
		model.Model = config.ModelID
		model.ReasoningEffort = config.ReasoningEffort
		if config.CustomCiphertext != "" {
			model.APIKeyCiphertext = config.CustomCiphertext
		}
		record.Definition.Model = model
	default:
		record.Definition.Model = types.BotDefinitionModel{
			Kind:    config.Kind,
			ModelID: config.ModelID,
		}
	}
	record.Runtime = runtimeFromLegacyModelConfig(config)
	record.Exists = true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

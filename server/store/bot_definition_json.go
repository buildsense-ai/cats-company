package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

const (
	botDefinitionJSONKey      = "bot_definition"
	botDefinitionApplyJSONKey = "bot_definition_apply"
)

var ErrStaleBotDefinitionRevision = errors.New("stale bot definition revision")
var ErrBotDefinitionManaged = errors.New("bot is managed by BotDefinition")

func NewDefaultBotDefinition(botUID int64) *types.BotDefinition {
	return &types.BotDefinition{
		Schema: "xiaoba.bot-definition.v1",
		BotID:  strconv.FormatInt(botUID, 10),
		Model: types.BotDefinitionModel{
			Kind:    "catalog",
			ModelID: "minimax-m3",
		},
		Prompt: types.BotDefinitionPrompt{Selected: "default"},
	}
}

// NewInitialBotDefinition preserves an existing model-only configuration while
// adding the Definition envelope and default prompt.
func NewInitialBotDefinition(botUID int64, config *types.BotModelConfig) *types.BotDefinition {
	definition := NewDefaultBotDefinition(botUID)
	if config == nil || config.ModelID == "" {
		return definition
	}
	kind := config.Kind
	if kind == "" {
		kind = "catalog"
	}
	definition.Model = types.BotDefinitionModel{
		Kind:            kind,
		ModelID:         config.ModelID,
		ReasoningEffort: config.ReasoningEffort,
	}
	if kind == "custom" {
		definition.Model.Model = config.ModelID
		definition.Model.ModelID = ""
		definition.Model.APIKeyEncrypted = config.CustomCiphertext
	}
	if config.CustomCiphertext != "" {
		definition.SavedCustomModel = &types.BotDefinitionCustomModel{
			Kind:            "custom",
			APIKeyEncrypted: config.CustomCiphertext,
		}
	}
	return definition
}

type storedBotDefinitionMetadata struct {
	Schema    string                    `json:"schema"`
	BotID     string                    `json:"botId"`
	Prompt    types.BotDefinitionPrompt `json:"prompt"`
	Revision  int64                     `json:"revision"`
	UpdatedAt string                    `json:"updatedAt,omitempty"`
}

// DecodeBotDefinitionJSON combines the existing cloud_model node with the
// Definition metadata. cloud_model remains the single encrypted model store.
func DecodeBotDefinitionJSON(raw []byte) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, nil, err
		}
	}
	var metadata *storedBotDefinitionMetadata
	if value := root[botDefinitionJSONKey]; len(value) > 0 && string(value) != "null" {
		metadata = &storedBotDefinitionMetadata{}
		if err := json.Unmarshal(value, metadata); err != nil {
			return nil, nil, err
		}
	}
	apply := &types.BotDefinitionApplyState{}
	if value := root[botDefinitionApplyJSONKey]; len(value) > 0 && string(value) != "null" {
		if err := json.Unmarshal(value, apply); err != nil {
			return nil, nil, err
		}
	}
	var record *types.BotDefinitionRecord
	if metadata != nil {
		modelConfig, err := DecodeBotModelConfigJSON(raw)
		if err != nil {
			return nil, nil, err
		}
		model := types.BotDefinitionModel{
			Kind:            modelConfig.Kind,
			ModelID:         modelConfig.ModelID,
			ReasoningEffort: modelConfig.ReasoningEffort,
		}
		if model.Kind == "" && model.ModelID != "" {
			model.Kind = "catalog"
		}
		if model.Kind == "custom" {
			model.Model = modelConfig.ModelID
			model.APIKeyEncrypted = modelConfig.CustomCiphertext
			model.ModelID = ""
		}
		var saved *types.BotDefinitionCustomModel
		if modelConfig.CustomCiphertext != "" {
			saved = &types.BotDefinitionCustomModel{
				Kind:            "custom",
				APIKeyEncrypted: modelConfig.CustomCiphertext,
			}
		}
		record = &types.BotDefinitionRecord{
			Definition: types.BotDefinition{
				Schema:           metadata.Schema,
				BotID:            metadata.BotID,
				Model:            model,
				SavedCustomModel: saved,
				Prompt:           metadata.Prompt,
			},
			Revision:  metadata.Revision,
			UpdatedAt: metadata.UpdatedAt,
		}
		apply.DesiredRevision = record.Revision
	}
	return record, apply, nil
}

// EncodeBotDefinitionJSON stores model data through the existing cloud_model
// node and stores only prompt/global revision metadata in bot_definition.
func EncodeBotDefinitionJSON(
	raw []byte,
	record *types.BotDefinitionRecord,
	apply *types.BotDefinitionApplyState,
) ([]byte, error) {
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}
	if record != nil {
		modelConfig, err := DecodeBotModelConfigJSON(raw)
		if err != nil {
			return nil, err
		}
		model := record.Definition.Model
		previousKind := modelConfig.Kind
		previousModelID := modelConfig.ModelID
		previousReasoning := modelConfig.ReasoningEffort
		previousCiphertext := modelConfig.CustomCiphertext
		modelConfig.Kind = model.Kind
		modelConfig.ReasoningEffort = model.ReasoningEffort
		if model.Kind == "custom" {
			modelConfig.ModelID = model.Model
			if model.APIKeyEncrypted != "" {
				modelConfig.CustomCiphertext = model.APIKeyEncrypted
			}
		} else {
			modelConfig.ModelID = model.ModelID
			if model.ClearSavedCustom {
				modelConfig.CustomCiphertext = ""
			} else if record.Definition.SavedCustomModel != nil &&
				record.Definition.SavedCustomModel.APIKeyEncrypted != "" {
				modelConfig.CustomCiphertext = record.Definition.SavedCustomModel.APIKeyEncrypted
			}
		}
		modelChanged := previousKind != modelConfig.Kind ||
			previousModelID != modelConfig.ModelID ||
			previousReasoning != modelConfig.ReasoningEffort ||
			previousCiphertext != modelConfig.CustomCiphertext
		if modelChanged {
			modelConfig.Revision++
			modelConfig.UpdatedAt = record.UpdatedAt
			modelConfig.LastAttemptRevision = 0
			modelConfig.LastAttemptAt = ""
			modelConfig.LastError = ""
		}
		metadata := storedBotDefinitionMetadata{
			Schema:    record.Definition.Schema,
			BotID:     record.Definition.BotID,
			Prompt:    record.Definition.Prompt,
			Revision:  record.Revision,
			UpdatedAt: record.UpdatedAt,
		}
		value, err := mergeBotDefinitionMetadata(root[botDefinitionJSONKey], metadata)
		if err != nil {
			return nil, err
		}
		root[botDefinitionJSONKey] = value

		modelPending := modelConfig.AppliedRevision != modelConfig.Revision ||
			modelConfig.AppliedKind != modelConfig.Kind ||
			modelConfig.AppliedModelID != modelConfig.ModelID ||
			modelConfig.AppliedReasoning != modelConfig.ReasoningEffort
		if apply != nil && apply.DesiredRevision == record.Revision && apply.LastAttemptAt != "" &&
			modelPending {
			modelConfig.LastAttemptRevision = modelConfig.Revision
			modelConfig.LastAttemptAt = apply.LastAttemptAt
			modelConfig.LastError = apply.LastError
			if apply.LastError == "" && apply.AppliedRevision == record.Revision {
				modelConfig.AppliedKind = modelConfig.Kind
				modelConfig.AppliedModelID = modelConfig.ModelID
				modelConfig.AppliedReasoning = modelConfig.ReasoningEffort
				modelConfig.AppliedRevision = modelConfig.Revision
				modelConfig.AppliedAt = apply.AppliedAt
			}
		}
		modelValue, err := mergeJSONNode(root[botModelConfigJSONKey], modelConfig)
		if err != nil {
			return nil, err
		}
		root[botModelConfigJSONKey] = modelValue
	}
	if apply != nil {
		value, err := mergeJSONNode(root[botDefinitionApplyJSONKey], apply)
		if err != nil {
			return nil, err
		}
		root[botDefinitionApplyJSONKey] = value
	}
	return json.Marshal(root)
}

func mergeJSONNode(existing json.RawMessage, value interface{}) ([]byte, error) {
	merged := map[string]json.RawMessage{}
	if len(existing) > 0 && string(existing) != "null" {
		if err := json.Unmarshal(existing, &merged); err != nil {
			return nil, err
		}
	}
	valueType := reflect.TypeOf(value)
	if valueType.Kind() == reflect.Ptr {
		valueType = valueType.Elem()
	}
	if valueType.Kind() == reflect.Struct {
		for i := 0; i < valueType.NumField(); i++ {
			tag := valueType.Field(i).Tag.Get("json")
			key := strings.Split(tag, ",")[0]
			if key != "" && key != "-" {
				delete(merged, key)
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	current := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &current); err != nil {
		return nil, err
	}
	for key, field := range current {
		merged[key] = field
	}
	return json.Marshal(merged)
}

func mergeBotDefinitionMetadata(
	existing json.RawMessage,
	metadata storedBotDefinitionMetadata,
) ([]byte, error) {
	merged, err := mergeJSONNode(existing, metadata)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(merged, &root); err != nil {
		return nil, err
	}
	prompt := map[string]json.RawMessage{}
	if len(existing) > 0 && string(existing) != "null" {
		var previous map[string]json.RawMessage
		if err := json.Unmarshal(existing, &previous); err != nil {
			return nil, err
		}
		if value := previous["prompt"]; len(value) > 0 && string(value) != "null" {
			if err := json.Unmarshal(value, &prompt); err != nil {
				return nil, err
			}
		}
	}
	delete(prompt, "selected")
	delete(prompt, "customSystemPrompt")
	currentPrompt, err := json.Marshal(metadata.Prompt)
	if err != nil {
		return nil, err
	}
	var currentFields map[string]json.RawMessage
	if err := json.Unmarshal(currentPrompt, &currentFields); err != nil {
		return nil, err
	}
	for key, value := range currentFields {
		prompt[key] = value
	}
	root["prompt"], err = json.Marshal(prompt)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

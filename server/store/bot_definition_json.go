package store

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

const (
	botDefinitionJSONKey = "bot_definition"
	BotDefinitionSchema  = "xiaoba.bot-definition.v1"
)

var (
	ErrBotDefinitionNotFound      = errors.New("bot definition not found")
	ErrBotDefinitionAlreadyExists = errors.New("bot definition already exists")
	ErrStaleBotDefinitionRevision = errors.New("stale bot definition revision")
)

type storedBotDefinitionSkills struct {
	Schema    string               `json:"schema"`
	Skills    *[]types.BotSkillRef `json:"skills"`
	Revision  int64                `json:"revision"`
	UpdatedAt string               `json:"updatedAt"`
}

// DecodeBotDefinitionJSON reads the cloud-model and bot-definition nodes from
// the same JSON document. A missing bot_definition node is distinct from a
// definition whose skills list is explicitly empty.
func DecodeBotDefinitionJSON(raw []byte) (*types.BotDefinitionSnapshot, error) {
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}

	model, err := DecodeBotModelConfigJSON(raw)
	if err != nil {
		return nil, err
	}
	snapshot := &types.BotDefinitionSnapshot{Model: model}
	value := root[botDefinitionJSONKey]
	if len(value) == 0 || string(value) == "null" {
		return snapshot, nil
	}

	var stored storedBotDefinitionSkills
	if err := json.Unmarshal(value, &stored); err != nil {
		return nil, fmt.Errorf("decode bot definition: %w", err)
	}
	if stored.Schema != BotDefinitionSchema || stored.Skills == nil || stored.Revision <= 0 || stored.UpdatedAt == "" {
		return nil, errors.New("invalid stored bot definition")
	}
	snapshot.Skills = &types.BotDefinitionSkillsState{
		Schema:    stored.Schema,
		Skills:    append([]types.BotSkillRef(nil), (*stored.Skills)...),
		Revision:  stored.Revision,
		UpdatedAt: stored.UpdatedAt,
	}
	if snapshot.Skills.Skills == nil {
		snapshot.Skills.Skills = []types.BotSkillRef{}
	}
	return snapshot, nil
}

// EncodeBotDefinitionJSON replaces only the bot-definition node and preserves
// cloud_model and every unrelated bot configuration field.
func EncodeBotDefinitionJSON(raw []byte, state *types.BotDefinitionSkillsState) ([]byte, error) {
	if state == nil {
		return nil, errors.New("bot definition state is required")
	}
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}
	skills := append([]types.BotSkillRef(nil), state.Skills...)
	if skills == nil {
		skills = []types.BotSkillRef{}
	}
	value, err := json.Marshal(storedBotDefinitionSkills{
		Schema:    BotDefinitionSchema,
		Skills:    &skills,
		Revision:  state.Revision,
		UpdatedAt: state.UpdatedAt,
	})
	if err != nil {
		return nil, err
	}
	root[botDefinitionJSONKey] = value
	return json.Marshal(root)
}

package postgres

import (
	"database/sql"
	"fmt"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, error) {
	var raw []byte
	if err := a.db.QueryRow(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1`,
		botUID,
	).Scan(&raw); err != nil {
		return nil, fmt.Errorf("get bot definition: %w", err)
	}
	record, err := store.DecodeBotDefinitionJSON(raw, botUID)
	if err != nil {
		return nil, fmt.Errorf("decode bot definition: %w", err)
	}
	return record, nil
}

func (a *Adapter) CreateBotDefinitionIfAbsent(botUID int64, definition types.BotDefinition) (*types.BotDefinitionRecord, error) {
	return a.updateBotDefinition(botUID, func(record *types.BotDefinitionRecord, _ string) error {
		initializeBotDefinitionIfAbsent(record, definition)
		return nil
	})
}

func initializeBotDefinitionIfAbsent(record *types.BotDefinitionRecord, definition types.BotDefinition) {
	if !record.Exists {
		if record.Definition.Model.Kind == "" {
			record.Definition.Model = definition.Model
		}
		if record.Definition.Prompt == nil && definition.Prompt != nil {
			prompt := *definition.Prompt
			record.Definition.Prompt = &prompt
		}
		if len(record.Definition.Skills) == 0 && len(definition.Skills) > 0 {
			record.Definition.Skills = append([]types.BotSkillRef(nil), definition.Skills...)
		}
		record.Definition.Schema = definition.Schema
		record.Definition.BotID = definition.BotID
		record.Exists = true
		return
	}
	if record.Definition.Model.Kind == "" {
		record.Definition.Model = definition.Model
	}
	if record.Definition.Prompt == nil && definition.Prompt != nil {
		prompt := *definition.Prompt
		record.Definition.Prompt = &prompt
	}
}

func (a *Adapter) UpdateBotDefinitionModel(
	botUID, expectedRevision int64,
	model types.BotDefinitionModel,
) (*types.BotDefinitionRecord, error) {
	return a.updateBotDefinition(botUID, func(record *types.BotDefinitionRecord, now string) error {
		if expectedRevision >= 0 && record.Runtime.DesiredRevision != expectedRevision {
			return store.ErrStaleBotModelRevision
		}
		record.Definition.Model = model
		if record.Definition.Prompt == nil {
			record.Definition.Prompt = &types.BotPromptDefinition{Selected: "default"}
		}
		record.Runtime.DesiredRevision++
		record.Runtime.UpdatedAt = now
		record.Runtime.LastAttemptRevision = 0
		record.Runtime.LastAttemptAt = ""
		record.Runtime.LastError = ""
		record.Exists = true
		return nil
	})
}

func (a *Adapter) UpdateBotDefinitionPrompt(
	botUID, expectedRevision int64,
	prompt types.BotPromptDefinition,
) (*types.BotDefinitionRecord, error) {
	return a.updateBotDefinition(botUID, func(record *types.BotDefinitionRecord, now string) error {
		if expectedRevision >= 0 && record.Runtime.DesiredRevision != expectedRevision {
			return store.ErrStaleBotModelRevision
		}
		record.Definition.Prompt = &prompt
		if record.Definition.Model.Kind == "" {
			record.Definition.Model = types.BotDefinitionModel{
				Kind:    "catalog",
				ModelID: "minimax-m3",
			}
		}
		record.Runtime.DesiredRevision++
		record.Runtime.UpdatedAt = now
		record.Runtime.LastAttemptRevision = 0
		record.Runtime.LastAttemptAt = ""
		record.Runtime.LastError = ""
		record.Exists = true
		return nil
	})
}

func (a *Adapter) UpdateBotDefinitionSkills(
	botUID, expectedRevision int64,
	skills []types.BotSkillRef,
) (*types.BotDefinitionRecord, error) {
	return a.updateBotDefinition(botUID, func(record *types.BotDefinitionRecord, now string) error {
		if expectedRevision >= 0 && record.Runtime.DesiredRevision != expectedRevision {
			return store.ErrStaleBotModelRevision
		}
		if reflect.DeepEqual(record.Definition.Skills, skills) {
			return nil
		}
		record.Definition.Skills = append([]types.BotSkillRef(nil), skills...)
		if record.Definition.Model.Kind == "" {
			record.Definition.Model = types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"}
		}
		if record.Definition.Prompt == nil {
			record.Definition.Prompt = &types.BotPromptDefinition{Selected: "default"}
		}
		record.Runtime.DesiredRevision++
		record.Runtime.UpdatedAt = now
		record.Runtime.LastAttemptRevision = 0
		record.Runtime.LastAttemptAt = ""
		record.Runtime.LastError = ""
		record.Exists = true
		return nil
	})
}

func (a *Adapter) AckBotDefinition(botUID, revision int64, applyError string) (*types.BotDefinitionRecord, error) {
	return a.updateBotDefinition(botUID, func(record *types.BotDefinitionRecord, now string) error {
		if revision != record.Runtime.DesiredRevision {
			return store.ErrStaleBotModelRevision
		}
		record.Runtime.LastAttemptRevision = revision
		record.Runtime.LastAttemptAt = now
		record.Runtime.LastError = applyError
		if applyError == "" {
			record.Runtime.AppliedKind = record.Definition.Model.Kind
			record.Runtime.AppliedModelID = record.Definition.Model.ModelID
			if record.Runtime.AppliedModelID == "" {
				record.Runtime.AppliedModelID = record.Definition.Model.Model
			}
			record.Runtime.AppliedReasoning = record.Definition.Model.ReasoningEffort
			record.Runtime.AppliedRevision = revision
			record.Runtime.AppliedAt = now
		}
		return nil
	})
}

func (a *Adapter) updateBotDefinition(
	botUID int64,
	update func(record *types.BotDefinitionRecord, now string) error,
) (*types.BotDefinitionRecord, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin bot definition update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var raw []byte
	if err := tx.QueryRow(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE`,
		botUID,
	).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("bot definition not found: %w", err)
		}
		return nil, fmt.Errorf("lock bot definition: %w", err)
	}
	record, err := store.DecodeBotDefinitionJSON(raw, botUID)
	if err != nil {
		return nil, fmt.Errorf("decode bot definition: %w", err)
	}
	if err := update(record, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	next, err := store.EncodeBotDefinitionJSON(raw, record)
	if err != nil {
		return nil, fmt.Errorf("encode bot definition: %w", err)
	}
	if _, err := tx.Exec(`UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2`, next, botUID); err != nil {
		return nil, fmt.Errorf("save bot definition: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bot definition: %w", err)
	}
	return record, nil
}

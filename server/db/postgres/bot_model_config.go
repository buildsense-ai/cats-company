package postgres

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) GetBotModelConfig(botUID int64) (*types.BotModelConfig, error) {
	var raw []byte
	if err := a.db.QueryRow(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1`,
		botUID,
	).Scan(&raw); err != nil {
		return nil, fmt.Errorf("get bot model config: %w", err)
	}
	config, err := store.DecodeBotModelConfigJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode bot model config: %w", err)
	}
	return config, nil
}

func (a *Adapter) MarkBotModelRuntimeProtocol(botUID int64, protocol string) (*types.BotModelConfig, error) {
	return a.updateBotModelConfig(botUID, func(config *types.BotModelConfig, now string) error {
		config.RuntimeProtocol = protocol
		config.RuntimeProtocolSeen = now
		return nil
	})
}

func (a *Adapter) SaveBotDesiredModelConfig(botUID int64, kind, modelID, reasoningEffort, customCiphertext string) (*types.BotModelConfig, error) {
	return a.updateBotModelConfig(botUID, func(config *types.BotModelConfig, now string) error {
		config.Kind = kind
		config.ModelID = modelID
		config.ReasoningEffort = reasoningEffort
		if customCiphertext != "" {
			config.CustomCiphertext = customCiphertext
		}
		config.Revision++
		config.UpdatedAt = now
		config.LastAttemptRevision = 0
		config.LastAttemptAt = ""
		config.LastError = ""
		return nil
	})
}

func (a *Adapter) AckBotModelConfig(botUID, revision int64, kind, modelID, reasoningEffort, applyError string) (*types.BotModelConfig, error) {
	return a.updateBotModelConfig(botUID, func(config *types.BotModelConfig, now string) error {
		if revision != config.Revision {
			return store.ErrStaleBotModelRevision
		}
		config.LastAttemptRevision = revision
		config.LastAttemptAt = now
		config.LastError = applyError
		if applyError == "" {
			config.AppliedKind = kind
			config.AppliedRevision = revision
			config.AppliedModelID = modelID
			config.AppliedReasoning = reasoningEffort
			config.AppliedAt = now
		}
		return nil
	})
}

func (a *Adapter) updateBotModelConfig(
	botUID int64,
	update func(config *types.BotModelConfig, now string) error,
) (*types.BotModelConfig, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin bot model config update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var raw []byte
	if err := tx.QueryRow(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1 FOR UPDATE`,
		botUID,
	).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("bot model config not found: %w", err)
		}
		return nil, fmt.Errorf("lock bot model config: %w", err)
	}
	config, err := store.DecodeBotModelConfigJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode bot model config: %w", err)
	}
	previousKind := config.Kind
	previousModelID := config.ModelID
	previousReasoning := config.ReasoningEffort
	previousCiphertext := config.CustomCiphertext
	now := time.Now().UTC().Format(time.RFC3339)
	if err := update(config, now); err != nil {
		return nil, err
	}
	desiredChanged := previousKind != config.Kind ||
		previousModelID != config.ModelID ||
		previousReasoning != config.ReasoningEffort ||
		previousCiphertext != config.CustomCiphertext
	if desiredChanged && config.ModelID == "" {
		record, _, decodeErr := store.DecodeBotDefinitionJSON(raw)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode bot definition: %w", decodeErr)
		}
		if record != nil {
			return nil, store.ErrBotDefinitionManaged
		}
	}
	next, err := store.EncodeBotModelConfigJSON(raw, config)
	if err != nil {
		return nil, fmt.Errorf("encode bot model config: %w", err)
	}
	if desiredChanged && config.ModelID != "" {
		record, apply, decodeErr := store.DecodeBotDefinitionJSON(next)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode bot definition: %w", decodeErr)
		}
		if record != nil {
			record.Definition.Model = types.BotDefinitionModel{
				Kind:            config.Kind,
				ModelID:         config.ModelID,
				ReasoningEffort: config.ReasoningEffort,
			}
			if config.Kind == "custom" {
				record.Definition.Model.Model = config.ModelID
				record.Definition.Model.ModelID = ""
				record.Definition.Model.APIKeyEncrypted = config.CustomCiphertext
			}
			if config.CustomCiphertext != "" {
				record.Definition.SavedCustomModel = &types.BotDefinitionCustomModel{
					Kind:            "custom",
					APIKeyEncrypted: config.CustomCiphertext,
				}
			}
			record.Revision++
			record.UpdatedAt = now
			apply.DesiredRevision = record.Revision
			apply.LastAttemptAt = ""
			apply.LastError = ""
			apply.LastErrorRevision = 0
			next, err = store.EncodeBotDefinitionJSON(next, record, apply)
			if err != nil {
				return nil, fmt.Errorf("encode bot definition: %w", err)
			}
		}
	}
	if _, err := tx.Exec(`UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2`, next, botUID); err != nil {
		return nil, fmt.Errorf("save bot model config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bot model config: %w", err)
	}
	return config, nil
}

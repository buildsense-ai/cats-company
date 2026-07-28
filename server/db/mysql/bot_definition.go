package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

var errSkipBotDefinitionInitialization = errors.New("skip bot definition initialization")

func (a *Adapter) GetBotDefinition(botUID int64) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	var raw []byte
	if err := a.db.QueryRow(
		`SELECT COALESCE(config, JSON_OBJECT()) FROM bot_config WHERE user_id = ?`,
		botUID,
	).Scan(&raw); err != nil {
		return nil, nil, fmt.Errorf("get bot definition: %w", err)
	}
	record, apply, err := store.DecodeBotDefinitionJSON(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("decode bot definition: %w", err)
	}
	return record, apply, nil
}

func (a *Adapter) SaveBotDefinition(
	botUID, expectedRevision int64,
	definition *types.BotDefinition,
) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	return a.updateBotDefinition(botUID, func(
		record *types.BotDefinitionRecord,
		apply *types.BotDefinitionApplyState,
		_ *types.BotModelConfig,
		now string,
	) error {
		current := int64(0)
		if record != nil {
			current = record.Revision
		}
		if current != expectedRevision {
			return store.ErrStaleBotDefinitionRevision
		}
		*record = types.BotDefinitionRecord{
			Definition: *definition,
			Revision:   current + 1,
			UpdatedAt:  now,
		}
		apply.DesiredRevision = record.Revision
		apply.LastAttemptAt = ""
		apply.LastError = ""
		apply.LastErrorRevision = 0
		return nil
	})
}

func (a *Adapter) AckBotDefinition(
	botUID, revision int64,
	applyError string,
) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	return a.updateBotDefinition(botUID, func(
		record *types.BotDefinitionRecord,
		apply *types.BotDefinitionApplyState,
		_ *types.BotModelConfig,
		now string,
	) error {
		if record == nil || record.Revision != revision {
			return store.ErrStaleBotDefinitionRevision
		}
		apply.DesiredRevision = record.Revision
		apply.LastAttemptAt = now
		apply.LastError = applyError
		apply.LastErrorRevision = 0
		if applyError == "" {
			apply.AppliedRevision = revision
			apply.AppliedAt = now
		} else {
			apply.LastErrorRevision = revision
		}
		return nil
	})
}

func (a *Adapter) InitializeDefaultBotDefinition(botUID int64) error {
	record, _, err := a.updateBotDefinition(botUID, func(
		record *types.BotDefinitionRecord,
		apply *types.BotDefinitionApplyState,
		model *types.BotModelConfig,
		now string,
	) error {
		if record.Revision > 0 {
			return store.ErrStaleBotDefinitionRevision
		}
		if model.Revision > 0 && model.ModelID == "" {
			return errSkipBotDefinitionInitialization
		}
		*record = types.BotDefinitionRecord{
			Definition: *store.NewInitialBotDefinition(botUID, model),
			Revision:   1,
			UpdatedAt:  now,
		}
		apply.DesiredRevision = 1
		return nil
	})
	if err == store.ErrStaleBotDefinitionRevision {
		return nil
	}
	if err == errSkipBotDefinitionInitialization {
		return nil
	}
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("initialize bot definition returned no record")
	}
	return nil
}

func (a *Adapter) updateBotDefinition(
	botUID int64,
	update func(*types.BotDefinitionRecord, *types.BotDefinitionApplyState, *types.BotModelConfig, string) error,
) (*types.BotDefinitionRecord, *types.BotDefinitionApplyState, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("begin bot definition update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var raw []byte
	if err := tx.QueryRow(
		`SELECT COALESCE(config, JSON_OBJECT()) FROM bot_config WHERE user_id = ? FOR UPDATE`,
		botUID,
	).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, fmt.Errorf("bot definition not found: %w", err)
		}
		return nil, nil, fmt.Errorf("lock bot definition: %w", err)
	}
	record, apply, err := store.DecodeBotDefinitionJSON(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("decode bot definition: %w", err)
	}
	if apply == nil {
		apply = &types.BotDefinitionApplyState{}
	}
	model, err := store.DecodeBotModelConfigJSON(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("decode bot model config: %w", err)
	}
	target := record
	if target == nil {
		target = &types.BotDefinitionRecord{}
	}
	if err := update(target, apply, model, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, nil, err
	}
	record = target
	next, err := store.EncodeBotDefinitionJSON(raw, record, apply)
	if err != nil {
		return nil, nil, fmt.Errorf("encode bot definition: %w", err)
	}
	if _, err := tx.Exec(`UPDATE bot_config SET config = ? WHERE user_id = ?`, next, botUID); err != nil {
		return nil, nil, fmt.Errorf("save bot definition: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit bot definition: %w", err)
	}
	return record, apply, nil
}

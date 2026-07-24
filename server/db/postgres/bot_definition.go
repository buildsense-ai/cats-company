package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) GetBotDefinition(botUID int64) (*types.BotDefinitionSnapshot, error) {
	var raw []byte
	if err := a.db.QueryRow(
		`SELECT COALESCE(config, '{}'::jsonb) FROM bot_config WHERE user_id = $1`,
		botUID,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrBotDefinitionNotFound
		}
		return nil, fmt.Errorf("get bot definition: %w", err)
	}
	snapshot, err := store.DecodeBotDefinitionJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode bot definition: %w", err)
	}
	return snapshot, nil
}

func (a *Adapter) CreateBotDefinition(botUID int64, skills []types.BotSkillRef) (*types.BotDefinitionSnapshot, error) {
	return a.updateBotDefinition(botUID, func(snapshot *types.BotDefinitionSnapshot, now string) error {
		if snapshot.Skills != nil {
			return store.ErrBotDefinitionAlreadyExists
		}
		snapshot.Skills = &types.BotDefinitionSkillsState{
			Schema: store.BotDefinitionSchema, Skills: append([]types.BotSkillRef{}, skills...),
			Revision: 1, UpdatedAt: now,
		}
		return nil
	})
}

func (a *Adapter) UpdateBotDefinition(
	botUID, expectedModelRevision, expectedSkillsRevision int64,
	skills []types.BotSkillRef,
) (*types.BotDefinitionSnapshot, error) {
	return a.updateBotDefinition(botUID, func(snapshot *types.BotDefinitionSnapshot, now string) error {
		modelRevision := int64(0)
		if snapshot.Model != nil {
			modelRevision = snapshot.Model.Revision
		}
		skillsRevision := int64(0)
		if snapshot.Skills != nil {
			skillsRevision = snapshot.Skills.Revision
		}
		if modelRevision != expectedModelRevision || skillsRevision != expectedSkillsRevision {
			return store.ErrStaleBotDefinitionRevision
		}
		if snapshot.Skills == nil {
			snapshot.Skills = &types.BotDefinitionSkillsState{
				Schema: store.BotDefinitionSchema, Revision: 1, UpdatedAt: now,
			}
		} else {
			snapshot.Skills.Revision++
			snapshot.Skills.UpdatedAt = now
		}
		snapshot.Skills.Skills = append([]types.BotSkillRef{}, skills...)
		return nil
	})
}

func (a *Adapter) updateBotDefinition(
	botUID int64,
	update func(snapshot *types.BotDefinitionSnapshot, now string) error,
) (*types.BotDefinitionSnapshot, error) {
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
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("bot definition not found: %w", err)
		}
		return nil, fmt.Errorf("lock bot definition: %w", err)
	}
	snapshot, err := store.DecodeBotDefinitionJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("decode bot definition: %w", err)
	}
	if err := update(snapshot, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	next, err := store.EncodeBotDefinitionJSON(raw, snapshot.Skills)
	if err != nil {
		return nil, fmt.Errorf("encode bot definition: %w", err)
	}
	if _, err := tx.Exec(`UPDATE bot_config SET config = $1::jsonb WHERE user_id = $2`, next, botUID); err != nil {
		return nil, fmt.Errorf("save bot definition: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bot definition: %w", err)
	}
	return snapshot, nil
}

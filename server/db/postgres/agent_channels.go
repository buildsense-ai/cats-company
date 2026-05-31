package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// GetAgentChannelBinding returns the channel binding for one agent and channel.
func (a *Adapter) GetAgentChannelBinding(agentUID int64, channel string) (*types.AgentChannelBinding, error) {
	binding := &types.AgentChannelBinding{}
	var metadataRaw []byte
	var boundBy sql.NullInt64
	err := a.db.QueryRow(
		`SELECT id, agent_uid, channel, status, COALESCE(secret_token, ''), COALESCE(token_hash, ''),
		        COALESCE(token_last4, ''), bound_by_uid, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
		   FROM agent_channel_bindings
		  WHERE agent_uid = $1 AND channel = $2`,
		agentUID, channel,
	).Scan(
		&binding.ID,
		&binding.AgentUID,
		&binding.Channel,
		&binding.Status,
		&binding.SecretToken,
		&binding.TokenHash,
		&binding.TokenLast4,
		&boundBy,
		&metadataRaw,
		&binding.CreatedAt,
		&binding.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent channel binding: %w", err)
	}
	if boundBy.Valid {
		binding.BoundByUID = boundBy.Int64
	}
	if len(metadataRaw) > 0 {
		if err := json.Unmarshal(metadataRaw, &binding.Metadata); err != nil {
			return nil, fmt.Errorf("decode agent channel metadata: %w", err)
		}
	}
	return binding, nil
}

// UpsertAgentChannelBinding creates or replaces the channel binding for one agent.
func (a *Adapter) UpsertAgentChannelBinding(binding *types.AgentChannelBinding) error {
	if binding == nil || binding.AgentUID <= 0 || binding.Channel == "" {
		return fmt.Errorf("invalid agent channel binding")
	}
	metadataRaw, err := json.Marshal(binding.Metadata)
	if err != nil {
		return fmt.Errorf("encode agent channel metadata: %w", err)
	}
	var boundBy interface{}
	if binding.BoundByUID > 0 {
		boundBy = binding.BoundByUID
	}
	_, err = a.db.Exec(
		`INSERT INTO agent_channel_bindings
		    (agent_uid, channel, status, secret_token, token_hash, token_last4, bound_by_uid, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (agent_uid, channel) DO UPDATE SET
		    status = EXCLUDED.status,
		    secret_token = EXCLUDED.secret_token,
		    token_hash = EXCLUDED.token_hash,
		    token_last4 = EXCLUDED.token_last4,
		    bound_by_uid = EXCLUDED.bound_by_uid,
		    metadata = EXCLUDED.metadata,
		    updated_at = CURRENT_TIMESTAMP`,
		binding.AgentUID,
		binding.Channel,
		binding.Status,
		binding.SecretToken,
		binding.TokenHash,
		binding.TokenLast4,
		boundBy,
		metadataRaw,
	)
	if err != nil {
		return fmt.Errorf("upsert agent channel binding: %w", err)
	}
	return nil
}

// DeleteAgentChannelBinding removes one external channel binding from an agent.
func (a *Adapter) DeleteAgentChannelBinding(agentUID int64, channel string) error {
	_, err := a.db.Exec(`DELETE FROM agent_channel_bindings WHERE agent_uid = $1 AND channel = $2`, agentUID, channel)
	if err != nil {
		return fmt.Errorf("delete agent channel binding: %w", err)
	}
	return nil
}

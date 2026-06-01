package postgres

import (
	"fmt"

	"github.com/openchat/openchat/server/store/types"
)

// SaveBotConfig saves or updates bot configuration with owner.
func (a *Adapter) SaveBotConfig(uid int64, apiEndpoint, model string) error {
	_, err := a.db.Exec(
		`INSERT INTO bot_config (user_id, api_endpoint, model, enabled)
		 VALUES ($1, $2, $3, true)
		 ON CONFLICT (user_id)
		 DO UPDATE SET api_endpoint = EXCLUDED.api_endpoint, model = EXCLUDED.model`,
		uid, apiEndpoint, model,
	)
	return err
}

// SaveBotConfigWithOwner saves bot configuration with owner_id.
func (a *Adapter) SaveBotConfigWithOwner(uid, ownerID int64, apiEndpoint, model string) error {
	_, err := a.db.Exec(
		`INSERT INTO bot_config (user_id, owner_id, api_endpoint, model, enabled)
		 VALUES ($1, $2, $3, $4, true)
		 ON CONFLICT (user_id)
		 DO UPDATE SET api_endpoint = EXCLUDED.api_endpoint, model = EXCLUDED.model`,
		uid, ownerID, apiEndpoint, model,
	)
	return err
}

// GetBotConfig retrieves bot configuration by user ID.
func (a *Adapter) GetBotConfig(uid int64) (*types.BotConfig, error) {
	bc := &types.BotConfig{}
	var visibility string
	err := a.db.QueryRow(
		`SELECT user_id, COALESCE(owner_id, 0), api_endpoint, model, enabled, COALESCE(visibility, 'public'), COALESCE(body_id, '')
		 FROM bot_config WHERE user_id = $1`, uid,
	).Scan(&bc.UserID, &bc.OwnerID, &bc.APIEndpoint, &bc.Model, &bc.Enabled, &visibility, &bc.BodyID)
	if err != nil {
		return nil, fmt.Errorf("get bot config: %w", err)
	}
	bc.Visibility = types.BotVisibility(visibility)
	return bc, nil
}

// ListBots returns all bot users with their configs.
func (a *Adapter) ListBots() ([]map[string]interface{}, error) {
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''), u.state,
		        COALESCE(b.api_endpoint, '') as api_endpoint,
		        COALESCE(b.model, '') as model,
		        COALESCE(b.enabled, true) as enabled
		 FROM users u LEFT JOIN bot_config b ON u.id = b.user_id
		 WHERE u.account_type = 'bot'
		 ORDER BY u.created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list bots: %w", err)
	}
	defer rows.Close()

	var bots []map[string]interface{}
	for rows.Next() {
		var id int64
		var username, displayName, avatarURL, apiEndpoint, model string
		var state int
		var enabled bool
		if err := rows.Scan(&id, &username, &displayName, &avatarURL, &state, &apiEndpoint, &model, &enabled); err != nil {
			return nil, err
		}
		bots = append(bots, map[string]interface{}{
			"id":           id,
			"username":     username,
			"display_name": displayName,
			"avatar_url":   avatarURL,
			"state":        state,
			"api_endpoint": apiEndpoint,
			"model":        model,
			"enabled":      enabled,
		})
	}
	return bots, rows.Err()
}

// ToggleBotEnabled toggles the enabled state of a bot.
func (a *Adapter) ToggleBotEnabled(uid int64) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET enabled = NOT enabled WHERE user_id = $1`,
		uid,
	)
	return err
}

// SaveAPIKey stores or updates the API key for a bot.
func (a *Adapter) SaveAPIKey(uid int64, apiKey string) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET api_key = $1 WHERE user_id = $2`,
		apiKey, uid,
	)
	return err
}

// GetBotDebugMessages returns recent messages sent by a bot, for debug purposes.
func (a *Adapter) GetBotDebugMessages(uid int64, limit int) ([]*types.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role
		 FROM messages WHERE from_uid = $1
		 ORDER BY id DESC LIMIT $2`,
		uid, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get bot debug messages: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan debug message")
}

// GetBotByAPIKey looks up a bot's user ID by its API key.
func (a *Adapter) GetBotByAPIKey(apiKey string) (int64, error) {
	var uid int64
	err := a.db.QueryRow(
		`SELECT user_id FROM bot_config WHERE api_key = $1 AND enabled = true`, apiKey,
	).Scan(&uid)
	if err != nil {
		return 0, fmt.Errorf("get bot by api key: %w", err)
	}
	return uid, nil
}

// GetBotAPIKey returns the API key for a bot.
func (a *Adapter) GetBotAPIKey(botUID int64) (string, error) {
	var apiKey *string
	err := a.db.QueryRow(
		`SELECT api_key FROM bot_config WHERE user_id = $1`, botUID,
	).Scan(&apiKey)
	if err != nil {
		return "", fmt.Errorf("get bot api key: %w", err)
	}
	if apiKey == nil {
		return "", nil
	}
	return *apiKey, nil
}

// EnsureBotBodyBinding binds a bot to a body if it is not bound yet.
func (a *Adapter) EnsureBotBodyBinding(botUID int64, bodyID string) (string, bool, error) {
	if botUID <= 0 || bodyID == "" {
		return "", false, fmt.Errorf("invalid bot body binding")
	}
	if _, err := a.db.Exec(
		`UPDATE bot_config
		 SET body_id = $1
		 WHERE user_id = $2 AND (body_id IS NULL OR body_id = '' OR body_id = $1)`,
		bodyID, botUID,
	); err != nil {
		return "", false, fmt.Errorf("ensure bot body binding: %w", err)
	}

	boundBodyID, err := a.GetBotBodyID(botUID)
	if err != nil {
		return "", false, err
	}
	return boundBodyID, boundBodyID == bodyID, nil
}

// GetBotBodyID returns the persistent body binding for a bot.
func (a *Adapter) GetBotBodyID(botUID int64) (string, error) {
	var bodyID string
	err := a.db.QueryRow(
		`SELECT COALESCE(body_id, '') FROM bot_config WHERE user_id = $1`,
		botUID,
	).Scan(&bodyID)
	if err != nil {
		return "", fmt.Errorf("get bot body id: %w", err)
	}
	return bodyID, nil
}

// ListBotsByOwner returns bots owned by a specific user.
func (a *Adapter) ListBotsByOwner(ownerID int64) ([]map[string]interface{}, error) {
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, COALESCE(u.avatar_url, ''), u.state,
		        COALESCE(b.api_endpoint, '') as api_endpoint,
		        COALESCE(b.model, '') as model,
		        COALESCE(b.enabled, true) as enabled,
		        COALESCE(b.visibility, 'public') as visibility,
		        b.tenant_name
		 FROM users u LEFT JOIN bot_config b ON u.id = b.user_id
		 WHERE u.account_type = 'bot' AND b.owner_id = $1
		 ORDER BY u.created_at`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list bots by owner: %w", err)
	}
	defer rows.Close()

	var bots []map[string]interface{}
	for rows.Next() {
		var id int64
		var username, displayName, avatarURL, apiEndpoint, model, visibility string
		var tenantName *string
		var state int
		var enabled bool
		if err := rows.Scan(&id, &username, &displayName, &avatarURL, &state,
			&apiEndpoint, &model, &enabled, &visibility, &tenantName); err != nil {
			return nil, err
		}
		bot := map[string]interface{}{
			"id":           id,
			"username":     username,
			"display_name": displayName,
			"avatar_url":   avatarURL,
			"state":        state,
			"api_endpoint": apiEndpoint,
			"model":        model,
			"enabled":      enabled,
			"visibility":   visibility,
		}
		if tenantName != nil {
			bot["tenant_name"] = *tenantName
		}
		bots = append(bots, bot)
	}
	return bots, rows.Err()
}

// GetBotOwner returns the owner_id for a bot.
func (a *Adapter) GetBotOwner(botUID int64) (int64, error) {
	var ownerID int64
	err := a.db.QueryRow(
		`SELECT COALESCE(owner_id, 0) FROM bot_config WHERE user_id = $1`, botUID,
	).Scan(&ownerID)
	if err != nil {
		return 0, fmt.Errorf("get bot owner: %w", err)
	}
	return ownerID, nil
}

// DeleteBot removes a bot's config, disables the user account, and removes all friend relationships.
func (a *Adapter) DeleteBot(botUID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM friends WHERE from_user_id = $1 OR to_user_id = $2`,
		botUID, botUID,
	); err != nil {
		return fmt.Errorf("delete bot friends: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM bot_config WHERE user_id = $1`, botUID); err != nil {
		return fmt.Errorf("delete bot config: %w", err)
	}
	if _, err := tx.Exec(`UPDATE users SET state = 1 WHERE id = $1`, botUID); err != nil {
		return fmt.Errorf("disable bot user: %w", err)
	}
	return tx.Commit()
}

// SetTenantName sets the tenant_name for a bot.
func (a *Adapter) SetTenantName(botUID int64, tenantName string) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET tenant_name = $1 WHERE user_id = $2`,
		tenantName, botUID,
	)
	return err
}

// GetTenantName returns the tenant_name for a bot. Empty string means self-hosted.
func (a *Adapter) GetTenantName(botUID int64) (string, error) {
	var tenantName *string
	err := a.db.QueryRow(
		`SELECT tenant_name FROM bot_config WHERE user_id = $1`,
		botUID,
	).Scan(&tenantName)
	if err != nil {
		return "", fmt.Errorf("get tenant name: %w", err)
	}
	if tenantName == nil {
		return "", nil
	}
	return *tenantName, nil
}

// SetBotVisibility updates the visibility of a bot.
func (a *Adapter) SetBotVisibility(botUID int64, visibility string) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET visibility = $1 WHERE user_id = $2`,
		visibility, botUID,
	)
	return err
}

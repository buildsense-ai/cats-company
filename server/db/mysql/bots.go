// Package mysql - Cats Company bot configuration database operations.
package mysql

import (
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// SaveBotConfig saves or updates bot configuration with owner.
func (a *Adapter) SaveBotConfig(uid int64, apiEndpoint, model string) error {
	config, err := store.DefaultBotDefinitionJSON(uid)
	if err != nil {
		return fmt.Errorf("encode default bot definition: %w", err)
	}
	_, err = a.db.Exec(
		`INSERT INTO bot_config (user_id, api_endpoint, model, enabled, config)
		 VALUES (?, ?, ?, 1, CAST(? AS JSON))
		 ON DUPLICATE KEY UPDATE api_endpoint = ?, model = ?, updated_at = CURRENT_TIMESTAMP`,
		uid, apiEndpoint, model, config, apiEndpoint, model,
	)
	return err
}

// SaveBotConfigWithOwner saves bot configuration with owner_id.
func (a *Adapter) SaveBotConfigWithOwner(uid, ownerID int64, apiEndpoint, model string) error {
	config, err := store.DefaultBotDefinitionJSON(uid)
	if err != nil {
		return fmt.Errorf("encode default bot definition: %w", err)
	}
	_, err = a.db.Exec(
		`INSERT INTO bot_config (user_id, owner_id, api_endpoint, model, enabled, config)
		 VALUES (?, ?, ?, ?, 1, CAST(? AS JSON))
		 ON DUPLICATE KEY UPDATE api_endpoint = ?, model = ?, updated_at = CURRENT_TIMESTAMP`,
		uid, ownerID, apiEndpoint, model, config, apiEndpoint, model,
	)
	return err
}

// GetBotConfig retrieves bot configuration by user ID.
func (a *Adapter) GetBotConfig(uid int64) (*types.BotConfig, error) {
	bc := &types.BotConfig{}
	var visibility, skillsVisibility, skillMutationMode string
	var artifactUploadEnabled bool
	err := a.db.QueryRow(
		`SELECT user_id, COALESCE(owner_id, 0), api_endpoint, model, enabled, COALESCE(visibility, 'public'), COALESCE(skills_visibility, 'owner'), COALESCE(body_id, ''), COALESCE(role, 'general'), COALESCE(description, ''), COALESCE(artifact_upload_enabled, 1), COALESCE(skill_mutation_mode, 'owner_only')
		 FROM bot_config WHERE user_id = ?`, uid,
	).Scan(&bc.UserID, &bc.OwnerID, &bc.APIEndpoint, &bc.Model, &bc.Enabled, &visibility, &skillsVisibility, &bc.BodyID, &bc.Role, &bc.Description, &artifactUploadEnabled, &skillMutationMode)
	if err != nil {
		return nil, fmt.Errorf("get bot config: %w", err)
	}
	bc.Visibility = types.BotVisibility(visibility)
	bc.SkillsVisibility = types.BotSkillsVisibility(skillsVisibility)
	bc.ArtifactUploadEnabled = &artifactUploadEnabled
	mode, ok := types.ParseBotSkillMutationMode(skillMutationMode)
	if !ok {
		return nil, fmt.Errorf("get bot config: invalid skill mutation mode %q", skillMutationMode)
	}
	bc.SkillMutationMode = mode
	return bc, nil
}

// ListBots returns all bot users with their configs.
func (a *Adapter) ListBots() ([]map[string]interface{}, error) {
	rows, err := a.db.Query(
		`SELECT u.id, u.username, u.display_name, u.avatar_url, u.state,
		        COALESCE(b.api_endpoint, '') as api_endpoint,
		        COALESCE(b.model, '') as model,
		        COALESCE(b.enabled, 1) as enabled
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
		`UPDATE bot_config SET enabled = NOT enabled, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
		uid,
	)
	return err
}

// SaveAPIKey stores or updates the API key for a bot.
func (a *Adapter) SaveAPIKey(uid int64, apiKey string) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET api_key = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
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
		`SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, client_msg_id, metadata
		 FROM messages WHERE from_uid = ?
		 ORDER BY id DESC LIMIT ?`,
		uid, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get bot debug messages: %w", err)
	}
	defer rows.Close()

	var msgs []*types.Message
	for rows.Next() {
		m := &types.Message{}
		var blocksJSON, metadataJSON []byte
		var mode, role, clientMsgID *string
		if err := rows.Scan(&m.ID, &m.TopicID, &m.FromUID, &m.Content, &m.MsgType, &m.CreatedAt, &blocksJSON, &mode, &role, &clientMsgID, &metadataJSON); err != nil {
			return nil, fmt.Errorf("scan debug message: %w", err)
		}
		if len(blocksJSON) > 0 {
			_ = json.Unmarshal(blocksJSON, &m.ContentBlocks)
		}
		if mode != nil {
			m.Mode = *mode
		}
		if role != nil {
			m.Role = *role
		}
		if clientMsgID != nil {
			m.ClientMsgID = *clientMsgID
		}
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &m.Metadata); err != nil {
				return nil, fmt.Errorf("scan debug message metadata: %w", err)
			}
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// GetBotByAPIKey looks up a bot's user ID by its API key.
func (a *Adapter) GetBotByAPIKey(apiKey string) (int64, error) {
	var uid int64
	err := a.db.QueryRow(
		`SELECT user_id FROM bot_config WHERE api_key = ? AND enabled = 1`, apiKey,
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
		`SELECT api_key FROM bot_config WHERE user_id = ?`, botUID,
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
		 SET body_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? AND (body_id IS NULL OR body_id = '' OR body_id = ?)`,
		bodyID, botUID, bodyID,
	); err != nil {
		return "", false, fmt.Errorf("ensure bot body binding: %w", err)
	}

	boundBodyID, err := a.GetBotBodyID(botUID)
	if err != nil {
		return "", false, err
	}
	return boundBodyID, boundBodyID == bodyID, nil
}

// SetBotBodyBinding force-updates the persistent body binding for a bot.
func (a *Adapter) SetBotBodyBinding(botUID int64, bodyID string) error {
	if botUID <= 0 || bodyID == "" {
		return fmt.Errorf("invalid bot body binding")
	}
	if _, err := a.db.Exec(
		`UPDATE bot_config
		 SET body_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE user_id = ?`,
		bodyID, botUID,
	); err != nil {
		return fmt.Errorf("set bot body binding: %w", err)
	}
	return nil
}

// GetBotBodyID returns the persistent body binding for a bot.
func (a *Adapter) GetBotBodyID(botUID int64) (string, error) {
	var bodyID string
	err := a.db.QueryRow(
		`SELECT COALESCE(body_id, '') FROM bot_config WHERE user_id = ?`,
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
		`SELECT u.id, u.username, u.display_name, u.avatar_url, u.state,
		        COALESCE(b.api_endpoint, '') as api_endpoint,
		        COALESCE(b.model, '') as model,
		        COALESCE(b.enabled, 1) as enabled,
		        COALESCE(b.visibility, 'public') as visibility,
		        COALESCE(b.skills_visibility, 'owner') as skills_visibility,
		        b.tenant_name,
		        COALESCE(b.role, 'general') as role,
		        COALESCE(b.description, '') as description,
		        COALESCE(b.artifact_upload_enabled, 1) as artifact_upload_enabled,
		        COALESCE(b.skill_mutation_mode, 'owner_only') as skill_mutation_mode
		 FROM users u LEFT JOIN bot_config b ON u.id = b.user_id
		 WHERE u.account_type = 'bot' AND b.owner_id = ?
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
		var username, displayName, avatarURL, apiEndpoint, model, visibility, skillsVisibility, role, description, skillMutationMode string
		var tenantName *string
		var state int
		var enabled, artifactUploadEnabled bool
		if err := rows.Scan(&id, &username, &displayName, &avatarURL, &state,
			&apiEndpoint, &model, &enabled, &visibility, &skillsVisibility, &tenantName, &role, &description, &artifactUploadEnabled, &skillMutationMode); err != nil {
			return nil, err
		}
		mode, ok := types.ParseBotSkillMutationMode(skillMutationMode)
		if !ok {
			return nil, fmt.Errorf("list bots by owner: invalid skill mutation mode %q", skillMutationMode)
		}
		bot := map[string]interface{}{
			"id":                      id,
			"username":                username,
			"display_name":            displayName,
			"avatar_url":              avatarURL,
			"state":                   state,
			"api_endpoint":            apiEndpoint,
			"model":                   model,
			"enabled":                 enabled,
			"visibility":              visibility,
			"skills_visibility":       skillsVisibility,
			"role":                    role,
			"description":             description,
			"artifact_upload_enabled": artifactUploadEnabled,
			"skill_mutation_mode":     string(mode),
		}
		if tenantName != nil {
			bot["tenant_name"] = *tenantName
		}
		bots = append(bots, bot)
	}
	return bots, rows.Err()
}

// UpdateBotProfile updates owner-defined assistant identity metadata. Nil
// values preserve the existing field so PATCH requests can be partial.
func (a *Adapter) UpdateBotProfile(botUID int64, role, description *string) error {
	_, err := a.db.Exec(
		`UPDATE bot_config
		 SET role = COALESCE(?, role), description = COALESCE(?, description), updated_at = CURRENT_TIMESTAMP
		 WHERE user_id = ?`,
		role, description, botUID,
	)
	return err
}

func (a *Adapter) UpdateBotArtifactUploadPolicy(botUID int64, enabled bool) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET artifact_upload_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
		enabled, botUID,
	)
	return err
}

func (a *Adapter) GetBotArtifactUploadPolicy(botUID int64) (bool, error) {
	var enabled bool
	if err := a.db.QueryRow(
		`SELECT COALESCE(artifact_upload_enabled, 1) FROM bot_config WHERE user_id = ?`,
		botUID,
	).Scan(&enabled); err != nil {
		return false, fmt.Errorf("get bot artifact upload policy: %w", err)
	}
	return enabled, nil
}

func (a *Adapter) UpdateBotSkillMutationMode(botUID int64, mode types.BotSkillMutationMode) error {
	normalized, ok := types.ParseBotSkillMutationMode(string(mode))
	if !ok {
		return fmt.Errorf("invalid bot skill mutation mode %q", mode)
	}
	_, err := a.db.Exec(
		`UPDATE bot_config SET skill_mutation_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
		string(normalized), botUID,
	)
	return err
}

func (a *Adapter) GetBotSkillMutationMode(botUID int64) (types.BotSkillMutationMode, error) {
	var raw string
	if err := a.db.QueryRow(
		`SELECT COALESCE(skill_mutation_mode, 'owner_only') FROM bot_config WHERE user_id = ?`,
		botUID,
	).Scan(&raw); err != nil {
		return "", fmt.Errorf("get bot skill mutation mode: %w", err)
	}
	mode, ok := types.ParseBotSkillMutationMode(raw)
	if !ok {
		return "", fmt.Errorf("get bot skill mutation mode: invalid value %q", raw)
	}
	return mode, nil
}

// GetBotOwner returns the owner_id for a bot.
func (a *Adapter) GetBotOwner(botUID int64) (int64, error) {
	var ownerID int64
	err := a.db.QueryRow(
		`SELECT COALESCE(owner_id, 0) FROM bot_config WHERE user_id = ?`, botUID,
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

	// Delete all friend relationships involving this bot (both directions)
	if _, err := tx.Exec(
		`DELETE FROM friends WHERE from_user_id = ? OR to_user_id = ?`,
		botUID, botUID,
	); err != nil {
		return fmt.Errorf("delete bot friends: %w", err)
	}
	// Delete bot config
	if _, err := tx.Exec(`DELETE FROM bot_config WHERE user_id = ?`, botUID); err != nil {
		return fmt.Errorf("delete bot config: %w", err)
	}
	// Disable the user account (state=1 means disabled)
	if _, err := tx.Exec(`UPDATE users SET state = 1 WHERE id = ?`, botUID); err != nil {
		return fmt.Errorf("disable bot user: %w", err)
	}
	return tx.Commit()
}

// SetTenantName sets the tenant_name for a bot (platform-managed deployment).
func (a *Adapter) SetTenantName(botUID int64, tenantName string) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET tenant_name = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
		tenantName, botUID,
	)
	return err
}

// GetTenantName returns the tenant_name for a bot. Empty string means self-hosted.
func (a *Adapter) GetTenantName(botUID int64) (string, error) {
	var tenantName *string
	err := a.db.QueryRow(
		`SELECT tenant_name FROM bot_config WHERE user_id = ?`, botUID,
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
		`UPDATE bot_config SET visibility = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
		visibility, botUID,
	)
	return err
}

// SetBotSkillsVisibility updates who can inspect a bot's redacted skill list.
func (a *Adapter) SetBotSkillsVisibility(botUID int64, visibility string) error {
	_, err := a.db.Exec(
		`UPDATE bot_config SET skills_visibility = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
		visibility, botUID,
	)
	return err
}

// Package mysql - schema initialization for Cats Company.
package mysql

import (
	"fmt"
	"strings"
)

// CreateSchema creates all required database tables and runs migrations.
func (a *Adapter) CreateSchema() error {
	tables := []string{
		createUsersTable,
		createFriendsTable,
		createTopicsTable,
		createMessagesTable,
		createBotConfigTable,
		createRateLimitTable,
		createGroupsTable,
		createGroupMembersTable,
		createFeedbackReportsTable,
		createAuthServicesTable,
	}
	for _, q := range tables {
		if _, err := a.db.Exec(q); err != nil {
			return fmt.Errorf("schema creation failed: %w", err)
		}
	}

	// Run migrations (safe to re-run; uses IF NOT EXISTS / column checks)
	migrations := []string{
		migrateBotConfigAddAPIKey,
		migrateUsersAddBotDisclose,
		migrateMessagesAddReplyTo,
		migrateBotConfigAddOwnerID,
		migrateBotConfigAddVisibility,
		migrateBotConfigAddTenantName,
		migrateBotConfigAddBodyID,
		migrateMessagesAddCodeMode,
		migrateMessagesAddClientMsgID,
		migrateMessagesAddClientMsgIDIndex,
		migrateGroupsAddCreatedAtColumn,
		migrateGroupsBackfillCreatedAt,
		migrateGroupsCreatedAtNotNull,
		migrateGroupsAddAnnouncement,
		migrateGroupMembersAddMuted,
		migrateFriendsAddFromStatusIndex,
		migrateMessagesAddTopicIDIndex,
	}
	for _, m := range migrations {
		if _, err := a.db.Exec(m); err != nil {
			// Ignore duplicate column/index errors for idempotent migrations.
			if !isIgnorableMigrationError(err) {
				return fmt.Errorf("migration failed: %w", err)
			}
		}
	}
	return nil
}

func isIgnorableMigrationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "1060") ||
		strings.Contains(msg, "Duplicate column") ||
		strings.Contains(msg, "1061") ||
		strings.Contains(msg, "Duplicate key name")
}

const createUsersTable = `
CREATE TABLE IF NOT EXISTS users (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE,
    email VARCHAR(255) DEFAULT NULL,
    phone VARCHAR(32) DEFAULT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    avatar_url VARCHAR(512) DEFAULT NULL,
    account_type ENUM('human','bot','service') NOT NULL DEFAULT 'human',
    pass_hash VARBINARY(128) NOT NULL,
    state TINYINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_users_account_type (account_type),
    INDEX idx_users_phone (phone),
    INDEX idx_users_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createFriendsTable = `
CREATE TABLE IF NOT EXISTS friends (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    from_user_id BIGINT NOT NULL,
    to_user_id BIGINT NOT NULL,
    status ENUM('pending','accepted','rejected','blocked') NOT NULL DEFAULT 'pending',
    message VARCHAR(255) DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_friend_pair (from_user_id, to_user_id),
    INDEX idx_friends_from_status (from_user_id, status),
    INDEX idx_friends_to_user (to_user_id, status),
    INDEX idx_friends_status (status),
    FOREIGN KEY (from_user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (to_user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createTopicsTable = `
CREATE TABLE IF NOT EXISTS topics (
    id VARCHAR(64) PRIMARY KEY,
    type ENUM('p2p','group') NOT NULL DEFAULT 'p2p',
    name VARCHAR(128) DEFAULT '',
    owner_id BIGINT DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_topics_type (type),
    FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createMessagesTable = `
CREATE TABLE IF NOT EXISTS messages (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    topic_id VARCHAR(64) NOT NULL,
    from_uid BIGINT NOT NULL,
    content TEXT NOT NULL,
    msg_type ENUM('text','image','voice','file') NOT NULL DEFAULT 'text',
    client_msg_id VARCHAR(128) DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_messages_topic (topic_id, created_at),
    INDEX idx_messages_topic_id (topic_id, id),
    UNIQUE KEY uk_messages_client_msg_id (topic_id, from_uid, client_msg_id),
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    FOREIGN KEY (from_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createBotConfigTable = `
CREATE TABLE IF NOT EXISTS bot_config (
    user_id BIGINT PRIMARY KEY,
    api_endpoint VARCHAR(512) DEFAULT '',
    model VARCHAR(128) DEFAULT '',
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    config JSON DEFAULT NULL,
    body_id VARCHAR(128) DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createRateLimitTable = `
CREATE TABLE IF NOT EXISTS rate_limits (
    account_type ENUM('human','bot','service') PRIMARY KEY,
    max_per_second INT NOT NULL DEFAULT 10,
    max_per_minute INT NOT NULL DEFAULT 120,
    burst_size INT NOT NULL DEFAULT 20
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

// Migration: add api_key column to bot_config table.
const migrateBotConfigAddAPIKey = `
ALTER TABLE bot_config ADD COLUMN api_key VARCHAR(128) DEFAULT NULL;
`

// Migration: add bot_disclose column to users table.
const migrateUsersAddBotDisclose = `
ALTER TABLE users ADD COLUMN bot_disclose TINYINT(1) NOT NULL DEFAULT 0;
`

const createGroupsTable = `
CREATE TABLE IF NOT EXISTS ` + "`groups`" + ` (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    owner_id BIGINT NOT NULL,
    avatar_url VARCHAR(512) DEFAULT NULL,
    announcement TEXT DEFAULT NULL,
    max_members INT NOT NULL DEFAULT 200,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createGroupMembersTable = `
CREATE TABLE IF NOT EXISTS group_members (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    group_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    role ENUM('owner','admin','member') NOT NULL DEFAULT 'member',
    muted TINYINT(1) NOT NULL DEFAULT 0,
    joined_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_group_user (group_id, user_id),
    INDEX idx_gm_user (user_id),
    FOREIGN KEY (group_id) REFERENCES ` + "`groups`" + `(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createFeedbackReportsTable = `
CREATE TABLE IF NOT EXISTS feedback_reports (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    category ENUM('bug','suggestion','other') NOT NULL DEFAULT 'bug',
    title VARCHAR(160) DEFAULT '',
    description TEXT NOT NULL,
    page_url VARCHAR(1024) DEFAULT '',
    user_agent VARCHAR(512) DEFAULT '',
    status ENUM('open','reviewing','resolved','closed') NOT NULL DEFAULT 'open',
    attachments JSON DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_feedback_user_created (user_id, created_at),
    INDEX idx_feedback_status_created (status, created_at),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createAuthServicesTable = `
CREATE TABLE IF NOT EXISTS auth_services (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    slug VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(128) NOT NULL,
    token_prefix VARCHAR(32) NOT NULL,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    scopes JSON NOT NULL,
    state TINYINT NOT NULL DEFAULT 0,
    last_used_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_auth_services_state (state),
    INDEX idx_auth_services_slug (slug)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

// Migration: add reply_to column to messages table.
const migrateMessagesAddReplyTo = `
ALTER TABLE messages ADD COLUMN reply_to BIGINT DEFAULT NULL;
`

// Migration: add owner_id column to bot_config table.
const migrateBotConfigAddOwnerID = `
ALTER TABLE bot_config ADD COLUMN owner_id BIGINT DEFAULT NULL;
`

// Migration: add visibility column to bot_config table.
const migrateBotConfigAddVisibility = `
ALTER TABLE bot_config ADD COLUMN visibility ENUM('public','private') NOT NULL DEFAULT 'public';
`

// Migration: add tenant_name column to bot_config table.
// NULL = self-hosted (third-party), non-NULL = platform-managed deployment.
const migrateBotConfigAddTenantName = `
ALTER TABLE bot_config ADD COLUMN tenant_name VARCHAR(128) DEFAULT NULL;
`

// Migration: add persistent bot body binding.
const migrateBotConfigAddBodyID = `
ALTER TABLE bot_config ADD COLUMN body_id VARCHAR(128) DEFAULT NULL;
`

// Migration: add code mode support to messages table.
const migrateMessagesAddCodeMode = `
ALTER TABLE messages
  ADD COLUMN content_blocks JSON DEFAULT NULL,
  ADD COLUMN mode VARCHAR(20) DEFAULT 'normal',
  ADD COLUMN role VARCHAR(20) DEFAULT NULL;
`

// Migration: add a client-generated id for safe retry deduplication.
const migrateMessagesAddClientMsgID = `
ALTER TABLE messages ADD COLUMN client_msg_id VARCHAR(128) DEFAULT NULL;
`

// Migration: add a retry deduplication index for client-generated ids.
const migrateMessagesAddClientMsgIDIndex = `
ALTER TABLE messages ADD UNIQUE KEY uk_messages_client_msg_id (topic_id, from_uid, client_msg_id);
`

// Migration: add and backfill created_at for legacy groups tables.
const migrateGroupsAddCreatedAtColumn = `
ALTER TABLE ` + "`groups`" + ` ADD COLUMN created_at TIMESTAMP NULL DEFAULT NULL;
`

const migrateGroupsBackfillCreatedAt = `
UPDATE ` + "`groups`" + ` g
LEFT JOIN topics t ON t.id = CONCAT('grp_', g.id)
LEFT JOIN (
    SELECT group_id, MIN(joined_at) AS first_joined_at
    FROM group_members
    GROUP BY group_id
) gm ON gm.group_id = g.id
SET g.created_at = COALESCE(g.created_at, t.created_at, gm.first_joined_at, CURRENT_TIMESTAMP)
WHERE g.created_at IS NULL;
`

const migrateGroupsCreatedAtNotNull = `
ALTER TABLE ` + "`groups`" + ` MODIFY COLUMN created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;
`

// Migration: add group announcement support.
const migrateGroupsAddAnnouncement = `
ALTER TABLE ` + "`groups`" + ` ADD COLUMN announcement TEXT DEFAULT NULL;
`

// Migration: add per-member mute state.
const migrateGroupMembersAddMuted = `
ALTER TABLE group_members ADD COLUMN muted TINYINT(1) NOT NULL DEFAULT 0;
`

// Migration: speed up friend list lookups by user and accepted status.
const migrateFriendsAddFromStatusIndex = `
ALTER TABLE friends ADD INDEX idx_friends_from_status (from_user_id, status);
`

// Migration: speed up latest-message and missed-message lookups by topic.
const migrateMessagesAddTopicIDIndex = `
ALTER TABLE messages ADD INDEX idx_messages_topic_id (topic_id, id);
`

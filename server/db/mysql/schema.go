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
		createPushSubscriptionsTable,
		createFriendsTable,
		createTopicsTable,
		createProjectsTable,
		createProjectTopicsTable,
		createConversationTitlesTable,
		createConversationNotificationMutesTable,
		createMessagesTable,
		createConversationTaskStatusesTable,
		createConversationTaskStatusSourcesTable,
		createBotConnectionGenerationsTable,
		createBotConfigTable,
		createRateLimitTable,
		createGroupsTable,
		createGroupMembersTable,
		createGroupInviteRequestsTable,
		createFeedbackReportsTable,
		createAuthServicesTable,
		createChannelAgentEntriesTable,
		createChannelAgentAccessRequestsTable,
		createChannelAgentBindingsTable,
		createChannelRouteSelectionLocksTable,
		createChannelAgentRoutesTable,
		createChannelIdentityMobileLinksTable,
		createChannelGroupMobileLinksTable,
		createChannelGroupBindingsTable,
		createChannelNativeGroupsTable,
		createWeixinClawBotTokensTable,
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
		migrateBotConfigAddSkillsVisibility,
		migrateBotConfigAddTenantName,
		migrateBotConfigAddBodyID,
		migrateBotConfigAddProfileRole,
		migrateBotConfigAddProfileDescription,
		migrateBotConfigAddArtifactUploadPolicy,
		migrateMessagesAddCodeMode,
		migrateMessagesAddClientMsgID,
		migrateMessagesAddClientMsgIDIndex,
		migrateMessagesAddMetadata,
		migrateGroupsAddCreatedAtColumn,
		migrateGroupsBackfillCreatedAt,
		migrateGroupsCreatedAtNotNull,
		migrateGroupsAddAnnouncement,
		migrateGroupsAddKind,
		migrateGroupsBackfillChannelManagedKind,
		migrateGroupMembersAddMuted,
		migrateFriendsAddFromStatusIndex,
		migrateMessagesAddTopicIDIndex,
		migrateChannelAgentEntriesAddAppID,
		migrateChannelAgentEntriesAddAccessMode,
		migrateChannelAgentEntriesDefaultAccessMode,
		migrateChannelAgentBindingsAddActorUID,
		migrateChannelAgentBindingsAddCanonicalUID,
		migrateChannelAgentBindingsAddDeviceAccessEnabled,
		migrateChannelAgentBindingsEnableCanonicalDeviceAccess,
		migrateChannelAgentEntriesOwnerAgentIndex,
		migrateChannelAgentBindingsLookupIndex,
		migrateChannelAgentBindingsActorAgentIndex,
		migrateChannelAgentBindingsActorAnyIndex,
		migrateChannelAgentRoutesLookupIndex,
		migrateChannelAgentRoutesActorIndex,
		migrateChannelGroupBindingsAddSelectedAt,
		migrateChannelGroupBindingsBackfillSelectedAt,
		migrateChannelGroupBindingsSelectedAtNotNull,
		migrateChannelNativeGroupsAddLastEventID,
		migrateChannelNativeGroupsAddLastEventTime,
		migrateChannelNativeGroupsAddLastEventClaimedAt,
		migrateChannelAgentAccessOwnerAgentIndex,
		migrateChannelAgentAccessActorAgentIndex,
		migrateChannelAgentAccessLookupIndex,
		migrateConversationTaskStatusesTimestampPrecision,
		migrateConversationTaskStatusesCreatedAtPrecision,
		migrateConversationTaskStatusesUpdatedAtPrecision,
		migrateConversationTaskStatusSourcesExpiresAtPrecision,
		migrateConversationTaskStatusSourcesAddEventUpdatedAt,
		migrateConversationTaskStatusSourcesBackfillEventUpdatedAt,
		migrateConversationTaskStatusSourcesEventUpdatedAtNotNull,
		migrateConversationTaskStatusSourcesCreatedAtPrecision,
		migrateConversationTaskStatusSourcesUpdatedAtPrecision,
	}
	for _, m := range migrations {
		if _, err := a.db.Exec(m); err != nil {
			// Ignore duplicate column/index errors for idempotent migrations.
			if !isIgnorableMigrationError(err) {
				return fmt.Errorf("migration failed: %w", err)
			}
		}
	}
	if err := a.ensureChannelAgentBindingUniqueIncludesAgent(); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) ensureChannelAgentBindingUniqueIncludesAgent() error {
	rows, err := a.db.Query(
		`SELECT COLUMN_NAME
		 FROM INFORMATION_SCHEMA.STATISTICS
		 WHERE TABLE_SCHEMA = DATABASE()
		   AND TABLE_NAME = 'channel_agent_bindings'
		   AND INDEX_NAME = 'uk_channel_agent_binding_identity'
		 ORDER BY SEQ_IN_INDEX`,
	)
	if err != nil {
		return fmt.Errorf("inspect channel agent binding unique index: %w", err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return fmt.Errorf("scan channel agent binding unique index: %w", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	expected := "channel,channel_app_id,channel_user_id,channel_conversation_id,agent_uid"
	if strings.Join(columns, ",") == expected {
		return nil
	}
	if _, err := a.db.Exec(migrateChannelAgentBindingsUniqueIncludesAgent); err != nil {
		return fmt.Errorf("migrate channel agent binding unique index: %w", err)
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
		strings.Contains(msg, "Duplicate key name") ||
		strings.Contains(msg, "1091") ||
		strings.Contains(msg, "check that column/key exists")
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

const createPushSubscriptionsTable = `
CREATE TABLE IF NOT EXISTS push_subscriptions (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    uid BIGINT NOT NULL,
    endpoint VARCHAR(512) COLLATE utf8mb4_bin NOT NULL,
    p256dh VARCHAR(256) NOT NULL,
    auth VARCHAR(128) NOT NULL,
    registration_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_push_subscriptions_endpoint (endpoint),
    INDEX idx_push_subscriptions_uid (uid),
    CONSTRAINT fk_push_subscriptions_uid FOREIGN KEY (uid) REFERENCES users(id) ON DELETE CASCADE
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

const createProjectsTable = `
CREATE TABLE IF NOT EXISTS projects (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    owner_uid BIGINT NOT NULL,
    name VARCHAR(128) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_projects_owner_name (owner_uid, name),
    INDEX idx_projects_owner_updated (owner_uid, updated_at, id),
    FOREIGN KEY (owner_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createProjectTopicsTable = `
CREATE TABLE IF NOT EXISTS project_topics (
    owner_uid BIGINT NOT NULL,
    topic_id VARCHAR(64) NOT NULL,
    project_id BIGINT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (owner_uid, topic_id),
    INDEX idx_project_topics_project (project_id, created_at),
    FOREIGN KEY (owner_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createConversationTitlesTable = `
CREATE TABLE IF NOT EXISTS conversation_titles (
    user_id BIGINT NOT NULL,
    topic_id VARCHAR(64) NOT NULL,
    title VARCHAR(80) NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, topic_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createConversationNotificationMutesTable = `
CREATE TABLE IF NOT EXISTS conversation_notification_mutes (
    user_id BIGINT NOT NULL,
    topic_id VARCHAR(64) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, topic_id),
    -- A P2P topic is created on its first message, while a user may mute the
    -- visible conversation before then.
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
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
    metadata JSON DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_messages_topic (topic_id, created_at),
    INDEX idx_messages_topic_id (topic_id, id),
    UNIQUE KEY uk_messages_client_msg_id (topic_id, from_uid, client_msg_id),
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    FOREIGN KEY (from_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createConversationTaskStatusesTable = `
CREATE TABLE IF NOT EXISTS conversation_task_statuses (
    topic_id VARCHAR(64) PRIMARY KEY,
    run_id VARCHAR(128) DEFAULT '',
    state ENUM('idle','running','completed','failed','cancelled','stale','waiting') NOT NULL DEFAULT 'idle',
    summary TEXT NOT NULL,
    error TEXT NOT NULL,
    source_uid BIGINT DEFAULT NULL,
    expires_at TIMESTAMP(6) NULL DEFAULT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    INDEX idx_conversation_task_statuses_updated_at (updated_at),
    INDEX idx_conversation_task_statuses_state (state),
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    FOREIGN KEY (source_uid) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createConversationTaskStatusSourcesTable = `
CREATE TABLE IF NOT EXISTS conversation_task_status_sources (
    topic_id VARCHAR(64) NOT NULL,
    source_uid BIGINT NOT NULL,
    run_id VARCHAR(128) DEFAULT '',
    state ENUM('idle','running','completed','failed','cancelled','stale','waiting') NOT NULL DEFAULT 'idle',
    summary TEXT NOT NULL,
    error TEXT NOT NULL,
    expires_at TIMESTAMP(6) NULL DEFAULT NULL,
    event_updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (topic_id, source_uid),
    INDEX idx_conversation_task_status_sources_updated_at (updated_at),
    INDEX idx_conversation_task_status_sources_state (state),
    FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    FOREIGN KEY (source_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createBotConnectionGenerationsTable = `
CREATE TABLE IF NOT EXISTS bot_connection_generations (
    bot_uid BIGINT PRIMARY KEY,
    generation BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    FOREIGN KEY (bot_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createBotConfigTable = `
CREATE TABLE IF NOT EXISTS bot_config (
    user_id BIGINT PRIMARY KEY,
    api_endpoint VARCHAR(512) DEFAULT '',
    model VARCHAR(128) DEFAULT '',
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    config JSON DEFAULT NULL,
    skills_visibility ENUM('owner','authorized','public') NOT NULL DEFAULT 'owner',
    body_id VARCHAR(128) DEFAULT NULL,
    role VARCHAR(32) NOT NULL DEFAULT 'general',
    description TEXT NULL,
    artifact_upload_enabled TINYINT(1) NOT NULL DEFAULT 1,
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
    group_kind VARCHAR(32) NOT NULL DEFAULT 'standard',
    avatar_url VARCHAR(512) DEFAULT NULL,
    announcement TEXT DEFAULT NULL,
    max_members INT NOT NULL DEFAULT 200,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const migrateGroupsAddKind = `ALTER TABLE ` + "`groups`" + ` ADD COLUMN group_kind VARCHAR(32) NOT NULL DEFAULT 'standard';`

const migrateGroupsBackfillChannelManagedKind = `
UPDATE ` + "`groups`" + ` g
JOIN channel_native_groups cng ON cng.group_id = g.id
SET g.group_kind = 'channel_managed'
WHERE g.group_kind <> 'channel_managed';
`

const migrateChannelNativeGroupsAddLastEventID = `ALTER TABLE channel_native_groups ADD COLUMN last_event_id VARCHAR(128) NOT NULL DEFAULT '';`

const migrateChannelNativeGroupsAddLastEventTime = `ALTER TABLE channel_native_groups ADD COLUMN last_event_time BIGINT NOT NULL DEFAULT 0;`

const migrateChannelNativeGroupsAddLastEventClaimedAt = `ALTER TABLE channel_native_groups ADD COLUMN last_event_claimed_at BIGINT NOT NULL DEFAULT 0;`

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

const createGroupInviteRequestsTable = `
CREATE TABLE IF NOT EXISTS group_invite_requests (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    group_id BIGINT NOT NULL,
    inviter_id BIGINT NOT NULL,
    invitee_id BIGINT NOT NULL,
    resolver_id BIGINT DEFAULT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_group_invite_request (group_id, invitee_id),
    INDEX idx_group_invite_requests_pending (group_id, status, created_at),
    FOREIGN KEY (group_id) REFERENCES ` + "`groups`" + `(id) ON DELETE CASCADE,
    FOREIGN KEY (inviter_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (invitee_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (resolver_id) REFERENCES users(id) ON DELETE SET NULL
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

const createChannelAgentEntriesTable = `
CREATE TABLE IF NOT EXISTS channel_agent_entries (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    scene_key VARCHAR(64) NOT NULL UNIQUE,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    access_mode VARCHAR(32) NOT NULL DEFAULT 'approval_required',
    owner_uid BIGINT NOT NULL,
    agent_uid BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    last_used_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_channel_agent_entries_owner_agent (owner_uid, agent_uid, channel, channel_app_id, status),
    FOREIGN KEY (owner_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (agent_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelAgentAccessRequestsTable = `
CREATE TABLE IF NOT EXISTS channel_agent_access_requests (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    entry_id BIGINT NOT NULL,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    actor_uid BIGINT NOT NULL,
    owner_uid BIGINT NOT NULL,
    agent_uid BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    reviewed_by_uid BIGINT DEFAULT NULL,
    reviewed_at TIMESTAMP NULL DEFAULT NULL,
    requested_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_channel_agent_access_identity (entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id),
    INDEX idx_channel_agent_access_owner_agent (owner_uid, agent_uid, status),
    INDEX idx_channel_agent_access_actor_agent (actor_uid, agent_uid, status),
    INDEX idx_channel_agent_access_lookup (channel, channel_app_id, channel_user_id, status),
    FOREIGN KEY (entry_id) REFERENCES channel_agent_entries(id) ON DELETE CASCADE,
    FOREIGN KEY (actor_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (owner_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (agent_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (reviewed_by_uid) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelAgentBindingsTable = `
CREATE TABLE IF NOT EXISTS channel_agent_bindings (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    actor_uid BIGINT DEFAULT NULL,
    canonical_uid BIGINT DEFAULT NULL,
    device_access_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    owner_uid BIGINT NOT NULL,
    agent_uid BIGINT NOT NULL,
    entry_id BIGINT DEFAULT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    bound_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMP NULL DEFAULT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_channel_agent_binding_identity (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid),
    INDEX idx_channel_agent_bindings_lookup (channel, channel_app_id, channel_user_id, status),
    INDEX idx_channel_agent_bindings_agent (owner_uid, agent_uid, status),
    INDEX idx_channel_agent_bindings_actor_agent (channel, channel_app_id, actor_uid, agent_uid, status),
    INDEX idx_channel_agent_bindings_actor_any (actor_uid, agent_uid, status),
    FOREIGN KEY (actor_uid) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (canonical_uid) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (owner_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (agent_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (entry_id) REFERENCES channel_agent_entries(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelAgentRoutesTable = `
CREATE TABLE IF NOT EXISTS channel_agent_routes (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    actor_uid BIGINT DEFAULT NULL,
    agent_uid BIGINT NOT NULL,
    source VARCHAR(32) NOT NULL DEFAULT '',
    selected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMP NULL DEFAULT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_channel_agent_route_identity (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type),
    INDEX idx_channel_agent_routes_lookup (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type),
    INDEX idx_channel_agent_routes_actor (channel, channel_app_id, actor_uid, agent_uid),
    FOREIGN KEY (actor_uid) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (agent_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelRouteSelectionLocksTable = `
CREATE TABLE IF NOT EXISTS channel_route_selection_locks (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_channel_route_selection_lock (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelIdentityMobileLinksTable = `
CREATE TABLE IF NOT EXISTS channel_identity_mobile_links (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    scene_key VARCHAR(64) NOT NULL UNIQUE,
    entry_id BIGINT NOT NULL,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    canonical_uid BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    expires_at TIMESTAMP NOT NULL,
    consumed_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_channel_mobile_links_entry (entry_id, status),
    INDEX idx_channel_mobile_links_canonical (canonical_uid, status, expires_at),
    FOREIGN KEY (entry_id) REFERENCES channel_agent_entries(id) ON DELETE CASCADE,
    FOREIGN KEY (canonical_uid) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelGroupMobileLinksTable = `
CREATE TABLE IF NOT EXISTS channel_group_mobile_links (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    scene_key VARCHAR(64) NOT NULL UNIQUE,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    canonical_uid BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    topic_id VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    expires_at TIMESTAMP NOT NULL,
    consumed_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_channel_group_mobile_links_group (group_id, canonical_uid, status, expires_at),
    FOREIGN KEY (canonical_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES ` + "`groups`" + `(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelGroupBindingsTable = `
CREATE TABLE IF NOT EXISTS channel_group_bindings (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    actor_uid BIGINT NULL DEFAULT NULL,
    canonical_uid BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
	topic_id VARCHAR(128) NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'active',
	bound_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	selected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_used_at TIMESTAMP NULL DEFAULT NULL,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_channel_group_binding_identity (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type),
    INDEX idx_channel_group_bindings_topic (topic_id, status),
    INDEX idx_channel_group_bindings_lookup (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type, status),
    FOREIGN KEY (actor_uid) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (canonical_uid) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (group_id) REFERENCES ` + "`groups`" + `(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createChannelNativeGroupsTable = `
CREATE TABLE IF NOT EXISTS channel_native_groups (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    tenant_key VARCHAR(128) NOT NULL DEFAULT '',
    conversation_id VARCHAR(256) NOT NULL,
    conversation_name VARCHAR(255) NOT NULL DEFAULT '',
    operator_channel_user_id VARCHAR(256) NOT NULL DEFAULT '',
    operator_actor_uid BIGINT NULL DEFAULT NULL,
    canonical_uid BIGINT NULL DEFAULT NULL,
    group_id BIGINT NULL DEFAULT NULL,
    topic_id VARCHAR(128) NOT NULL DEFAULT '',
    source_kind VARCHAR(32) NOT NULL DEFAULT '',
    source_group_id BIGINT NULL DEFAULT NULL,
    source_agent_uid BIGINT NULL DEFAULT NULL,
    status ENUM('pending','active','disconnected') NOT NULL DEFAULT 'pending',
	last_event_id VARCHAR(128) NOT NULL DEFAULT '',
	last_event_time BIGINT NOT NULL DEFAULT 0,
	last_event_claimed_at BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_channel_native_group_identity (channel, channel_app_id, tenant_key, conversation_id),
    INDEX idx_channel_native_groups_topic (topic_id, status),
    FOREIGN KEY (operator_actor_uid) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (canonical_uid) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (group_id) REFERENCES ` + "`groups`" + `(id) ON DELETE SET NULL,
    FOREIGN KEY (source_group_id) REFERENCES ` + "`groups`" + `(id) ON DELETE SET NULL,
    FOREIGN KEY (source_agent_uid) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const createWeixinClawBotTokensTable = `
CREATE TABLE IF NOT EXISTS weixin_clawbot_tokens (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    bot_token TEXT NOT NULL,
    token_last4 VARCHAR(8) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    owner_uid BIGINT NOT NULL,
    ilink_bot_id VARCHAR(128) NOT NULL DEFAULT '',
    ilink_user_id VARCHAR(128) NOT NULL DEFAULT '',
    base_url TEXT NOT NULL,
    source_scene_key VARCHAR(64) NOT NULL DEFAULT '',
    get_updates_buf TEXT NOT NULL,
    context_tokens JSON NOT NULL,
    last_poll_at TIMESTAMP NULL DEFAULT NULL,
    last_used_at TIMESTAMP NULL DEFAULT NULL,
    last_error_at TIMESTAMP NULL DEFAULT NULL,
    last_error TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_weixin_clawbot_tokens_active (status, updated_at),
    INDEX idx_weixin_clawbot_tokens_owner (owner_uid, status),
    INDEX idx_weixin_clawbot_tokens_ilink (ilink_bot_id, ilink_user_id),
    FOREIGN KEY (owner_uid) REFERENCES users(id) ON DELETE CASCADE
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

// Migration: add redacted skill-list visibility to bot_config.
const migrateBotConfigAddSkillsVisibility = `
ALTER TABLE bot_config ADD COLUMN skills_visibility ENUM('owner','authorized','public') NOT NULL DEFAULT 'owner';
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

const migrateBotConfigAddProfileRole = `
ALTER TABLE bot_config ADD COLUMN role VARCHAR(32) NOT NULL DEFAULT 'general';
`

const migrateBotConfigAddProfileDescription = `
ALTER TABLE bot_config ADD COLUMN description TEXT NULL;
`

const migrateBotConfigAddArtifactUploadPolicy = `
ALTER TABLE bot_config ADD COLUMN artifact_upload_enabled TINYINT(1) NOT NULL DEFAULT 1;
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

// Migration: persist optional normalized message metadata.
const migrateMessagesAddMetadata = `
ALTER TABLE messages ADD COLUMN metadata JSON DEFAULT NULL;
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

const migrateChannelAgentEntriesAddAppID = `
ALTER TABLE channel_agent_entries ADD COLUMN channel_app_id VARCHAR(128) NOT NULL DEFAULT '';
`

const migrateChannelAgentEntriesAddAccessMode = `
ALTER TABLE channel_agent_entries ADD COLUMN access_mode VARCHAR(32) NOT NULL DEFAULT 'approval_required';
`

const migrateChannelAgentEntriesDefaultAccessMode = `
ALTER TABLE channel_agent_entries ALTER COLUMN access_mode SET DEFAULT 'approval_required';
`

const migrateChannelAgentBindingsAddActorUID = `
ALTER TABLE channel_agent_bindings ADD COLUMN actor_uid BIGINT DEFAULT NULL;
`

const migrateChannelAgentBindingsAddCanonicalUID = `
ALTER TABLE channel_agent_bindings ADD COLUMN canonical_uid BIGINT DEFAULT NULL;
`

const migrateChannelAgentBindingsAddDeviceAccessEnabled = `
ALTER TABLE channel_agent_bindings ADD COLUMN device_access_enabled BOOLEAN NOT NULL DEFAULT FALSE;
`

const migrateChannelAgentBindingsEnableCanonicalDeviceAccess = `
UPDATE channel_agent_bindings
SET device_access_enabled = TRUE, updated_at = CURRENT_TIMESTAMP
WHERE status = 'active' AND canonical_uid IS NOT NULL AND canonical_uid > 0 AND device_access_enabled = FALSE;
`

const migrateChannelAgentBindingsUniqueIncludesAgent = `
ALTER TABLE channel_agent_bindings
  DROP INDEX uk_channel_agent_binding_identity,
  ADD UNIQUE KEY uk_channel_agent_binding_identity (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid);
`

const migrateChannelAgentEntriesOwnerAgentIndex = `
ALTER TABLE channel_agent_entries ADD INDEX idx_channel_agent_entries_owner_agent (owner_uid, agent_uid, channel, channel_app_id, status);
`

const migrateChannelAgentBindingsLookupIndex = `
ALTER TABLE channel_agent_bindings ADD INDEX idx_channel_agent_bindings_lookup (channel, channel_app_id, channel_user_id, status);
`

const migrateChannelAgentBindingsActorAgentIndex = `
ALTER TABLE channel_agent_bindings ADD INDEX idx_channel_agent_bindings_actor_agent (channel, channel_app_id, actor_uid, agent_uid, status);
`

const migrateChannelAgentBindingsActorAnyIndex = `
ALTER TABLE channel_agent_bindings ADD INDEX idx_channel_agent_bindings_actor_any (actor_uid, agent_uid, status);
`

const migrateChannelAgentRoutesLookupIndex = `
ALTER TABLE channel_agent_routes ADD INDEX idx_channel_agent_routes_lookup (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type);
`

const migrateChannelAgentRoutesActorIndex = `
ALTER TABLE channel_agent_routes ADD INDEX idx_channel_agent_routes_actor (channel, channel_app_id, actor_uid, agent_uid);
`

const migrateChannelGroupBindingsAddSelectedAt = `
ALTER TABLE channel_group_bindings ADD COLUMN selected_at TIMESTAMP NULL DEFAULT NULL;
`

const migrateChannelGroupBindingsBackfillSelectedAt = `
UPDATE channel_group_bindings SET selected_at = COALESCE(bound_at, updated_at, CURRENT_TIMESTAMP) WHERE selected_at IS NULL;
`

const migrateChannelGroupBindingsSelectedAtNotNull = `
ALTER TABLE channel_group_bindings MODIFY COLUMN selected_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;
`

const migrateChannelAgentAccessOwnerAgentIndex = `
ALTER TABLE channel_agent_access_requests ADD INDEX idx_channel_agent_access_owner_agent (owner_uid, agent_uid, status);
`

const migrateChannelAgentAccessActorAgentIndex = `
ALTER TABLE channel_agent_access_requests ADD INDEX idx_channel_agent_access_actor_agent (actor_uid, agent_uid, status);
`

const migrateChannelAgentAccessLookupIndex = `
ALTER TABLE channel_agent_access_requests ADD INDEX idx_channel_agent_access_lookup (channel, channel_app_id, channel_user_id, status);
`

// Migration: upgrade task-status timestamps to fractional-second precision.
// Recovery compares a Go time.Now() (with sub-second precision) against the
// stored updated_at; a second-precision TIMESTAMP would treat progress written
// later in the same wall-clock second as <= disconnectedAt and could mark
// healthy work stale. TIMESTAMP(6) keeps the cutoff CAS exact. Statements are
// kept one-per-Exec because the MySQL driver does not enable multiStatements.
const migrateConversationTaskStatusesTimestampPrecision = `
ALTER TABLE conversation_task_statuses
  MODIFY expires_at TIMESTAMP(6) NULL DEFAULT NULL;
`
const migrateConversationTaskStatusesCreatedAtPrecision = `
ALTER TABLE conversation_task_statuses
  MODIFY created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6);
`
const migrateConversationTaskStatusesUpdatedAtPrecision = `
ALTER TABLE conversation_task_statuses
  MODIFY updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6);
`
const migrateConversationTaskStatusSourcesExpiresAtPrecision = `
ALTER TABLE conversation_task_status_sources
  MODIFY expires_at TIMESTAMP(6) NULL DEFAULT NULL;
`
const migrateConversationTaskStatusSourcesAddEventUpdatedAt = `
ALTER TABLE conversation_task_status_sources
  ADD COLUMN event_updated_at TIMESTAMP(6) NULL DEFAULT NULL AFTER expires_at;
`
const migrateConversationTaskStatusSourcesBackfillEventUpdatedAt = `
UPDATE conversation_task_status_sources
  SET event_updated_at = updated_at
  WHERE event_updated_at IS NULL;
`
const migrateConversationTaskStatusSourcesEventUpdatedAtNotNull = `
ALTER TABLE conversation_task_status_sources
  MODIFY event_updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6);
`
const migrateConversationTaskStatusSourcesCreatedAtPrecision = `
ALTER TABLE conversation_task_status_sources
  MODIFY created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6);
`
const migrateConversationTaskStatusSourcesUpdatedAtPrecision = `
ALTER TABLE conversation_task_status_sources
  MODIFY updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6);
`

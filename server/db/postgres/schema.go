package postgres

import "fmt"

// CreateSchema creates all required database tables and idempotent indexes.
func (a *Adapter) CreateSchema() error {
	statements := []string{
		createUpdatedAtFunction,
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
		createChannelAgentEntriesTable,
		createChannelAgentBindingsTable,
		migrateUsersAddBotDisclose,
		migrateMessagesAddReplyTo,
		migrateBotConfigAddAPIKey,
		migrateBotConfigAddOwnerID,
		migrateBotConfigAddVisibility,
		migrateBotConfigAddTenantName,
		migrateBotConfigAddBodyID,
		migrateChannelAgentEntriesAddAppID,
		migrateChannelAgentBindingsAddActorUID,
		migrateChannelAgentBindingsAddCanonicalUID,
		migrateMessagesAddCodeMode,
		migrateMessagesAddClientMsgID,
		migrateGroupsAddCreatedAtColumn,
		migrateGroupsBackfillCreatedAt,
		migrateGroupsCreatedAtDefault,
		migrateGroupsCreatedAtNotNull,
		migrateGroupsAddAnnouncement,
		migrateGroupMembersAddMuted,
		createUsersIndexes,
		createFriendsIndexes,
		createTopicsIndexes,
		createMessagesIndexes,
		createBotConfigIndexes,
		createGroupMembersIndexes,
		createFeedbackIndexes,
		createAuthServicesIndexes,
		createChannelAgentIndexes,
		createUpdatedAtTriggers,
	}
	for _, statement := range statements {
		if _, err := a.db.Exec(statement); err != nil {
			return fmt.Errorf("postgres schema statement failed: %w", err)
		}
	}
	return nil
}

const createUpdatedAtFunction = `
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = CURRENT_TIMESTAMP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
`

const createUsersTable = `
CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE,
    email VARCHAR(255) DEFAULT NULL,
    phone VARCHAR(32) DEFAULT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    avatar_url VARCHAR(512) DEFAULT NULL,
    account_type VARCHAR(16) NOT NULL DEFAULT 'human' CHECK (account_type IN ('human','bot','service')),
    pass_hash BYTEA NOT NULL,
    state SMALLINT NOT NULL DEFAULT 0,
    bot_disclose BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createFriendsTable = `
CREATE TABLE IF NOT EXISTS friends (
    id BIGSERIAL PRIMARY KEY,
    from_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    to_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','rejected','blocked')),
    message VARCHAR(255) DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_friend_pair UNIQUE (from_user_id, to_user_id)
);
`

const createTopicsTable = `
CREATE TABLE IF NOT EXISTS topics (
    id VARCHAR(64) PRIMARY KEY,
    type VARCHAR(16) NOT NULL DEFAULT 'p2p' CHECK (type IN ('p2p','group')),
    name VARCHAR(128) DEFAULT '',
    owner_id BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createMessagesTable = `
CREATE TABLE IF NOT EXISTS messages (
    id BIGSERIAL PRIMARY KEY,
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    from_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    msg_type VARCHAR(16) NOT NULL DEFAULT 'text' CHECK (msg_type IN ('text','image','voice','file')),
    content_blocks JSONB DEFAULT NULL,
    mode VARCHAR(20) DEFAULT 'normal',
    role VARCHAR(20) DEFAULT NULL,
    reply_to BIGINT DEFAULT NULL,
    client_msg_id VARCHAR(128) DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createBotConfigTable = `
CREATE TABLE IF NOT EXISTS bot_config (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    owner_id BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    api_endpoint VARCHAR(512) DEFAULT '',
    model VARCHAR(128) DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    config JSONB DEFAULT NULL,
    api_key VARCHAR(128) DEFAULT NULL,
    visibility VARCHAR(16) NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','private')),
    tenant_name VARCHAR(128) DEFAULT NULL,
    body_id VARCHAR(128) DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createRateLimitTable = `
CREATE TABLE IF NOT EXISTS rate_limits (
    account_type VARCHAR(16) PRIMARY KEY CHECK (account_type IN ('human','bot','service')),
    max_per_second INT NOT NULL DEFAULT 10,
    max_per_minute INT NOT NULL DEFAULT 120,
    burst_size INT NOT NULL DEFAULT 20
);
`

const createGroupsTable = `
CREATE TABLE IF NOT EXISTS "groups" (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    owner_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    avatar_url VARCHAR(512) DEFAULT NULL,
    announcement TEXT DEFAULT NULL,
    max_members INT NOT NULL DEFAULT 200,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createGroupMembersTable = `
CREATE TABLE IF NOT EXISTS group_members (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES "groups"(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(16) NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
    muted BOOLEAN NOT NULL DEFAULT FALSE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_group_user UNIQUE (group_id, user_id)
);
`

const createFeedbackReportsTable = `
CREATE TABLE IF NOT EXISTS feedback_reports (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category VARCHAR(16) NOT NULL DEFAULT 'bug' CHECK (category IN ('bug','suggestion','other')),
    title VARCHAR(160) DEFAULT '',
    description TEXT NOT NULL,
    page_url VARCHAR(1024) DEFAULT '',
    user_agent VARCHAR(512) DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'open' CHECK (status IN ('open','reviewing','resolved','closed')),
    attachments JSONB DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createAuthServicesTable = `
CREATE TABLE IF NOT EXISTS auth_services (
    id BIGSERIAL PRIMARY KEY,
    slug VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(128) NOT NULL,
    token_prefix VARCHAR(32) NOT NULL,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
    state SMALLINT NOT NULL DEFAULT 0,
    last_used_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createChannelAgentEntriesTable = `
CREATE TABLE IF NOT EXISTS channel_agent_entries (
    id BIGSERIAL PRIMARY KEY,
    scene_key VARCHAR(64) NOT NULL UNIQUE,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    last_used_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createChannelAgentBindingsTable = `
CREATE TABLE IF NOT EXISTS channel_agent_bindings (
    id BIGSERIAL PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
	channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
	channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
	actor_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
	canonical_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
	owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    entry_id BIGINT DEFAULT NULL REFERENCES channel_agent_entries(id) ON DELETE SET NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    bound_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMPTZ DEFAULT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_channel_agent_binding_identity UNIQUE (channel, channel_app_id, channel_user_id, channel_conversation_id)
);
`

const migrateUsersAddBotDisclose = `ALTER TABLE users ADD COLUMN IF NOT EXISTS bot_disclose BOOLEAN NOT NULL DEFAULT FALSE;`
const migrateMessagesAddReplyTo = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_to BIGINT DEFAULT NULL;`
const migrateBotConfigAddAPIKey = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS api_key VARCHAR(128) DEFAULT NULL;`
const migrateBotConfigAddOwnerID = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS owner_id BIGINT DEFAULT NULL;`
const migrateBotConfigAddVisibility = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS visibility VARCHAR(16) NOT NULL DEFAULT 'public';`
const migrateBotConfigAddTenantName = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS tenant_name VARCHAR(128) DEFAULT NULL;`
const migrateBotConfigAddBodyID = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS body_id VARCHAR(128) DEFAULT NULL;`
const migrateChannelAgentEntriesAddAppID = `ALTER TABLE channel_agent_entries ADD COLUMN IF NOT EXISTS channel_app_id VARCHAR(128) NOT NULL DEFAULT '';`
const migrateChannelAgentBindingsAddActorUID = `ALTER TABLE channel_agent_bindings ADD COLUMN IF NOT EXISTS actor_uid BIGINT DEFAULT NULL;`
const migrateChannelAgentBindingsAddCanonicalUID = `ALTER TABLE channel_agent_bindings ADD COLUMN IF NOT EXISTS canonical_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL;`
const migrateMessagesAddCodeMode = `
ALTER TABLE messages
  ADD COLUMN IF NOT EXISTS content_blocks JSONB DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS mode VARCHAR(20) DEFAULT 'normal',
  ADD COLUMN IF NOT EXISTS role VARCHAR(20) DEFAULT NULL;
`
const migrateMessagesAddClientMsgID = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS client_msg_id VARCHAR(128) DEFAULT NULL;`
const migrateGroupsAddCreatedAtColumn = `ALTER TABLE "groups" ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ DEFAULT NULL;`
const migrateGroupsBackfillCreatedAt = `
UPDATE "groups" g
SET created_at = COALESCE(
  g.created_at,
  (SELECT t.created_at FROM topics t WHERE t.id = 'grp_' || g.id::text),
  (SELECT MIN(gm.joined_at) FROM group_members gm WHERE gm.group_id = g.id),
  CURRENT_TIMESTAMP
)
WHERE g.created_at IS NULL;
`
const migrateGroupsCreatedAtDefault = `ALTER TABLE "groups" ALTER COLUMN created_at SET DEFAULT CURRENT_TIMESTAMP;`
const migrateGroupsCreatedAtNotNull = `ALTER TABLE "groups" ALTER COLUMN created_at SET NOT NULL;`
const migrateGroupsAddAnnouncement = `ALTER TABLE "groups" ADD COLUMN IF NOT EXISTS announcement TEXT DEFAULT NULL;`
const migrateGroupMembersAddMuted = `ALTER TABLE group_members ADD COLUMN IF NOT EXISTS muted BOOLEAN NOT NULL DEFAULT FALSE;`

const createUsersIndexes = `
CREATE UNIQUE INDEX IF NOT EXISTS uk_users_username_lower ON users (lower(username));
CREATE UNIQUE INDEX IF NOT EXISTS uk_users_email_lower ON users (lower(email)) WHERE email IS NOT NULL AND email <> '';
CREATE INDEX IF NOT EXISTS idx_users_account_type ON users (account_type);
CREATE INDEX IF NOT EXISTS idx_users_phone ON users (phone);
CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);
`
const createFriendsIndexes = `
CREATE INDEX IF NOT EXISTS idx_friends_from_status ON friends (from_user_id, status);
CREATE INDEX IF NOT EXISTS idx_friends_to_user ON friends (to_user_id, status);
CREATE INDEX IF NOT EXISTS idx_friends_status ON friends (status);
`
const createTopicsIndexes = `CREATE INDEX IF NOT EXISTS idx_topics_type ON topics (type);`
const createMessagesIndexes = `
CREATE INDEX IF NOT EXISTS idx_messages_topic ON messages (topic_id, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_topic_id ON messages (topic_id, id);
CREATE INDEX IF NOT EXISTS idx_messages_reply_to ON messages (reply_to);
CREATE UNIQUE INDEX IF NOT EXISTS uk_messages_client_msg_id ON messages (topic_id, from_uid, client_msg_id) WHERE client_msg_id IS NOT NULL AND client_msg_id <> '';
`
const createBotConfigIndexes = `
CREATE UNIQUE INDEX IF NOT EXISTS uk_bot_config_api_key ON bot_config (api_key) WHERE api_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bot_config_owner ON bot_config (owner_id);
`
const createGroupMembersIndexes = `
CREATE INDEX IF NOT EXISTS idx_gm_user ON group_members (user_id);
CREATE INDEX IF NOT EXISTS idx_gm_group_joined ON group_members (group_id, joined_at);
`
const createFeedbackIndexes = `
CREATE INDEX IF NOT EXISTS idx_feedback_user_created ON feedback_reports (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_feedback_status_created ON feedback_reports (status, created_at);
`

const createAuthServicesIndexes = `
CREATE INDEX IF NOT EXISTS idx_auth_services_state ON auth_services (state);
CREATE INDEX IF NOT EXISTS idx_auth_services_slug ON auth_services (slug);
`

const createChannelAgentIndexes = `
CREATE INDEX IF NOT EXISTS idx_channel_agent_entries_owner_agent ON channel_agent_entries (owner_uid, agent_uid, channel, channel_app_id, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_lookup ON channel_agent_bindings (channel, channel_app_id, channel_user_id, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_agent ON channel_agent_bindings (owner_uid, agent_uid, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_actor_agent ON channel_agent_bindings (channel, channel_app_id, actor_uid, agent_uid, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_actor_any ON channel_agent_bindings (actor_uid, agent_uid, status);
`

const createUpdatedAtTriggers = `
CREATE OR REPLACE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_friends_updated_at BEFORE UPDATE ON friends
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_bot_config_updated_at BEFORE UPDATE ON bot_config
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_feedback_reports_updated_at BEFORE UPDATE ON feedback_reports
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_auth_services_updated_at BEFORE UPDATE ON auth_services
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_agent_entries_updated_at BEFORE UPDATE ON channel_agent_entries
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_agent_bindings_updated_at BEFORE UPDATE ON channel_agent_bindings
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
`

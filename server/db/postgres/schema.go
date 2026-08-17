package postgres

import (
	"fmt"
	"strings"
)

// CreateSchema creates all required database tables and idempotent indexes.
func (a *Adapter) CreateSchema() error {
	statements := []string{
		createUpdatedAtFunction,
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
		createBotSkillMutationsTable,
		createBotSkillMutationsActiveIndex,
		createRateLimitTable,
		createGroupsTable,
		createGroupMembersTable,
		createGroupInviteRequestsTable,
		createFeedbackReportsTable,
		createAuthServicesTable,
		createCommercialPlansTable,
		migrateCommercialPlansAddSaleFields,
		migrateCommercialPlansAddInternalQuota,
		createCommercialInviteCodesTable,
		createCommercialEntitlementsTable,
		createCommercialQuotaGrantsTable,
		createCommercialQuotaLedgerTable,
		createCommercialOrdersTable,
		createCommercialOrderRequestIDsTable,
		createCommercialPaymentEventsTable,
		createCommercialManagedRelayBudgetsTable,
		createCommercialOperatorEventsTable,
		migrateCommercialRefundColumns,
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
		migrateChannelNativeGroupsAddLastEventID,
		migrateChannelNativeGroupsAddLastEventTime,
		migrateChannelNativeGroupsAddLastEventClaimedAt,
		migrateGroupsAddKind,
		migrateGroupsBackfillChannelManagedKind,
		migrateUsersAddBotDisclose,
		migrateMessagesAddReplyTo,
		migrateBotConfigAddAPIKey,
		migrateBotConfigAddOwnerID,
		migrateBotConfigAddVisibility,
		migrateBotConfigAddSkillsVisibility,
		migrateBotConfigAddTenantName,
		migrateBotConfigAddBodyID,
		migrateBotConfigAddRole,
		migrateBotConfigAddDescription,
		migrateBotConfigAddArtifactUploadPolicy,
		migrateBotConfigAddSkillMutationMode,
		migrateChannelAgentEntriesAddAppID,
		migrateChannelAgentEntriesAddAccessMode,
		migrateChannelAgentEntriesDefaultAccessMode,
		migrateChannelAgentBindingsAddActorUID,
		migrateChannelAgentBindingsAddCanonicalUID,
		migrateChannelAgentBindingsAddDeviceAccessEnabled,
		migrateChannelAgentBindingsEnableCanonicalDeviceAccess,
		migrateChannelGroupBindingsAddSelectedAt,
		migrateChannelGroupBindingsBackfillSelectedAt,
		migrateChannelGroupBindingsSelectedAtDefault,
		migrateChannelGroupBindingsSelectedAtNotNull,
		migrateMessagesAddCodeMode,
		migrateMessagesAddClientMsgID,
		migrateMessagesAddMetadata,
		migrateGroupsAddCreatedAtColumn,
		migrateGroupsBackfillCreatedAt,
		migrateGroupsCreatedAtDefault,
		migrateGroupsCreatedAtNotNull,
		migrateGroupsAddAnnouncement,
		migrateGroupMembersAddMuted,
		migrateConversationTaskStatusSourcesAddEventUpdatedAt,
		createUsersIndexes,
		createFriendsIndexes,
		createTopicsIndexes,
		createProjectIndexes,
		createMessagesIndexes,
		createConversationTaskStatusIndexes,
		createBotConfigIndexes,
		createGroupMembersIndexes,
		createFeedbackIndexes,
		createAuthServicesIndexes,
		createCommercialIndexes,
		createChannelAgentIndexes,
		createUpdatedAtTriggers,
		createSchemaMigrationsTable,
		createSchemaMigrationsBaseline,
	}
	for _, statement := range statements {
		if _, err := a.db.Exec(statement); err != nil {
			return fmt.Errorf("postgres schema statement failed: %w", err)
		}
	}
	if err := a.ensureChannelAgentBindingUniqueIncludesAgent(); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) ensureChannelAgentBindingUniqueIncludesAgent() error {
	row := a.db.QueryRow(
		`SELECT COALESCE(string_agg(att.attname, ',' ORDER BY cols.ord), '')
		 FROM pg_constraint con
		 JOIN unnest(con.conkey) WITH ORDINALITY AS cols(attnum, ord) ON true
		 JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = cols.attnum
		 WHERE con.conrelid = 'channel_agent_bindings'::regclass
		   AND con.conname = 'uk_channel_agent_binding_identity'`,
	)
	var columns string
	if err := row.Scan(&columns); err != nil {
		return fmt.Errorf("inspect channel agent binding unique constraint: %w", err)
	}
	expected := "channel,channel_app_id,channel_user_id,channel_conversation_id,agent_uid"
	if strings.TrimSpace(columns) == expected {
		return nil
	}
	if _, err := a.db.Exec(migrateChannelAgentBindingsUniqueIncludesAgent); err != nil {
		return fmt.Errorf("migrate channel agent binding unique constraint: %w", err)
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

const createPushSubscriptionsTable = `
CREATE TABLE IF NOT EXISTS push_subscriptions (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL,
    endpoint VARCHAR(512) NOT NULL UNIQUE,
    p256dh VARCHAR(256) NOT NULL,
    auth VARCHAR(128) NOT NULL,
    registration_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_push_subscriptions_uid FOREIGN KEY (uid) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_push_subscriptions_uid ON push_subscriptions (uid);
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

const createProjectsTable = `
CREATE TABLE IF NOT EXISTS projects (
    id BIGSERIAL PRIMARY KEY,
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_projects_owner_name UNIQUE (owner_uid, name)
);
`

const createProjectTopicsTable = `
CREATE TABLE IF NOT EXISTS project_topics (
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (owner_uid, topic_id)
);
`

const createConversationTitlesTable = `
CREATE TABLE IF NOT EXISTS conversation_titles (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    title VARCHAR(80) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, topic_id)
);
`

const createConversationNotificationMutesTable = `
CREATE TABLE IF NOT EXISTS conversation_notification_mutes (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- A P2P topic is created on its first message, while a user may mute the
    -- visible conversation before then.
    topic_id VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, topic_id)
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
    metadata JSONB DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createConversationTaskStatusesTable = `
CREATE TABLE IF NOT EXISTS conversation_task_statuses (
    topic_id VARCHAR(64) PRIMARY KEY REFERENCES topics(id) ON DELETE CASCADE,
    run_id VARCHAR(128) DEFAULT '',
    state VARCHAR(20) NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','running','completed','failed','cancelled','stale','waiting')),
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    source_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createConversationTaskStatusSourcesTable = `
CREATE TABLE IF NOT EXISTS conversation_task_status_sources (
    topic_id VARCHAR(64) NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    source_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    run_id VARCHAR(128) DEFAULT '',
    state VARCHAR(20) NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','running','completed','failed','cancelled','stale','waiting')),
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ DEFAULT NULL,
    event_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (topic_id, source_uid)
);
`

const migrateConversationTaskStatusSourcesAddEventUpdatedAt = `
ALTER TABLE conversation_task_status_sources
ADD COLUMN IF NOT EXISTS event_updated_at TIMESTAMPTZ;
UPDATE conversation_task_status_sources
SET event_updated_at = updated_at
WHERE event_updated_at IS NULL;
ALTER TABLE conversation_task_status_sources
ALTER COLUMN event_updated_at SET DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE conversation_task_status_sources
ALTER COLUMN event_updated_at SET NOT NULL;
`

const createBotConnectionGenerationsTable = `
CREATE TABLE IF NOT EXISTS bot_connection_generations (
    bot_uid BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    generation BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
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
	 skills_visibility VARCHAR(16) NOT NULL DEFAULT 'owner' CHECK (skills_visibility IN ('owner','authorized','public')),
    tenant_name VARCHAR(128) DEFAULT NULL,
    body_id VARCHAR(128) DEFAULT NULL,
    role VARCHAR(32) NOT NULL DEFAULT 'general',
    description TEXT NOT NULL DEFAULT '',
    artifact_upload_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    skill_mutation_mode VARCHAR(16) NOT NULL DEFAULT 'owner_only'
        CONSTRAINT bot_config_skill_mutation_mode_check CHECK (skill_mutation_mode IN ('owner_only','shared_live')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createBotSkillMutationsTable = `
CREATE TABLE IF NOT EXISTS bot_skill_mutations (
    id BIGSERIAL PRIMARY KEY,
    bot_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    local_skill_id VARCHAR(128) NOT NULL,
    actor_user_uid BIGINT NOT NULL,
    source_topic_id VARCHAR(255) NOT NULL,
    source_message_id BIGINT NOT NULL,
    runtime_body_id VARCHAR(128) NOT NULL,
    client_request_id VARCHAR(128) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    operation VARCHAR(16) NOT NULL CHECK (operation IN ('create','replace','rollback')),
    candidate_content_hash CHAR(64) NOT NULL,
    expected_definition_revision BIGINT NOT NULL,
    expected_previous_content_hash CHAR(64) DEFAULT NULL,
    before_reference JSONB DEFAULT NULL,
    after_reference JSONB DEFAULT NULL,
    git_commit_sha VARCHAR(64) DEFAULT NULL,
    definition_revision BIGINT DEFAULT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'validating'
        CHECK (status IN ('validating','version_ready','definition_committed','activation_pending','active','rejected','compensation_pending','rolled_back')),
    error_code VARCHAR(64) DEFAULT NULL,
    error_summary VARCHAR(512) DEFAULT NULL,
    rollback_of BIGINT DEFAULT NULL REFERENCES bot_skill_mutations(id) ON DELETE SET NULL,
    lease_generation BIGINT NOT NULL DEFAULT 1,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    activated_at TIMESTAMPTZ DEFAULT NULL,
    CONSTRAINT uk_bot_skill_mutations_request UNIQUE (actor_user_uid, bot_uid, client_request_id)
);
CREATE INDEX IF NOT EXISTS idx_bot_skill_mutations_audit
    ON bot_skill_mutations (bot_uid, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_bot_skill_mutations_source
    ON bot_skill_mutations (source_topic_id, source_message_id);
`

const createBotSkillMutationsActiveIndex = `
CREATE UNIQUE INDEX IF NOT EXISTS uk_bot_skill_mutations_active
    ON bot_skill_mutations (bot_uid)
    WHERE status IN ('validating','version_ready','definition_committed','activation_pending','compensation_pending');
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
    group_kind VARCHAR(32) NOT NULL DEFAULT 'standard',
    avatar_url VARCHAR(512) DEFAULT NULL,
    announcement TEXT DEFAULT NULL,
    max_members INT NOT NULL DEFAULT 200,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const migrateGroupsAddKind = `ALTER TABLE "groups" ADD COLUMN IF NOT EXISTS group_kind VARCHAR(32) NOT NULL DEFAULT 'standard';`

const migrateGroupsBackfillChannelManagedKind = `
UPDATE "groups" g
SET group_kind = 'channel_managed'
FROM channel_native_groups cng
WHERE cng.group_id = g.id AND g.group_kind <> 'channel_managed';
`

const migrateChannelNativeGroupsAddLastEventID = `ALTER TABLE channel_native_groups ADD COLUMN IF NOT EXISTS last_event_id VARCHAR(128) NOT NULL DEFAULT '';`

const migrateChannelNativeGroupsAddLastEventTime = `ALTER TABLE channel_native_groups ADD COLUMN IF NOT EXISTS last_event_time BIGINT NOT NULL DEFAULT 0;`

const migrateChannelNativeGroupsAddLastEventClaimedAt = `ALTER TABLE channel_native_groups ADD COLUMN IF NOT EXISTS last_event_claimed_at BIGINT NOT NULL DEFAULT 0;`

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

const createGroupInviteRequestsTable = `
CREATE TABLE IF NOT EXISTS group_invite_requests (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES "groups"(id) ON DELETE CASCADE,
    inviter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invitee_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    resolver_id BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_group_invite_request UNIQUE (group_id, invitee_id)
);
CREATE INDEX IF NOT EXISTS idx_group_invite_requests_pending
    ON group_invite_requests (group_id, status, created_at);
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

const createCommercialPlansTable = `
CREATE TABLE IF NOT EXISTS commercial_plans (
    id BIGSERIAL PRIMARY KEY,
    slug VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
	price_fen BIGINT NOT NULL DEFAULT 0,
	currency VARCHAR(8) NOT NULL DEFAULT 'CNY',
	sale_state VARCHAR(16) NOT NULL DEFAULT 'hidden',
	purchase_limit INT NOT NULL DEFAULT 0,
    monthly_budget_cny NUMERIC(14,6) NOT NULL DEFAULT 0,
    model_budgets JSONB NOT NULL DEFAULT '{}'::jsonb,
    internal_quota_tokens BIGINT NOT NULL DEFAULT 0,
    duration_days INT NOT NULL DEFAULT 30,
    state SMALLINT NOT NULL DEFAULT 0,
    sort_order INT NOT NULL DEFAULT 100,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_plans_state CHECK (state IN (0, 1)),
	CONSTRAINT chk_commercial_plans_price CHECK (price_fen >= 0),
	CONSTRAINT chk_commercial_plans_sale_state CHECK (sale_state IN ('hidden','test','public')),
	CONSTRAINT chk_commercial_plans_purchase_limit CHECK (purchase_limit >= 0),
    CONSTRAINT chk_commercial_plans_internal_quota_tokens CHECK (internal_quota_tokens >= 0),
    CONSTRAINT chk_commercial_plans_duration CHECK (duration_days > 0),
    CONSTRAINT chk_commercial_plans_budget CHECK (monthly_budget_cny >= 0)
);
`

const migrateCommercialPlansAddSaleFields = `
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS price_fen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS currency VARCHAR(8) NOT NULL DEFAULT 'CNY';
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS sale_state VARCHAR(16) NOT NULL DEFAULT 'hidden';
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS purchase_limit INT NOT NULL DEFAULT 0;
DO $$ BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_price') THEN
		ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_price CHECK (price_fen >= 0);
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_sale_state') THEN
		ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_sale_state CHECK (sale_state IN ('hidden','test','public'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_purchase_limit') THEN
		ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_purchase_limit CHECK (purchase_limit >= 0);
	END IF;
END $$;
`

const migrateCommercialPlansAddInternalQuota = `
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS internal_quota_tokens BIGINT NOT NULL DEFAULT 0;
DO $$ BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_internal_quota_tokens') THEN
		ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_internal_quota_tokens CHECK (internal_quota_tokens >= 0);
	END IF;
END $$;
`

const createCommercialInviteCodesTable = `
CREATE TABLE IF NOT EXISTS commercial_invite_codes (
    id BIGSERIAL PRIMARY KEY,
    code VARCHAR(64) NOT NULL UNIQUE,
    plan_id BIGINT NOT NULL REFERENCES commercial_plans(id) ON DELETE RESTRICT,
    max_redemptions INT NOT NULL DEFAULT 1,
    redeemed_count INT NOT NULL DEFAULT 0,
    state SMALLINT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_by_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_invites_state CHECK (state IN (0, 1)),
    CONSTRAINT chk_commercial_invites_redemptions CHECK (max_redemptions > 0 AND redeemed_count >= 0 AND redeemed_count <= max_redemptions)
);
`

const createCommercialEntitlementsTable = `
CREATE TABLE IF NOT EXISTS commercial_entitlements (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id BIGINT NOT NULL REFERENCES commercial_plans(id) ON DELETE RESTRICT,
    source VARCHAR(32) NOT NULL DEFAULT 'manual',
    source_ref VARCHAR(128) NOT NULL DEFAULT '',
    state VARCHAR(16) NOT NULL DEFAULT 'active',
    starts_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_entitlements_state CHECK (state IN ('active','expired','revoked'))
);
`

const createCommercialQuotaGrantsTable = `
CREATE TABLE IF NOT EXISTS commercial_quota_grants (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id BIGINT DEFAULT NULL REFERENCES commercial_plans(id) ON DELETE SET NULL,
    invite_code_id BIGINT DEFAULT NULL REFERENCES commercial_invite_codes(id) ON DELETE SET NULL,
    grant_type VARCHAR(32) NOT NULL DEFAULT 'manual',
    model VARCHAR(128) NOT NULL DEFAULT '*',
    amount_cny NUMERIC(14,6) NOT NULL,
	reset_duration VARCHAR(16) NOT NULL DEFAULT '1M',
	effective_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	expires_at TIMESTAMPTZ DEFAULT NULL,
	source_ref VARCHAR(128) NOT NULL DEFAULT '',
	revoked_at TIMESTAMPTZ DEFAULT NULL,
	note TEXT NOT NULL DEFAULT '',
    operator_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_commercial_quota_grants_amount CHECK (amount_cny > 0)
);
`

const createCommercialQuotaLedgerTable = `
CREATE TABLE IF NOT EXISTS commercial_quota_ledger (
    id BIGSERIAL PRIMARY KEY,
    uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    model VARCHAR(128) NOT NULL DEFAULT '*',
    amount_cny NUMERIC(14,6) NOT NULL,
    entry_type VARCHAR(32) NOT NULL,
    source_type VARCHAR(32) NOT NULL,
    source_id BIGINT DEFAULT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

// Keep the application-managed schema path compatible with databases created
// before refund support. SQL migration files are not executed by CreateSchema.
const migrateCommercialRefundColumns = `
ALTER TABLE commercial_orders
	ADD COLUMN IF NOT EXISTS refund_request_no VARCHAR(64) NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS refunded_at TIMESTAMPTZ DEFAULT NULL;

ALTER TABLE commercial_quota_grants
	ADD COLUMN IF NOT EXISTS source_ref VARCHAR(128) NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ DEFAULT NULL;

UPDATE commercial_quota_grants
SET source_ref = substring(note FROM 7)
WHERE grant_type = 'order' AND source_ref = '' AND note LIKE 'order %';

UPDATE commercial_quota_grants
SET source_ref = substring(note FROM 8)
WHERE grant_type = 'invite' AND source_ref = '' AND note LIKE 'invite %';
`

const createCommercialOrdersTable = `
CREATE TABLE IF NOT EXISTS commercial_orders (
	id BIGSERIAL PRIMARY KEY,
	order_no VARCHAR(40) NOT NULL UNIQUE,
	uid BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
	plan_id BIGINT NOT NULL REFERENCES commercial_plans(id) ON DELETE RESTRICT,
	plan_slug VARCHAR(64) NOT NULL,
	plan_name VARCHAR(128) NOT NULL,
	plan_description TEXT NOT NULL DEFAULT '',
	plan_duration_days INT NOT NULL,
	plan_monthly_budget_cny NUMERIC(14,6) NOT NULL DEFAULT 0,
	plan_model_budgets JSONB NOT NULL DEFAULT '{}'::jsonb,
	amount_fen BIGINT NOT NULL,
	currency VARCHAR(8) NOT NULL DEFAULT 'CNY',
	channel VARCHAR(32) NOT NULL,
	status VARCHAR(24) NOT NULL DEFAULT 'created',
	provider_trade_no VARCHAR(128) NOT NULL DEFAULT '',
	checkout_url TEXT NOT NULL DEFAULT '',
	client_request_id VARCHAR(64) NOT NULL,
	expires_at TIMESTAMPTZ DEFAULT NULL,
	paid_at TIMESTAMPTZ DEFAULT NULL,
	fulfilled_at TIMESTAMPTZ DEFAULT NULL,
	closed_at TIMESTAMPTZ DEFAULT NULL,
	refund_request_no VARCHAR(64) NOT NULL DEFAULT '',
	refunded_at TIMESTAMPTZ DEFAULT NULL,
	last_error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CONSTRAINT chk_commercial_orders_amount CHECK (amount_fen > 0),
	CONSTRAINT chk_commercial_orders_status CHECK (status IN ('created','pending','paid','fulfilled','closed','failed','refunding','refunded'))
);
`

const createCommercialOrderRequestIDsTable = `
CREATE TABLE IF NOT EXISTS commercial_order_request_ids (
	uid BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
	client_request_id VARCHAR(64) NOT NULL,
	order_no VARCHAR(40) NOT NULL REFERENCES commercial_orders(order_no) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY(uid, client_request_id)
);

INSERT INTO commercial_order_request_ids(uid, client_request_id, order_no)
SELECT uid, client_request_id, order_no
FROM commercial_orders
ON CONFLICT(uid, client_request_id) DO NOTHING;
`

const createCommercialPaymentEventsTable = `
CREATE TABLE IF NOT EXISTS commercial_payment_events (
	id BIGSERIAL PRIMARY KEY,
	channel VARCHAR(32) NOT NULL,
	event_id VARCHAR(160) NOT NULL,
	order_no VARCHAR(40) NOT NULL REFERENCES commercial_orders(order_no) ON DELETE RESTRICT,
	provider_trade_no VARCHAR(128) NOT NULL DEFAULT '',
	event_type VARCHAR(32) NOT NULL DEFAULT 'payment_success',
	payload_hash VARCHAR(64) NOT NULL DEFAULT '',
	status VARCHAR(16) NOT NULL DEFAULT 'processed',
	error_message TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CONSTRAINT chk_commercial_payment_events_status CHECK (status IN ('processed','rejected','ignored')),
	UNIQUE(channel, event_id)
);
`

const createCommercialManagedRelayBudgetsTable = `
CREATE TABLE IF NOT EXISTS commercial_managed_relay_budgets (
	uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	model VARCHAR(128) NOT NULL,
	provider VARCHAR(128) NOT NULL,
	allowed_models JSONB NOT NULL DEFAULT '[]'::jsonb,
	max_limit NUMERIC(14,6) NOT NULL,
	reset_duration VARCHAR(16) NOT NULL DEFAULT '1M',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY(uid, model, provider, allowed_models)
);
`

const createCommercialOperatorEventsTable = `
CREATE TABLE IF NOT EXISTS commercial_operator_events (
	id BIGSERIAL PRIMARY KEY,
	service VARCHAR(128) NOT NULL,
	action VARCHAR(128) NOT NULL,
	target_type VARCHAR(64) NOT NULL DEFAULT '',
	target_ref VARCHAR(160) NOT NULL DEFAULT '',
	status_code INT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createChannelAgentEntriesTable = `
CREATE TABLE IF NOT EXISTS channel_agent_entries (
    id BIGSERIAL PRIMARY KEY,
    scene_key VARCHAR(64) NOT NULL UNIQUE,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    access_mode VARCHAR(32) NOT NULL DEFAULT 'approval_required',
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    last_used_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createChannelAgentAccessRequestsTable = `
CREATE TABLE IF NOT EXISTS channel_agent_access_requests (
    id BIGSERIAL PRIMARY KEY,
    entry_id BIGINT NOT NULL REFERENCES channel_agent_entries(id) ON DELETE CASCADE,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    actor_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    reviewed_by_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    reviewed_at TIMESTAMPTZ DEFAULT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_channel_agent_access_identity UNIQUE (entry_id, channel, channel_app_id, channel_user_id, channel_conversation_id)
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
	device_access_enabled BOOLEAN NOT NULL DEFAULT FALSE,
	owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    entry_id BIGINT DEFAULT NULL REFERENCES channel_agent_entries(id) ON DELETE SET NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    bound_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMPTZ DEFAULT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_channel_agent_binding_identity UNIQUE (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid)
);
`

const createChannelAgentRoutesTable = `
CREATE TABLE IF NOT EXISTS channel_agent_routes (
    id BIGSERIAL PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    actor_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    agent_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source VARCHAR(32) NOT NULL DEFAULT '',
    selected_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMPTZ DEFAULT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_channel_agent_route_identity UNIQUE (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type)
);
`

const createChannelRouteSelectionLocksTable = `
CREATE TABLE IF NOT EXISTS channel_route_selection_locks (
    id BIGSERIAL PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_channel_route_selection_lock UNIQUE (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type)
);
`

const createChannelIdentityMobileLinksTable = `
CREATE TABLE IF NOT EXISTS channel_identity_mobile_links (
    id BIGSERIAL PRIMARY KEY,
    scene_key VARCHAR(64) NOT NULL UNIQUE,
    entry_id BIGINT NOT NULL REFERENCES channel_agent_entries(id) ON DELETE CASCADE,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    canonical_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createChannelGroupMobileLinksTable = `
CREATE TABLE IF NOT EXISTS channel_group_mobile_links (
    id BIGSERIAL PRIMARY KEY,
    scene_key VARCHAR(64) NOT NULL UNIQUE,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    canonical_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES "groups"(id) ON DELETE CASCADE,
    topic_id VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createChannelGroupBindingsTable = `
CREATE TABLE IF NOT EXISTS channel_group_bindings (
    id BIGSERIAL PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_user_id VARCHAR(128) NOT NULL,
    channel_conversation_id VARCHAR(128) NOT NULL DEFAULT '',
    channel_conversation_type VARCHAR(32) NOT NULL DEFAULT 'p2p',
    actor_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    canonical_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES "groups"(id) ON DELETE CASCADE,
	topic_id VARCHAR(128) NOT NULL,
	status VARCHAR(16) NOT NULL DEFAULT 'active',
	bound_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	selected_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_used_at TIMESTAMPTZ DEFAULT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CONSTRAINT uk_channel_group_binding_identity UNIQUE (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type)
);
`

const createChannelNativeGroupsTable = `
CREATE TABLE IF NOT EXISTS channel_native_groups (
    id BIGSERIAL PRIMARY KEY,
    channel VARCHAR(32) NOT NULL,
    channel_app_id VARCHAR(128) NOT NULL DEFAULT '',
    tenant_key VARCHAR(128) NOT NULL DEFAULT '',
    conversation_id VARCHAR(256) NOT NULL,
    conversation_name VARCHAR(255) NOT NULL DEFAULT '',
    operator_channel_user_id VARCHAR(256) NOT NULL DEFAULT '',
    operator_actor_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    canonical_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    group_id BIGINT DEFAULT NULL REFERENCES "groups"(id) ON DELETE SET NULL,
    topic_id VARCHAR(128) NOT NULL DEFAULT '',
    source_kind VARCHAR(32) NOT NULL DEFAULT '',
    source_group_id BIGINT DEFAULT NULL REFERENCES "groups"(id) ON DELETE SET NULL,
    source_agent_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','disconnected')),
	last_event_id VARCHAR(128) NOT NULL DEFAULT '',
	last_event_time BIGINT NOT NULL DEFAULT 0,
	last_event_claimed_at BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uk_channel_native_group_identity UNIQUE (channel, channel_app_id, tenant_key, conversation_id)
);
`

const createWeixinClawBotTokensTable = `
CREATE TABLE IF NOT EXISTS weixin_clawbot_tokens (
    id BIGSERIAL PRIMARY KEY,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    bot_token TEXT NOT NULL,
    token_last4 VARCHAR(8) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ilink_bot_id VARCHAR(128) NOT NULL DEFAULT '',
    ilink_user_id VARCHAR(128) NOT NULL DEFAULT '',
    base_url TEXT NOT NULL DEFAULT '',
    source_scene_key VARCHAR(64) NOT NULL DEFAULT '',
    get_updates_buf TEXT NOT NULL DEFAULT '',
    context_tokens JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_poll_at TIMESTAMPTZ DEFAULT NULL,
    last_used_at TIMESTAMPTZ DEFAULT NULL,
    last_error_at TIMESTAMPTZ DEFAULT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

const createSchemaMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT NOT NULL PRIMARY KEY,
    dirty BOOLEAN NOT NULL
);
`

const createSchemaMigrationsBaseline = `
INSERT INTO schema_migrations (version, dirty)
SELECT 1, FALSE
WHERE NOT EXISTS (SELECT 1 FROM schema_migrations);
`

const migrateUsersAddBotDisclose = `ALTER TABLE users ADD COLUMN IF NOT EXISTS bot_disclose BOOLEAN NOT NULL DEFAULT FALSE;`
const migrateMessagesAddReplyTo = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_to BIGINT DEFAULT NULL;`
const migrateBotConfigAddAPIKey = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS api_key VARCHAR(128) DEFAULT NULL;`
const migrateBotConfigAddOwnerID = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS owner_id BIGINT DEFAULT NULL;`
const migrateBotConfigAddVisibility = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS visibility VARCHAR(16) NOT NULL DEFAULT 'public';`
const migrateBotConfigAddSkillsVisibility = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS skills_visibility VARCHAR(16) NOT NULL DEFAULT 'owner';`
const migrateBotConfigAddTenantName = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS tenant_name VARCHAR(128) DEFAULT NULL;`
const migrateBotConfigAddBodyID = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS body_id VARCHAR(128) DEFAULT NULL;`
const migrateBotConfigAddRole = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS role VARCHAR(32) NOT NULL DEFAULT 'general';`
const migrateBotConfigAddDescription = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';`
const migrateBotConfigAddArtifactUploadPolicy = `ALTER TABLE bot_config ADD COLUMN IF NOT EXISTS artifact_upload_enabled BOOLEAN NOT NULL DEFAULT TRUE;`
const migrateBotConfigAddSkillMutationMode = `
ALTER TABLE bot_config
    ADD COLUMN IF NOT EXISTS skill_mutation_mode VARCHAR(16) NOT NULL DEFAULT 'owner_only';
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'bot_config_skill_mutation_mode_check'
          AND conrelid = 'bot_config'::regclass
    ) THEN
        ALTER TABLE bot_config
            ADD CONSTRAINT bot_config_skill_mutation_mode_check
            CHECK (skill_mutation_mode IN ('owner_only','shared_live'));
    END IF;
END $$;
`
const migrateChannelAgentEntriesAddAppID = `ALTER TABLE channel_agent_entries ADD COLUMN IF NOT EXISTS channel_app_id VARCHAR(128) NOT NULL DEFAULT '';`
const migrateChannelAgentEntriesAddAccessMode = `ALTER TABLE channel_agent_entries ADD COLUMN IF NOT EXISTS access_mode VARCHAR(32) NOT NULL DEFAULT 'approval_required';`
const migrateChannelAgentEntriesDefaultAccessMode = `ALTER TABLE channel_agent_entries ALTER COLUMN access_mode SET DEFAULT 'approval_required';`
const migrateChannelAgentBindingsAddActorUID = `ALTER TABLE channel_agent_bindings ADD COLUMN IF NOT EXISTS actor_uid BIGINT DEFAULT NULL;`
const migrateChannelAgentBindingsAddCanonicalUID = `ALTER TABLE channel_agent_bindings ADD COLUMN IF NOT EXISTS canonical_uid BIGINT DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL;`
const migrateChannelAgentBindingsAddDeviceAccessEnabled = `ALTER TABLE channel_agent_bindings ADD COLUMN IF NOT EXISTS device_access_enabled BOOLEAN NOT NULL DEFAULT FALSE;`

const migrateChannelAgentBindingsEnableCanonicalDeviceAccess = `
UPDATE channel_agent_bindings
SET device_access_enabled = TRUE, updated_at = CURRENT_TIMESTAMP
WHERE status = 'active' AND canonical_uid IS NOT NULL AND canonical_uid > 0 AND device_access_enabled = FALSE;
`
const migrateChannelGroupBindingsAddSelectedAt = `ALTER TABLE channel_group_bindings ADD COLUMN IF NOT EXISTS selected_at TIMESTAMPTZ DEFAULT NULL;`
const migrateChannelGroupBindingsBackfillSelectedAt = `UPDATE channel_group_bindings SET selected_at = COALESCE(bound_at, updated_at, CURRENT_TIMESTAMP) WHERE selected_at IS NULL;`
const migrateChannelGroupBindingsSelectedAtDefault = `ALTER TABLE channel_group_bindings ALTER COLUMN selected_at SET DEFAULT CURRENT_TIMESTAMP;`
const migrateChannelGroupBindingsSelectedAtNotNull = `ALTER TABLE channel_group_bindings ALTER COLUMN selected_at SET NOT NULL;`
const migrateChannelAgentBindingsUniqueIncludesAgent = `
ALTER TABLE channel_agent_bindings DROP CONSTRAINT IF EXISTS uk_channel_agent_binding_identity;
ALTER TABLE channel_agent_bindings ADD CONSTRAINT uk_channel_agent_binding_identity UNIQUE (channel, channel_app_id, channel_user_id, channel_conversation_id, agent_uid);
`
const migrateMessagesAddCodeMode = `
ALTER TABLE messages
  ADD COLUMN IF NOT EXISTS content_blocks JSONB DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS mode VARCHAR(20) DEFAULT 'normal',
  ADD COLUMN IF NOT EXISTS role VARCHAR(20) DEFAULT NULL;
`
const migrateMessagesAddClientMsgID = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS client_msg_id VARCHAR(128) DEFAULT NULL;`
const migrateMessagesAddMetadata = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT NULL;`
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
const createProjectIndexes = `
CREATE INDEX IF NOT EXISTS idx_projects_owner_updated ON projects (owner_uid, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_project_topics_project ON project_topics (project_id, created_at DESC);
`
const createMessagesIndexes = `
CREATE INDEX IF NOT EXISTS idx_messages_topic ON messages (topic_id, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_topic_id ON messages (topic_id, id);
CREATE INDEX IF NOT EXISTS idx_messages_reply_to ON messages (reply_to);
CREATE UNIQUE INDEX IF NOT EXISTS uk_messages_client_msg_id ON messages (topic_id, from_uid, client_msg_id) WHERE client_msg_id IS NOT NULL AND client_msg_id <> '';
`
const createConversationTaskStatusIndexes = `
CREATE INDEX IF NOT EXISTS idx_conversation_task_statuses_updated_at ON conversation_task_statuses (updated_at);
CREATE INDEX IF NOT EXISTS idx_conversation_task_statuses_state ON conversation_task_statuses (state);
CREATE INDEX IF NOT EXISTS idx_conversation_task_status_sources_updated_at ON conversation_task_status_sources (updated_at);
CREATE INDEX IF NOT EXISTS idx_conversation_task_status_sources_state ON conversation_task_status_sources (state);
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

const createCommercialIndexes = `
CREATE INDEX IF NOT EXISTS idx_commercial_plans_state_sort ON commercial_plans (state, sort_order, id);
CREATE INDEX IF NOT EXISTS idx_commercial_invite_codes_plan ON commercial_invite_codes (plan_id, state);
CREATE INDEX IF NOT EXISTS idx_commercial_invite_codes_code_lower ON commercial_invite_codes (lower(code));
CREATE INDEX IF NOT EXISTS idx_commercial_entitlements_uid_state ON commercial_entitlements (uid, state, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_entitlements_invite_once
    ON commercial_entitlements (uid, source, source_ref)
    WHERE source = 'invite';
CREATE INDEX IF NOT EXISTS idx_commercial_quota_grants_uid_model ON commercial_quota_grants (uid, model, effective_at);
CREATE INDEX IF NOT EXISTS idx_commercial_quota_grants_expires ON commercial_quota_grants (expires_at);
CREATE INDEX IF NOT EXISTS idx_commercial_quota_grants_source ON commercial_quota_grants (grant_type, source_ref);
CREATE INDEX IF NOT EXISTS idx_commercial_quota_ledger_uid_created ON commercial_quota_ledger (uid, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_refund_ledger_grant
	ON commercial_quota_ledger (source_type, source_id, entry_type)
	WHERE source_type = 'refund' AND entry_type = 'revoke';
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_entitlements_order_once
	ON commercial_entitlements (uid, source, source_ref)
	WHERE source = 'order';
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_entitlements_trial_once
	ON commercial_entitlements (uid, source)
	WHERE source = 'trial';
CREATE UNIQUE INDEX IF NOT EXISTS uk_commercial_orders_uid_request ON commercial_orders (uid, client_request_id);
CREATE INDEX IF NOT EXISTS idx_commercial_orders_uid_created ON commercial_orders (uid, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_commercial_orders_status_expires ON commercial_orders (status, expires_at);
CREATE INDEX IF NOT EXISTS idx_commercial_order_request_ids_order ON commercial_order_request_ids (order_no);
CREATE INDEX IF NOT EXISTS idx_commercial_payment_events_order ON commercial_payment_events (order_no, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_commercial_managed_relay_uid ON commercial_managed_relay_budgets (uid);
CREATE INDEX IF NOT EXISTS idx_commercial_operator_events_created ON commercial_operator_events (created_at DESC);
`

const createChannelAgentIndexes = `
CREATE INDEX IF NOT EXISTS idx_channel_agent_entries_owner_agent ON channel_agent_entries (owner_uid, agent_uid, channel, channel_app_id, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_lookup ON channel_agent_bindings (channel, channel_app_id, channel_user_id, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_agent ON channel_agent_bindings (owner_uid, agent_uid, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_actor_agent ON channel_agent_bindings (channel, channel_app_id, actor_uid, agent_uid, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_bindings_actor_any ON channel_agent_bindings (actor_uid, agent_uid, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_routes_lookup ON channel_agent_routes (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type);
CREATE INDEX IF NOT EXISTS idx_channel_agent_routes_actor ON channel_agent_routes (channel, channel_app_id, actor_uid, agent_uid);
CREATE INDEX IF NOT EXISTS idx_channel_agent_access_owner_agent ON channel_agent_access_requests (owner_uid, agent_uid, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_access_actor_agent ON channel_agent_access_requests (actor_uid, agent_uid, status);
CREATE INDEX IF NOT EXISTS idx_channel_agent_access_lookup ON channel_agent_access_requests (channel, channel_app_id, channel_user_id, status);
CREATE INDEX IF NOT EXISTS idx_channel_mobile_links_entry ON channel_identity_mobile_links (entry_id, status);
CREATE INDEX IF NOT EXISTS idx_channel_mobile_links_canonical ON channel_identity_mobile_links (canonical_uid, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_channel_group_mobile_links_group ON channel_group_mobile_links (group_id, canonical_uid, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_channel_group_bindings_topic ON channel_group_bindings (topic_id, status);
CREATE INDEX IF NOT EXISTS idx_channel_group_bindings_lookup ON channel_group_bindings (channel, channel_app_id, channel_user_id, channel_conversation_id, channel_conversation_type, status);
CREATE INDEX IF NOT EXISTS idx_channel_native_groups_topic ON channel_native_groups (topic_id, status);
CREATE INDEX IF NOT EXISTS idx_weixin_clawbot_tokens_active ON weixin_clawbot_tokens (status, updated_at);
CREATE INDEX IF NOT EXISTS idx_weixin_clawbot_tokens_owner ON weixin_clawbot_tokens (owner_uid, status);
CREATE INDEX IF NOT EXISTS idx_weixin_clawbot_tokens_ilink ON weixin_clawbot_tokens (ilink_bot_id, ilink_user_id);
`

const createUpdatedAtTriggers = `
CREATE OR REPLACE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_push_subscriptions_updated_at BEFORE UPDATE ON push_subscriptions
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_friends_updated_at BEFORE UPDATE ON friends
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_projects_updated_at BEFORE UPDATE ON projects
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_bot_config_updated_at BEFORE UPDATE ON bot_config
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_bot_skill_mutations_updated_at BEFORE UPDATE ON bot_skill_mutations
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_feedback_reports_updated_at BEFORE UPDATE ON feedback_reports
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_conversation_task_statuses_updated_at BEFORE UPDATE ON conversation_task_statuses
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_conversation_task_status_sources_updated_at BEFORE UPDATE ON conversation_task_status_sources
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_auth_services_updated_at BEFORE UPDATE ON auth_services
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_plans_updated_at BEFORE UPDATE ON commercial_plans
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_invite_codes_updated_at BEFORE UPDATE ON commercial_invite_codes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_entitlements_updated_at BEFORE UPDATE ON commercial_entitlements
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_orders_updated_at BEFORE UPDATE ON commercial_orders
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_commercial_managed_relay_budgets_updated_at BEFORE UPDATE ON commercial_managed_relay_budgets
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_agent_entries_updated_at BEFORE UPDATE ON channel_agent_entries
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_agent_bindings_updated_at BEFORE UPDATE ON channel_agent_bindings
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_agent_routes_updated_at BEFORE UPDATE ON channel_agent_routes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_agent_access_requests_updated_at BEFORE UPDATE ON channel_agent_access_requests
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_identity_mobile_links_updated_at BEFORE UPDATE ON channel_identity_mobile_links
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_group_mobile_links_updated_at BEFORE UPDATE ON channel_group_mobile_links
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_group_bindings_updated_at BEFORE UPDATE ON channel_group_bindings
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_channel_native_groups_updated_at BEFORE UPDATE ON channel_native_groups
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_weixin_clawbot_tokens_updated_at BEFORE UPDATE ON weixin_clawbot_tokens
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
`

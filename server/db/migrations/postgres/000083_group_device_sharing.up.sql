
CREATE TABLE IF NOT EXISTS group_device_share_bindings (
 owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 device_id VARCHAR(128) NOT NULL,
 installation_id VARCHAR(128) NOT NULL DEFAULT '',
 epoch BIGINT NOT NULL CHECK (epoch > 0),
 active BOOLEAN NOT NULL,
 revoked_before_ms BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(owner_uid,device_id)
);
CREATE TABLE IF NOT EXISTS group_device_shares (
 id VARCHAR(64) PRIMARY KEY,
 owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 grantee_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 topic_id VARCHAR(200) NOT NULL CHECK (topic_id ~ '^grp_[1-9][0-9]*$'),
 device_id VARCHAR(128) NOT NULL,
 device_epoch BIGINT NOT NULL CHECK (device_epoch > 0),
 operations JSONB NOT NULL CHECK (jsonb_typeof(operations) = 'array'),
 status VARCHAR(16) NOT NULL CHECK (status IN ('active','revoked')),
 revision BIGINT NOT NULL CHECK (revision > 0),
 created_at_ms BIGINT NOT NULL,
 updated_at_ms BIGINT NOT NULL,
 CHECK (owner_uid <> grantee_uid),
 UNIQUE (owner_uid, grantee_uid, topic_id, device_id)
);
CREATE INDEX IF NOT EXISTS idx_group_device_shares_grantee ON group_device_shares(topic_id, grantee_uid);
CREATE TABLE IF NOT EXISTS group_device_share_requests (
 id VARCHAR(64) PRIMARY KEY,
 requester_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 owner_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 topic_id VARCHAR(200) NOT NULL CHECK (topic_id ~ '^grp_[1-9][0-9]*$'),
 device_id VARCHAR(128) NOT NULL DEFAULT '',
 description TEXT NOT NULL DEFAULT '',
 client_request_id VARCHAR(128) NOT NULL,
 status VARCHAR(16) NOT NULL CHECK (status IN ('pending','approved','rejected','invalidated')),
 revision BIGINT NOT NULL CHECK (revision > 0),
 share_id VARCHAR(64) NOT NULL DEFAULT '',
 message_id BIGINT NOT NULL DEFAULT 0,
 created_at_ms BIGINT NOT NULL,
 updated_at_ms BIGINT NOT NULL,
 CHECK (requester_uid <> owner_uid),
 UNIQUE (requester_uid, topic_id, client_request_id)
);

ALTER TABLE conversation_device_selections ADD COLUMN IF NOT EXISTS owner_uid BIGINT;

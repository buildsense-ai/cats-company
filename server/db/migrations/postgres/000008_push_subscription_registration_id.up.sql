ALTER TABLE push_subscriptions
  ADD COLUMN IF NOT EXISTS registration_id VARCHAR(64) NOT NULL DEFAULT '';

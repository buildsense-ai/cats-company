DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM group_device_shares) OR EXISTS (SELECT 1 FROM group_device_share_requests)
 OR EXISTS (SELECT 1 FROM conversation_device_selections WHERE owner_uid IS NOT NULL AND owner_uid <> actor_uid)
 THEN RAISE EXCEPTION 'Retained sharing permission or selection evidence prevents rollback'; END IF;
END $$;
DROP TABLE group_device_share_requests;
DROP TABLE group_device_shares;
DROP TABLE group_device_share_bindings;
ALTER TABLE conversation_device_selections DROP COLUMN owner_uid;

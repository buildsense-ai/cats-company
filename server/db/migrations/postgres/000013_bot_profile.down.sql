ALTER TABLE bot_config
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS role;

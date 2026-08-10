ALTER TABLE bot_config
    ADD COLUMN IF NOT EXISTS skills_visibility VARCHAR(16) NOT NULL DEFAULT 'owner';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'bot_config_skills_visibility_check'
          AND conrelid = 'bot_config'::regclass
    ) THEN
        ALTER TABLE bot_config
            ADD CONSTRAINT bot_config_skills_visibility_check
            CHECK (skills_visibility IN ('owner', 'authorized', 'public'));
    END IF;
END $$;

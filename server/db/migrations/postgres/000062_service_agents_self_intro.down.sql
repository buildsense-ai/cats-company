-- 000062_service_agents_self_intro (down)
ALTER TABLE service_agents
    DROP COLUMN IF EXISTS self_intro_input_schema,
    DROP COLUMN IF EXISTS self_intro_description;

-- 000062_service_agents_self_intro (up)
-- 自述建议（self_intro）：应用跑完后「建议」的自我描述，等 owner 确认才写进 definition。
--
-- 为什么不直接改 definition：写这段的是模型。它可以提建议，但不能替人决定对外文案
-- （那句话决定了别人会不会调用它）。中间态不进 tools/list，只有 owner 在面板上能看到。
--
-- 幂等：IF NOT EXISTS，可在已有 data 上重复执行。
ALTER TABLE service_agents
    ADD COLUMN IF NOT EXISTS self_intro_description TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS self_intro_input_schema TEXT NOT NULL DEFAULT '';

-- Persist the real upstream cost budget that backs a plan's platform points.
-- Official plans use it to convert quota at the true cost pace; internal and
-- custom plans may set their own anchor. 0 keeps the relay default rate.
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS real_cost_cny NUMERIC(14,6) NOT NULL DEFAULT 0;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_real_cost') THEN
        ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_real_cost CHECK (real_cost_cny >= 0);
    END IF;
END $$;

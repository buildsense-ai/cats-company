package postgres

const migrateCommercialPlanManagement = `
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
ALTER TABLE commercial_plans DROP CONSTRAINT IF EXISTS chk_commercial_plans_duration;
ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_duration
 CHECK (duration_days>0 OR (duration_days=-1 AND sale_state='hidden' AND price_fen=0));
CREATE INDEX IF NOT EXISTS idx_commercial_entitlements_plan_users
 ON commercial_entitlements(plan_id,uid,starts_at,expires_at) WHERE state='active';
CREATE INDEX IF NOT EXISTS idx_commercial_grants_plan_users
 ON commercial_quota_grants(plan_id,uid,effective_at,expires_at) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_commercial_orders_plan_status ON commercial_orders(plan_id,status);

-- Lock the referenced plan before creating/reassigning a relationship. Archive
-- takes FOR UPDATE, so a concurrent grant/invite/order either blocks deletion
-- or observes the archived plan and fails without creating orphaned benefits.
CREATE OR REPLACE FUNCTION guard_commercial_plan_reference() RETURNS trigger AS $$
DECLARE archived TIMESTAMPTZ;
BEGIN
 IF NEW.plan_id IS NULL THEN RETURN NEW; END IF;
 SELECT archived_at INTO archived FROM commercial_plans WHERE id=NEW.plan_id FOR SHARE;
 IF FOUND AND archived IS NOT NULL THEN
  RAISE EXCEPTION 'commercial plan is archived' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER trg_commercial_entitlements_plan_guard
 BEFORE INSERT OR UPDATE OF plan_id ON commercial_entitlements
 FOR EACH ROW EXECUTE FUNCTION guard_commercial_plan_reference();
CREATE OR REPLACE TRIGGER trg_commercial_grants_plan_guard
 BEFORE INSERT OR UPDATE OF plan_id ON commercial_quota_grants
 FOR EACH ROW EXECUTE FUNCTION guard_commercial_plan_reference();
CREATE OR REPLACE TRIGGER trg_commercial_invites_plan_guard
 BEFORE INSERT OR UPDATE OF plan_id ON commercial_invite_codes
 FOR EACH ROW EXECUTE FUNCTION guard_commercial_plan_reference();
CREATE OR REPLACE TRIGGER trg_commercial_orders_plan_guard
 BEFORE INSERT OR UPDATE OF plan_id ON commercial_orders
 FOR EACH ROW EXECUTE FUNCTION guard_commercial_plan_reference();
`

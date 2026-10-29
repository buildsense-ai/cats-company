package postgres

// Plan tier: the ordinal position of a package in the upgrade ladder.
//
// The webapp used to hard-code `{catsco-basic: 1, catsco-personal: 2, catsco-pro: 3}`
// and compare slugs to decide three things the buyer sees:
//
//   - "续费"    — this card is the package you already hold
//   - "已包含"  — you hold a higher package, so buying down is refused
//   - "升级至 X" — this card is above the one you hold
//
// Keying that off a slug means renaming or replacing a package silently drops
// all three behaviours, and a new package cannot join the ladder at all. It is
// the same failure mode that `cloud_worker_included`, the deployment profile and
// the runtime tier each had: business meaning stored in an identity string.
//
// The number is ordinal and never displayed. 0 means "not part of the ladder" —
// free, internal and legacy packages — which is why the column defaults to 0
// rather than 1: a package that never opted in must not silently become the
// cheapest sellable tier.
//
// The public catalog does expose this value (see commercialPlanForUser): the
// buyer's own upgrade path is exactly what it describes, and the number carries
// no pricing or cost information.
const migrateCommercialPlansAddTier = `
ALTER TABLE commercial_plans ADD COLUMN IF NOT EXISTS plan_tier INT NOT NULL DEFAULT 0;
DO $$ BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_commercial_plans_plan_tier') THEN
		ALTER TABLE commercial_plans ADD CONSTRAINT chk_commercial_plans_plan_tier CHECK (plan_tier >= 0);
	END IF;
END $$;
-- Backfill the two packages that occupied rungs 2 and 3 before this column
-- existed, so an existing buyer keeps its "续费" / "已包含" / "升级" behaviour the
-- moment this deploys. Guarded on 0 for the same reason as the machine flag: the
-- backfill must run once, and re-asserting it on every boot would undo an
-- operator who deliberately took a retired package off the ladder.
--
-- Rung 1 has no historical occupant: the ¥59 package is new.
UPDATE commercial_plans SET plan_tier = 2 WHERE slug = 'catsco-personal' AND plan_tier = 0;
UPDATE commercial_plans SET plan_tier = 3 WHERE slug = 'catsco-pro'      AND plan_tier = 0;
`

const rollbackCommercialPlansAddTier = `
ALTER TABLE commercial_plans DROP COLUMN IF EXISTS plan_tier;
`

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
-- Deliberately no backfill. The ladder describes the packages on sale, and
-- joining it is an explicit decision an operator records on the row.
--
-- Backfilling the retired ¥399 / ¥799 packages would give an existing buyer a
-- backend rung while the public catalog no longer lists them: the webapp derives
-- the buyer's rung by matching entitlements against the catalog, so it would
-- keep offering every new package as a plain purchase while the backend refused
-- anything below the retired rung. A package hidden from the catalog has to be
-- off the ladder, or the two sides disagree about what the buyer may buy.
`

const rollbackCommercialPlansAddTier = `
ALTER TABLE commercial_plans DROP COLUMN IF EXISTS plan_tier;
`

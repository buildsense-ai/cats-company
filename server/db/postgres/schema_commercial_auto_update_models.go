package postgres

// Per-plan model-set policy. Paid plans used to carry a hardcoded model list
// maintained by startup migrations, so every relay model launch needed a
// control-plane change. The relay catalog is now the source of truth and a
// startup reconcile keeps official plans in step with it; this column records
// whether a given plan opts into that behaviour.
//
// Default TRUE so existing and future official plans follow the relay unless an
// operator deliberately pins a plan's model set.
const migrateCommercialPlansAutoUpdateModels = `
ALTER TABLE commercial_plans
    ADD COLUMN IF NOT EXISTS auto_update_models BOOLEAN NOT NULL DEFAULT TRUE;
`

// rollbackCommercialPlansAutoUpdateModels drops the policy column. The plans'
// model_budgets are left exactly as the reconcile last set them: dropping the
// switch must not rewrite which models a plan sells.
const rollbackCommercialPlansAutoUpdateModels = `
ALTER TABLE commercial_plans
    DROP COLUMN IF EXISTS auto_update_models;
`

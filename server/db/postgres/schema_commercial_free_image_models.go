package postgres

// Free plan image lane: open image generation to every public plan by adding
// the five image-lane models to the Free package at 100 CNY each, the same
// per-model amount the Personal plan grants. This mirrors how 000022 opened
// native search (DeepSeek Flash) to Free; the add-on raises the advertised
// Free pooled allowance by 500. Existing free grants are left untouched and
// each image model is granted to a free entitlement exactly once, which keeps
// the migration repeatable on every startup.
const migrateCommercialPlansFreeImageModels = `
-- Historical migration, now a no-op: The image lane joined the Free plan.
--
-- The plan model sets used to be written here as literal JSON, which
-- meant every relay model launch needed a control-plane change and each
-- startup re-applied the same list, silently reverting a hand-edited
-- plan. The relay catalog is the source of truth now and the startup
-- reconcile keeps the paid plans in step with it, so this migration is
-- kept only as a named, ordered placeholder. It stays in the statement
-- list because the ordering comment in schema.go still refers to it.
SELECT 1;
`

// rollbackCommercialPlansFreeImageModels restores the five-model Free package
// and revokes only the free image-lane grants (grant_type='free' on an image
// lane model). Usage history and every other grant stay as they are.
const rollbackCommercialPlansFreeImageModels = `
-- Remove the free image add-on: restore the five-model Free package and
-- revoke only the free image-lane grants. Free grants on any other model and
-- all pooled usage history stay as they are.
UPDATE commercial_plans
SET model_budgets = '{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"deepseek-flash":100,"glm-5.3-flash":100}'::jsonb
WHERE slug = 'catsco-free';

DO $$
DECLARE
    grant_row RECORD;
BEGIN
    FOR grant_row IN
        SELECT id, uid, model, amount_cny
        FROM commercial_quota_grants
        WHERE grant_type = 'free'
          AND model IN (
              'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare',
              'gpt-image-2.5-sunburst', 'chatgpt-image-latest'
          )
          AND revoked_at IS NULL
    LOOP
        INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
        VALUES (grant_row.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                'plan_model_migration_rollback', grant_row.id,
                'remove image models from free grant set');
        UPDATE commercial_quota_grants
        SET revoked_at = CURRENT_TIMESTAMP,
            expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
        WHERE id = grant_row.id;
    END LOOP;
END $$;
`

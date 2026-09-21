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

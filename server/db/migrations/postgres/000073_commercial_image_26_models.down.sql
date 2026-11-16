-- Roll back the 2.6 image add-on: drop only the three new budget keys and
-- revoke only the grants this migration wrote (matched by the note it stamped
-- plus the model name), so every other budget key, grant and usage row stays
-- as it is.
UPDATE commercial_plans
SET model_budgets = model_budgets - ARRAY['gpt-image-2.6', 'gpt-image-2.6-flare', 'gpt-image-2.6-sunburst']
WHERE slug IN ('catsco-free', 'catsco-personal', 'catsco-pro', 'catsco-internal-all-models-50k');

DO $$
DECLARE
    grant_row RECORD;
BEGIN
    FOR grant_row IN
        SELECT id, uid, model, amount_cny
        FROM commercial_quota_grants
        WHERE model IN ('gpt-image-2.6', 'gpt-image-2.6-flare', 'gpt-image-2.6-sunburst')
          AND note IN ('CatsCo free image 2.6 model access', 'Public plan image 2.6 model access')
          AND revoked_at IS NULL
    LOOP
        INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
        VALUES (grant_row.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                'plan_model_migration_rollback', grant_row.id,
                'remove image 2.6 models from grant set');
        UPDATE commercial_quota_grants
        SET revoked_at = CURRENT_TIMESTAMP,
            expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
        WHERE id = grant_row.id;
    END LOOP;
END $$;

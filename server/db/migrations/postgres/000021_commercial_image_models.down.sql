-- Remove the image add-on and restore the five-model public plans without
-- resetting usage. Internal/custom plans, operator add-ons and fulfilled
-- history stay as they are. Only the add-on grants created by 000021 are
-- revoked, and they are identified exclusively by the marker the up migration
-- recorded for each of them in the ledger: source_type='image_models_v1' with
-- source_id = the new grant id. Grants that merely share the note or shape
-- are never touched.
-- Roll this file out BEFORE 000020.commercial_public_models.down.sql.
UPDATE commercial_plans SET model_budgets = CASE slug
    WHEN 'catsco-personal' THEN '{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-v4-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100}'::jsonb
    WHEN 'catsco-pro' THEN '{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-v4-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300}'::jsonb
END WHERE slug IN ('catsco-personal', 'catsco-pro');

UPDATE commercial_orders o SET plan_model_budgets = p.model_budgets
FROM commercial_plans p WHERE p.slug = o.plan_slug
    AND p.slug IN ('catsco-personal', 'catsco-pro')
    AND o.status IN ('created', 'pending', 'paid');

DO $$
DECLARE
    grant_row RECORD;
BEGIN
    FOR grant_row IN
        SELECT g.id, g.uid, g.model, g.amount_cny
        FROM commercial_quota_grants g
        WHERE g.revoked_at IS NULL
          AND EXISTS (
              SELECT 1
              FROM commercial_quota_ledger marker
              WHERE marker.source_type = 'image_models_v1'
                AND marker.entry_type = 'grant'
                AND marker.source_id = g.id
          )
    LOOP
        INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
        VALUES (grant_row.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                'image_models_v1_rollback', grant_row.id, 'Remove public plan image model access');
        UPDATE commercial_quota_grants SET revoked_at = CURRENT_TIMESTAMP WHERE id = grant_row.id;
    END LOOP;
END $$;

-- Public Pro/Max add the image lane models to the five public chat models.
-- The add-on raises the advertised pool by 500 (Personal) and 1500 (Pro).
-- Internal/custom plans and fulfilled order snapshots remain unchanged.
UPDATE commercial_plans SET model_budgets = CASE slug
    WHEN 'catsco-personal' THEN '{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-v4-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb
    WHEN 'catsco-pro' THEN '{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-v4-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}'::jsonb
END WHERE slug IN ('catsco-personal', 'catsco-pro');

UPDATE commercial_orders o SET plan_model_budgets = p.model_budgets
FROM commercial_plans p WHERE p.slug = o.plan_slug
    AND p.slug IN ('catsco-personal', 'catsco-pro')
    AND o.status IN ('created', 'pending', 'paid');

-- Existing public packages gain the image add-on at the original interval so a
-- renewal cannot be brought forward or merged with the current period. The five
-- chat grants are left untouched; only packages that currently hold exactly the
-- public five-model set qualify, which keeps the migration repeatable.
DO $$
DECLARE
    package RECORD;
    image_model TEXT;
    image_amount NUMERIC(14,6);
    new_grant_id BIGINT;
BEGIN
    FOR package IN
        SELECT g.uid, g.plan_id, p.slug, g.grant_type, g.source_ref,
               g.effective_at, g.expires_at, g.reset_duration,
               MAX(g.invite_code_id) AS invite_code_id,
               MAX(g.operator_uid) AS operator_uid
        FROM commercial_quota_grants g JOIN commercial_plans p ON p.id = g.plan_id
        WHERE p.slug IN ('catsco-personal', 'catsco-pro')
          AND g.grant_type IN ('order', 'invite', 'operator_plan')
          AND g.revoked_at IS NULL
          AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
        GROUP BY g.uid, g.plan_id, p.slug, g.grant_type, g.source_ref,
                 g.effective_at, g.expires_at, g.reset_duration
        HAVING COUNT(*) = 5 AND COUNT(DISTINCT g.model) = 5
           AND COUNT(*) FILTER (WHERE g.model IN (
               'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash', 'glm-5.3-flash', 'gpt-5.6-terra')) = 5
    LOOP
        image_amount := CASE WHEN package.slug = 'catsco-personal' THEN 100 ELSE 300 END;
        FOREACH image_model IN ARRAY ARRAY[
            'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare', 'gpt-image-2.5-sunburst', 'chatgpt-image-latest'
        ] LOOP
            INSERT INTO commercial_quota_grants(
                uid, plan_id, invite_code_id, grant_type, model, amount_cny,
                reset_duration, effective_at, expires_at, source_ref, note, operator_uid
            ) VALUES (
                package.uid, package.plan_id, package.invite_code_id, package.grant_type,
                image_model, image_amount, package.reset_duration, package.effective_at,
                package.expires_at, package.source_ref, 'Public plan image model access', package.operator_uid
            ) RETURNING id INTO new_grant_id;
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, image_model, image_amount, 'grant',
                    'image_models_v1', new_grant_id, 'Public plan image model access');
        END LOOP;
    END LOOP;
END $$;

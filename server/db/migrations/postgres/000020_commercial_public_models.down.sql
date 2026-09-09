-- Restore seven-model public packages without resetting usage or trial spend.
-- Public Pro/Max keep their total platform allowance. Internal/custom plans
-- and fulfilled order snapshots remain unchanged.
UPDATE commercial_plans SET model_budgets = CASE slug
    WHEN 'catsco-personal' THEN '{"MiniMax-M2.7":1500,"MiniMax-M3":1500,"deepseek-v4-flash":1500,"glm-5.3-flash":1500,"gpt-5.6-terra":1500,"gpt-5.6-sol":1500,"gpt-5.6-luna":1500}'::jsonb
    WHEN 'catsco-pro' THEN '{"MiniMax-M2.7":4500,"MiniMax-M3":4500,"deepseek-v4-flash":4500,"glm-5.3-flash":4500,"gpt-5.6-terra":4500,"gpt-5.6-sol":4500,"gpt-5.6-luna":4500}'::jsonb
END WHERE slug IN ('catsco-personal', 'catsco-pro');

UPDATE commercial_orders o SET plan_model_budgets = p.model_budgets
FROM commercial_plans p WHERE p.slug = o.plan_slug
    AND p.slug IN ('catsco-personal', 'catsco-pro')
    AND o.status IN ('created', 'pending', 'paid');

-- Include future renewals. Group by the original grant interval so that a
-- renewal cannot be brought forward or merged with the current period.
DO $$
DECLARE
    package RECORD;
    grant_row RECORD;
    public_model TEXT;
    model_amount NUMERIC(14,6);
    new_grant_id BIGINT;
BEGIN
    FOR package IN
        SELECT g.uid, g.plan_id, g.grant_type, g.source_ref,
               g.effective_at, g.expires_at, g.reset_duration,
               MAX(g.invite_code_id) AS invite_code_id,
               MAX(g.operator_uid) AS operator_uid, SUM(g.amount_cny) AS total
        FROM commercial_quota_grants g JOIN commercial_plans p ON p.id = g.plan_id
        WHERE p.slug IN ('catsco-personal', 'catsco-pro')
          AND g.grant_type IN ('order', 'invite', 'operator_plan')
          AND g.revoked_at IS NULL
          AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
        GROUP BY g.uid, g.plan_id, g.grant_type, g.source_ref,
                 g.effective_at, g.expires_at, g.reset_duration
        HAVING COUNT(*) = 5 AND COUNT(DISTINCT g.model) = 5 AND COUNT(*) FILTER (WHERE g.model IN ('gpt-5.6-sol', 'gpt-5.6-luna')) = 0
           AND COUNT(*) FILTER (WHERE g.model NOT IN (
               'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash', 'glm-5.3-flash',
               'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna')) = 0
    LOOP
        FOR grant_row IN
            SELECT id, model, amount_cny FROM commercial_quota_grants
            WHERE uid = package.uid AND plan_id = package.plan_id
              AND grant_type = package.grant_type AND source_ref = package.source_ref
              AND effective_at = package.effective_at
              AND expires_at IS NOT DISTINCT FROM package.expires_at
              AND reset_duration = package.reset_duration AND revoked_at IS NULL
            FOR UPDATE
        LOOP
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                    'public_models_v2_rollback', grant_row.id, 'Restore previous public model access');
            UPDATE commercial_quota_grants SET revoked_at = CURRENT_TIMESTAMP WHERE id = grant_row.id;
        END LOOP;
        FOREACH public_model IN ARRAY ARRAY[
            'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash', 'glm-5.3-flash', 'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna'
        ] LOOP
            model_amount := CASE WHEN public_model = 'gpt-5.6-luna'
                THEN package.total - ROUND(package.total / 7, 6) * 6
                ELSE ROUND(package.total / 7, 6) END;
            INSERT INTO commercial_quota_grants(
                uid, plan_id, invite_code_id, grant_type, model, amount_cny,
                reset_duration, effective_at, expires_at, source_ref, note, operator_uid
            ) VALUES (
                package.uid, package.plan_id, package.invite_code_id, package.grant_type,
                public_model, model_amount, package.reset_duration, package.effective_at,
                package.expires_at, package.source_ref, 'Restore previous public model access', package.operator_uid
            ) RETURNING id INTO new_grant_id;
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, public_model, model_amount, 'grant',
                    'public_models_v2_rollback', new_grant_id, 'Restore previous public model access');
        END LOOP;
    END LOOP;
END $$;

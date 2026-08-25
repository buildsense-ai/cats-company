-- Roll back the product declaration and restore the original two-model
-- allocation for active paid grants.  The shared Relay pool size is unchanged.
UPDATE commercial_plans
SET model_budgets = CASE slug
    WHEN 'catsco-personal' THEN '{"gpt-5.6-terra":5250,"gpt-5.6-sol":5250}'::jsonb
    WHEN 'catsco-pro' THEN '{"gpt-5.6-terra":15750,"gpt-5.6-sol":15750}'::jsonb
    ELSE model_budgets
END
WHERE slug IN ('catsco-personal', 'catsco-pro');

UPDATE commercial_orders
SET plan_model_budgets = CASE plan_slug
    WHEN 'catsco-personal' THEN '{"gpt-5.6-terra":5250,"gpt-5.6-sol":5250}'::jsonb
    WHEN 'catsco-pro' THEN '{"gpt-5.6-terra":15750,"gpt-5.6-sol":15750}'::jsonb
    ELSE plan_model_budgets
END
WHERE plan_slug IN ('catsco-personal', 'catsco-pro')
  AND status IN ('created', 'pending', 'paid');

DO $$
DECLARE
    package RECORD;
    grant_row RECORD;
    old_model TEXT;
    old_amount NUMERIC(14,6);
    new_grant_id BIGINT;
BEGIN
    FOR package IN
        SELECT g.uid, g.plan_id, g.grant_type, g.source_ref,
               MIN(g.effective_at) AS effective_at,
               MAX(g.expires_at) AS expires_at,
               MAX(g.invite_code_id) AS invite_code_id,
               MAX(g.operator_uid) AS operator_uid,
               p.slug AS plan_slug, p.name AS plan_name
        FROM commercial_quota_grants g
        JOIN commercial_plans p ON p.id = g.plan_id
        WHERE p.slug IN ('catsco-personal', 'catsco-pro')
          AND g.grant_type IN ('order', 'invite', 'operator_plan')
          AND g.revoked_at IS NULL
          AND g.effective_at <= CURRENT_TIMESTAMP
          AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
          AND EXISTS (
              SELECT 1 FROM commercial_entitlements e
              WHERE e.uid = g.uid AND e.plan_id = g.plan_id
                AND e.state = 'active' AND e.starts_at <= CURRENT_TIMESTAMP
                AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
                AND e.source_ref = g.source_ref
                AND ((g.grant_type = 'operator_plan' AND e.source = 'operator')
                     OR (g.grant_type <> 'operator_plan' AND e.source = g.grant_type))
          )
        GROUP BY g.uid, g.plan_id, g.grant_type, g.source_ref, p.slug, p.name
        HAVING COUNT(*) = 6
           AND COUNT(DISTINCT g.model) = 6
           AND COUNT(*) FILTER (WHERE g.model IN ('MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash', 'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna')) = 6
           AND COALESCE(SUM(g.amount_cny), 0) = CASE p.slug
               WHEN 'catsco-personal' THEN 10500
               WHEN 'catsco-pro' THEN 31500
           END
    LOOP
        FOR grant_row IN
            SELECT id, model, amount_cny
            FROM commercial_quota_grants
            WHERE uid = package.uid AND plan_id = package.plan_id
              AND grant_type = package.grant_type AND source_ref = package.source_ref
              AND revoked_at IS NULL
        LOOP
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                    'plan_model_migration_rollback', grant_row.id,
                    'restore paid plan two-model grant');
        END LOOP;

        UPDATE commercial_quota_grants
        SET revoked_at = CURRENT_TIMESTAMP,
            expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
        WHERE uid = package.uid AND plan_id = package.plan_id
          AND grant_type = package.grant_type AND source_ref = package.source_ref
          AND revoked_at IS NULL;

        old_amount := CASE package.plan_slug WHEN 'catsco-personal' THEN 5250 WHEN 'catsco-pro' THEN 15750 END;
        FOREACH old_model IN ARRAY ARRAY['gpt-5.6-terra', 'gpt-5.6-sol']
        LOOP
            INSERT INTO commercial_quota_grants(
                uid, plan_id, invite_code_id, grant_type, model, amount_cny,
                reset_duration, effective_at, expires_at, source_ref, note, operator_uid
            ) VALUES (
                package.uid, package.plan_id, package.invite_code_id, package.grant_type,
                old_model, old_amount, '1M', package.effective_at, package.expires_at,
                package.source_ref, 'paid plan two-model migration rollback', package.operator_uid
            ) RETURNING id INTO new_grant_id;
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, old_model, old_amount, 'grant',
                    'plan_model_migration_rollback', new_grant_id, package.plan_name);
        END LOOP;
    END LOOP;
END $$;

-- Paid plans are a single shared Relay pool.  Keep the advertised pool size,
-- but declare a positive budget for every public model so the model selector
-- and provider scopes do not hide models from paid users.
UPDATE commercial_plans
SET model_budgets = CASE slug
    WHEN 'catsco-personal' THEN '{"MiniMax-M2.7":1750,"MiniMax-M3":1750,"deepseek-v4-flash":1750,"gpt-5.6-terra":1750,"gpt-5.6-sol":1750,"gpt-5.6-luna":1750}'::jsonb
    WHEN 'catsco-pro' THEN '{"MiniMax-M2.7":5250,"MiniMax-M3":5250,"deepseek-v4-flash":5250,"gpt-5.6-terra":5250,"gpt-5.6-sol":5250,"gpt-5.6-luna":5250}'::jsonb
    ELSE model_budgets
END
WHERE slug IN ('catsco-personal', 'catsco-pro');

-- Orders that have not been fulfilled yet carry a plan snapshot.  Refresh it
-- so a payment made after this migration cannot create the old two-model
-- grants.
UPDATE commercial_orders
SET plan_model_budgets = CASE plan_slug
    WHEN 'catsco-personal' THEN '{"MiniMax-M2.7":1750,"MiniMax-M3":1750,"deepseek-v4-flash":1750,"gpt-5.6-terra":1750,"gpt-5.6-sol":1750,"gpt-5.6-luna":1750}'::jsonb
    WHEN 'catsco-pro' THEN '{"MiniMax-M2.7":5250,"MiniMax-M3":5250,"deepseek-v4-flash":5250,"gpt-5.6-terra":5250,"gpt-5.6-sol":5250,"gpt-5.6-luna":5250}'::jsonb
    ELSE plan_model_budgets
END
WHERE plan_slug IN ('catsco-personal', 'catsco-pro')
  AND status IN ('created', 'pending', 'paid');

-- Existing active orders/invites/operator assignments already have grant
-- rows based on the old two-model snapshot.  Replace only those exact base
-- grants, preserving their total, expiry and effective window.  The guard in
-- HAVING makes this block idempotent when CreateSchema is run again.
DO $$
DECLARE
    package RECORD;
    grant_row RECORD;
    paid_model TEXT;
    paid_amount NUMERIC(14,6);
    new_grant_id BIGINT;
BEGIN
    FOR package IN
        SELECT g.uid,
               g.plan_id,
               g.grant_type,
               g.source_ref,
               MIN(g.effective_at) AS effective_at,
               MAX(g.expires_at) AS expires_at,
               MAX(g.invite_code_id) AS invite_code_id,
               MAX(g.operator_uid) AS operator_uid,
               p.slug AS plan_slug,
               p.name AS plan_name
        FROM commercial_quota_grants g
        JOIN commercial_plans p ON p.id = g.plan_id
        WHERE p.slug IN ('catsco-personal', 'catsco-pro')
          AND g.grant_type IN ('order', 'invite', 'operator_plan')
          AND g.revoked_at IS NULL
          AND g.effective_at <= CURRENT_TIMESTAMP
          AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
          AND EXISTS (
              SELECT 1
              FROM commercial_entitlements e
              WHERE e.uid = g.uid
                AND e.plan_id = g.plan_id
                AND e.state = 'active'
                AND e.starts_at <= CURRENT_TIMESTAMP
                AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
                AND e.source_ref = g.source_ref
                AND ((g.grant_type = 'operator_plan' AND e.source = 'operator')
                     OR (g.grant_type <> 'operator_plan' AND e.source = g.grant_type))
          )
        GROUP BY g.uid, g.plan_id, g.grant_type, g.source_ref, p.slug, p.name
        HAVING COUNT(*) = 2
           AND COUNT(DISTINCT g.model) = 2
           AND COUNT(*) FILTER (WHERE g.model IN ('gpt-5.6-terra', 'gpt-5.6-sol')) = 2
           AND COALESCE(SUM(g.amount_cny), 0) = CASE p.slug
               WHEN 'catsco-personal' THEN 10500
               WHEN 'catsco-pro' THEN 31500
           END
    LOOP
        FOR grant_row IN
            SELECT id, model, amount_cny
            FROM commercial_quota_grants
            WHERE uid = package.uid
              AND plan_id = package.plan_id
              AND grant_type = package.grant_type
              AND source_ref = package.source_ref
              AND revoked_at IS NULL
        LOOP
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                    'plan_model_migration', grant_row.id,
                    'replace paid plan two-model grant with all-model grant');
        END LOOP;

        UPDATE commercial_quota_grants
        SET revoked_at = CURRENT_TIMESTAMP,
            expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
        WHERE uid = package.uid
          AND plan_id = package.plan_id
          AND grant_type = package.grant_type
          AND source_ref = package.source_ref
          AND revoked_at IS NULL;

        paid_amount := CASE package.plan_slug
            WHEN 'catsco-personal' THEN 1750
            WHEN 'catsco-pro' THEN 5250
        END;
        FOREACH paid_model IN ARRAY ARRAY[
            'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash',
            'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna'
        ]
        LOOP
            INSERT INTO commercial_quota_grants(
                uid, plan_id, invite_code_id, grant_type, model, amount_cny,
                reset_duration, effective_at, expires_at, source_ref, note, operator_uid
            ) VALUES (
                package.uid, package.plan_id, package.invite_code_id, package.grant_type,
                paid_model, paid_amount, '1M', package.effective_at, package.expires_at,
                package.source_ref, 'paid plan all-model migration', package.operator_uid
            ) RETURNING id INTO new_grant_id;
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, paid_model, paid_amount, 'grant',
                    'plan_model_migration', new_grant_id, package.plan_name);
        END LOOP;
    END LOOP;
END $$;

-- Add GLM-5.3-Flash without increasing the paid shared Relay pool.
UPDATE commercial_plans
SET model_budgets = CASE slug
    WHEN 'catsco-personal' THEN '{"MiniMax-M2.7":1500,"MiniMax-M3":1500,"deepseek-v4-flash":1500,"glm-5.3-flash":1500,"gpt-5.6-terra":1500,"gpt-5.6-sol":1500,"gpt-5.6-luna":1500}'::jsonb
    WHEN 'catsco-pro' THEN '{"MiniMax-M2.7":4500,"MiniMax-M3":4500,"deepseek-v4-flash":4500,"glm-5.3-flash":4500,"gpt-5.6-terra":4500,"gpt-5.6-sol":4500,"gpt-5.6-luna":4500}'::jsonb
    ELSE model_budgets
END
WHERE slug IN ('catsco-personal', 'catsco-pro');

-- Only unfulfilled orders use this snapshot to create future grants. Keep
-- fulfilled/refunding/refunded history immutable; their active grants are
-- migrated separately below.
UPDATE commercial_orders
SET plan_model_budgets = CASE plan_slug
    WHEN 'catsco-personal' THEN '{"MiniMax-M2.7":1500,"MiniMax-M3":1500,"deepseek-v4-flash":1500,"glm-5.3-flash":1500,"gpt-5.6-terra":1500,"gpt-5.6-sol":1500,"gpt-5.6-luna":1500}'::jsonb
    WHEN 'catsco-pro' THEN '{"MiniMax-M2.7":4500,"MiniMax-M3":4500,"deepseek-v4-flash":4500,"glm-5.3-flash":4500,"gpt-5.6-terra":4500,"gpt-5.6-sol":4500,"gpt-5.6-luna":4500}'::jsonb
    ELSE plan_model_budgets
END
WHERE plan_slug IN ('catsco-personal', 'catsco-pro')
  AND status IN ('created', 'pending', 'paid');

-- Replace only the exact six-model paid package created by migration 000016.
-- Manual grants and legacy packages are deliberately outside this predicate.
DO $$
DECLARE
    package RECORD;
    grant_row RECORD;
    paid_model TEXT;
    paid_amount NUMERIC(14,6);
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
           AND COUNT(*) FILTER (WHERE g.model IN (
               'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash',
               'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna'
           )) = 6
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
                    'plan_model_migration', grant_row.id,
                    'replace paid plan six-model grant with GLM-5.3-Flash grant set');
        END LOOP;

        UPDATE commercial_quota_grants
        SET revoked_at = CURRENT_TIMESTAMP,
            expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
        WHERE uid = package.uid AND plan_id = package.plan_id
          AND grant_type = package.grant_type AND source_ref = package.source_ref
          AND revoked_at IS NULL;

        paid_amount := CASE package.plan_slug WHEN 'catsco-personal' THEN 1500 WHEN 'catsco-pro' THEN 4500 END;
        FOREACH paid_model IN ARRAY ARRAY[
            'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash', 'glm-5.3-flash',
            'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna'
        ]
        LOOP
            INSERT INTO commercial_quota_grants(
                uid, plan_id, invite_code_id, grant_type, model, amount_cny,
                reset_duration, effective_at, expires_at, source_ref, note, operator_uid
            ) VALUES (
                package.uid, package.plan_id, package.invite_code_id, package.grant_type,
                paid_model, paid_amount, '1M', package.effective_at, package.expires_at,
                package.source_ref, 'paid plan GLM-5.3-Flash migration', package.operator_uid
            ) RETURNING id INTO new_grant_id;
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, paid_model, paid_amount, 'grant',
                    'plan_model_migration', new_grant_id, package.plan_name);
        END LOOP;
    END LOOP;
END $$;

-- Free keeps its existing grants and receives a separate GLM grant.
UPDATE commercial_plans
SET model_budgets = '{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"glm-5.3-flash":100}'::jsonb
WHERE slug = 'catsco-free';

WITH inserted AS (
    INSERT INTO commercial_quota_grants(
        uid, plan_id, grant_type, model, amount_cny, reset_duration,
        effective_at, expires_at, source_ref, note
    )
    SELECT e.uid, e.plan_id, 'free', 'glm-5.3-flash', 100, '1M',
           e.starts_at, e.expires_at, e.source_ref, 'CatsCo free GLM-5.3-Flash migration'
    FROM commercial_entitlements e
    JOIN commercial_plans p ON p.id = e.plan_id
    WHERE p.slug = 'catsco-free'
      AND e.source = 'free'
      AND e.state = 'active'
      AND e.starts_at <= CURRENT_TIMESTAMP
      AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
      AND NOT EXISTS (
          SELECT 1 FROM commercial_quota_grants g
          WHERE g.uid = e.uid AND g.plan_id = e.plan_id
            AND g.grant_type = 'free' AND g.model = 'glm-5.3-flash'
            AND g.source_ref = e.source_ref AND g.revoked_at IS NULL
            AND g.effective_at <= CURRENT_TIMESTAMP
            AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
      )
    RETURNING id, uid, model, amount_cny
)
INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
SELECT uid, model, amount_cny, 'grant', 'free', id, 'Free GLM-5.3-Flash migration'
FROM inserted;

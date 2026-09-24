-- Retire DeepSeek V4 Flash from every public commercial package. The paid
-- plans keep their advertised totals (11000 / 33000): the five remaining
-- chat models split the same allowance the six used to hold (mirroring how
-- 000022 spread 10500 over six). The Free pool drops the model and its
-- 100-CNY grant. Exact prior-state predicates keep the migration idempotent;
-- custom paid packages and manual quota are left untouched, while the free
-- V4 grant is revoked for every free entitlement by design.

UPDATE commercial_plans
SET model_budgets = '{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb
WHERE slug = 'catsco-personal'
  AND model_budgets = '{"MiniMax-M2.7":1750,"MiniMax-M3":1750,"deepseek-v4-flash":1750,"deepseek-flash":1750,"glm-5.3-flash":1750,"gpt-5.6-terra":1750,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb;

UPDATE commercial_plans
SET model_budgets = '{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}'::jsonb
WHERE slug = 'catsco-pro'
  AND model_budgets = '{"MiniMax-M2.7":5250,"MiniMax-M3":5250,"deepseek-v4-flash":5250,"deepseek-flash":5250,"glm-5.3-flash":5250,"gpt-5.6-terra":5250,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}'::jsonb;

-- Only unfulfilled orders use this snapshot to create future grants. Keep
-- fulfilled/refunding/refunded history immutable; their active grants are
-- migrated separately below.
UPDATE commercial_orders
SET plan_model_budgets = '{"MiniMax-M2.7":2100,"MiniMax-M3":2100,"deepseek-flash":2100,"glm-5.3-flash":2100,"gpt-5.6-terra":2100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb
WHERE plan_slug = 'catsco-personal'
  AND plan_model_budgets = '{"MiniMax-M2.7":1750,"MiniMax-M3":1750,"deepseek-v4-flash":1750,"deepseek-flash":1750,"glm-5.3-flash":1750,"gpt-5.6-terra":1750,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb
  AND status IN ('created', 'pending', 'paid');

UPDATE commercial_orders
SET plan_model_budgets = '{"MiniMax-M2.7":6300,"MiniMax-M3":6300,"deepseek-flash":6300,"glm-5.3-flash":6300,"gpt-5.6-terra":6300,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}'::jsonb
WHERE plan_slug = 'catsco-pro'
  AND plan_model_budgets = '{"MiniMax-M2.7":5250,"MiniMax-M3":5250,"deepseek-v4-flash":5250,"deepseek-flash":5250,"glm-5.3-flash":5250,"gpt-5.6-terra":5250,"gpt-image-2":300,"gpt-image-2.5":300,"gpt-image-2.5-flare":300,"gpt-image-2.5-sunburst":300,"chatgpt-image-latest":300}'::jsonb
  AND status IN ('created', 'pending', 'paid');

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
          AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
        GROUP BY g.uid, g.plan_id, g.grant_type, g.source_ref, p.slug, p.name
        HAVING COUNT(*) FILTER (WHERE g.model IN (
               'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash',
               'deepseek-flash', 'glm-5.3-flash', 'gpt-5.6-terra'
           )) = 6
           AND COUNT(*) FILTER (WHERE g.model NOT IN (
               'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash',
               'deepseek-flash', 'glm-5.3-flash', 'gpt-5.6-terra',
               'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare',
               'gpt-image-2.5-sunburst', 'chatgpt-image-latest'
           )) = 0
           AND COALESCE(SUM(g.amount_cny) FILTER (WHERE g.model IN (
               'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash',
               'deepseek-flash', 'glm-5.3-flash', 'gpt-5.6-terra'
           )), 0) = CASE p.slug
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
              AND model IN (
                  'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash',
                  'deepseek-flash', 'glm-5.3-flash', 'gpt-5.6-terra'
              )
        LOOP
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                    'plan_model_migration', grant_row.id,
                    'retire DeepSeek V4 Flash from paid plan grant set');
        END LOOP;

        UPDATE commercial_quota_grants
        SET revoked_at = CURRENT_TIMESTAMP,
            expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
        WHERE uid = package.uid AND plan_id = package.plan_id
          AND grant_type = package.grant_type AND source_ref = package.source_ref
          AND revoked_at IS NULL
          AND model IN (
              'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash',
              'deepseek-flash', 'glm-5.3-flash', 'gpt-5.6-terra'
          );

        paid_amount := CASE package.plan_slug WHEN 'catsco-personal' THEN 2100 WHEN 'catsco-pro' THEN 6300 END;
        FOREACH paid_model IN ARRAY ARRAY[
            'MiniMax-M2.7', 'MiniMax-M3', 'deepseek-flash',
            'glm-5.3-flash', 'gpt-5.6-terra'
        ]
        LOOP
            INSERT INTO commercial_quota_grants(
                uid, plan_id, invite_code_id, grant_type, model, amount_cny,
                reset_duration, effective_at, expires_at, source_ref, note, operator_uid
            ) VALUES (
                package.uid, package.plan_id, package.invite_code_id, package.grant_type,
                paid_model, paid_amount, '1M', package.effective_at, package.expires_at,
                package.source_ref, 'paid plan DeepSeek V4 retirement migration', package.operator_uid
            ) RETURNING id INTO new_grant_id;
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, paid_model, paid_amount, 'grant',
                    'plan_model_migration', new_grant_id, package.plan_name);
        END LOOP;
    END LOOP;
END $$;

-- Free drops the retired model; the remaining nine models keep their amounts.
UPDATE commercial_plans
SET model_budgets = '{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-flash":100,"glm-5.3-flash":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb
WHERE slug = 'catsco-free'
  AND model_budgets = '{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"deepseek-flash":100,"glm-5.3-flash":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb;

DO $$
DECLARE
    grant_row RECORD;
BEGIN
    FOR grant_row IN
        SELECT id, uid, model, amount_cny
        FROM commercial_quota_grants
        WHERE grant_type = 'free' AND model = 'deepseek-v4-flash' AND revoked_at IS NULL
    LOOP
        INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
        VALUES (grant_row.uid, grant_row.model, -grant_row.amount_cny, 'revoke',
                'free', grant_row.id, 'retire DeepSeek V4 Flash from free grant set');
        UPDATE commercial_quota_grants
        SET revoked_at = CURRENT_TIMESTAMP,
            expires_at = LEAST(COALESCE(expires_at, CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
        WHERE id = grant_row.id;
    END LOOP;
END $$;

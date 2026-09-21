-- Open the image lane to the Free plan: the five image-lane models join the
-- free chat model set at 100 CNY per model, the same per-model amount the
-- Personal plan grants, so every public plan can use image generation.
-- The add-on raises the advertised Free pooled allowance by 500 (mirroring
-- how 000022 added DeepSeek Flash to Free). Existing free grants are left
-- untouched; each image model is granted exactly once.
UPDATE commercial_plans
SET model_budgets = '{"MiniMax-M2.7":1000,"MiniMax-M3":500,"deepseek-v4-flash":100,"deepseek-flash":100,"glm-5.3-flash":100,"gpt-image-2":100,"gpt-image-2.5":100,"gpt-image-2.5-flare":100,"gpt-image-2.5-sunburst":100,"chatgpt-image-latest":100}'::jsonb
WHERE slug = 'catsco-free';

WITH inserted AS (
    INSERT INTO commercial_quota_grants(
        uid, plan_id, grant_type, model, amount_cny, reset_duration,
        effective_at, expires_at, source_ref, note
    )
    SELECT e.uid, e.plan_id, 'free', image_model, 100, '1M',
           e.starts_at, e.expires_at, e.source_ref, 'CatsCo free image model access'
    FROM commercial_entitlements e
    JOIN commercial_plans p ON p.id = e.plan_id
    CROSS JOIN unnest(ARRAY[
        'gpt-image-2', 'gpt-image-2.5', 'gpt-image-2.5-flare',
        'gpt-image-2.5-sunburst', 'chatgpt-image-latest'
    ]) AS image_lane(image_model)
    WHERE p.slug = 'catsco-free'
      AND e.source = 'free'
      AND e.state = 'active'
      AND e.starts_at <= CURRENT_TIMESTAMP
      AND (e.expires_at IS NULL OR e.expires_at > CURRENT_TIMESTAMP)
      AND NOT EXISTS (
          SELECT 1 FROM commercial_quota_grants g
          WHERE g.uid = e.uid AND g.plan_id = e.plan_id
            AND g.grant_type = 'free' AND g.model = image_lane.image_model
            AND g.source_ref = e.source_ref AND g.revoked_at IS NULL
            AND g.effective_at <= CURRENT_TIMESTAMP
            AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
      )
    RETURNING id, uid, model, amount_cny
)
INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
SELECT uid, model, amount_cny, 'grant', 'free', id, 'Free image model access'
FROM inserted;

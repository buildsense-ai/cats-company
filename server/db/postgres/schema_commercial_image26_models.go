package postgres

// Image 2.6 lane: the 2.6 image family joins the Free pool and the paid image
// packages at 100 CNY per model, the per-model image amount 000021 (paid) and
// 000024 (Free) established. Only the three new budget keys are written, with
// the jsonb || operator, so a plan whose budgets an operator edited in place
// keeps every other key; paid packages gain one grant per new model at the
// grant type, interval and source_ref they already carry, and Free
// entitlements gain a free grant per model. Both grant paths are guarded, so
// the migration is repeatable.
//
// This constant is kept byte-identical to the numbered migration: the model-set
// migrations are no longer part of the startup statement list (they reverted
// operator edits on restart), so the numbered file is the only thing applied
// and this copy exists for the parity test and for reference. See
// schema.go's statement list comment.
const migrateCommercialPlansImage26Models = `-- Add the 2.6 image family (gpt-image-2.6, gpt-image-2.6-flare,
-- gpt-image-2.6-sunburst) to the Free pool and to the paid image packages.
-- Each new image model carries 100 CNY, the per-model image amount 000021 and
-- 000024 established (Personal 100 / Pro 300 at the time, Free 100), so the
-- advertised totals rise by 300 per package: Free 2200 -> 2500.
--
-- Only the three new keys are written. The plans' other budgets are preserved
-- with the jsonb || operator, because the stored maps have been edited in place:
-- production catsco-free currently sells a single MiniMax-M3 budget in the
-- control plane, and a literal rewrite would silently revert that data.
--
-- Take a transaction-scoped advisory lock so two concurrent runs cannot both
-- pass the NOT EXISTS guards below and double the grants.
SELECT pg_advisory_xact_lock(hashtextextended('catsco_commercial_image_26_models_v1', 0));

UPDATE commercial_plans
SET model_budgets = model_budgets || '{"gpt-image-2.6":100,"gpt-image-2.6-flare":100,"gpt-image-2.6-sunburst":100}'::jsonb
WHERE slug IN ('catsco-free', 'catsco-personal', 'catsco-pro', 'catsco-internal-all-models-50k');

-- Free: one free grant per new image model for every active free entitlement,
-- mirroring 000024. The (uid, plan, grant_type, model, source_ref) guard keeps
-- the migration repeatable on every run.
WITH inserted AS (
    INSERT INTO commercial_quota_grants(
        uid, plan_id, grant_type, model, amount_cny, reset_duration,
        effective_at, expires_at, source_ref, note
    )
    SELECT e.uid, e.plan_id, 'free', image_model, 100, '1M',
           e.starts_at, e.expires_at, e.source_ref, 'CatsCo free image 2.6 model access'
    FROM commercial_entitlements e
    JOIN commercial_plans p ON p.id = e.plan_id
    CROSS JOIN unnest(ARRAY[
        'gpt-image-2.6', 'gpt-image-2.6-flare', 'gpt-image-2.6-sunburst'
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
SELECT uid, model, amount_cny, 'grant', 'free', id, 'Free image 2.6 model access'
FROM inserted;

-- Paid packages: one grant per new image model at 100 CNY, mirroring the
-- grant_type, source_ref and interval the package already holds (000021 wrote
-- the order/invite/operator_plan image grants exactly this way). Manual grants
-- are excluded so an operator's hand-tuned quota is never extended. Packages
-- are discovered from their live grants, so a plan nobody holds produces none.
DO $$
DECLARE
    package RECORD;
    image_model TEXT;
    new_grant_id BIGINT;
BEGIN
    FOR package IN
        SELECT DISTINCT g.uid, g.plan_id, g.grant_type, g.source_ref,
               g.effective_at, g.expires_at, g.reset_duration,
               g.invite_code_id, g.operator_uid
        FROM commercial_quota_grants g JOIN commercial_plans p ON p.id = g.plan_id
        WHERE p.slug IN ('catsco-personal', 'catsco-pro', 'catsco-internal-all-models-50k')
          AND g.grant_type IN ('order', 'invite', 'operator_plan')
          AND g.revoked_at IS NULL
          AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
    LOOP
        FOREACH image_model IN ARRAY ARRAY[
            'gpt-image-2.6', 'gpt-image-2.6-flare', 'gpt-image-2.6-sunburst'
        ] LOOP
            IF EXISTS (
                SELECT 1 FROM commercial_quota_grants g
                WHERE g.uid = package.uid AND g.plan_id = package.plan_id
                  AND g.grant_type = package.grant_type AND g.model = image_model
                  AND g.source_ref = package.source_ref AND g.revoked_at IS NULL
            ) THEN
                CONTINUE;
            END IF;
            INSERT INTO commercial_quota_grants(
                uid, plan_id, invite_code_id, grant_type, model, amount_cny,
                reset_duration, effective_at, expires_at, source_ref, note, operator_uid
            ) VALUES (
                package.uid, package.plan_id, package.invite_code_id, package.grant_type,
                image_model, 100, package.reset_duration, package.effective_at,
                package.expires_at, package.source_ref,
                'Public plan image 2.6 model access', package.operator_uid
            ) RETURNING id INTO new_grant_id;
            INSERT INTO commercial_quota_ledger(uid, model, amount_cny, entry_type, source_type, source_id, note)
            VALUES (package.uid, image_model, 100, 'grant',
                    'image_models_v1', new_grant_id, 'Public plan image 2.6 model access');
        END LOOP;
    END LOOP;
END $$;
`

// rollbackCommercialPlansImage26Models drops only the three new budget keys and
// revokes only the grants this migration stamped, so every other budget key,
// grant and usage row survives a rollback.
const rollbackCommercialPlansImage26Models = `-- Roll back the 2.6 image add-on: drop only the three new budget keys and
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

`

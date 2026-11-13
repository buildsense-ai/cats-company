-- Roll back 000072_cloud_worker_retain_data.
--
-- Both columns are additive with a FALSE default, so dropping them returns a
-- pre-upgrade schema that still holds every lifecycle row, paid window and
-- credit. What is lost is the retention intent itself: a worker provisioned as
-- a credit environment becomes an ordinary row again and would be scheduled for
-- cleanup at its next expiry. Destroy or re-flag those environments deliberately
-- before rolling this back.
ALTER TABLE cloud_worker_credits DROP COLUMN IF EXISTS retain_data;
ALTER TABLE cloud_worker_lifecycles DROP COLUMN IF EXISTS retain_data;

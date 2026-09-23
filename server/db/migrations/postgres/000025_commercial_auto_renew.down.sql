-- Roll back internal auto-renew: drop the run log first, then the opt-in
-- switch. Renewal ledger rows and grants created by the runner stay as they
-- are; uninstalling the scheduler never rewrites commercial history.
DROP INDEX IF EXISTS idx_commercial_auto_renew_runs_uid;
DROP TABLE IF EXISTS commercial_auto_renew_runs;
DROP TABLE IF EXISTS commercial_auto_renew_configs;

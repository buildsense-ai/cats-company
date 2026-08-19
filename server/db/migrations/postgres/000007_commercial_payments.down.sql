DROP TABLE IF EXISTS commercial_managed_relay_budgets;
DROP TABLE IF EXISTS commercial_payment_events;
DROP TABLE IF EXISTS commercial_orders;
DROP INDEX IF EXISTS uk_commercial_entitlements_order_once;
DROP INDEX IF EXISTS uk_commercial_entitlements_trial_once;
ALTER TABLE commercial_plans DROP COLUMN IF EXISTS purchase_limit;
ALTER TABLE commercial_plans DROP COLUMN IF EXISTS sale_state;
ALTER TABLE commercial_plans DROP COLUMN IF EXISTS currency;
ALTER TABLE commercial_plans DROP COLUMN IF EXISTS price_fen;

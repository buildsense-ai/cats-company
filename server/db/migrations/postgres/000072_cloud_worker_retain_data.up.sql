-- Approved credit rules (docs/credit-plan-rules-20261109.md, P1-2): a credit
-- environment keeps its environment, files and history with no cleanup
-- deadline. Running out of credit or letting the package lapse may pause the
-- machine but must never delete existing data; only an explicit user/admin
-- deletion does.
--
-- The fact is stored, not derived. expiry_mode='follow_plan' is NOT the marker:
-- a legacy shared package also follows its package, and those rows keep the
-- retention window they were sold with. The marker is set once, from the credit
-- that actually funded the worker (a package with model_credit_units > 0, the
-- order/entitlement snapshot, or an operator-adopted worker).
--
-- Both halves are additive with a FALSE default, so every existing row keeps
-- exactly the behaviour it has today. No backfill is part of this migration:
-- production carries no credit-funded cloud worker yet, and guessing a marker
-- for historical rows would either delete data the product promised to keep or
-- promise storage nobody bought.
ALTER TABLE cloud_worker_lifecycles ADD COLUMN IF NOT EXISTS retain_data BOOLEAN NOT NULL DEFAULT FALSE;

-- The credit carries the same flag so it survives the trip from purchase to the
-- provisioning call, including credits that outlive the plan row they came from
-- (a retired package stays spendable).
ALTER TABLE cloud_worker_credits ADD COLUMN IF NOT EXISTS retain_data BOOLEAN NOT NULL DEFAULT FALSE;

-- Read paths: the sweeper lists retained rows to pause them, and the cluster
-- fallback reaper needs the marker per tenant. Neither scans by retain_data
-- alone today, so no new index is introduced here.

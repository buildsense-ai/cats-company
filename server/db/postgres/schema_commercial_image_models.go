package postgres

// Historical (2026-08): the image lane joined the paid plans, at 100 CNY per
// model on Personal and 300 on Pro. Superseded by the relay-catalog reconcile;
// kept as an ordered placeholder.

const migrateCommercialImageModels = `
-- Historical migration, now a no-op: The image lane joined the paid plans.
--
-- The plan model sets used to be written here as literal JSON, which
-- meant every relay model launch needed a control-plane change and each
-- startup re-applied the same list, silently reverting a hand-edited
-- plan. The relay catalog is the source of truth now and the startup
-- reconcile keeps the paid plans in step with it, so this migration is
-- kept only as a named, ordered placeholder. It stays in the statement
-- list because the ordering comment in schema.go still refers to it.
SELECT 1;
`

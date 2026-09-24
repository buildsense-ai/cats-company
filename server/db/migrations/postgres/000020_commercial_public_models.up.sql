
-- Historical migration, now a no-op: The public catalog replaced the early model set.
--
-- The plan model sets used to be written here as literal JSON, which
-- meant every relay model launch needed a control-plane change and each
-- startup re-applied the same list, silently reverting a hand-edited
-- plan. The relay catalog is the source of truth now and the startup
-- reconcile keeps the paid plans in step with it, so this migration is
-- kept only as a named, ordered placeholder. It stays in the statement
-- list because the ordering comment in schema.go still refers to it.
SELECT 1;

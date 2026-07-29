-- Deliberately irreversible: this migration reconciles ambiguous historical
-- version-2 state and must not drop commercial tables that may predate it.
SELECT 1;

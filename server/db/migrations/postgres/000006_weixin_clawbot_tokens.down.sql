-- Deliberately irreversible: some databases created this table under the
-- historical conflicting version 000002. Rolling version 000006 back must not
-- delete a table that predates the corrected migration number.
SELECT 1;

# CatsCompany PostgreSQL Migrations

This directory is the public, versioned home for database schema migrations.

Current state:

- `postgres/000001_baseline` marks the production PostgreSQL schema that is still created by `server/db/postgres/schema.go`.
- `CreateSchema()` is the schema module: it creates and safely extends ordinary tables, indexes, constraints, and triggers when the service starts.
- Keep ordinary additive schema changes in that one module. Do not duplicate the same table or column in both `schema.go` and an SQL migration.
- Add a numbered SQL migration only when a release needs a separately controlled data transformation or an operation that cannot be safely expressed as idempotent schema initialization.
- Do not edit an already-applied migration. Add a new one.
- MySQL is not part of the migration system. Keep MySQL compatibility fixes in code only unless the product explicitly reintroduces MySQL migrations.

Sensitive values never belong here:

- real database URLs or passwords
- `.pgpass`, `.my.cnf`, private keys, or service tokens
- production backup dumps
- command output that contains a full DSN

Use `scripts/db-migrate.sh` with a server-local environment file for real runs.

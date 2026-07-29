# CatsCompany PostgreSQL Migrations

This directory is the public, versioned home for database schema migrations.

Current state:

- `postgres/000001_baseline` marks the production PostgreSQL schema that is still created by `server/db/postgres/schema.go`.
- New production schema changes should be added as new numbered SQL migrations.
- Do not edit an already-applied migration. Add a new one.
- MySQL is not part of the migration system. Keep MySQL compatibility fixes in code only unless the product explicitly reintroduces MySQL migrations.

Historical note:

- Two independent branches introduced different `000002` migrations. The earlier commercial migration keeps version 2, the later Weixin migration is version 6, and version 7 idempotently reconciles the commercial schema for databases whose recorded version 2 came from the Weixin branch.
- The version-6 and version-7 down migrations are intentionally no-ops because reconciliation must never delete schema that may have existed before migration tracking was corrected.

Sensitive values never belong here:

- real database URLs or passwords
- `.pgpass`, `.my.cnf`, private keys, or service tokens
- production backup dumps
- command output that contains a full DSN

Use `scripts/db-migrate.sh` with a server-local environment file for real runs.

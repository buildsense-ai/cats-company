# Production Docker Deploy

This stack is the production-side Docker deployment scaffold intended for a
server path such as `/srv/catscompany-prod`.

It is designed to deploy the exact same GHCR image tag that has already passed
the test deployment workflow.

This production scaffold runs the GHCR image behind host nginx. Keep the stack
root as `/srv/catscompany-prod` so the existing GitHub Actions deployment
workflow can continue to upload compose, env, and release files to the expected
location.

Default ports bind to `127.0.0.1` and should be published through the host nginx
instead of exposed directly to the internet:

- API: `26061`
- gRPC: `26062`
- Web: `28080`

The database is external to this compose stack and is configured through
`OC_DB_DRIVER` and `OC_DB_DSN`. Current production uses PostgreSQL; fill the
real host and password in `prod.env`.

```env
OC_DB_DRIVER=postgres
OC_DB_DSN=postgres://catsco:***@postgres.internal:5432/catsco?sslmode=prefer
```

## Required server files

Before enabling automatic production deploys:

1. Run `deploy/prod/bootstrap-server.sh` on the server, or let the workflow
   create the directories automatically.
2. Create `<prod-stack-root>/env/prod.env`
3. Copy values from `deploy/prod/env.prod.example`
4. Keep `PROD_STACK_ROOT=<prod-stack-root>`
5. Fill real secrets in `prod.env`
6. Point `OC_DB_DSN` at the active database and set `OC_DB_DRIVER`

## Manual start

```bash
cd /srv/catscompany-prod/compose
docker compose --env-file /srv/catscompany-prod/env/prod.env pull
docker compose --env-file /srv/catscompany-prod/env/prod.env up -d
```

## Manual rollback

```bash
bash deploy/prod/remote-rollback.sh /srv/catscompany-prod
```

# Production Docker Deploy

This stack is the production-side Docker deployment scaffold intended for a
server path such as `/srv/catscompany-prod`.

It is designed to deploy the exact same GHCR image tag that has already passed
the test deployment workflow.

This production scaffold is designed to reuse the existing production MySQL so
application data stays consistent. Only the application layer is containerized
here.

Default ports are intentionally non-conflicting so the first rollout can run as
an isolated shadow stack:

- API: `26061`
- gRPC: `26062`
- Web: `28080`

The main repository can later change these values in `prod.env` or move traffic
through the host nginx once the production cutover plan is confirmed.

The default `OC_DB_DSN` example points to `host.docker.internal:3306`, which is
appropriate when the existing production MySQL is already published on the host.

## Required server files

Before enabling automatic production deploys:

1. Run `deploy/prod/bootstrap-server.sh` on the server, or let the workflow
   create the directories automatically.
2. Create `<prod-stack-root>/env/prod.env`
3. Copy values from `deploy/prod/env.prod.example`
4. Keep `PROD_STACK_ROOT=<prod-stack-root>`
5. Fill real secrets in `prod.env`
6. Point `OC_DB_DSN` at the existing production MySQL

## Manual start

```bash
cd /srv/catscompany-prod/compose
/usr/local/bin/docker-compose --env-file /srv/catscompany-prod/env/prod.env pull
/usr/local/bin/docker-compose --env-file /srv/catscompany-prod/env/prod.env up -d
```

## Manual rollback

```bash
bash deploy/prod/remote-rollback.sh /srv/catscompany-prod
```

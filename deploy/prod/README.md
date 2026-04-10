# Production Docker Deploy

This stack is the production-side Docker deployment scaffold intended for a
server path such as `/srv/catscompany-prod`.

It is designed to deploy the exact same GHCR image tag that has already passed
the test deployment workflow.

Default ports are intentionally non-conflicting so the first rollout can run as
an isolated shadow stack:

- MySQL: `23306`
- API: `26061`
- gRPC: `26062`
- Web: `28080`

The main repository can later change these values in `prod.env` or move traffic
through the host nginx once the production cutover plan is confirmed.

## Required server files

Before enabling automatic production deploys:

1. Run `deploy/prod/bootstrap-server.sh` on the server, or let the workflow
   create the directories automatically.
2. Create `<prod-stack-root>/env/prod.env`
3. Copy values from `deploy/prod/env.prod.example`
4. Keep `PROD_STACK_ROOT=<prod-stack-root>`
5. Fill real secrets in `prod.env`

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

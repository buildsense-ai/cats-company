# Test Docker Deploy

This stack is the isolated Docker test environment intended for a server path
such as `/srv/catscompany-test`.

It is intentionally separated from production:

- data lives under `<test-stack-root>/data`
- compose runtime files live under `<test-stack-root>/compose`
- ports are isolated and bind to `127.0.0.1` by default:
  - MySQL: `13306`
  - API: `16061`
  - gRPC: `16062`
  - Web: `18080`

The test deploy workflow builds images in GitHub Actions, pushes them to GHCR,
and lets the server pull and run those images. The server no longer builds the
application source for this test stack.

## Required server files

Before running the deploy workflow for the first time:

1. Run `deploy/test/bootstrap-server.sh` on the server, or let the workflow
   create the directories automatically.
2. Create `<test-stack-root>/env/test.env`
3. Copy values from `deploy/test/env.test.example`
4. Keep `TEST_STACK_ROOT=<test-stack-root>`
5. Fill real secrets in `test.env`

The deploy workflow only touches the configured test stack root and uses:

- Docker Compose (`docker compose` plugin or legacy `docker-compose`)
- `<test-stack-root>/compose`
- `<test-stack-root>/env`
- `<test-stack-root>/data`

It does not touch production directories.

## PostgreSQL test

To run the test stack against PostgreSQL, copy
`deploy/test/env.test.postgres.example` to `<test-stack-root>/env/test.env` and
fill real secrets.

Important values:

```env
COMPOSE_PROFILES=
OC_DB_DRIVER=postgres
OC_DB_DSN=postgres://catsco:***@postgres.internal:5432/catsco?sslmode=prefer
```

Leaving `COMPOSE_PROFILES` empty prevents the local MySQL profile from starting.
The default `env.test.example` keeps the MySQL profile for local isolated tests.

## GitHub secrets

The current workflow expects:

- `SSH_HOST`
- `SSH_USER`
- `SSH_PRIVATE_KEY`
- `GHCR_USERNAME`
- `GHCR_TOKEN`

`GHCR_USERNAME` / `GHCR_TOKEN` should be able to pull packages from
`ghcr.io/<owner>/cats-company-*`. A PAT with `read:packages` is enough for the
server side pull. The workflow itself pushes images with the repository
`GITHUB_TOKEN`.

## Manual start

Run on the server:

```bash
cd /srv/catscompany-test/compose
docker compose --env-file /srv/catscompany-test/env/test.env pull
docker compose --env-file /srv/catscompany-test/env/test.env up -d
```

## Manual stop

```bash
cd /srv/catscompany-test/compose
docker compose --env-file /srv/catscompany-test/env/test.env down
```

## Manual deploy of a revision

```bash
GHCR_OWNER=<github-owner> GHCR_USERNAME=<ghcr-user> GHCR_TOKEN=<ghcr-token> \
  bash deploy/test/remote-deploy.sh /srv/catscompany-test <sha>
```

## Check current status

```bash
bash deploy/test/remote-status.sh /srv/catscompany-test
```

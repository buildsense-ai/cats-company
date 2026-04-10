# CI/CD Handoff

This document describes the current CI/CD draft on branch `chore/pr-ready-test-deploy`
and what the upstream repository needs to do after merging it.

## Current status

This branch is ready to open a PR.

What has been validated already:

- `go test ./server/...`
- `docker compose -f deploy/test/docker-compose.yml --env-file deploy/test/env.test.example config`
- `docker compose -f deploy/prod/docker-compose.yml --env-file deploy/prod/env.prod.example config`
- The fork test workflow path has already been verified end-to-end on a real server:
  - build images in GitHub Actions
  - push to GHCR
  - pull and run on the server

What is still a draft:

- `deploy/prod` and `deploy-prod.yml`
- production traffic cutover
- production nginx switching

## What this PR adds

1. CI workflow:
   - `.github/workflows/ci.yml`
2. Test deployment scaffold:
   - `deploy/test/*`
   - `.github/workflows/deploy-test.yml`
3. Production deployment scaffold:
   - `deploy/prod/*`
   - `.github/workflows/deploy-prod.yml`

## Deployment model

The intended model is:

1. Code lands on `main`
2. `Deploy Docker Test` runs automatically
3. GitHub Actions builds and pushes GHCR images tagged with the commit SHA
4. Test server pulls and runs that exact SHA
5. If the test workflow completes successfully, `Deploy Docker Prod` is triggered
6. Production pulls the exact same SHA

Important:

- Test and prod must use the same image SHA
- Prod does not rebuild source code on the server
- Prod reuses the existing MySQL container/data

## Server directory layout

Recommended layout after merge:

```text
/srv/
  catscompany-test/
    compose/
    env/
      test.env
      env.test.example
    data/
      mysql/
      uploads/
    logs/
    CURRENT_REVISION
    releases/

  catscompany-prod/
    compose/
    env/
      prod.env
      env.prod.example
    data/
      uploads/
    logs/
    CURRENT_REVISION
    PREVIOUS_REVISION
```

Existing directories should be kept as rollback safety until the new flow is stable:

- `/root/catscompany`
- `/root/catscompany-test`
- `/root/text/catscompany-docker-test`

## GitHub environments and secrets

Create two environments in the upstream repository:

- `test`
- `production`

### `test` environment secrets

- `SSH_HOST`
- `SSH_USER`
- `SSH_PRIVATE_KEY`
- `GHCR_USERNAME`
- `GHCR_TOKEN`

### `production` environment secrets

- `SSH_HOST`
- `SSH_USER`
- `SSH_PRIVATE_KEY`
- `GHCR_USERNAME`
- `GHCR_TOKEN`

Notes:

- If full automation is desired, do not require manual approval on `production`
- `GHCR_TOKEN` only needs package pull access on the server side

## Server-side files to prepare

### Test

Create:

- `/srv/catscompany-test/env/test.env`

Start from:

- `deploy/test/env.test.example`

Fill at least:

- `GHCR_OWNER`
- `MYSQL_ROOT_PASSWORD`
- `MYSQL_PASSWORD`
- `BOT_ASSISTANT_PASSWORD`
- `OC_JWT_SECRET`

### Prod

Create:

- `/srv/catscompany-prod/env/prod.env`

Start from:

- `deploy/prod/env.prod.example`

Fill at least:

- `GHCR_OWNER`
- `OC_JWT_SECRET`
- `OC_DB_DSN`

## Production database note

Production is intentionally configured to reuse the existing MySQL instead of
creating a new one.

The default production example expects the current MySQL container to remain
published on the host:

```text
OC_DB_DSN=openchat:<password>@tcp(host.docker.internal:3306)/openchat?parseTime=true&charset=utf8mb4
```

This means:

- production app containers are Dockerized
- the production MySQL remains the existing persistent container/infrastructure
- database data stays consistent

## Ports

### Test defaults

- MySQL: `13306`
- API: `16061`
- gRPC: `16062`
- Web: `18080`

### Prod defaults

- API: `26061`
- gRPC: `26062`
- Web: `28080`

Production defaults are shadow ports. This PR does not automatically switch
public traffic to the new prod stack.

## Expected first rollout sequence upstream

1. Merge this PR
2. Configure `test` environment secrets
3. Prepare `/srv/catscompany-test/env/test.env`
4. Let `main` trigger `Deploy Docker Test`
5. Verify:
   - GHCR packages created under the upstream owner
   - test stack is healthy
6. Configure `production` environment secrets
7. Prepare `/srv/catscompany-prod/env/prod.env`
8. Verify prod can reach the existing MySQL through `OC_DB_DSN`
9. Allow `Deploy Docker Prod` to run
10. Verify the prod shadow stack on `26061` / `28080`
11. Separately decide how nginx should switch traffic

## Rollback

Production scaffold includes:

- `deploy/prod/remote-rollback.sh`

It rolls back to `PREVIOUS_REVISION` by restoring the previous image SHA in
`prod.env` and running compose again.

## Current limitations

- Prod has not been server-validated yet in the fork
- Nginx traffic cutover is not automated in this PR
- Database migrations still rely on the app startup path in `server/cmd/server.go`
- This PR focuses on image-based deploy automation, not full infrastructure migration

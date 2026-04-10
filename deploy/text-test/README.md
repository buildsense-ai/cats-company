# Text Docker Test

This stack is the isolated Docker test environment used on the server under
`/root/text/catscompany-docker-test`.

It is intentionally separated from production:

- data lives under `/root/text/catscompany-docker-test/data`
- compose runtime files live under `/root/text/catscompany-docker-test/compose`
- ports are isolated:
  - MySQL: `13306`
  - API: `16061`
  - gRPC: `16062`
  - Web: `18080`

The test deploy workflow now builds images in GitHub Actions, pushes them to
GHCR, and lets the server pull and run those images. The server no longer
builds the application source for this test stack.

## Required server files

Before running the deploy workflow for the first time:

1. Run `deploy/text-test/bootstrap-server.sh` on the server, or let the workflow
   create the directories automatically.
2. Create `/root/text/catscompany-docker-test/env/text-test.env`
3. Copy values from `deploy/text-test/text-test.env.example`
4. Keep `TEXT_STACK_ROOT=/root/text/catscompany-docker-test`
5. Fill real secrets in `text-test.env`

The deploy workflow only touches `/root/text/...` and uses:

- `/root/text/bin/docker-compose`
- `/root/text/catscompany-docker-test/compose`
- `/root/text/catscompany-docker-test/env`
- `/root/text/catscompany-docker-test/data`

It does not touch production directories.

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
cd /root/text/catscompany-docker-test/compose
/root/text/bin/docker-compose --env-file /root/text/catscompany-docker-test/env/text-test.env pull
/root/text/bin/docker-compose --env-file /root/text/catscompany-docker-test/env/text-test.env up -d
```

## Manual stop

```bash
cd /root/text/catscompany-docker-test/compose
/root/text/bin/docker-compose --env-file /root/text/catscompany-docker-test/env/text-test.env down
```

## Manual deploy of a revision

```bash
GHCR_OWNER=<github-owner> GHCR_USERNAME=<ghcr-user> GHCR_TOKEN=<ghcr-token> \
  bash deploy/text-test/remote-deploy.sh /root/text/catscompany-docker-test <sha>
```

## Check current status

```bash
bash deploy/text-test/remote-status.sh /root/text/catscompany-docker-test
```

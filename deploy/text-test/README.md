# Text Docker Test

This stack is the isolated Docker test environment used on the server under
`/root/text/catscompany-docker-test`.

It is intentionally separated from production:

- data lives under `/root/text/catscompany-docker-test/data`
- compose runtime files live under `/root/text/catscompany-docker-test/compose`
- app releases live under `/root/text/catscompany-docker-test/app/releases`
- ports are isolated:
  - MySQL: `13306`
  - API: `16061`
  - gRPC: `16062`
  - Web: `18080`

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
- `/root/text/catscompany-docker-test/releases`
- `/root/text/catscompany-docker-test/app/releases`
- `/root/text/catscompany-docker-test/data`

It does not touch production directories.

## GitHub secrets

The current workflow expects:

- `SSH_HOST`
- `SSH_USER`
- `SSH_PRIVATE_KEY`

## Manual start

Run on the server:

```bash
cd /root/text/catscompany-docker-test/compose
/root/text/bin/docker-compose --env-file /root/text/catscompany-docker-test/env/text-test.env up -d --build
```

## Manual stop

```bash
cd /root/text/catscompany-docker-test/compose
/root/text/bin/docker-compose --env-file /root/text/catscompany-docker-test/env/text-test.env down
```

## Manual deploy of a revision

After uploading `cats-company-<sha>.tar.gz` into `releases/`:

```bash
bash deploy/text-test/remote-deploy.sh /root/text/catscompany-docker-test <sha>
```

## Check current status

```bash
bash deploy/text-test/remote-status.sh /root/text/catscompany-docker-test
```

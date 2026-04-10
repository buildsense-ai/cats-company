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

1. Create `/root/text/catscompany-docker-test/env/text-test.env`
2. Copy values from `deploy/text-test/text-test.env.example`
3. Keep `TEXT_STACK_ROOT=/root/text/catscompany-docker-test`
3. If `/root/text/bin/docker-compose` is missing, the workflow will install it
   without touching system-wide Docker Compose

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

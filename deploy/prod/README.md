# Production Docker Deploy

This stack is the production-side Docker deployment scaffold intended for a
server path such as `/srv/catscompany-prod`.

It is designed to deploy the exact same GHCR image tag that has already passed
the test deployment workflow.

This production scaffold runs the GHCR image behind host nginx. Keep the stack
root as `/srv/catscompany-prod` so the existing GitHub Actions deployment
workflow can continue to upload compose, env, and release files to the expected
location.

The production deploy also reconciles only `proxy_read_timeout` and
`proxy_send_timeout` inside the TLS `app.catsco.cc` `/v1/` location. It does not
replace the host site file, so unrelated host-only routes remain intact. The
update keeps a `.catsco-image-timeout.bak` copy, runs `nginx -t`, and restores
the previous config if validation or reload fails. When the SSH deploy user is
not root, the updater requires non-interactive passwordless `sudo` and refuses
to prompt during a deployment.

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

## Image generation gateway

Keep image-provider credentials only under the persistent server root. For the
race gateway, create `/srv/catscompany-prod/secrets/image-providers.json` from
`deploy/prod/image-providers.example.json`, configure exactly three providers,
and make it
readable only by the deployment administrator:

```bash
chmod 600 /srv/catscompany-prod/secrets/image-providers.json
```

Then point the container at the mounted file from the persistent
`/srv/catscompany-prod/env/prod.env`:

```env
CATSCO_IMAGE_UPSTREAMS_FILE=/run/catsco-secrets/image-providers.json
CATSCO_IMAGE_MODEL=gpt-image-2
CATSCO_IMAGE_TIMEOUT_SECONDS=260
CATSCO_IMAGE_RACE_DEADLINE_SECONDS=270
CATSCO_IMAGE_RACE_BACKOFF_MS=750
CATSCO_IMAGE_RACE_MAX_ATTEMPTS_PER_PROVIDER=2
CATSCO_IMAGE_EDIT_MAX_REQUEST_BYTES=25165824
CATSCO_IMAGE_MAX_RESPONSE_BYTES=41943040
```

The deployment scripts create and preserve the `secrets` directory across
releases, and Compose mounts it read-only at `/run/catsco-secrets`. Do not copy
the real provider file into the repository, image, deployment bundle, or GitHub
Actions. After changing it, recreate the server container with the manual start
commands below.

Every configured provider must set `generation_url`, `edit_url`, and an explicit
`edit_transport`. Use `json_data_url` for an upstream that accepts the CatsCo
JSON reference format and `multipart` for an OpenAI-compatible file upload.
The gateway removes `async` and accepts only a completed image response, so a
task ID never wins the race. Both provider lanes start together, which means a
single user request can create one billable request at each provider even when
the slower request is cancelled locally. Explicit HTTP 429 and 5xx responses
can be retried within the configured attempt bound. Network errors, timeouts,
and invalid 200 responses are not retried because the provider may already have
accepted or billed the job without returning a trustworthy status.
`CATSCO_IMAGE_RACE_MAX_ATTEMPTS_PER_PROVIDER` defaults to 2 and is hard-capped
at 4. With three providers, the default absolute request bound is six provider
calls. The race also stops when `CATSCO_IMAGE_RACE_DEADLINE_SECONDS` expires.
The deadline is capped at 285 seconds so the gateway can return a structured
failure before the caller's roughly 300-second connection budget ends.

For rollback, clear `CATSCO_IMAGE_UPSTREAMS_FILE` and restore the legacy
`CATSCO_IMAGE_UPSTREAM_URL`, `CATSCO_IMAGE_UPSTREAM_API_KEY` or
`CATSCO_IMAGE_UPSTREAM_API_KEY_FILE`, and `CATSCO_IMAGE_MODEL` values. The
legacy path is represented internally as a one-provider pool.

`CATSCO_IMAGE_EDIT_MAX_REQUEST_BYTES` limits only the JSON request containing
base64 references; its 24 MiB default remains below the bundled Nginx 32 MiB
body limit.

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

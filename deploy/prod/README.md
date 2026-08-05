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

## Web Push deployment secrets

Web Push is disabled by default, so these GitHub Environment secrets are not
required for a production deploy. To enable it, configure all three in `prod`:

- `VAPID_PUBLIC_KEY`
- `VAPID_PRIVATE_KEY`
- `VAPID_SUBJECT` (for example, `mailto:ops@catsco.cc`)

The deploy workflow sends the values over SSH standard input and atomically
updates the persistent `prod.env`; it does not put the private key in the
repository or a remote command line. It also hardens `prod.env` to owner-only
mode (`0600`). Supplying none of the three removes any earlier VAPID values and
keeps Web Push disabled; supplying only some is rejected. It retains the
existing first-deploy check, so `prod.env` must already contain the rest of its
real configuration. Keep the production key pair separate from test.

When the production host cannot reach browser push providers directly, also
configure these two `prod` environment secrets with the same values used by the
Cloudflare Worker relay:

- `CATSCO_PUSH_RELAY_URL` (for example, `https://push-relay.example.workers.dev/v1/push/relay`)
- `CATSCO_PUSH_RELAY_TOKEN`

They are synchronized over SSH standard input as a pair. Leaving both empty
removes an earlier relay configuration; supplying only one is rejected. The
Worker never receives the VAPID private key.

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

## Distributed artifact nodes

With no node registry configured, artifact management keeps using the legacy
`CATSCO_ARTIFACT_MANAGEMENT_URL` and `CATSCO_ARTIFACT_MANAGEMENT_TOKEN` only
when direct Artifact discovery is disabled.

Production enables one-server-per-Agent static discovery by default:

```env
CATSCO_DIRECT_ARTIFACT_URL_TEMPLATE=https://agent-{uid}.artifacts.catsco.fun:19991/artifacts
```

For Agent `535`, CatsCo reads:

```text
https://agent-535.artifacts.catsco.fun:19991/artifacts/artifacts-index.json
```

The template must use HTTPS, contain exactly one `{uid}` in the hostname, end
with `/artifacts`, and use a different origin from `CATSCO_PUBLIC_BASE_URL`.
Set `CATSCO_DIRECT_ARTIFACT_URL_TEMPLATE=` explicitly to disable this route.
An unresolved hostname or index HTTP 404 means the Agent has not published an
Artifact yet and returns an empty list. Connection failures, HTTP 5xx, invalid
JSON, invalid Artifact URLs, and wrong-host URLs remain errors.

Direct template nodes are list-only: CatsCo projects the selected Agent UID,
sets `can_delete=false` and `can_restore=false`, and does not send a management
token to the employee host.

To route each managed Agent to the artifact host on its deployment node, copy
`deploy/prod/artifact-nodes.example.json` to the persistent secrets directory
and set:

```env
CATSCO_ARTIFACT_NODES_FILE=/run/catsco-secrets/artifact-nodes.json
```

The JSON maps an Agent UID to one node. Every node declares its public artifact
base URL. A fully managed node also declares a protected management URL and
exactly one bearer-token source: `management_token_env` or
`management_token_file`. A static-only node may omit all three management
fields; CatsCo then reads
`<public_base_url>/by-agent/<uid>/artifacts-index.json`, verifies that every
artifact URL stays inside the same Agent namespace, does not offer delete or
restore, and returns an empty recycle bin. The token itself must not be written
into the JSON. Prefer a separate file under
`/run/catsco-secrets` for each managed node; the directory is already mounted
read-only in the server container.
Every `public_base_url` must use a different origin (scheme, host, or port) from
`CATSCO_PUBLIC_BASE_URL`. The server rejects a same-origin registry at startup
so executable Artifact HTML cannot share the CatsCo application origin.
For example:

```bash
printf '%s' '<node-b-token>' > /srv/catscompany-prod/secrets/artifact-node-b.token
chmod 600 /srv/catscompany-prod/secrets/artifact-node-b.token
```

`management_token_env` remains useful for the legacy node or local testing.
Several nodes may reference `CATSCO_ARTIFACT_MANAGEMENT_TOKEN` only when those
nodes intentionally share one service token.

Resolution order is: explicit Agent mapping, direct URL template, then legacy.
An explicit mapping can therefore override one exceptional Agent while all
other Agents use deterministic direct discovery. If the direct template is
disabled, an unmapped Agent fails closed by default. During a staged legacy
migration, set `"fallback_to_legacy": true` to keep those unmapped Agents on
`CATSCO_ARTIFACT_MANAGEMENT_URL`.
The registry is loaded at server startup, so changing a node, mapping, or token
file requires recreating the CatsCo server container. Changing the direct
template also requires recreating the container.

When a node registry or direct template is enabled, the old unscoped
`/api/artifacts` endpoint is disabled. All list, delete, and restore requests use
`/api/agents/{uid}/artifacts`, which prevents a request from silently falling
back to the legacy node.

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

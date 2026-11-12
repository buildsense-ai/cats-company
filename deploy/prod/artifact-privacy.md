# Private artifact delivery

The application authorizes an active human or bot by current access to an actual persisted message containing the resource. Knowing an artifact ID, agent ownership, source topic title, uploader metadata or the shared origin secret does not grant file access. Verified publishers have access to their own drafts only until binding; unknown historical provenance is denied.

## Application and worker contract

The application wires `FileAccessService` into uploads and `CloudArtifactHandler`, and registers:

- POST/DELETE `/api/files/media-session`: platform upload browser sessions.
- POST `/api/files/artifact-session`: authenticated minting of a 15-minute, single-artifact transport URL, optionally using a verified historical `message_id`.
- GET/HEAD `/api/artifacts/file-authorization`: `FileMediaAuth.ArtifactMiddleware` followed by `CloudArtifactHandler.HandleFileAuthorization`.

The worker calls the fixed authorization endpoint with `origin=<HTTPS artifact origin>` and `path=<escaped resource path>`. It supplies `X-Catsco-Artifact-Origin-Secret` plus either the requesting actor's explicit credential or `X-Catsco-Artifact-Media` containing the scoped media token. Explicit Authorization takes precedence. The server verifies the origin secret, active identity, token scope/expiry/revocation, actual stored message membership and current message-read permission. Only HTTP 204 authorizes bytes. Redirects, login HTML, other status codes, timeout and callback failure deny access. Callback cookies/credentials are sent only to this fixed endpoint, with redirects disabled.

Resource identity includes the configured HTTPS origin, namespace and artifact root:

```text
artifact:https://agent-42.example:19991/artifacts/demo
artifact:https://node.example/artifacts/by-agent/42/demo
artifact:https://node.example/by-agent/42/demo
```

Versions and subresources beneath the same root share a resource. Different origins and agent namespaces never alias. Aggregate indexes, empty/dot segments, traversal, backslashes and ambiguous encodings are denied. The worker also denies filesystem escape through symlinks/junctions.

On a valid `media_token` bootstrap request, the worker authorizes first, sets a host-only Secure HttpOnly SameSite=None cookie scoped to that artifact directory (10-minute cookie lifetime), and returns 303 stripping only `media_token`. Every subsequent HTML/JSON/JavaScript/CSS/image/font/manifest request, including HEAD and Range, performs authorization again. The token's server session lasts 15 minutes and remains subject to current permissions and parent revocation. A platform bearer credential is never placed in the artifact URL. Browsers blocking cross-site cookies may block assets; validate target browser behavior during integration. Denied requests do not retry anonymous URLs.

The gateway overwrites visitor origin/secret headers, removes spoofable artifact callback headers, forwards the actual request cookie/Authorization, disables artifact access logging and caching, and proxies to the protected worker. The worker performs the callback; the gateway does not separately cache an allow decision. The worker requires an explicit public base and never lets request headers choose its authorization origin. Missing public-base configuration denies files.

## Configuration

Application server:

```env
CATSCO_PUBLIC_BASE_URL=https://<application-host>
# Additional trusted upload origins, only when required:
CATSCO_FILE_TRUSTED_ORIGINS=https://<other-platform-origin>
# Additional canonical artifact origins:
CATSCO_ARTIFACT_FILE_ORIGINS=https://<artifact-host>:19991
CATSCO_ARTIFACT_ORIGIN_SECRET=<32-256 base64url characters>
```

Artifact origin discovery also uses `CLOUD_ARTIFACTS_INDEX_URL`, configured `CATSCO_ARTIFACT_NODES_JSON` / `CATSCO_ARTIFACT_NODES_FILE`, and `CATSCO_DIRECT_ARTIFACT_URL_TEMPLATE`. Keep these consistent during ingestion, reads and backfill. Use protected management metadata for discovery: static aggregate indexes are deliberately inaccessible to file readers. Disable the direct-template discovery shortcut when using protected managed nodes.

Gateway bootstrap environment and every worker service require:

```env
CATSCO_ARTIFACT_AUTHORIZATION_URL=https://<application-host>/api/artifacts/file-authorization
CATSCO_ARTIFACT_ORIGIN_SECRET=<the same generated secret>
# Worker: existing public-base provisioning field, including its real port:
CATSCO_ARTIFACT_PUBLIC_BASE_URL=https://<artifact-host>:19991/artifacts
```

Bootstrap validates the fixed HTTPS callback URL and secret before remote changes. Runtime callbacks allow HTTP only on loopback for local tests. Missing/invalid configuration denies artifact files. Install the generated Nginx configuration with mode 0600; never publish the secret or expose it to a browser. Health routes contain no artifact bytes. Worker health fields `privacy_enforced` and `authorization_configured` are diagnostics, not evidence of correct message authorization.

Current provisioning does not automatically propagate all new privacy environment variables to existing systemd workers. Configure them explicitly on every worker, including older direct-public instances.

## Private publication and metadata

Publishing validates an instance-local HTTPS upload and current `CanUseResource` before contacting management. The production uploader signs a two-minute exact-file `publish_token` transport URL. The management request uses only the configured service credential; it never forwards the visitor's Authorization/cookie and never follows service redirects. Source fetches remain subject to active identity, parent revocation and current file permission. The external management downloader must accept and immediately fetch this signed URL, avoid logging/persisting it, and never return it as public metadata. This integration requires verification outside this repository.

A verified HTTP 201 creation response with an initial/legacy publication version and matching full node/namespace/ID/uploader can establish draft provenance. Re-publication of an existing resource requires current use permission and cannot transfer ownership. Artifact lists filter every entry by current read permission; reconciliation uses the complete validated upstream list before filtering so one viewer cannot delete others' tags.

## Rollout and validation

1. Back up and apply the database schema. Use the bounded dry-run/apply backfill from `../../docs/message-attachment-privacy.md`, record successful cursors, and verify representative historical messages. Configure every trusted origin before scanning.
2. Prepare the application and companion browser/CLI consumers together. New protected upload routes take effect immediately when the new server starts; coordinate cutover or use a maintenance window. Run a final catch-up backfill.
3. Install the protected static host and service environment on every worker. Restrict worker port 19990 to the private gateway transport. Apply the gateway bootstrap with the same callback and secret.
4. On the existing Tencent host, do not overwrite the host-maintained app vhosts. After deploying the helper bundle, record the legacy-route retirement date and explicitly run `sudo -n /srv/catscompany-prod/compose/ensure-uploads-compat-nginx.sh`. It leaves the existing `.cc` app vhost unchanged, replaces only the unique inline `/uploads/` location in the `.cn` vhost with the managed snippet include, and updates `/etc/nginx/snippets/uploads-zos.conf`. It backs up both changed files, runs `nginx -t`, reloads Nginx and restores the previous files if validation or reload fails; ambiguous routes fail closed. The production workflow only packages the helper; it does not run it. During this temporary bridge, requests with `Authorization` go through Go message authorization and fail closed; requests without it retain the anonymous ZOS bearer-link route for legacy clients. This exception is not private attachment delivery. Keep the app-only Compose route unchanged. Both application domains deny executable `/artifacts/` and `/by-agent/` paths; use the isolated artifact origin.
5. Audit and close public bucket ACLs, direct IP/port listeners, old static services, alternate Nginx aliases/origins and CDN caches. The temporary legacy ZOS route intentionally remains a bearer-link path; it is not an authorization boundary. Disable access logging of query-bearing upload source credentials at every serving proxy.
6. Run Nginx syntax/runtime validation in the deployment environment. Test authenticated readable messages as a human and bot, anonymous and unrelated actors, removed membership, disabled actor, revoked/expired sessions, direct worker requests, source theft, forged headers, nested resources, manifests, indexes, HEAD, Range and callback failures. Authenticated denials and app failures must never return file bytes or fall back to ZOS. Verify legacy anonymous reads separately as an explicitly temporary compatibility behavior.
7. Verify real management publishing and target-browser third-party-cookie behavior. Record unresolved historical provenance separately; never infer ownership from the first reader.

Retirement: remove the ZOS route and its maps after the supported XiaoBa client floor has moved to authenticated downloads and logs show no required legacy traffic for the agreed observation window. Record a retirement date before enabling the bridge and do not extend it without an explicit review.

Rollback must preserve the authenticated app route. The only anonymous ZOS path permitted is the explicitly time-bounded legacy bridge above; never add an anonymous fallback from the authenticated location. Do not restore public aliases, pre-privacy workers or public caches. Preserve provenance and bindings for roll-forward.

Repository tests do not establish live topology, bucket policies, real database migrations, Nginx runtime validity or management downloader compatibility. No production deployment, secret installation or merge is performed by these changes.

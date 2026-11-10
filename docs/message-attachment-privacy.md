# Message attachment privacy

Baseline: cats-company `fd88e7c`; companion XiaoBa-CLI `9430803`.

## Authorization contract

An active authenticated human or bot may read a resource when it can currently read any persisted message that actually references it. The existing topic/message read rules remain authoritative; `agent_context` eligibility and task ownership do not add attachment restrictions. Knowing a URL, artifact ID, uploader field or message ID does not grant access.

Verified uploaders may read their own unattached drafts. Binding permanently ends draft access: `file_uploads.bound` remains true even after the final referring message is deleted. Ownership is recorded at authenticated byte ingestion (including mobile and channel uploads), never on first read. A verified new artifact publication may establish draft ownership; an existing publication cannot be claimed through a repeated import.

Existing attachment `url`, `name`, `size` payloads and resource URLs are preserved. Public tutorial assets under `/uploads/tutorial/` remain separate.

## Persistence and request flow

The existing MySQL/PostgreSQL adapters implement `store.FileAccessStore` and `store.FileMessageWriter`. Three tables hold immutable upload provenance, message-resource bindings and hashed expiring media sessions. PostgreSQL migration `000067_file_access` and both adapter schema initializers define the same fields. Complete schema creation before the new server accepts traffic; do not remove these tables while it runs.

For PostgreSQL, follow `DB_MIGRATIONS.md`, load the approved server-local `CATS_MIGRATION_DATABASE_URL` into the environment, review the current version and all pending migrations, then use the existing runner:

```sh
CATS_DB_DRIVER=postgres scripts/db-migrate.sh version
CATS_DB_DRIVER=postgres scripts/db-migrate.sh up
```

Verify version 67 (or a reviewed later version) and all three tables before backfill. Do not force a version to skip missing DDL. The script supports PostgreSQL only. MySQL uses its existing startup `CreateSchema()` path; the new table definitions are in `server/db/mysql/file_access_schema.go` and must complete before serving requests. Validate both engines against real test databases before production use; local evidence here is SQL mocks.

File-bearing HTTP, WebSocket, channel and artifact-bearing service-prompt messages validate `CanUseResource` before persistence. The message, bindings and permanent bound state are saved in one transaction. Duplicate client IDs retain the original message and resources; transaction failures roll back all changes.

Bindings only locate candidate messages. Every read reloads the persisted message, verifies topic and canonical resource membership, checks the active actor, and applies current message-read permission. Removed membership, edited/deleted messages, forged bindings and disabled actors cannot keep an old grant. Multiple readable references are supported.

Canonical uploads use `upload:<category>/<filename>`. Absolute URLs must use a configured HTTPS platform origin. Configure `CATSCO_PUBLIC_BASE_URL` and, if needed, comma-separated `CATSCO_FILE_TRUSTED_ORIGINS`. Canonical artifacts include the HTTPS origin and full namespace, for example `artifact:https://node.example/artifacts/by-agent/42/demo`. Configure artifact origins as described in `../deploy/prod/artifact-privacy.md` before backfill.

## Authentication and browser transport

- `/uploads/` uses `FileMediaAuth.UploadMiddleware` and then normal file authorization before opening storage or generating a preview. GET, HEAD, Range and preview/download variants share this check. Responses use private/no-store caching; there is no anonymous storage fallback.
- API clients use existing bearer/API-key credentials. Device connectors need explicit `device:download` scope, an unrevoked token and an active human owner, followed by the same file permission check. Upload scope alone does not grant reads.
- Authenticated POST `/api/files/media-session` creates a 15-minute host-only HttpOnly SameSite=Strict platform media cookie; DELETE revokes the parent credential's media sessions. Origin checks, authoritative explicit headers, account revision tracking and serialized frontend updates prevent stale account responses from installing a usable old session.
- Authenticated POST `/api/files/artifact-session` with `{ "url": "...", "message_id": 123 }` issues a 15-minute token scoped to one full artifact resource. `message_id` is optional. The browser retains the canonical source identity and uses the returned URL only for transport. Account changes remove existing frames.
- The artifact worker validates the scoped token through the fixed application callback, sets a host-only artifact-directory cookie, and returns 303 with only `media_token` removed. Every subresource request checks current permission again. Aggregate indexes remain inaccessible.

Platform cookies accept only the platform token format. Artifact and publication-source credentials cannot be reused as broad platform cookies. Session rows store hashes, actor, expiry and originating credential hash; parent revocation invalidates related media access.

## Private publication source

Publishing first validates the canonical instance-local source path and current source-use permission. The production `UploadHandler` then creates a two-minute, exact-upload `publish_token` URL for the configured management service's immediate fetch. Only that transport URL enters the management request. The visitor's Authorization/cookie is never forwarded; the management request uses its own configured service credential and rejects redirects.

The source token permits GET/HEAD/Range of its exact file, still checks active identity and current resource access, and is revoked with its parent session. It is not stored in message attachment payloads or substituted for the artifact's canonical identity. Do not log query credentials. The external management downloader is outside this repository: verify it accepts and immediately fetches the signed URL without persisting or exposing it before production rollout.

## Historical messages and bounded backfill

The offline command does not start the HTTP server, Redis or notification workers. It scans global persisted message IDs in ascending order, indexes real structured/legacy attachments and configured artifact text URLs, and never rewrites message bodies or re-uploads bytes.

```sh
# Use the built server binary and the deployment's normal DB configuration.
./server file-access-backfill --config tinode.conf --after-id 0 --limit 500
./server file-access-backfill --config tinode.conf --after-id 0 --limit 500 --apply
```

The first command is a dry-run. Limits must be 1..1000; each command has a five-minute context. JSON reports `after_id`, `next_after_id`, `scanned`, `messages_with_files`, `references`, `dry_run` and `has_more`. Review each dry-run, apply the same batch, then resume from its successful `next_after_id` until `has_more` is false. Keep the input cursor on failure and retry it: bindings are idempotent. Do not advance the cursor merely because a failed run printed partial progress.

A positive `message_id` query hint can repair a missed index on first read only after the server loads that exact persisted message, verifies the resource and checks current access. Browser message previews and XiaoBa downloads pass this hint only for trusted platform uploads. A hint never creates upload ownership. Unreferenced historical bytes with unknown ownership remain denied; investigate provenance instead of assigning them to the first requester.

## Coordinated rollout and verification

1. Back up the database; apply and validate the new schema in the target database engine. Configure all canonical platform/artifact origins consistently for server and backfill.
2. Prepare both clients and the server together. There is no permissive read feature flag: enabling the server immediately protects uploads. Schedule a maintenance window or a coordinated release so a partial rollout cannot reopen storage.
3. Dry-run and apply historical backfill, record cursors/counts and verify representative human/bot, legacy and multi-message samples. Run a final catch-up batch around cutover.
4. Install the worker/gateway and both Tencent Nginx changes. Close public buckets, alternate origins, old static hosts, direct ports and CDN caches. Follow `../deploy/prod/artifact-privacy.md`.
5. Verify allowed human/bot reads and denials for anonymous/unrelated actors, membership removal, disabled actors, forged references and expired/revoked sessions. Exercise HEAD, Range, previews, nested assets and direct origins. Verify private publication with the real management downloader.

Rollback must preserve the authorization boundary. Keep protected routes and storage policies; disable the affected feature or restore a compatible protected build. Do not restore anonymous aliases, public fallbacks or pre-privacy workers. Keep bindings/provenance for a subsequent roll-forward.

Local SQL mock/transaction tests, service HTTP tests, browser tests/build, worker tests and companion CLI tests/build are recorded in the draft PRs. They do not establish real migration execution, Nginx runtime validity, production bucket policies or external management integration. This task prepares draft PRs only; it does not merge or deploy.

# Restart-safe device grants (Rachel incident, 2026-11-02)

## Cause and boundaries

Rachel's grant renewed successfully at 15:45:33 CST. The API container was
recreated at 15:45:34, and the next RPC returned `device grant is not active`.
A second recreation at 16:03:32 reproduced the sequence. The production runtime
had neither a Redis URL nor a Redis store configured: grants lived in a process
map. Durable conversation selections do not persist execution capabilities.

This change reuses the existing Redis runtime; it does not resurrect grants
from client fields, reissue authorization automatically, or replay interrupted
shell/write requests. An unknown execution result must remain unknown.

The PR supplies an opt-in persistent Redis Compose service, an explicit
`OC_RUNTIME_REQUIRE_DURABLE` startup gate and memory-mode warning, and restart
tests. It also rejects authorization when Redis revocation reads fail or contain
invalid watermarks. Previously those errors could be mistaken for no revocation.

The gate verifies the selected backend, NOT the Redis operator's disk policy.
An external Redis still needs persistence, no eviction and restricted access.
No production environment is changed by this PR. Memory mode remains available
for development/backward compatibility, and is explicitly not restart-safe.

## Reviewed rollout, not automatic deployment

1. Schedule a short maintenance window. Existing memory grants cannot migrate:
   finish current remote tasks, then require a new user turn after cutover.
2. Choose a dedicated Redis instance. The supplied `runtime-redis` profile has
   no host port, an explicit persistent bind mount, AOF with `appendfsync always`
   and `noeviction`. Its Docker network must contain trusted services only;
   use authenticated/TLS Redis for a less trusted or multi-host network.
3. Back up production configuration. Merge `runtime-redis` into existing
   `COMPOSE_PROFILES` (do NOT replace `shimo` or other profiles). For the local
   service configure privately:

   ```dotenv
   COMPOSE_PROFILES=shimo,runtime-redis
   OC_RUNTIME_STORE=redis
   OC_RUNTIME_REQUIRE_DURABLE=true
   OC_REDIS_URL=redis://runtime-redis:6379/0
   OC_REDIS_KEY_PREFIX=catsco:prod
   PROD_RUNTIME_REDIS_ROOT=/srv/catscompany-prod/data/runtime-redis
   ```

   Omit `shimo` if unused. Ensure this exact bind path is backed up and writable
   by the image's Redis user. Do not delete it during deploy/rollback. Keep the
   same Redis URL, database and prefix across API versions/instances. Use unique
   per-process node IDs or the existing generated IDs, not one shared ID.
4. Before recreating the API, validate Compose without printing secrets, start
   only `runtime-redis`, and confirm health plus `CONFIG GET appendonly`,
   `appendfsync` and `maxmemory-policy`. Take a backup before Redis upgrades.
   Load-test the whole shared runtime (not just grants): `appendfsync always`
   applies to routing/attention writes too. Observe write latency/p95/p99, CPU,
   AOF/disk headroom, `used_memory`, `maxmemory`, `evicted_keys` (must stay zero),
   rejected writes and the new authorization storage warnings. Size memory and
   disk before cutover; `noeviction` rejects writes when full rather than evicting
   authorization. Grant-write failures now log a redacted, rate-limited reason
   (`out_of_memory`, `timeout`, etc.); this is not automatic capacity recovery.
5. Deploy the API and confirm `runtime state initialized: mode=redis`. Invalid
   backend/URL or required-durable memory mode must fail startup; unavailable
   Redis must not silently fall back to process memory.
   Each grant read/write and the combined token/device revocation check have
   150ms budgets;
   context deadlines are also enabled at the Redis socket layer. Storage-error
   logs distinguish `grant_read`, `grant_write`, `revoked_token_read` and
   `revoked_device_read`, without IDs, keys, payloads, URLs or raw Redis errors.
   Identical operation/reason warnings are limited to one per 30 seconds.
6. Test with two harmless target desktops: keep a read-only task alive over an
   API-only recreation, reconnect the same Bot body and desktop incarnation,
   then verify its existing grant/renewal still targets the original desktop.
   Also test explicit targets, selection switch, expiry, logout/relogin and
   Redis unavailability. Old grants must stay denied after logout or switch.
   This is still subject to live routes, membership, body leases and the
   original ten-minute expiry / 24-hour renewal ceiling. New messages may be
   needed after the initial memory-to-Redis migration, not on every later deploy.
7. Do not claim in-flight RPC survives API replacement. Pub/sub and pending
   request waiters are not durable. Inspect uncertain writes before retrying.

## Rollback and verification limits

Prefer rolling back the API image while keeping Redis and its prefix enabled
(the previous production version already supports Redis). Disabling Redis
invalidates active tasks again and loses shared revocation protection; do not
use memory fallback as a transparent recovery. Redis failure denies grant use;
restore Redis/data rather than bypassing authorization. Disk loss or deletion
of the Redis data directory is not covered by API restart protection.

Automated tests use miniredis and fresh runtime clients/registries; they prove
API state replacement semantics, scope/expiry/revision checks and revocation
behavior, not Redis disk crash recovery or a real desktop E2E. Compose storage
invariants are checked statically. Real production acceptance is required after
colleague review and an explicitly scheduled rollout.

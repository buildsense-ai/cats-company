# Device authorization-only persistence

## Scope and reason

Rachel's 2026-11-02 incident exposed two independent failures. XiaoBa #595 fixes
snapshot I/O blocking its heartbeat. CatsCo API replacement separately discarded
the in-memory device grant table. This change addresses only the latter.

The shared Redis runtime from #858 is NOT enabled by this configuration. This
is an alternative rollout, not an extra switch required by that rollout.

| State | Authorization-only deployment |
| --- | --- |
| Scoped device operation grants | Dedicated Redis prefix, existing short expiry |
| Device unlink/revocation watermarks | Same Redis prefix, existing retention |
| Connector token revocations | Same Redis prefix, existing expiry |
| Conversation device selection and revision | Existing SQL store, unchanged |
| WebSocket connections, routes and Bot body leases | Process-local, unchanged |
| Registered/online devices | Process-local; connectors must reconnect/register |
| Pending RPC requests and response waiters | Process-local; NOT restart-safe |
| Pairing codes and short-term device audit | Process-local, unchanged |
| Messaging attention and publication/Artifact coordination | Existing behavior, unchanged |
| Chats, users, files and model context | No migration in this change |

The narrow wrapper reuses the existing Redis grant encoding, TTL, bounded reads
and redacted diagnostics. It cannot implement the shared-runtime interface,
does not register a Redis runtime node and starts no routing/pubsub/heartbeat
workers. It writes only grants and device/token revocations. Redis initialization
does not by itself prove durable disk configuration.

## Safety boundaries

- No new authority from model/user-supplied fields. The server's original grant
  remains canonical. Selection revision, expiry, Bot/device identity, membership,
  capability and live-route validation are unchanged.
- Persisting the device revocation watermark alongside grants is mandatory:
  persisting grants alone could revive old tasks after an unlink and restart.
- Unlink saves revocation BEFORE removing/disconnecting the device. A Redis
  write failure returns HTTP 503, not a false success. Retrying unlink is safe;
  concurrent older writes cannot decrease a device watermark.
- Read failures/corrupt authorization data deny use; no memory fallback. Grant
  writes and revocation reads/writes retain bounded socket contexts (150ms per
  operation/check). Storage diagnostics redact raw Redis connection errors.
- Ordinary chat does not gain a Redis authorization dependency. Remote-device
  registration, token checks and execution do depend on available revocation
  storage. Redis loss is not silently bypassed.
- This does not support multiple independent API instances routing devices
  between each other. Use the existing shared runtime for that separate need.
- In-flight RPCs do not survive API replacement. Never replay uncertain writes
  automatically. This stores authorization, not an execution checkpoint.

## Configuration and rollout (not executed by this PR)

Defaults preserve today's memory behavior. Explicit opt-in is required; a Redis
URL alone cannot enable either the narrow mode or shared runtime accidentally.
Do not set `OC_REDIS_URL` for this rollout: it can implicitly select shared Redis.

```dotenv
OC_RUNTIME_STORE=memory
OC_RUNTIME_REQUIRE_DURABLE=false
OC_REDIS_URL=

OC_DEVICE_AUTH_STORE=redis
OC_DEVICE_AUTH_REQUIRE_DURABLE=true
OC_DEVICE_AUTH_REDIS_URL=redis://runtime-redis:6379/0
OC_DEVICE_AUTH_REDIS_KEY_PREFIX=catsco:prod:device-auth
```

The narrow durability gate is independent of `OC_RUNTIME_REQUIRE_DURABLE`, which
continues to require the FULL shared Redis runtime. Mixing full runtime with an
explicit narrow store is rejected at startup, not silently overridden.

1. Review this change and run tests in CI. Do not change production yet.
2. Prepare a dedicated persistent Redis, using #858's opt-in `runtime-redis`
   Compose service if appropriate: private trusted network/no public port,
   AOF enabled, `appendfsync always`, `noeviction`, backed-up persistent mount.
   Merge that profile with existing profiles; do not replace them. Keep the
   service's existing `PROD_RUNTIME_REDIS_ROOT` path stable. Size memory/disk,
   measure authorization write/read latency and ensure evicted_keys stays zero.
3. Schedule maintenance and finish remote tasks. Existing memory grants and
   revocations are NOT migrated. Existing memory-only revoked tokens have a
   pre-existing restart-loss limitation; for the first cutover, reconcile/reseed
   known revocations or explicitly invalidate/re-pair affected connector
   credentials before reopening remote access. Merely starting a new chat is
   not a migration of revocations. Agree this security cutover with operators;
   do not claim that this PR recovers unknown historical revocations.
4. Back up config, start/check Redis, then configure the independent settings
   above. Require durable selection SQL support. Recreate the API once.
5. Confirm startup reports `mode=memory; device_authorization=redis` and only
   grant/revocation keys appear under the dedicated prefix. No automatic memory
   fallback is allowed if Redis initialization fails.
6. Start fresh harmless tasks. With the same Bot body/device incarnation, let
   endpoints reconnect after an API-only replacement and verify still-valid
   grants can be used/renewed. Test wrong Bot/device/session/operations, target
   switch and switch-back, expiry, unlink/re-pair, revocation across replacement,
   storage outage and retry. Verify ordinary chat, pairing and local routing.
7. Roll back API code/config only with a reviewed maintenance plan. Keep Redis
   and its data/prefix. Returning to memory loses this protection and can lose
   revocation checks; it is not a transparent or safe automatic fallback.

## Verification limits

Unit tests use independent Redis clients and replacement Hubs plus miniredis.
They check grant preservation, reconnect/renewal, SQL selection invalidation,
revocation, expiry, outage denial, unlink failure handling and exact Redis key
scope. They do not prove real Redis AOF crash recovery, physical-disk durability,
production latency or real-desktop E2E. Those need the maintenance-window checks.

## Plain-language note: process memory

A process is a running instance of a program. The OS gives it a virtual address
space backed by RAM (and sometimes paging). Program variables, maps, buffers,
connection objects, goroutine stacks and caches occupy this space. A process
does not have physical RAM built inside it. It uses memory managed by the OS.

An API restart creates a new process with freshly initialized variables. The
old heap is not automatically inherited, even without a server reboot. Files
on disk and external databases can survive; writing a map in RAM does not
automatically write it to either place. Redis itself is also a process using
RAM: independence protects against an API-only restart, while configured AOF
and persistent disk are needed for Redis restart recovery.

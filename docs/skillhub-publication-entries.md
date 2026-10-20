# SkillHub publication entry persistence

This implements staged CatsCo foundations of the
[approved three-repository contract](https://github.com/buildsense-ai/catsco-skillhub-cloud/blob/c369cc9d5e956a4c42095ebcbb93b4ac7506e5f0/docs/api/publication-entrypoint-delegation.md).
It adds a PostgreSQL store for durable **workspace publication intents**, a
default-off private status API and a default-off, source-verified workspace
entry protocol, plus opt-in durable background source probing. It does not upload
or publish files. Upload-grant storage and a default-off, authenticated Cloud-peer
introspection route exist; no reachable credential issuer or upload consumer exists.

## Implemented behavior

The optional `store.SkillHubPublicationEntryStore` creates an immutable entry and
a reference-only `capture_candidate` outbox row in one transaction. The optional
worker below can lease it for read-only source probing, not capture. Creation
returns `waiting_runtime` for a new intent; replay preserves `cancelled` for a
cancelled intent. Neither state is a claim
that a file was captured, validated, uploaded or published.

The database checks an active human account, an active enabled Bot owned by that
human, and the Bot's durable body binding. These rows stay read-locked through
insertion, serializing account disablement, owner transfer and body changes
against admission. This is necessary but **not sufficient** runtime authorization:
the workspace protocol authenticates the human and verifies device,
installation and route generation against server facts, and asks the exact
runtime to confirm the workspace generation/local Skill reference. Future
capture/upload consumers must recheck those facts. Caller-provided UIDs or copied
record fields are not authority. Do not blindly decode HTTP bodies into this store.

Exact `(actor UID, request ID, immutable input)` replay returns the same entry
without another event or quota charge. A conflicting input is rejected. The
server generates the entry ID and timestamp. Identity hashes bind every input
field under `catsco-publication-entry-v1`; they are **not** file/candidate hashes.
Same-name Skills and Bots never participate in identity lookup.

Reads are limited to the original active human publisher. A new Bot owner or
friend cannot read the previous publisher's entry; the original publisher can
read historical intent after transfer, but cannot replay it as a new authorized
operation. The read API below authenticates the human and explicitly projects
safe fields. The store's internal records must not be serialized wholesale into
chat, browser responses or logs.

## Storage and limits

The additive tables are registered in `server/db/postgres/schema.go` and mirrored
in `000028_skillhub_publication_entries.up.sql`, following the dual-track policy
in [DB_MIGRATIONS.md](DB_MIGRATIONS.md). `CreateSchema` adds them idempotently on
the next normal startup; external migration tools can apply the same DDL. A test
checks that both definitions match. Store operations execute no runtime DDL.
Startup schema failures retain existing startup behavior;
missing tables in an already-running process return an error, never memory-only
success. MySQL does not implement this optional store; later routes must report
unsupported storage rather than silently switch adapters or use an in-memory job.

| Table | Contents |
| --- | --- |
| `skillhub_publication_entries` | Immutable publisher/Bot/source binding, request and identity hash, initial state and server timestamp. |
| `skillhub_publication_outbox` | One reference-only capture event per entry; no payload or credentials. |
| `skillhub_publication_admission` | One row serializing quota checks and insertion across API processes. |

Default admission budgets are 10,000 total entries, 100 per publisher, and 20 new
entries per publisher per minute. Caller policies are internal and validated;
different processes must use the same policy. Replays are checked after current
authority but before rate/storage admission. Global locking keeps concurrent
new requests from exceeding a shared configured budget. Each creation has a
10-second context deadline, 3-second lock timeout and 5-second statement timeout;
reads have a 5-second context deadline. There is no network or file work inside
the transaction. Database errors must be sanitized by future handlers.

No cleanup or expiry is implemented in this slice. At capacity, new entries fail
closed; no active intent or replay evidence is evicted. Future retention work
must preserve accepted-commit evidence and must not reuse expired request keys
automatically. Actor/Bot IDs are immutable historical values without foreign keys
to `users`, so these inert records do not block existing account deletion. Creation
checks the real rows transactionally; subsequent operations must recheck authority.
Outbox rows reference the entry with a restrictive foreign key.

## Private status API

`CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED` defaults off; only `1` or `true`
(case-insensitive, trimmed) enables reads. Workspace creation requires the separate
flag and protocol below; there is no HTTP creation route.
The handler authenticates an active CatsCo human JWT from one `Authorization:
Bearer ...` header itself, including a fresh account-state check. It accepts no
query credentials, API keys, cookie-only sessions, caller UID, query parameters,
request body or arbitrary source path. A display name is never an identity key.

| Route | Behavior |
| --- | --- |
| `GET /api/skillhub/publications/capabilities` | Available to authenticated humans even with reads off. Returns `schemaVersion: 1`, effective `entryReadsEnabled`, `sourceStatusReadsEnabled`, `cloudStatusReadsEnabled`, effective `workspaceCreateEnabled`, `chatCreateEnabled: false`, and `minPollIntervalMs: 5000`. Adapter support is not a database or target-runtime health check. |
| `GET /api/skillhub/publications/entries/{entryRequestId}` | When enabled on a supported store, reads the original publisher's record. Missing, wrong-publisher and disabled reads return the same 404. Bot ownership transfer does not grant historical record access to the new owner. |
| `GET /api/skillhub/publications/requests/{requestId}` | Same private projection, using the original account-scoped request ID to recover a lost creation response. This does not reauthorize, create, replay or mutate an entry. |
| `GET /api/skillhub/publications/entries/{entryRequestId}/source-status` | Original publisher's historical source-probe progress, using the same read flag and private transport guards. Requires the optional PostgreSQL source-status reader. Never claims, settles or resumes a task. |
| `GET /api/skillhub/publications/entries/{entryRequestId}/cloud-status` | Original publisher's local observation of Cloud acceptance evidence. Requires the optional PostgreSQL Cloud-status reader. No remote lookup, execution-state claim or write. |

The entry response contains only `schemaVersion` and `entry` with
`entryRequestId`, decimal-string `botUid`, `entrypoint`, `state` and UTC
`createdAt`. It omits request keys, publisher identity, device/body/installation,
workspace/source references, identity hash, candidates and credentials. The only
currently supported states are `waiting_runtime` and `cancelled`; neither means
capture, validation or publication success. Local cancellation does not confirm
that a remote Cloud task has stopped. Unknown states fail closed with 503
until their safe projection is implemented. No Cloud report is fetched here.

Every handler response, including authentication failures, is `private, no-store`
JSON. Invalid credentials return 401; disabled/non-human accounts 403; malformed
read transport 400; non-GET methods on known routes 405; throttling 429; unsupported
storage, unavailable authority/storage or an unknown record format 503. Unknown
routes/IDs return 404. Errors use stable `publication.*` codes, a generic message
and (for 503) a server-generated correlation ID logged without raw errors, tokens
or source data. They are not validation rejects and must not trigger model repair.

Polling is bounded per API process: 60 requests/minute per authenticated account
with burst 10 (including capabilities), and 12 entry reads/minute per account/entry
with burst 3. At most 16 entry-store reads run concurrently, each with a five-second
context deadline; client cancellation propagates to the store. 429 and concurrency
503 responses supply `Retry-After: 5`. These are local load guards, not a durable
cross-replica quota. They do not replace deployment-wide request/authentication
limits. Clients should wait at least five seconds and back off on failures.

### Source check progress

The additive `source-status` route leaves existing entry/request responses
unchanged. It shares the per-entry polling budget with the entry-ID route and
the handler's 16 concurrent reads/five-second deadline. The existing read flag
is sufficient; delivery/admission need not be enabled to read history after a
worker rollback. Unsupported storage returns 503, never invented progress.
Neither capabilities nor this endpoint is connected to a new UI in this slice.

The response contains only `schemaVersion: 1`, `entryRequestId`, and
`sourceStatus` with `stage`, `attempts`, `maxAttempts: 8`, `observedAt`, and the
optional fields described below. Times are UTC; `observedAt` comes from the
database in the same statement as the committed ledger observation. These are
transport-probe attempts, not the later three-round automatic repair budget.

| Stage | Meaning and optional fields |
| --- | --- |
| `waiting_runtime` | No background attempt recorded. Does not prove the Bot is offline or the worker enabled. |
| `probing` | A source-probe lease is unexpired at `observedAt`; it does not prove socket delivery or ongoing execution. |
| `recovery_wait` | The saved lease has expired, including an expired eighth attempt. Only a worker can reclaim or durably settle it; a read does not change the ledger. |
| `retry_wait` | A saved retryable timeout/unavailable receipt; includes allowlisted `code` and `retryNotBeforeAt`. This is the earliest eligible time, not a guaranteed retry time or current authority check. It may be in the past while the runtime/worker is unavailable. |
| `verified` | A historical read-only source receipt, with `verifiedAt`. Not a captured candidate, validation result, upload, publication, or current execution permission. |
| `needs_user_action` | A saved terminal source failure or exhausted eight-attempt budget, with allowlisted `code`. No retry/reset button or auto-repair is introduced. |

Codes are limited to `publication.runtime_timeout`, `publication.runtime_unavailable`,
`publication.source_missing`, `publication.source_changed`, `publication.source_unsafe`,
`publication.runtime_unsupported`, and `publication.delivery_exhausted`, in their
matching stages. Unknown state/code combinations, inconsistent counters or
missing evidence return a sanitized 503 instead of being passed through.
No free-form messages, publisher UID, source references, device/body/installation,
lease token, lease deadline, node, connection or connection generation are exposed.

Original active human publishers retain historical reads after Bot ownership or
binding changes; new owners/friends do not inherit them. Account disablement is
checked again by the database query. This route does not check or grant current
runtime authority, clear a terminal failure, reset attempts, create another
intent, or trigger a model. An entry's orchestration state remains
`waiting_runtime` even after `verified`; only a future Cloud result can claim
publication. Clients must not present this observation as a live online/permission
indicator or fabricate a repair action from infrastructure errors.

### Cloud acceptance observation

The additive `cloud-status` route reads only CatsCo's retained receipt ledger;
it does not contact Cloud or poll a Bot. It uses the existing default-off reads
flag, authenticated-human checks, shared entry/source-status polling budget,
16 concurrent reads and five-second context. Its capability means adapter
support and read enablement, not Cloud reachability or worker health. Missing
tables/unsupported storage fail with 503 rather than reporting no receipt.

The response has exactly `schemaVersion: 1`, `entryRequestId`, and `cloudStatus`:

| Field | Meaning |
| --- | --- |
| `stage: not_observed` | No receipt visible in this database snapshot. Upload may already have happened while its callback is delayed, lost or blocked; this is not permission to create a new entry or reupload. |
| `stage: acceptance_recorded` | CatsCo retained Cloud's historical acceptance receipt. Not validation success, completed publication, current execution permission or proof that candidate bytes still exist. |
| `localIntentState` | `waiting_runtime` or `cancelled`, read in the same database statement as the receipt. A late receipt never hides cancellation. |
| `executionState` | Always `unknown` in this protocol version. Neither accepted nor locally cancelled implies a remote execution result. |
| `recordedAt` | UTC first receipt timestamp, present only for `acceptance_recorded`. Stable across callback retries and process reconstruction. |
| `observedAt` | UTC database observation time, not a fresh check of Cloud or runtime authority. |

Only the original active human publisher may read this history, even after Bot
transfer, disconnect or disablement. New owners/friends do not inherit it.
The database repeats the human/account-state filter; HTTP also checks the
returned actor/entry and allowlisted state/timestamps. Unknown states, zero or
future receipt times fail closed. No task/event IDs, grant IDs, candidate hashes,
request keys, actor UID, source/route references, tokens or raw errors are exposed.

Reading never writes the entry, outbox, grant or receipt, renews a token, resets
retry budget, cancels or publishes a task. Consumers must use the original entry
ID and treat missing observations as uncertainty; they must not infer a repair
action from infrastructure failures. Cloud execution progress and delegated
cancel/commit ordering remain separate protocol work. There is no new UI,
schema migration, runtime capture/upload or production enablement in this slice.

Before connecting consumers or enabling this flow, communicate the cancellation
limit: an entry still in `waiting_runtime` with `acceptance_recorded` cannot be
cancelled through the current local-only endpoint. It returns HTTP 409
`publication.cancellation_conflict` until the distributed cancellation protocol
is implemented. Do not advertise remote cancellation, endlessly retry that 409,
delete retained evidence or create a replacement entry to bypass it. The
`entryCancellationEnabled` capability reports route support, not cancellability
of every entry. A `not_observed` read is not a cancellation preflight: the
callback may arrive before POST, and even a successful local cancellation does
not confirm that Cloud stopped. An already `cancelled` entry remains cancelled
and its authorized cancellation replay remains idempotent after a late receipt.

HTTP tests cover the exact projection, authentication, strict transport,
default-off/unsupported cases, shared polling, deadlines, concurrent reads and
safe failures. Real PostgreSQL tests cover absent/present receipts, token
renewal/expiry, reopened adapters, unchanged ledger snapshots, transfer and
disabled-account privacy, concurrent callback reads and late receipts after
cancellation. Existing two-node recovery tests also read through the actual
human HTTP handler before/after actual HTTPS receipt ingestion into PostgreSQL.
The runtime and Cloud sender in those tests remain synthetic; this is not a
deployed three-component publication test.

## Workspace source verification and entry admission

### Durable admission before probing

New orchestration clients should use `skillhub.publication.createEntry.v2` when
`workspaceDurableCreateEnabled` is true in the capabilities response. The original
`createEntry.v1` below keeps its probe-before-admission contract; neither protocol
is connected to the production publishing UI yet. v2 requires all three existing
read/workspace/delivery flags, all default off, and a supported durable adapter.
Capabilities describe configuration/support, not worker or runtime health; the
RPC also refuses admission when the local delivery worker is not running.

The v2 request uses the same exact three-field payload, request ID and owner/device
headers as v1. It first authenticates the current human/Owner and verifies the
active, exact, same-node Bot/device/body/installation and connection generation.
Then it saves the immutable entry and reference-only outbox **before sending any
source probe**. Browser source references are still unverified selectors at this
point, not permission to read or upload files. Only the existing durable delivery
worker sends the read-only probe, with its fresh authority/connection checks,
leases, bounded retries and terminal source-failure handling.

The private final RPC result has exactly `schema_version: 2`, `entry_request_id`,
`state: "waiting_runtime"`, and `replay`. It means **durably accepted, not source
verified or published**. There is no provisional success ACK or transient probe
to the Bot on this path. Source progress is read using the existing private
`source-status` route. A missing/changed/unsafe source is a durable worker result,
not a reason to erase the admitted request or trigger automatic format repair.

Admission runs outside the WebSocket read loop, with at most 16 in-flight
admissions per Hub/process, no waiting queue, the shared v1/v2 account rate limit,
and a maximum ten-second context shortened by caller expiry or worker shutdown.
The database's transactional authority, capacity, quota and idempotency checks
remain in force. No new schema or production environment configuration is added.

An exact same-key retry after a legitimate reconnect preserves the original
saved connection generation if all other immutable identity/source fields match
and the current trusted generation has not regressed. It rechecks current
authority and does not reset the outbox, attempts or terminal outcome. Changed
Bot, device, installation, body, local selector or workspace generation conflicts;
new keys are never invented automatically. On an uncertain DB outcome or lost
response, use `/requests/{requestId}` with the original publisher login. A
concurrent request can still commit, so a transient 404 is not proof of failure.

After commit, a Bot disconnect does not retract admission. Loss of the publisher's
socket suppresses the private reply but does not cancel the durable intent; a
new login retrieves it through the private read API. The worker can recover it
on a different node after the same runtime reconnects, even when no first probe
was sent. Account/Owner/body changes still prevent new execution; original active
publishers keep private historical reads after transfer.

**Boundary:** a brand-new request still needs an online, same-node, trusted runtime
binding to establish its immutable source identity. v2 does not accept arbitrary
offline/cross-node browser selectors. Durable offline first admission still needs
a separately authenticated source-binding mechanism. This change does not
capture files, upload, delegate credentials, create repair conversations, invoke
models, bill accounts or change the legacy publishing buttons.

### Original probe first protocol

This protocol is an integration foundation, not a ready publishing button. Both
`CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED` and
`CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED` must be `1`/`true`.
Both default off. The adapter must implement durable entries, private request-ID
recovery and durable Bot connection generations; no in-memory generation fallback.
No production environment is changed by this implementation.

An authenticated, active human on an ordinary owner WebSocket sends a
`thin_tool_rpc` request with tool `skillhub.publication.createEntry.v1`, an
account-scoped `request_id` matching `[A-Za-z0-9_-]{8,128}`, the canonical
`target_owner_user_id` (`usr` plus the actor UID), and `target_device_id`.
The payload has exactly three string fields:

```json
{"bot_uid":"42","local_skill_id":"local-skill-1","workspace_generation":"workspace-v1"}
```

Bot UID is a canonical positive decimal string. Local references are bounded to
128 characters and validated by the entry contract; paths, URLs, source bytes,
extra identity fields and caller-provided generations of connections are rejected.
Chat, Wiki (including an owner Wiki login), service, Bot and Connector initiators
are not allowed. Friendship and Skill visibility do not authorize publication.

CatsCo checks active actor and Bot, current enabled Bot ownership, the exact
durable body binding, active registered device, installation, runtime role,
current Bot-authenticated socket, unexpired body lease and positive durable
`BotConnectionGeneration`. The durable generation must match this node's accepted
connection generation. Neither a legacy body nor DeviceID-as-BodyID fallback is
allowed. A desktop works only when already bound to that Bot through its Bot
socket, not a generic owner device. No selection invokes Bot switch or rebinding.

The runtime must explicitly advertise `skillhub.publication.sourceProbe.v1`.
Legacy share capability is not enough. CatsCo sends a fresh server-random probe ID
with the same three source selectors and a maximum 30-second expiry. The probe is
read-only: the XiaoBa adapter must verify the scoped local reference and workspace
generation without executing, repairing, finalizing, copying or uploading the Skill;
invalid manifest metadata must not be mistaken for an unsafe local source. The
adapter is not implemented by this CatsCo PR and must not advertise the capability
before it actually implements those checks. Browser/model callers cannot invoke
the reserved probe directly through thin-tool RPC.

Only the matching still-current Bot socket can answer the probe. A success result
has exactly `schema_version: 1`, `local_skill_id` and `workspace_generation`, with
references matching the pending selectors. This is a source-existence/generation
receipt, **not** a candidate digest, publication approval or bearer credential.
Unknown fields or mismatches are rejected; controlled local errors use
`publication.source_missing`, `publication.source_changed`,
`publication.source_unsafe`, `publication.runtime_unsupported` or
`publication.runtime_timeout`. Runtime `INVALID_REQUEST`, `OWNER_MISMATCH` and
`DEVICE_MISMATCH` map to `publication.runtime_unsupported`; `REQUEST_EXPIRED`
maps to `publication.runtime_timeout`. Other runtime errors, including
`BOT_NOT_ACTIVE`, become `publication.source_unavailable` on this workspace
admission path; raw messages/files are never sent to the browser or persisted.
These errors do not trigger model repair.

Background delivery uses the same request-error normalization, with
`publication.runtime_unavailable` as its fallback. `BOT_NOT_ACTIVE` also covers
transient workspace switching in XiaoBa, so it remains retryable (`pending`),
not `needs_user_action`. Retries retain the existing backoff, attempt limit and
fresh authority/binding checks; this does not authorize switching Bots or
retrying indefinitely. Invalid request/owner/device errors are terminal, while
expiry remains retryable as `publication.runtime_timeout`.

After checking authority/binding again and claiming the probe once, CatsCo creates
the immutable entry plus inert capture outbox using the existing transactional
store checks. The success result contains only `schema_version: 1`,
`entry_request_id`, `state: "waiting_runtime"` and `replay`. An accepted RPC ACK
means only that source verification is pending, not that a durable entry exists.
There is no candidate snapshot or final Cloud publication in this result.

Admission preserves the original request key for retries. Store conflicts,
authority denials, rate/storage limits and infrastructure failures are distinct
`publication.*` errors. On an uncertain admission, lost reply or browser restart,
first read `/requests/{requestId}` with the original human login. A 404 while work
is still in flight does not prove it cannot commit: wait/retry with the same key,
never automatically mint a new intent. Original-publisher history remains readable
after Bot transfer or runtime reconnect, even when a privileged retry is blocked.
The read lookup uses the existing account/request unique index and needs no DDL.

Probes are transient, rate limited per process/account (20/minute, burst 3), and
use existing bounded per-requester/device pending slots. Timeout or socket
replacement returns a safe error with the browser's original request ID; it does
not create a durable task. A process crash before admission requires retry. Once
admission commits, the entry and outbox survive even if its response is lost.

This slice intentionally requires browser and Bot sockets on the **same CatsCo
node**. Shared-runtime routing to another node fails closed with
`publication.route_unsupported`, because thin-tool pending replies are node-local.
It must not be enabled across replicas until durable routing/receipt handling is
implemented. This is not a change to legacy routing or a promise of cross-node
or offline intake support. Recheck authority, exact binding and source generation
again before future capture/upload; the probe is not a lock on future file content.

## Durable background source verification

`store.SkillHubPublicationDeliveryStore` adds an **internal** source-probe
delivery ledger to the existing outbox. It is the persistence prerequisite for
cross-node recovery of an **already accepted** entry, not cross-node/offline
admission. The PR627 browser protocol above remains same-node and online-only.
The worker is gated by `CATSCO_SKILLHUB_PUBLICATION_DELIVERY_ENABLED` AND both
existing read/admission flags. All default off; no deployment configuration is
changed. PostgreSQL entry/delivery/generation interfaces and the scoped runtime
lister are required; unsupported adapters do not fall back to memory.

An internal scheduler can list at most 32 due entry IDs for one Bot. Claiming an
ID checks the original actor, current active human/Bot, enabled ownership/body
binding, immutable device/installation/workspace/local reference, and the current
durable Bot connection generation. Account/config/generation rows stay locked
through the short DB mutation. A reconnect can advance generation, but cannot
substitute any source selector. Neither the caller's observation nor a saved
record independently authenticates a socket: the node-local worker verifies
the exact Bot socket, body lease and explicit capability immediately before
dispatch and before accepting a receipt. These methods must never be exposed as
raw browser/runtime endpoints or called with model-supplied identity.

Each claim persists a random fencing token, node, connection, generation, attempt
count and a 30-second lease using database time. An active lease cannot be stolen;
after expiry another authorized node may claim with a new token. Stale, expired,
wrong-socket or wrong-generation receipts cannot settle a newer attempt. The
fence protects database receipts only; it is not an upload grant, file-access
permission or a guarantee of exactly-once external execution.

Only controlled timeout/unavailable results retry, with durable backoff of
5/10/20/40/80/160/300 seconds. At most eight claims are allowed per entry. Exhaustion
settles to internal `needs_user_action`, including recovery of a crashed eighth
attempt. No retry counter reset or automatic new entry is provided. Missing,
changed or unsafe source and unsupported runtime also require user action;
unknown/raw errors are rejected, not persisted or sent to a repair model.

A successful result records internal `source_verified` and its timestamp; an
identical receipt can replay after a lost acknowledgement without extending the
lease or changing the result. This is only a read-only source receipt. It does
not capture bytes, consume/delete the original event, issue a grant, upload,
install or publish. The immutable entry and public status remain `waiting_runtime`.
Future capture dispatch needs its own explicit transition and fresh authority
checks; it cannot treat this timestamp as current permission. The private
`source-status` projection above exposes historical progress, not a resume action.

Migration `000030_skillhub_publication_delivery` mirrors the startup DDL. It adds
columns/defaults/constraints/indexes without rewriting old identities or Bot
configuration. Rollback may remove the new columns only when no delivery attempt
exists, preserving old entries/outbox; once an attempt exists, rollback refuses
to erase fencing or receipt evidence. Stop consumers and back up before rollback.
Migration tests use frozen migration 000028 plus the existing relevant-table
user/Bot fixture and an actual pre-column intent/outbox, not a production dump.
Empty SQL, historical SQL-first/startup-first, repeated startup/migration, safe
rollback/reapply and refusal after a delivery attempt are covered.

### Scheduling and receipts

The server starts this worker only after choosing its final Hub and cancels it
on shutdown. A startup sweep and five-second ticks consider only locally
connected, registered Bot runtimes. Each sweep rotates through at most 16 Bots,
fetches at most four due IDs per runtime, and admits at most eight probes; each
node retains at most 32 pending probes. SQL filters the exact owner/Bot/device/
body/installation **before LIMIT**, so obsolete installations cannot starve
current work. Offline or unsupported runtimes consume no new claims. A process
restart or a move to another CatsCo node recovers accepted work from PostgreSQL
after the previous lease expires, using the same immutable source binding and
the newly verified durable connection generation. No Owner socket is required.

Dispatch uses the existing read-only `sourceProbe.v1` contract and a fresh random
`pubdelivery_` wire ID, separate from the database fence token. The token and
entry ID are not sent to the runtime. A second binding check runs after claiming
and immediately before local delivery. No source selectors are forwarded through
the shared-runtime relay. The socket queue is nonblocking; background backpressure
does not disconnect a Bot or perform database work on its WebSocket read loop.

Reserved receipt IDs bypass the legacy requester-bound router and its generic
logs, including late/unknown IDs. Only the exact registered Bot connection can
enqueue a receipt. The strict three-field schema and optional routing headers
must match; mixed/error payloads cannot claim success. Raw messages are discarded,
unknown errors become retryable runtime-unavailable, and only normalized outcomes
enter a bounded queue served by two workers. Fresh runtime binding checks and
the transactional owner/generation/fence checks precede durable settlement.
Concurrent receipts enqueue once; overload returns a safe acknowledgement without
consuming the pending receipt. After completion, a duplicate wire response may
return 404; it cannot repeat work or create another entry.

The probe deadline is five seconds before the 30-second DB lease deadline, leaving
time to persist timeouts. A one-second sweep queues expired probes independently
of scheduling; receipt DB calls have five-second contexts. If a write fails or its
outcome is uncertain, the worker never fabricates success or creates a new intent:
a committed result remains durable, otherwise lease expiry allows bounded retry.
Only the read-only probe may be retried; the fence is not an exactly-once guarantee
for future side effects. Existing authority lookup APIs remain synchronous in
the background workers, never in the WebSocket receipt admission path.

The real-PostgreSQL dispatcher test uses two independent Hubs and a synthetic wire
peer: it covers owner absence, node loss, lease recovery, stale replies, immutable
intent and obsolete-device starvation. It is not a live XiaoBa or Redis deployment
test. XiaoBa still must implement and explicitly advertise the new capability.

## Pre-Cloud cancellation

`CATSCO_SKILLHUB_PUBLICATION_CANCELLATION_ENABLED` defaults off. Set it to `1`
or `true` (trimmed, case-insensitive) together with the read flag to enable
`POST /api/skillhub/publications/entries/{entryRequestId}/cancel`. The request
has an empty body, no query parameters and the same active-human Bearer JWT
guards as reads. `entryCancellationEnabled` reports effective flag and adapter
support. Admission and delivery flags are not required: stopping existing work
must remain possible while those producers are disabled. GET never cancels.

Only the original publisher who is **still the Bot Owner** can cancel. The
store locks the entry, active human and current Bot ownership in one transaction.
Offline/disabled/rebound Bots can be cancelled by that same Owner without runtime
access; former Owners, new Owners, friends, Bots and service identities cannot
cancel this historical intent. Reads retain their original-publisher policy.

This action changes `waiting_runtime` to `cancelled`; repeating it is idempotent.
It returns the ordinary private entry projection, never a Cloud publication or
deletion claim. Missing/wrong publisher/ownership denial returns 404; an
unsupported lifecycle transition returns 409. Unknown future read states fail
closed. A 503 `publication.cancellation_uncertain` can follow a lost commit
response: query the existing ID or retry the same cancel, not a new publication.
Rate, concurrency, transport and context limits also apply to cancellation.

The scheduler excludes cancelled entries **before LIMIT**. Claim and receipt
transactions lock/read the entry before their authority and outbox checks, so
they cannot settle new results after cancellation commits. A read-only probe
already claimed or sent may still run; this is not remote process interruption.
Its late receipt cannot overwrite the cancellation. Existing lease/receipt rows,
immutable inputs and request keys remain intact. Entry and source-status reads
show `cancelled`, with no stale retry or verification timestamp. Same-key v1/v2
replay keeps `cancelled`; legacy v1 may still run its read-only preflight.

Migration `000035_skillhub_publication_cancellation` mirrors startup DDL and
only expands the entry-state constraint. Tests use a genuinely empty database
and a frozen relevant-table pre-PR620 baseline plus unchanged migrations 000028
and 000030, with an existing intent. They cover SQL-first/startup-first upgrades,
repeated startup/up, safe down/up, and refusal to roll back after cancellation
evidence exists. No files, entries or outbox records are deleted.

Before enabling this flag, upgrade **all** admission/delivery workers to this
version. Old binaries do not enforce the cancellation guard. Once cancellations
exist, do not roll back to those workers; stop admission/delivery and retain a
cancellation-aware binary. Turning the cancellation flag off does not revive
work. The down migration refuses to erase retained cancellations.

Retained Cloud receipt associations now exist, but there is no reachable
upload-grant issuer or capture dispatcher yet. For a `waiting_runtime` entry,
the transaction rejects cancellation with 409 `publication.cancellation_conflict`
if a Cloud receipt is already recorded; no local state changes in that case.
Already-cancelled entries retain idempotent cancellation even after a late
receipt, subject to the same actor/current-Owner checks. Future capture/Cloud
stages need their own pending/too-late/commit-permit protocol; this endpoint
must not claim remote cancellation. See the consumer limits in
[Cloud acceptance observation](#cloud-acceptance-observation). This PR
does not wire a UI button, enable production switches, uninstall Skills, revoke
installed versions, or change model switching.

## Upload grant storage foundation

The optional PostgreSQL `SkillHubPublicationUploadGrantStore` provides internal
issue/check methods. **There is no HTTP/RPC issuer, upload consumer or runtime
capability advertisement yet.** Only the read-only peer check below is exposed,
behind its own default-off flag. The source-probe worker does not issue grants.
This is not an activated
delegated-upload API and does not change `waiting_runtime` into an upload state.

An upload grant has one action, `candidate.upload`, and binds the entry, original
human, Bot, source/device/body/installation, current route generation/socket,
configured canonical HTTPS issuer/audience, revision 1, source snapshot digest,
exact HTTP byte SHA-256 and byte count (at most 75 MiB). It grants no listing,
download, final publication, visibility change, installation or billing rights.
Cloud must independently recompute hashes and enforce its effective limits.
These store inputs are trusted internal facts, not a browser/model request DTO.

Issuance requires current active human/active enabled Bot ownership and body,
current durable connection generation, an uncancelled entry, a successful
source-probe receipt and a matching retained capture receipt (see below).
**A probe is not capture proof.** The future adapter must
also check the live socket/body lease, capture capability, current workspace,
safe private-copy provenance and actual exact bytes before calling this store.
In particular, the store must not become a shortcut for arbitrary runtime files.

The first issue freezes a stable grant ID, candidate facts and Cloud-compatible
idempotency key. Each token is 256 random bits, valid for 120 seconds using the
database clock; only its SHA-256 is saved. Token formatting/JSON is redacted.
The explicit `Reveal` method is reserved for the future dedicated secure
response/header, never generic RPC logs, chats, events, URLs or model context.

Reissue rotates the token and increments its epoch while retaining the ID,
candidate and key. It cannot change content, scope, issuer, audience or revision.
An authenticated reconnect may advance the route generation and replace the
node/socket only; same-generation socket substitution fails. A durable five-second
cooldown and eight total issuances per entry bound retries, including restarts.
At exhaustion, stop for action; do not create a replacement entry automatically.
One row per already-quota-limited entry bounds storage. No candidate bytes or
plaintext credentials are stored in CatsCo.

After an uncertain issue response, the caller must keep the same candidate
bytes and intent, then request a replacement token within these bounds. The old
secret cannot be recovered from the database; rotation invalidates it. The stable
key/hash ensures token renewal is not permission to invent another Cloud task.
The future runtime must durably save its exact private request bytes separately.

Issue takes the entry lock exclusively; check takes it shared, then both recheck
current authority under the same lock order as cancellation. Cancelled entries,
expired/rotated tokens, changed scopes/epochs and stale runtime generations fail.
A successful check is an observation **at that transaction**, not a reservation
of future authority or a commit permit. A later cancellation may win before any
Cloud acceptance. A Cloud client with origin pinning, durable Cloud association
and pending/too-late cancellation/commit ordering remain mandatory before exposing
issuance or upload. Do not wire them to existing session-only Cloud
routes or reuse a long-lived user/service token as publication authority.

Migration `000036_skillhub_publication_upload_grants` mirrors startup creation.
It adds one FK-dependent table; previous migrations are unchanged. Down refuses
any retained grant (including expired/cancelled records) and never cascades.
Rollback newer dependent tables before older publication tables. Tests cover
empty SQL/startup, frozen pre-grant relevant tables (pre-PR620 user/Bot fixture
plus migrations 000028/000030/000035 and an existing intent), both upgrade orders,
repeated up/startup, safe empty down/up and refused populated down. Live grants
and immutable legacy Bot/entry facts survive initialization. No retention/GC is
introduced; expiry removes authority, not replay evidence.

## Cloud peer upload grant introspection

`POST /api/skillhub/publications/internal/upload-grants/introspect` is a
service-only, read-only check. It does not issue/renew credentials, capture files,
create a Cloud task, change publication state or reserve permission to commit.
It adds no schema. All configuration is loaded at startup, separately from human
login and existing publication flags:

| Variable | Policy |
| --- | --- |
| `CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED` | Defaults off; only trimmed `1` or case-insensitive `true` enables the route. |
| `CATSCO_SKILLHUB_PUBLICATION_ISSUER` | Canonical HTTPS origin of CatsCo. |
| `CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE` | Canonical HTTPS origin of the single allowed Cloud peer, different from issuer. |
| `CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET` | Dedicated 256-bit random secret encoded as exactly 64 lowercase hex characters. Never reuse a human JWT, Bot key or existing service token. |
| `CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS` | Optional comma-separated canonical proxy networks, at most 16, no `/0`. Empty means no proxy is trusted. |

Disabled returns 404. Invalid enabled configuration or an unsupported store
returns 503 without affecting other endpoints. A request requires direct TLS,
or an explicitly trusted socket peer with one exact `X-Forwarded-Proto: https`
header. Do not trust a shared client network: the configured reverse proxy must
strip incoming forwarded-protocol headers and set its own based on TLS. Global
proxy settings and caller-supplied forwarded headers do not establish trust here.
Restrict network access to the Cloud service in addition to application auth.

Provision the dedicated peer secret through the deployment secret manager, not
source control, chat, URLs or command logs. Configure both services consistently;
rotation replaces the single accepted secret and requires CatsCo restart and a
coordinated Cloud update. During a mismatch checks fail closed; there is no
fallback to user auth. Redact both credential headers at every proxy/APM layer:

```text
Authorization: Bearer <dedicated Cloud peer secret>
X-SkillHub-Upload-Grant: pubupload_<opaque 64 hex characters>
Content-Type: application/json
```

The body has exactly these eight fields; issuer/audience are server configuration,
and publisher/Bot identity is read from the stored grant, never accepted as input:

```json
{
  "schemaVersion": 1,
  "entryRequestId": "pubentry_0123456789abcdef0123456789abcdef",
  "action": "candidate.upload",
  "candidateRevision": 1,
  "authorityEpoch": 1,
  "requestHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "sourceSnapshotHash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "requestBytes": 1234
}
```

Unknown, duplicate, differently-cased, missing or null fields, trailing JSON,
query strings, browser Origin, compressed/chunked bodies and duplicate credentials
are rejected. The JSON body needs a known positive Content-Length of at most
4 KiB; the declared candidate size is separately bounded to 75 MiB. A five-second
deadline covers body read and store access, with a socket read deadline (an
unsupported ResponseWriter fails closed). Limits per process are 120/minute
with burst 20 for the peer, 30/minute with burst 5 per entry, and 16 in-flight
checks. Rate/concurrency failures return Retry-After: 5. Deployment-wide header,
connection and unauthenticated-request limits are still required.

Valid checks return `schemaVersion: 1`, `active: true` and an explicit `grant`
projection: `delegationId`, `issuer`, `audience`, `action`, decimal-string
`actorCatsCoUid` and `botUid`, `entryRequestId`, `candidateRevision`,
`authorityEpoch`, `requestHash`, `sourceSnapshotHash`, `requestBytes`,
`idempotencyKey`, UTC `expiresAt` and database `observedAt`. It contains no token
or token hash, local path/reference, device/body/installation, node or socket.
The entry is locked before reading canonical grant facts; cancellation/rotation
serialize with the check, and current human/Owner/body/generation are revalidated.
Successful reads do not mutate the entry, grant or outbox.

Unknown, mismatched, expired, rotated, cancelled or stale-authority grants all
return the same HTTP 200 `{ "schemaVersion": 1, "active": false }`. Malformed
requests return 400 (oversize 413), bad service credentials 401, insecure
transport 403, unsupported methods 405, throttling 429, and storage/deadline or
invalid adapter results 503. Responses are no-store; errors do not expose raw
storage exceptions. A transient failure is not a format rejection or a reason
to invoke a model repair.

**Not a commit permit:** `active: true` describes authority at `observedAt` only.
Cancellation can occur immediately afterward. The future Cloud client must pin
the configured HTTPS origin and exact route, reject redirects, redact both
headers, validate the exact response schema/scope/expiry and independently hash
the actual candidate bytes. Cloud implements its introspection client and held
intake separately. The receipt receiver below does not implement Cloud delivery
or the cancel/commit protocol. Keep production disabled until those pieces and
trusted private capture/issuance pass joint isolation tests.

## Cloud acceptance receipt receiver

`POST /api/skillhub/publications/internal/cloud-receipts` records the association
between an existing CatsCo intent/grant and a held Cloud task. It is independently
disabled by default (`CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED=true` opts
in). No deployment configuration enables it. It uses the same explicitly
configured issuer, Cloud audience, peer secret and HTTPS/proxy policy as
introspection; a user JWT, Bot key or cookie cannot authenticate this endpoint.
The adapter must support `SkillHubPublicationCloudReceiptStore` or it fails closed.

The strict JSON object has exactly these fields:

```json
{
  "schemaVersion": 1,
  "entryRequestId": "pubentry_0123456789abcdef0123456789abcdef",
  "candidateRevision": 1,
  "acceptanceEpoch": 1,
  "requestHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "sourceSnapshotHash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "requestBytes": 1234,
  "delegationId": "pubgrant_0123456789abcdef0123456789abcdef",
  "idempotencyKey": "pubcandidate_0123456789abcdef0123456789abcdef",
  "taskId": "pubtask_11111111-1111-4111-8111-111111111111",
  "eventId": "pubevent_22222222-2222-4222-8222-222222222222"
}
```

`acceptanceEpoch` is Cloud's immutable **first acceptance** epoch, not the latest
upload token epoch. Cloud's sender must derive it and the remaining fields from
its persisted association/task/outbox, not from a model or browser callback.
For revision 1, `sourceSnapshotHash` is the accepted candidate hash. Issuer and
audience come from CatsCo configuration; the actor/Bot remain those of the stored
entry. No actor UID, namespace, issuer URL, file, status or upload credential is
accepted in JSON. The upload-grant header is explicitly rejected: a durable
callback must not carry short-lived bearer material.

The receiver uses the existing 4 KiB body cap, five-second deadline, 16 shared
in-flight slots, peer rate limit, strict transport/JSON rules and no-store error
redaction. Receipts have their own per-entry limit of 30/minute, burst 5.
HTTP 200 is returned only after durable commit or an exact replay. Unknown or
conflicting scope/task/event returns generic 409 `publication.receipt_conflict`;
storage uncertainty returns 503 and may be retried with the **same** receipt.

Migration `000037` and matching startup DDL add one immutable receipt per entry,
with unique Cloud-audience/task and Cloud-audience/event pairs. The receiver locks
the entry before reading the grant, serializing with issuance and local cancel.
It requires the same grant ID/key, issuer/audience, revision, exact request/source
digests and byte count, and `1 <= acceptanceEpoch <= stored grant epoch <= 8`.
A replay must match every stored receipt field, including its original epoch.
It never changes the entry, delivery outbox, grant, token or publication state.

Receipt delivery is historical reconciliation, not an online authority check.
Expiry, renewal, disconnect, Owner transfer or account disablement must not erase
evidence of an earlier Cloud acceptance. These events therefore do not prevent a
matching authenticated receipt from being saved, but saving it restores **no**
authority. The reply contains only `schemaVersion: 1`, `recorded: true`, the
entry/task/event IDs, `localIntentCancelled` and `commitAllowed: false`.
`localIntentCancelled` is a transaction-time observation, not remote cancellation
confirmation; false is never permission to execute. Cloud must continue holding
the task regardless of this flag until the future commit-permit protocol permits
publication.

If local cancel wins the entry lock, the late receipt is retained and the intent
stays cancelled. If a receipt is already recorded, the existing **pre-Cloud-only**
cancel action returns conflict instead of claiming a distributed cancellation.
An already cancelled intent remains idempotently cancelled after receipt arrival.
The callback acknowledgement is never sufficient to mark a Cloud task cancelled.

Receipt evidence survives restart and candidate expiry. Update/delete guards and
the entry foreign key prevent accidental rewriting or deletion; down migration
refuses populated tables and permits empty rollback only. An explicit lifecycle
for tombstones and cleanup remains a production activation gate. Older binaries
do not understand the receipt-aware cancellation guard: do not roll back to them
with retained receipts. Disable admission/receipt ingestion and deploy a compatible
binary instead; stopping ingestion alone is not permission to delete evidence.

This change implements the CatsCo receiver only. Cloud outbox delivery/recovery,
owner-private Cloud status projection, distributed cancellation, commit permits,
legacy issuer verification and runtime capture/upload remain separate integration
work. Current entry/source-status projections are unchanged and must not be used
to infer Cloud task execution or success. No UI, model billing or production Bot
change is included.

## Internal durable cancellation intent

After Cloud PR36's held-task cancellation foundation, CatsCo now has an internal
`SkillHubPublicationCancellationIntentStore`. There is **no HTTP/RPC adapter,
worker, environment switch or production caller** for it. Deploying this schema
does not activate cancellation delivery, change the existing browser capability
or call Cloud. The existing local-only cancellation route still returns 409 for
an uncancelled entry with a recorded Cloud receipt.

`RequestSkillHubPublicationCancellation` requires the active original human to
still own the Bot, using the same entry → human → Bot row-lock order as local
cancellation. A future caller must derive actor UID from trusted human context,
not model arguments. Offline/disabled/rebound Bots do not stop their current
Owner cancelling an old intent. Neither a new Owner nor an ex-Owner can mutate
another ownership period. No user token, source path or new publisher identity
is stored in the cancellation record.

Under the entry lock, this method cancels **local intent** and inserts one
immutable cancellation ID/time in the same transaction. The record freezes
whether retained upload authority or Cloud acceptance evidence exists. Failed
insertion rolls back the local cancellation too. Repeated actions and lost
replies reuse the same ID/time. Upload issuance, renewal and receipt recording
share this ordering point, so cancellation cannot miss an issuance racing with
it. A future commit-permit issuer must also share this ordering point and define
pending/too-late outcomes before it can be enabled.

| Internal state | Meaning |
| --- | --- |
| `local_only` | No retained upload grant or receipt exists; cancelled local intent prevents new upload grants |
| `waiting_receipt` | Upload authority existed; absence of a receipt is not proof that Cloud did not accept |
| `ready` | The original receipt is available for future cancellation delivery; not sent or remotely confirmed |

`GetSkillHubPublicationCancellationIntent` reads one committed snapshot for the
original active human. It derives waiting/ready from the retained receipt rather
than updating a second mutable state: a late callback naturally makes the saved
intent ready, including after process restart or Bot transfer. New Owners and
friends do not inherit the history. Reads do not send anything or retry a job.
The returned internal record contains only cancellation/entry IDs, state and
requested time, and is not a browser response schema. Existing `cloud-status`
still reports `executionState:unknown`.

Migration 000038 and startup DDL add one bounded row per existing entry, with
guards against rewriting/deleting its evidence, contradictory insertions and
reviving the associated local intent. It does not alter original inputs, receipt
evidence, capture outbox, grants, files, Bot definitions or model configuration.
The migration does not auto-enrol old cancellations. An explicitly authorized
call may adopt a legacy local cancellation; a policy for draining historical
records after ownership loss is still a rollout prerequisite, not permission
to guess authority or reassign an Owner.

Apply the migration (or compatible startup DDL) before invoking the new store.
Down refuses any retained cancellation intent and only removes an empty ledger,
in reverse migration order without CASCADE. Existing legacy cancellation remains
usable without this table. Rollback means disabling future admission/delivery
while preserving compatible storage and recovery, not dropping pending work.

This is not a durable sender: leases, bounded retries, independently authenticated
service endpoints, exact receipt delivery to Cloud PR36, durable remote outcomes
and operator recovery remain required. There is no cancellation-confirmed state
or commit permit in this release, so no UI may use `ready` or local `cancelled`
to promise remote completion. Full capture/upload, retention and isolated
cross-repository acceptance remain production gates.

New PostgreSQL tests cover all three states, process reconstruction, historical
reads and authority changes, concurrent receipt/duplicate requests, upload-grant
races, atomic failure/lost replies, immutable evidence, legacy API regression,
populated rollback and missing-schema recovery. CI runs them in the existing
publication database group with the race detector; local DSN skips are not
database verification. These are real CatsCo database tests, not a complete
Cloud cancellation transport or Bot end-to-end acceptance test.

## Remaining integration

Durable **pre-probe** admission is implemented by v2. UI integration of delivery progress, safe resume, and the
later capture/upload adapter remain required. Initial browser submission is still
same-node and online-only. The worker resumes already accepted requests, not new
offline requests. No production switch should be enabled on these tests alone.

There are no snapshot/upload dispatchers. Existing workspace
buttons, chat publishing, Bot binding, model switching and private backup paths
are unchanged. Enabling status reads alone does not activate entry admission.

Chat inputs are rejected until a trusted live-turn grant and owner-private topic
authorization are implemented. The current schema supports only the initial
workspace state; later lifecycle changes require explicit schema evolution,
compare-and-swap and crash-recovery tests, not arbitrary state strings.

The rest of contract stage 1 remains: durable cross-node/offline intake, chat
authorization, authenticated grant issuance and the Cloud introspection client,
Cloud-linked cancellation and commit-permit ordering. XiaoBa source-probe is
implemented separately; capture adapters, Cloud upload and the workspace UI
still need to be connected and jointly tested.
Repair conversations, automatic formatting repairs, approvals, model billing,
Chinese guides and frontend redesign are not part of this change.

## Durable cancellation delivery

After PR661's immutable cancellation intent, `PublicationCancellationDispatcher`
can deliver that exact intent to Cloud PR37's authenticated control endpoint.
Construction is explicit and default off. There is no environment switch, Hub
registration, user-route change, browser projection or production caller yet.

Migration 000039 and startup DDL add a separate delivery ledger without modifying
the immutable intent/receipt. Claim selects only retained cancelled intents with
a matching historical receipt and configured issuer/audience. Missing receipts
wait without spending attempts; receipt arrival makes them eligible. Local-only
and legacy cancellations without the new intent are never auto-enrolled.

Claims use the entry lock, a random lease token, 30-second expiry, SKIP LOCKED and
eight persisted attempts. SQL never holds a lock across HTTPS. Finish matches the
exact receipt, cancellation ID, attempt, lease token and expiry. A stale process
cannot overwrite a newer result. Retries use persisted exponential backoff from
five seconds to five minutes; crashes also consume attempts. Exhaustion is not
cancellation success. The polling loop runs every five seconds, with at most four
concurrent run-once calls per dispatcher.

The HTTPS client uses a fixed configured audience, a dedicated service secret,
normal certificate verification, no proxy environment, no redirects, six-second
transport budget and 4 KiB response limit. It rejects malformed/duplicate/unknown
fields, nulls, encodings and caching. Confirmation requires the exact task, entry,
event and cancellation IDs, revision 1, `cancelled`, unavailable candidate bytes
and valid cancellation/observation times. It does not infer success from local
state, HTTP 202, an unknown reference, conflict, timeout or connection loss.

| Result | Persisted delivery state |
| --- | --- |
| Exact Cloud cancellation acknowledgement | `confirmed`, immutable remote timestamps |
| Lost reply, timeout, authentication/configuration/transport failure | `pending` with retry backoff, eventually `exhausted` |
| Exact authenticated unknown-reference or execution-conflict reply | `needs_attention`, no success timestamp |

Delivery completes an already authorized action and may finish after human
disablement, Bot transfer or disconnect. It never creates a new cancellation.
Historical result reads still require the original active human; the new Bot
Owner and friends cannot inherit them. No raw secrets, source bytes or errors
are stored in this ledger. Confirmed/blocked/exhausted evidence cannot be reset
or deleted, and populated down migration refuses rollback. Recovery from a
terminal failure needs a future explicit reconciliation procedure, not row deletion.

CatsCo CI includes the existing real-PG suite. The two-repository contract runs
in the private Cloud repository (which can check out public CatsCo without
sharing private-repository credentials), with a pinned CatsCo revision and the
Cloud control implementation from PR37. That contract uses both actual PostgreSQL stores, the real
CatsCo sender and Cloud HTTPS handler: cancel before delayed receipt, committed
Cloud cancellation with lost response, CatsCo retry state, reopened adapter and
dispatcher, replay confirmation with the original timestamp, and no resending
after confirmation. Intake authority in that fixture is injected from a real
CatsCo grant; runtime capture and live upload introspection are not exercised by
this contract and it is not full Bot publication acceptance.

Next: authenticated commit coordination and Cloud-side commit/cancel ordering,
then runtime capture/upload, historical task state, retention and isolated user-flow wiring.
Do not expose the new cancellation path or activate the dispatcher until those
rollout prerequisites are satisfied. Existing HTTP cancellation semantics and
Cloud-status responses remain unchanged.

## Frozen publication intent and CatsCo commit permits

Migration 000040 and the matching startup DDL add two bounded, immutable records
per entry: an exact publication intent and a one-task commit permit. They are
internal storage methods, with no human admission adapter or worker registration.
The default-off service transport described below does not enable publishing or
change existing browser status/cancellation responses.

`RecordSkillHubPublicationCommitIntent` requires the original active human,
current Bot ownership, active/enabled Bot and unchanged body, plus an exact
already-recorded Cloud receipt. The future trusted admission adapter must derive
the human from authenticated action context, verify Cloud publisher mapping and
the frozen release/reservation, and satisfy any required human confirmations.
Never decode a browser or model payload directly into this internal input; the
store does not query Cloud or validate that its claimed publisher IDs exist.
In particular, `confirmVersionPublish` is a recorded decision, not evidence that
a human approved a newly generated description. Semantic repair approvals remain
a separate rollout prerequisite; these methods do not authorize automatic repair.

The intent freezes the receipt (entry/task/event, peer origins, first acceptance
epoch, revision and source/request digests), Cloud candidate and manifest digests,
Cloud publisher user/profile/namespace, Skill ID, reserved version/upload ID,
release timestamp and version-confirmation choice. Changed retries conflict.
The integrity hash is a CatsCo-local typed-JSON digest, not a cross-language JSON
canonicalization contract. No candidate bytes or bearer token are saved here.

`AuthorizeSkillHubPublicationCommit` must be called only by an independently
authenticated, fixed Cloud peer. It requires the exact stored receipt and intent,
locks the entry before checking the current human/Bot authority, and writes one
immutable permit ID and database timestamp. Current upload epoch is recorded
separately from the receipt's first-acceptance epoch; it is not a global ownership
epoch. The store rechecks live authority instead of treating a past grant as live.

After upload, an expired upload token or ordinary transport disconnect/reconnect
does not revoke the saved candidate. First permit issuance still fails on local
cancellation, inactive human/Bot, Bot disablement, ownership or body change.
There is no network or filesystem work under a database lock.

| Order at the CatsCo entry lock | Result |
| --- | --- |
| Cancellation commits before first permit | No permit can be issued |
| Permit commits before cancellation | Same permit remains recoverable; cancellation still needs Cloud confirmation |
| Reply lost or process restarted | Exact replay returns the same permit and timestamp |
| Different task, candidate, publisher, release or confirmation | Conflict, never a second authorization |

Permit replay is recovery of an already-authorized action, so the exact service
replay survives later local cancellation or authority loss. It is not permission
to revive a Cloud-cancelled task. Cloud must atomically record the permit with its
own commit transition, serialize that transition against cancellation, and keep
its own publisher/package checks. **That Cloud integration is not in this slice.**
Existing Cloud guards still reject delegated commits. Neither a permit nor local
`cancelled` means published or remotely cancelled. `ready` in the internal cancel
intent means only that a receipt is available to send; it does not promise that
Cloud will accept cancellation after commit starts.

Private historical reads require the original active human; transfer does not
give a new Owner, a friend or the Bot access. SQL triggers reject updates/deletes
and verify first-insert authority and epoch. Populated rollback fails closed;
empty rollback must run before migration 000037/000028 rollback. Startup never
creates intents or permits from old receipts. Intent admission remains internal;
the separately gated service endpoint below only authorizes existing intents.
Production activation still requires admission authentication/approval, Cloud
commit recovery and the cross-repository cancellation race contract.

Seven real PostgreSQL groups cover exact scope, concurrent replay and both cancel
orders, restart/lost-reply recovery, authority loss before/after permit, offline
completion, history privacy, repeated migrations/immutable guards and failed-write
rollback. Unit tests cover bounded target validation and identity hashing. CI's
existing publication PostgreSQL group runs them with the race detector; local
skips without a DSN do not count as database verification.

## Authenticated commit permit transport

`POST /api/skillhub/publications/internal/commit-permits/authorize` is independently
default-off under `CATSCO_SKILLHUB_PUBLICATION_COMMIT_PERMITS_ENABLED`. It reuses
the configured issuer, Cloud audience, dedicated peer secret and explicit trusted
TLS proxy CIDRs. The Host must match the issuer (default HTTPS port spellings are
equivalent). Human/Bot sessions, upload grants, Origin and Cookie headers cannot
authorize this endpoint. Introspection and receipt ingestion can remain off.

The body has exactly `schemaVersion: 1`, the existing 11-field historical
`receipt`, and the exact 10-field `target` defined above. Unknown, duplicate,
case-mismatched, null or malformed fields fail closed. Issuer/audience come from
configuration, never the caller. There is no intent-recording fallback: a trusted
human admission path must have already stored the exact verified release facts.

Success returns only `schemaVersion`, `receipt`, `target`, `permitId`,
`intentHash`, `authorityEpoch` and `authorizedAt`. Timestamp precision is retained
as RFC3339Nano; consumers must not round PostgreSQL microseconds to JS milliseconds.
Only exact 403 `publication.commit_denied`, 409 `publication.commit_cancelled`
and 409 `publication.commit_conflict` are stable policy outcomes; outages remain
503. Replies are non-cacheable. The existing 4 KiB body, 5 second deadline,
16 concurrent requests and global/per-entry rate limits apply.

`TestSkillHubPublicationPeerCommitPostgres` uses real PostgreSQL and HTTPS in
CatsCo CI. `TestSkillHubPublicationCommitCloudContract` additionally loads the
actual Cloud Node client from `SKILLHUB_COMMIT_CONTRACT_ROOT`; Cloud CI pins this
CatsCo checkout and requires the test dependencies. Both prove no service-side
intent creation, exact-target rejection, permit commit followed by lost HTTP
response, reopened-handler recovery with identical ID/time, cancel-before-permit
denial and historical replay after later cancellation/disablement. Receipt and
human intent are explicitly seeded test facts, not real user approval or a full
publication E2E. No Cloud package-writing worker is registered by this transport.

## Cloud plan and execution transport

`NewPublicationCommitCloudClient` is an explicit, default-off service client for
Cloud's fixed `/api/skills/publication-delegations/commit` HTTPS endpoint. It
supports `Plan`, `Authorize` and read-only `Read`. The original complete receipt
must match configured issuer/audience. No browser/model identity or upload grant
is substituted for service authentication; no caller URL, proxy fallback or
redirect is followed. Replies are bounded to 8 KiB, headers to 8 KiB, calls to
22 seconds and concurrent operations to four, with strict nested JSON/echo checks.

`Plan` returns one exact frozen target and verifies candidate/confirmation
binding. Its boolean is not human authorization. A trusted human admission
adapter must independently validate authority and required confirmations and
record that exact intent before `Authorize`. This transport does not write an
intent, issue permits, retry work, register a Hub worker, read a browser account,
or enable any production switch. Receipt access itself must be authorized by
that future adapter; do not expose this client as a generic RPC/model tool.

An authorize ACK distinguishes committing, queued, failed and published, but
contains no package result. Only `Read` with a validated published result supplies
the exact Skill/version/package/checksum/time. Held and cancelled observations
cannot contain a permit or final result; post-commit observations must contain
the frozen target and permit. Private source, internal proof and upstream error
text are not exposed. Wire results are internal, not a user-facing projection.

TLS tests cover scope, malformed/duplicate/null/extra fields, status consistency,
untrusted certificates, redirect and response-loss failures, bounded concurrency
and context cancellation. The race CI group explicitly includes CommitClient.
The paired Cloud contract exercises this actual Go client against its real Node
handler/coordinator/worker and both PostgreSQL adapters. Human intent and Runtime
source probing remain test setup, not a live user publishing experience.

## Verification

### Human publication confirmation

An independently default-off `CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED`
registers `/api/skillhub/publications/commit/entries/{entryRequestId}/` with three
actions. It uses the existing fixed issuer, Cloud audience and dedicated peer
secret configuration. It is not a model/RPC tool and cannot use Bot/API-key or
service credentials in place of the active original human's Bearer JWT.

- `POST plan` takes exactly `{schemaVersion:1,confirmVersionPublish:boolean}`.
  The server loads the original retained receipt, checks current Bot ownership,
  body and enabled state, and obtains the immutable Cloud plan. The response
  displays only Skill ID, version, publisher namespace, the version choice and a
  `confirmationKey` bound to the complete plan and receipt. Planning never writes
  human intent. A required new-version confirmation returns 409, not auto-approval.
- `POST confirm` takes exactly `{schemaVersion:1,confirmationKey:string}`. It
  retrieves the current immutable Cloud target independently; the caller cannot
  send receipt, target, publisher, source, permit or actor fields. A changed key
  returns 409. The exact human action is recorded by the existing SQL transaction,
  which rechecks current authority and serializes against cancellation; only
  after this commit is Cloud authorization attempted. The key is a content
  binding, not a bearer credential or proof of authority by itself.
- `GET status` has no body/query and reads verified execution state and, only
  when published, Skill/version/package/checksum/time. Original active-human
  privacy survives Bot ownership transfer; new owners do not inherit history.
  Internal receipt/permit IDs, paths and manifest hashes are never projected.

Authorization returns 202 `commit_accepted` with `executionState:unknown`, even
if its ACK claims published. Only verified status supplies the final result.
Timeout/lost reply after intent recording is uncertain, not proof of rollback:
read status, then retry the same entry/key if appropriate. No new version or
intent is created by exact replay. A missing receipt means acceptance has not
been observed, not that uploading never happened.

Four calls may be in flight, with bounded per-actor/entry rates, a 2 KiB body,
five-second body deadline and a 55-second operation context. Slots remain held
until the actual call settles. Responses recheck original-account access after
peer IO. Errors are static and private/no-store; no upstream SQL/content/token
is returned. Cookies are not authentication; incidental browser cookies are
ignored. Duplicate/unknown/null fields and malformed paths fail closed.

This slice handles publication of an already accepted immutable candidate; it
does not approve semantic repairs or synthesize description text. Future repair
revisions require their own exact draft confirmation before being eligible for
this publication step. No UI, Runtime capture/upload or automatic CatsCo intent
dispatch is enabled. Cloud's separately gated delegated scheduler can execute
only commits for which authorization already completed. Keep all new production
switches off until isolated end-to-end acceptance.

Tests cover signed human JWTs, exact plan binding, privacy, authority changes,
cancel/deny/write failure, lost-reply replay, result consistency and concurrency.
The existing required PostgreSQL peer test also drives the human handler over
real TLS/JWT/PostgreSQL, with a synthetic Cloud plan/status adapter and a forced
execution-call failure; it verifies durable exact intent before actual peer
permit recovery. The separate dual-store contract tests real Cloud and CatsCo
clients/handlers. Neither test is represented as a live UI/Runtime E2E.

### Durable delivery of confirmed publication intents

`CATSCO_SKILLHUB_PUBLICATION_COMMIT_DISPATCH_ENABLED` defaults off. When enabled,
the server starts an independent bounded worker using the dedicated publication
issuer, Cloud audience and peer secret. It requires the PostgreSQL adapter and
migration 000041 (also mirrored in startup DDL), the Cloud commit service and
CatsCo permit service. It does not enable human admission or Cloud execution.

The existing immutable human intent is queue admission. Receipts alone never
qualify, and the worker never calls plan, records intent or issues a permit.
Existing confirmed intents become eligible only when this switch is explicitly
enabled; the migration itself backfills neither intents nor delivery records.
Each worker iteration claims one due intent, then waits five seconds. Database
leases last 90 seconds; transport operations have a 60-second budget with at most
four concurrent calls, and finish bookkeeping has an independent five-second
budget after request cancellation. Multiple instances share fenced SQL leases.

The worker reads Cloud first, compares the exact frozen target, and authorizes
only the same held target. A committed observation or acknowledgement is retained
as `accepted` only with a matching local permit. This means delivery accepted,
**not published**; the existing human status route verifies final execution.
Historical permit recovery does not require a live Bot/current Owner, but first
permit issuance still rechecks authority and serializes against cancellation.

Transient failures retry with durable backoff, at most eight transport attempts
(distinct from three Cloud publishing attempts and three bounded format-repair rounds).
Cancellation, denied authority, target conflict and missing task stop in
`needs_attention`; budget depletion is `exhausted`. These states mean automatic
delivery stopped, not proof that an uncertain remote commit rolled back. Terminal
evidence cannot be reset/deleted. The original active human can read the private
store projection; this PR adds no UI/status endpoint for delivery bookkeeping.

An empty down migration is idempotent and atomic; populated rollback refuses.
Tests cover lease races, expiry, lost replies, reopen, write rollback, bounded
attempts, privacy and terminal retention. The required PostgreSQL/TLS peer test
uses real human admission and permit requests with an explicitly synthetic Cloud
observation adapter. It verifies dispatcher recovery after a lost permit reply,
unchanged permit identity and cancellation-first refusal, not live Runtime upload.
Production switches remain off pending isolated end-to-end acceptance.

## Immutable runtime capture evidence

### Internal authenticated capture transport

`Hub.CaptureSkillHubPublication(ctx, actorUID, entryID)` is an internal Go service
operation. It does not register an HTTP/browser/model endpoint or scheduler.
It is disabled unless both the existing workspace-entry gate and
`CATSCO_SKILLHUB_PUBLICATION_CAPTURE_ENABLED=true` are set at Hub construction.
No production enablement is part of this change. A future lifecycle-bound
dispatcher must call it explicitly with an already accepted entry.

The transport reads the original actor, Bot and source from admission, requires
a persisted successful source probe, and resolves the current authenticated
node-local Bot socket, device, body lease, installation and durable connection
generation. Reconnect may advance the generation but cannot move the source.
The runtime must advertise `skillhub.publication.capture.v1` in addition to the
probe capability. Old runtimes and remote-node routes fail closed.

Each request has a server-generated `pubcapture_` ID. Its only payload fields
are `entry_id`, `bot_uid`, `local_skill_id` and `workspace_generation`; the owner
and device envelope is derived from the authenticated route. It never carries
paths, file contents, upload tokens or a publication confirmation. The reserved
`skillhub.publication.*` namespace remains rejected for generic client requests.
Both source selectors are opaque references under the admission/store/schema
contract (`^[A-Za-z0-9._:-]{1,128}$`) and are forwarded unchanged. For example,
`local-skill-1` / `workspace-v1` remains valid here; this transport does not require
a `pubsrc-` prefix or interpret a workspace generation as a digest. The runtime
must still verify that these selectors identify its admitted source.
The expected result has exactly six fields matching XiaoBa's retained receipt:
`schemaVersion:1`, `entryId`, `candidateRevision:1`, `requestHash`,
`sourceSnapshotHash`, `requestBytes`. Digests must be lowercase SHA256 and bytes
must be an integer from 1 to 32 MiB, the current runtime transport ceiling.
This does not lower the store's independent 75 MiB authority ceiling below.

Only the exact authenticated runtime can claim the pending response. Owner
sessions, other sockets, late/unknown IDs, mixed errors/results, extra fields
and mismatched owner/device/tool/entry facts cannot supply capture evidence.
The read loop queues only validated scalar facts and performs no DB work;
the calling goroutine rechecks live authority before recording. Duplicate replies
cannot replace a queued/in-flight write. Reserved late replies never fall through
to generic routing/logging. The ack is 200 only after the store succeeds; it is
not an upload or publication receipt. A returned record may describe the original
connection after a legitimate reconnect; it is historical evidence, never current
upload authority by itself.

There are eight operation slots per Hub, one operation per entry and a maximum
25-second deadline bounded by the caller's context. Sending is nonblocking and
never disconnects a Bot for backpressure. There is no automatic retry or new
durable attempt budget: future orchestration must provide bounded scheduling and
recovery. A process loss can leave a private runtime capture, but no inferred DB
receipt; a new authenticated request must return the same retained candidate.
Cancellation can race with local capture, but store checks reject its late receipt
and this operation never grants upload authority.

Tests cover opaque selector pass-through, invalid admission references, strict
receipt digests/byte bounds, bounded dispatch, spoof/malformed/duplicate/late replies, owner and
generation changes, cancellation, disabled/legacy runtimes and a blocked database
without blocking the socket read loop. The mandatory PostgreSQL contract uses
real Hub routing and storage across adapter/reconnect reconstruction, candidate
substitution and cancellation. Its runtime peer is synthetic; the XiaoBa RPC
consumer, durable capture scheduler and upload-grant delivery remain to be wired
before live three-repository acceptance or production enablement.

Migration 000043 adds a retained capture receipt per entry, distinct from the
read-only `source_verified` probe. It stores candidate revision 1, request SHA256,
Cloud candidate SHA256, exact request byte count (at most 75 MiB, matching grants), and the
observed route generation/node/socket with a database timestamp. It stores no
files, local paths, upload secrets or model content. UPDATE, DELETE and populated
down migrations are rejected; startup/up never infer evidence from old probes
or grants.

The capture migration was renumbered from 000042 to 000043 after PR674 merged:
the independently merged session-fork migration already owns 000042. The capture
up/down SQL is unchanged. Applying 000043 to a table already created by startup
DDL preserves retained receipts and grants, and populated rollback remains
forbidden. No migration ledger is reset or forced. SQL-only installations that
manually recorded the old capture-only 000042 must check that the session-fork
schema is also present; a version number alone cannot identify which colliding
file was applied. Normal startup initializes both schemas idempotently.

This is the CatsCo metadata/authority contract, not a promise that every runtime
or transport can upload 75 MiB. Capture validation aliases
`SkillHubPublicationUploadMaxBytes`; migration 000043 and startup DDL use the same
78,643,200-byte ceiling. No candidate in the previously valid (32 MiB, 75 MiB]
range is silently stranded between recording its receipt and issuing its grant.

The separate XiaoBa implementation currently has a smaller limit. In merged
[XiaoBa PR541](https://github.com/buildsense-ai/XiaoBa-CLI/pull/541),
[`publication-upload.ts`](https://github.com/buildsense-ai/XiaoBa-CLI/blob/d7b3cbdea91bc300a0a5b3008cee51b83c0fc760/src/skillhub/publication-upload.ts#L46)
rejects a grant request body larger than 32 MiB;
[`publication-capture.ts`](https://github.com/buildsense-ai/XiaoBa-CLI/blob/d7b3cbdea91bc300a0a5b3008cee51b83c0fc760/src/skillhub/publication-capture.ts#L24)
limits the serialized private envelope to 32 MiB (including envelope overhead,
not just the HTTP request body). Those are runtime implementation limits, not
CatsCo protocol constants. This PR does not increase them or Cloud's configured
HTTP body limit, and does not claim end-to-end support for 75 MiB. A future
dispatcher must respect the effective runtime/Cloud limits before issuing work.

`RecordSkillHubPublicationCapture` is an **internal store API**, not a public
HTTP/RPC endpoint. The internal Hub capture transport correlates a response
with its own outstanding request and rechecks the exact live Bot socket, device,
body lease and installation before calling it. The database independently checks
the original active human, Bot ownership/enabled state, immutable source selector
and durable connection generation, under the same entry lock used by cancellation
and grant issuance. A probe alone does not prove that an immutable envelope exists.

First capture freezes the candidate. Exact replay, including an authenticated
reconnect of the same source, returns the original evidence and time. New bytes,
digests, device/body/installation, generation rollback or same-generation socket
substitution cannot replace it. Replays still check current authority and reject
cancelled entries. Source files are not copied, modified, executed or deleted by
CatsCo.

New upload grants and renewals now require that stored receipt and matching
digests/size/revision/binding. Pre-042 grants are not retroactively converted into
evidence: an unexpired grant remains introspectable until its original expiry,
subject to the existing authority checks; renewal requires an actual matching
capture receipt. A late receipt cannot contradict a candidate already frozen in
a legacy grant. No new grants are backfilled or automatically issued.

Tests explicitly seed trusted-runtime receipts where an upload is required;
source-probe dispatch tests are **not** live capture tests. The Cloud commit-peer
contract fixture must likewise record its actual captured digests when its CatsCo
pin advances past this change. Authenticated Runtime capture RPC is implemented
in XiaoBa PR546. The default-off scheduler below consumes that capability;
upload grant delivery and complete isolated publishing acceptance remain separate
work. No production flag, capability or UI is enabled here.

### Durable capture scheduling

`CATSCO_SKILLHUB_PUBLICATION_CAPTURE_DISPATCH_ENABLED` is a separate default-off
gate. A worker is started only on the final server Hub, with an enabled capture
transport (which also requires the workspace-entry and capture gates). Shutdown
cancels and joins its goroutines. Unsupported stores have no in-memory fallback.
No deployment configuration enables this gate.

Every sweep visits up to 16 local Bots in rotating order, selects at most eight
tasks, and uses at most four concurrent workers. A sweep is followed by a
five-second pause. Selection has its own five-second deadline. Only the exact
online owner/Bot/device/body/installation with capture capability is considered;
offline or unsupported runtimes consume no attempt. SQL applies that scope
before LIMIT and accepts only verified sources lacking immutable capture evidence.
Each claim and receipt independently rechecks current authority.

Migration 000044 and startup DDL add a separate durable capture-delivery ledger.
An entry row lock serializes claims with cancellation and capture recording.
Each claim has a fresh token, exact route binding, attempt and 40-second lease.
The transport still has a 25-second deadline; the caller reserves five seconds
before lease expiry for settlement. Retry backoff starts at five seconds and is
exponential, capped at five minutes. At most eight transport attempts survive
restarts; this is **not** the three-round format-repair budget.

Only well-formed, bound runtime busy/timeout errors are transient. Malformed
receipts and source/security rejections stop at `needs_attention`; raw runtime
messages never enter the ledger or logs. Route loss and database/network failures
are bounded retries. A stale finisher cannot change another lease. A persisted
capture always removes the entry from scheduling, even if the process died before
marking the lease finished. No source probe, successful send or timeout can
fabricate capture evidence. Populated rollback refuses to erase retry history.

Internal diagnostics use `skillhub_publication_capture_deliveries` for attempts
and fixed codes and `skillhub_publication_captures` for authoritative evidence.
If evidence exists, a stale `leased` diagnostic row only means the finishing ACK
was lost. These internal states are not a published Skill, Cloud task, upload
authorization or user-facing completion. Capture status projection and upload
grant delivery are subsequent integration work; do not enable this worker as a
standalone user release.

Tests include real PostgreSQL restart/lease/exhaustion/migration checks and a
real Hub plus PostgreSQL scheduler round trip with a **simulated runtime reply**.
That contract covers busy → persisted backoff → rebuilt dispatcher → capture,
not deployed XiaoBa upload or full publishing acceptance. CI runs the dispatcher
and PostgreSQL cases with the race detector.

Pure store tests cover bounded inputs, policies and immutable identity fields.
HTTP tests use real signed JWTs and adapter doubles to cover active-human-only
access, repeat account checks, privacy/projection, strict routes and transports,
default-off capabilities, unsupported adapters, poll limits, deadlines and
concurrent-read bounds. The CI race-sensitive group includes these HTTP tests.
PostgreSQL integration tests cover persistence across Adapter instances, schema
reinitialization, private reads, current authority on replay, outbox rollback,
concurrent duplicate requests, quota races, owner-transfer races, rate limits
and cancelled request contexts. CI runs the new integration group against its
isolated PostgreSQL service with the Go race detector:

```sh
go test ./server/store -run SkillHubPublication -count=1
go test -race ./server -run '^TestSkillHubPublication(Read|Peer)' -count=1
# CATS_PG_TEST_DSN must point to a disposable test database.
go test -race ./server/db/postgres -run '^TestPostgresSkillHubPublication' -count=1 -v
```

Peer tests cover credential separation, strict parsing, proxy spoofing, redaction,
rate/concurrency guards, actual TLS and an actual slow-body read deadline. New
PostgreSQL tests verify canonical identity, read-only state, reopened adapters,
authority invalidation, rotation and concurrent cancellation. The two existing
PostgreSQL delivery-recovery tests also call the real peer handler before/after
cancellation, then send a late receipt through actual HTTPS into PostgreSQL and
replay it after reopening the adapter. Receipt tests cover strict scope, renewal,
expiry, lost authority, concurrent replay/cancel, cross-entry task/event collisions,
failed inserts and immutable evidence. Migration tests start from the frozen
relevant-table baseline, the frozen pre-receipt connection-generation ledger
from `9b06eecf`, and unchanged migrations through 000036, including a
stored grant predating the new table, in both startup-first and SQL-first order.
These use synthetic Cloud receipt requests and a simulated runtime, not a live
Cloud outbox worker or XiaoBa deployment.

Without `CATS_PG_TEST_DSN`, database tests skip; this is not evidence of database
correctness. User manual testing waits for the XiaoBa source-probe adapter and
workspace button to be connected in an isolated environment. Do not enable Cloud
publication switches based on this PR alone.

Migration verification uses a frozen **relevant-table** baseline from pre-PR620
main `bf2e938d`: `users` and `bot_config`, plus synthetic owner/Bot rows. This is
not a production database dump or a claim to test every historic production table.
The fixture contains no publication tables. Tests cover empty SQL creation,
historical startup-first and SQL-first upgrades, repeated startup/up execution,
preservation of the legacy rows, and working admission after either order.

The `down` migration atomically removes only empty publication tables, in reverse
dependency order, without CASCADE. Retained entries/outbox rows make it fail
closed, preserving replay evidence. Tests verify empty down/up and rejection of
populated rollback. Stop writers, back up and deploy a compatible older binary
before an actual rollback; otherwise current `CreateSchema()` recreates the
tables. No migration is executed on production by this PR.

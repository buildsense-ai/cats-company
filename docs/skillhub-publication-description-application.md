# Confirmed description application

This step retains an application identity and the metadata of one independent
candidate derived from an exactly confirmed description. It does not publish a
Skill, overwrite its source, resume a model or complete the automatic repair loop.

## Default-off internal boundary

`applyPublicationDescription` is an internal coordinator. No HTTP route, RPC,
model tool, worker or production executor is registered for it. This migration
enables no production flag. Existing publication/admission switches remain
unchanged; enabling those switches does not register this coordinator.

The three store operations are prepare, current-authority get and complete.
They require the original active human publisher, current enabled Bot with the
same owner/body, an unsettled workspace entry, and the private same-Bot repair
task's current owner/type/topic/exact two-member membership. Cancel or commit
intents block all three, including exact result replay. Retained rows remain for
audit after settlement, but these methods do not expose a post-settlement history
API. Transfer does not grant a later owner access to the original application.

## Identity and durable state

Migration `000053_skillhub_publication_description_application` and the mirrored
startup DDL retain one row per entry/round, linked to migration 000052's immutable
confirmation. Preparation requires an exactly confirmed draft for the current
round and the original retained Cloud receipt. The application input binds that
receipt, the full draft (task/round/evidence/selector/exact text) and confirmation
key. `identity_hash` is the domain-separated Go-JSON input digest, and the
immutable idempotency key is `pubdesc_` followed by that digest.

Go validates the computed identity on reads and writes. SQL guards additionally
enforce exact stored draft/receipt/confirmation/round binding, ID/hash shape and
correlation, current authority, and a single completion. Neither a digest nor
an application ID or browser assertion is authority. JSON receipt fields retain
their Go names, including `receipt.Query.RequestHash` (capitalized).

Prepare commits before external work. Duplicate prepares serialize on the
entry lock and return the same row. Completion accepts one result and timestamp;
an exact adapter replay recovers it, while different metadata conflicts. Direct
SQL UPDATE/DELETE cannot replace a completed row. Nonempty down migration
refuses to erase application evidence; empty down/up is idempotent and must be
ordered before rollback of 000052.

`Get` deliberately uses the same entry/task authority locks as prepare/complete:
it is a short current-authority checkpoint between external operations, not a
read-only historical status query. These locks serialize with cancellation and
membership changes; no database lock spans Runtime/Cloud network calls. A future
history API must be separate and must not serve as an execution checkpoint.

## Required adapter behavior

The CatsCo trusted Cloud client is implemented in
`server/skillhub_publication_description_derivation_client.go`. It is a
contract-only adapter and is not registered as a production executor. The
environment switch is `CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED`;
when it is false or absent, calls fail closed without network I/O. Enabling the
client requires a configured HTTPS endpoint whose host matches the configured
audience, a dedicated
`pubderive_` bearer secret and a bounded timeout. The client sends only the
application/task/hash/proposal bindings, never cookies, Origin or source bytes,
copies any injected HTTP client rather than mutating it, and forcibly refuses
redirects (including when an injected client has a permissive redirect callback).
It rejects duplicate or unknown response JSON fields. It binds both configured
issuer and audience to the retained upload receipt and accepts a response only
when the application identity, task, original hashes, proposal digest, top-level
and receipt-derived hashes, derivation algorithm/schema, replay marker and
derived byte bounds all match the retained application. Proposal digest JSON
quoting matches Cloud's `JSON.stringify` behavior, including `<`, `>`, `&`, and
Unicode line separators.

Cloud's current derivation implementation supports a description selector only
in `SKILL.md` with an exact zero-based entry index. The broader draft record also
allows `skill.json`, but this derivation adapter returns a distinct unsupported
input error for that selector before sending a request. It does not imply that
`skill.json` can currently be transformed.

`DeriveAndRetain` returns Cloud's durable derivation receipt metadata to the
application coordinator. The separate read/recovery lane below can retrieve
that same copy; neither operation wires a publication continuation.

The coordinator executes prepare, `VerifySource`, current-authority get,
`DeriveAndRetain`, `VerifySource` again, and complete. Context cancellation is
checked before dispatch and before completion. A previously accepted result is
returned without repeating derivation; any subsequent continuation needs its
own fresh source/authority check.

- `VerifySource` must read the live source using the original trusted capture
  under its Runtime workspace fence. Replaying frozen upload bytes alone does
  not prove that the current source is still the same.
- `DeriveAndRetain` must consume the exact retained confirmation, rederive that
  text deterministically, prove every other byte unchanged, and durably retain
  one independent private copy under `app.ID`, with quota/retention accounting.
  It must recover that same copy after a lost reply; it must not overwrite the
  source or substitute another candidate under the same identity.
- The result includes byte count and Cloud derivation receipt. Supported
  missing-description transformations must change request and candidate hashes;
  a no-op result is rejected. The result binds the original request/candidate,
  derived request/candidate, algorithm/schema, validation/proposal digests and
  limits fingerprint. Cloud canonical-JSON digests are distinct from CatsCo's
  Go-JSON confirmation/evidence digests.

Stored result metadata checks correlation, not the existence or authenticity of
retained candidate bytes. An HTTP client/model hash or validation-only response
cannot implement `DeriveAndRetain`. If retention succeeds but the reply or database
completion is lost, retry only the same deterministic application identity;
recheck current source/authority and recover the same copy. Cancellation in this
gap blocks completion; private-copy lifecycle accounting remains the retention
adapter's responsibility. Never bypass cancellation to recover an execution.

## Remaining scope and verification

This step never clears `waiting_confirmation`, changes an immutable repair-round
result, resets/spends the three-round budget, or retries an uncertain paid model
call. Cloud now implements durable private-copy retention and private recovery;
the CatsCo peer client implements both contracts. Production registration,
controlled use of the default-off Runtime source-verification adapter, model-call
idempotency/reconciliation, controlled continuation and the two publication
entry points still need separate implementation. Chinese presentation text and
author-managed images remain later work.

Real PostgreSQL tests exercise prepare/get/complete, restart/lost-reply and
concurrent replay, exact-result conflicts, SQL forged inputs/results and immutable
rows, authority changes, cancellation races and down/up lifecycle. Derivation
metadata is synthetic: these are database contracts, not real Cloud retention or
model/file repair E2E. Tests without `CATS_PG_TEST_DSN` skip locally; CI's required
publication job runs all `TestPostgresSkillHubPublication*` tests against real
PostgreSQL, including the concurrency/recovery subset under the race detector.

## Private byte recovery

`ReadRetainedCopy` uses the same constructor-validated HTTPS peer, dedicated
`pubderive_` credential, forced redirect rejection and isolated cookie policy.
It has an additional default-off switch:
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_READ_ENABLED`. Both this switch and the
derivation client switch must be enabled. The read URL is derived from the
validated peer origin; no caller can provide a recovery URL.

The request contains exactly the original application/task/request/candidate
bindings, confirmed proposal, complete immutable derivation receipt and exact
stored byte count. It never uploads source bytes. The client requires a completed
application with a valid identity, proposal digest and receipt; it does not call
derive as a fallback. It requests identity encoding and accepts only HTTP 200,
one exact `application/octet-stream` content type, no content/transfer encoding,
and the exact Content-Length. A bounded read must match both byte count and
the retained derived request SHA-256. Truncated, corrupted, oversized, redirected,
cancelled or timed-out responses return no private bytes. Peer response bodies
are never included in errors. Cloud must independently validate retained source
state, deterministic rederivation and integrity; a hash is not authority.

`readConfirmedPublicationDescription` is an internal read-only coordinator,
not a production HTTP/RPC route, worker, executor or model tool. Its store port
exposes only the existing current-authority `Get` checkpoint. In order it does:

1. Current-authority get of a completed immutable application.
2. Live source verification under the original Runtime capture fence.
3. Current-authority get, requiring the same full application/result.
4. Private peer copy read with the exact retained application.
5. Independent byte count/SHA-256 check and ownership of the returned buffer.
6. Live source verification again, then a final identical current-authority get.

The store rechecks the original active human, current Bot/Owner/body and private
same-Bot task, current round/confirmation, and cancel/commit intents on every get.
The coordinator passes isolated snapshots to its ports; a mutable port/store
object cannot silently replace the baseline application across awaits. Every
failure returns no copy. Reads do not prepare/complete an application, mutate
source or task state, change the three-round budget, publish, or call a model.

These checks prove only a point-in-time copy. No database lock spans a network
operation, and the returned internal object is not authority to execute, edit,
publish or spend quota. A future controlled continuation must recheck authority
and source at its own state transition. Missing adapters fail closed.

The added TLS wire tests use synthetic retained peer bytes and validated
application fixtures; coordinator tests use a synthetic authority store/source
port. They cover exact bindings, client recreation, default-off configuration,
cookie/redirect isolation, partial/changed/encoded responses, timeout/cancel,
authority/source changes at each checkpoint and snapshot isolation. These are
not a live Cloud/CatsCo/Runtime or paid-model repair E2E.

## Dedicated live Runtime source adapter

The internal `Hub.publicationDescriptionSourceVerifier(actor)` now implements
the existing application and copy `VerifySource` ports. Bind `actor` from the
trusted authenticated service invocation; never infer it from an application
hash, browser/model input or upload query. This adapter has no public HTTP route,
browser/model tool, worker or continuation executor. Its existence does not
enable repair, editing, publication or paid model calls.

All four CatsCo switches must be explicitly enabled:

- `CATSCO_SKILLHUB_PUBLICATION_WORKSPACE_ENTRIES_ENABLED`
- `CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED`
- `CATSCO_SKILLHUB_PUBLICATION_CAPTURE_ENABLED`
- `CATSCO_SKILLHUB_PUBLICATION_REPAIR_SOURCE_ENABLED` (new, default off)

The full Runtime must advertise `skillhub.publication.verifyRepairSource.v1`
and enable its probe, capture and repair-source switches. Connector/WikiAgent
clients cannot supply proof. No production switch is enabled by this change.

The service checks the complete current application/confirmation/result and
immutable original entry, then the current human/Bot/Owner, enabled body,
installation, device, local route, connection generation and capability. It
reads the existing durable capture and requires the exact original receipt in
the confirmed application. A newer connection generation on
the same body/installation is allowed before dispatch; changing the route during
the operation fails closed. The original entry and capture are never rewritten.

This lane is not round-1-only. It proves that the **original live source** still
matches that original receipt in every round. Repair produces independent
private candidates; it does not overwrite the live source. The current draft's
`Evidence.CandidateHash` may therefore differ from the original receipt's
`Query.SourceSnapshotHash` in rounds 2 and 3. Requiring equality would wrongly
reject a valid later-round application. Current-authority application Get binds
the exact confirmed draft/round/evidence; it does not replace the original
capture or turn candidate hashes into authority.

Success here is only original-source freshness, never proof of the current
repair candidate's bytes or its ancestry. That separate private-candidate chain,
per-round report/retention verification and controlled continuation remain
follow-up work before enabling the full repair loop. The current deterministic
Cloud description-derivation endpoint is still bound to its retained original
candidate, so this change does not claim round-2/3 Cloud derivation works. No
new per-round capture, source overwrite, automatic continuation or budget reset
is introduced. Changing the original source still fails in every round.

The reserved request uses `pubrepairsource_` and sends exactly `entry_id`,
`bot_uid`, `local_skill_id`, `workspace_generation` and `expected_capture`.
Both device envelope fields equal the original Runtime device/installation ID.
The nested expected receipt has exactly six scalars: `schemaVersion`, `entryId`,
`candidateRevision`, `requestHash`, `sourceSnapshotHash`, `requestBytes`.
Only those same six values plus boolean `sourceVerified: true` are accepted.
No source bytes, paths, credentials, grants or repair text cross this channel.

XiaoBa calls `readRepairCandidate`, checking the retained capture and live source
under its workspace fence on every call. Ordinary capture replay is insufficient:
it may return frozen evidence after source changes. This lane never recaptures a
missing record. CatsCo then rechecks the current application/entry, full retained
capture, socket, route and capability before returning success. Proof remains
point-in-time; later state transitions must check again.

There are eight bounded service slots, one in-flight check per application and
a 25-second outer deadline. Sending is nonblocking; queue pressure returns busy
without disconnecting the Bot. The WebSocket result callback only compares
registered-client facts and bounded scalar evidence, then queues one result.
Database reads run in the waiting service goroutine. Wrong sockets cannot consume
the result; duplicates and late results cannot replace it. Only correctly bound
`publication.runtime_unavailable` / `publication.runtime_timeout` error envelopes
are retryable busy responses; source/security/malformed failures remain closed.
Upstream error messages are never forwarded.

Verification includes coordinator-port composition, changed authority/capture,
wrong socket, duplicate/late result, cancellation including the last fence,
backpressure, database blocking, caller snapshot isolation and legal reconnect.
Store-valid round-2/3 applications cover prepared/completed checks, forged capture
replies and caller evidence that is not retained by the authoritative application.
`node scripts/test-publication-repair-source-paired.mjs` additionally requires
XiaoBa merge `062c7cf1b11fa49fdc44466c1cbfe80b041d11c4` and runs real Runtime
handler/storage checks for normal/malformed metadata, restart, changed/removed
source, disabled capability, owner switch, transient Bot switch, missing capture
and mismatched receipt, plus rounds 2/3 with unchanged or changed original source.
All three suites and thirteen subcases must pass. The runner fails on any skip
or missing case and is a required CI dependency. These are real cross-repository handler contracts with
synthetic CatsCo authority/store and socket; not deployed or paid-model E2E.

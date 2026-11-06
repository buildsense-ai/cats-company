# Continuing an exactly confirmed description

The internal `continueConfirmedPublicationDescription` operation connects the
existing exact human confirmation, deterministic private application, durable
passing-report acceptance, and retained-copy recovery. It returns one private
handoff containing an authoritative checkpoint and verified bytes. It is not
registered as a production route, RPC, worker, model tool or publishing action.

## Recovery and history

The operation reuses the original application identity and first accepted
report. A lost application-completion or report-acceptance reply is recovered
from PostgreSQL on the next invocation. A completed application skips derivation;
an accepted report skips a fresh report request. Reading private bytes remains
necessary on every handoff, so an expired or missing Cloud copy fails closed.
Deterministic retention replay is not permission to retry a paid model call.

No new table or migration is needed. The read-only
`GetSkillHubPublicationDescriptionContinuation` transaction assembles the repair
task, exact confirmed draft, completed application and immutable acceptance
under the existing current entry/human/Bot/body/private-task locks. It also
rechecks the original retained receipt, cancellation and commit state. Missing,
incomplete or inconsistent evidence fails without constructing a handoff.

The complete round chain must be consistent, ordered and within the three-round
budget. Earlier rounds must have made progress and permitted another round.
The last round remains `waiting_confirmation`; the separate accepted report says
that the derived candidate passed validation. Do not replace the historical round
with `validated`, create another round for this continuation, or reset the budget.
No public task/UI state changes in this implementation.

## Current checks and private bytes

After application and acceptance recovery, the coordinator reads the complete
transactional checkpoint. It brackets a retained-copy read with original Runtime
source verification and identical current checkpoint reads. Copy length and
SHA-256 must match the retained derivation result, whose candidate/limits are
bound to the accepted report. Original source and derived private copy are
different identities and must never be compared as if they were the same bytes.

Nested application, draft, report, round-result pointers and private byte buffers
are copied. Changes by a subsequent port invocation cannot silently change an
earlier baseline or a returned handoff.

Runtime/Cloud observations and PostgreSQL transactions are separate operations;
this is not a distributed atomic authority check. The returned object is a
point-in-time handoff, not a long-lived execution or commit grant. The release
consumer still needs current source/authority checks and a commit protocol bound
to the derived candidate. The existing original rejected-candidate commit path
must not be reused by substituting hashes or replacing its receipt.

## Planning the independent repaired release

`planConfirmedPublicationDescription` consumes this private handoff, validates
its bytes and full checkpoint, and brackets `PlanRetainedCopy` with current
transactional checkpoint reads and original Runtime source checks. The complete
task/draft/application/acceptance must remain identical. Port-owned pointers
cannot change the baseline or returned plan. This coordinator has only a
read-only store interface and is not registered as a production action.

The peer client requires the derivation configuration plus the separate
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_PLAN_ENABLED` switch (default false).
It derives `/internal/plan` exclusively from the configured HTTPS origin, clears
the cookie jar, refuses redirects, bounds request/response sizes and concurrent
calls, and accepts only exact noncacheable JSON. Original upload receipt,
application, complete derivation receipt, derived byte length, derived candidate
hash, target structure and requested version-policy boolean must all match.
Nested duplicates, null/missing fields, old-candidate targets and receipt
substitution fail closed. Errors never include upstream private bodies/URLs.

Cloud #55 retains one immutable independently reserved target, excluding the old
original-candidate plan for the same task. A lost reply is recovered by asking
for the same plan; neither side automatically retries a paid model call. The
description's human confirmation does not implicitly authorize a new version:
`confirmVersionPublish` is a distinct policy input, not human authorization.

The returned private checkpoint/plan is still a point-in-time observation, not
a durable CatsCo release intent, execution permit or publication success. No
CatsCo row, round, original Skill, task state or budget is changed. The old
commit path remains original-only. A subsequent step must durably bind the
accepted application/report/target and obtain a distinct candidate-bound permit
under current authority/source checks before publishing.

## Authorizing the retained derived release

After a separate durable release intent has been confirmed by the actor, the
internal authorize client sends only the original upload receipt and application
identifier to Cloud. Cloud recovers or issues a permit for the exact retained
derived target; it never creates a commit or publication job. CatsCo brackets the
call with current continuation, intent, identity, target, source and cancellation
checks. A permit is therefore an internal handoff, not permission to bypass the
future derived commit worker. The response is strict wrapper-plus-proof JSON:
the nested proof must match the application, intent digest, target and UTC
authorization time. Replays are immutable and pointer-isolated. Unknown,
cacheable, compressed, redirected, oversized, duplicated, null or mismatched
responses fail closed; upstream unavailable errors do not become a new intent.

The authorize client uses a separate default-off
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_AUTHORIZE_ENABLED` gate and
shares a bounded service concurrency budget with planning. No production route,
RPC, worker, model call or actual publication is wired by this slice.

The internal release-flow coordinator only consumes a release intent that was
already durably recorded by a separately authenticated human release action.
It never creates that intent from description confirmation or from the
`confirmVersionPublish` policy flag. It then replays continuation and planning
under the current checkpoint before asking Cloud for the permit. This keeps
description approval, version-policy selection and release authorization as
three distinct facts; the coordinator remains an internal default-off adapter.

## Isolated Cloud commit admission

The internal commit client is a distinct irreversible step after the durable
human release intent and exact Cloud permit exist. It posts only
`schemaVersion`, `applicationId` and `permitId` to Cloud's isolated
`/api/skills/publication-descriptions/internal/commit` endpoint. The response
must bind the same application, original task and permit, and return one of the
known isolated queue states. A retry after an uncertain response uses the same
permit; Cloud returns the same durable admission instead of creating duplicate
publication work.

The separate
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_COMMIT_ENABLED` gate defaults
off and also requires the derivation, planning and release-authorization
configuration. This slice adds no RPC, HTTP route, model tool, worker or
automatic invocation. Cloud's own commit-admission switch remains independently
default-off. When later wired and both sides are enabled, admission can start
the asynchronous isolated publication worker; it is not merely a status read
or a reversible preview. It never enters the legacy original-candidate commit
path. An idempotent replay can also return a terminal `published` or
`publish_failed` state; callers must inspect the returned state, and a nil
error alone does not mean publication is still in progress. Production
activation and user-facing wiring are later steps.

The mandatory paired contract is run with
`node scripts/test-publication-description-commit-paired.mjs`. It pins the
reviewed Cloud commit, requires a real PostgreSQL DSN, and exercises the
actual Cloud authorize/admission endpoints with this CatsCo client/coordinator;
the release-permit issuer is stubbed inside the disposable fixture. The test
does not publish a user Skill or enable any production gate.

The strict `scripts/test-publication-description-plan-paired.mjs` runner pins
Cloud's reviewed planner revision and requires a real PostgreSQL DSN; missing
dependencies or any skipped case fail. It pairs actual Cloud intake, retention,
bounded workers, PostgreSQL planner/reservation and verified HTTPS with this
CatsCo client/coordinator. It covers reconstruction after a committed lost reply,
one exact target/version, unchanged original bytes, no commits/old plans, source
cancellation, publisher suspension and default-off rejection. CatsCo authority,
accepted report and Runtime source remain synthetic, not dual-database or
production/paid-model E2E evidence. The private Cloud repository hosts the pinned
CI job so public CatsCo PRs receive no private-repository credential.

## Read-only derived release outcome

`ReadRetainedRelease` reads Cloud's exact retained application/permit outcome
under the independent default-off
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_STATUS_ENABLED` gate plus the
base derivation gate. Planning, authorization, commit admission and copy-read
gates need not be enabled to recover historical evidence. The request carries
the complete original upload receipt; its source hash is never replaced by the
derived candidate hash. The response must match that receipt, application,
task, permit and the exact confirmed derived target. Published results require
the exact target skill/version/time, a valid package ID and SHA-256 checksum.
Only terminal failures accept a static allowlist of error codes. Unknown/null/
duplicate/extra fields and unsafe HTTPS responses fail closed.

Reads never automatically retry, re-admit, authorize, publish, call a model,
refresh current source authority or clear cancellation fences. Receipt expiry
does not erase a completed result. This private client is historical evidence,
not current execution authorization; a future human-facing caller must check
its current actor before exposing it. No browser route or production wiring is
added. The mandatory commit paired fixture also calls the real Cloud status
route, loses a reply, recovers the same evidence and checks unchanged row counts.
Cloud's real PostgreSQL tests separately cover terminal publication and expiry.

## Internal outcome consumption

`readConfirmedPublicationDescriptionReleaseOutcome` consumes that private
status without replaying the derivation/plan/authorization/admission flow.
Its dedicated PostgreSQL reader uses a read-only repeatable-read transaction
to recover the retained task/draft/application/acceptance, separate human
release intent and already-issued local permit. Missing permits are conflicts;
reads cannot issue or recover them through an authorizing operation.

Historical viewing follows the existing publication-history rule: the original
publisher must still be an active human account. A new Bot owner cannot inherit
that publisher's history. Bot transfer, disabling, body changes and offline
Runtime do not invalidate historical evidence. This is deliberately separate
from current continuation/execution authority and cannot authorize another run.
The coordinator checks this same immutable observation before and after peer
I/O, validates even alternate adapters, freezes pointer-bearing records, and
discards the response if the publisher is disabled or the observation changes.

It distinguishes in-progress states from `published`/`publish_failed` terminal
evidence. The reader alone does not persist observations or rewrite repair
state/budget, clear cancellation fences, poll automatically, project data into a
browser, or activate a publication entry point. Durable receipt storage is
described below; browser projection remains a subsequent step.

## Durable terminal receipts

The internal `recordConfirmedPublicationDescriptionReleaseTerminal` first uses
the guarded outcome reader. Queued/publishing/committing observations produce no
write; network failures remain unknown, never a synthetic publication failure.
Only exact published/publish_failed evidence can enter the independent
`skillhub_publication_description_release_terminals` table (migration 000064).
The transaction checks the active original publisher, the same retained
observation and existing permit. A unique application row recovers identical
concurrent/lost-response retries; a changed result conflicts. The stored digest
covers the full terminal payload and immutable derived target.

Published results whitelist skill/version/package/checksum/time; failures use
the same static code allowlist as Cloud. Database guards bind entry, intent,
permit and exact target, prohibit updates/deletes, and refuse nonempty rollback.
The original repair task, round budget, legacy commit path and cancellation
fences remain unchanged. Historical receipt reads still require the original
active human publisher but not a live Bot or source. No browser route, automatic
polling, worker registration or production gate activation is included.

## Background terminal reconciliation

`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RECONCILE_ENABLED` is an independent
default-off server worker gate. It also requires a valid enabled derivation
client and its release-status read gate. No publishing/plan/authorize/admit
method is exposed to this worker. Production flags remain unchanged.

Existing derived permits form the queue, scoped by the original receipt's
issuer/audience and active original human publisher; live Bot/source authority
is not substituted for history. Migration 000065 retains scheduling leases,
generations and due times separately from immutable terminal receipts. Claims
use permit-row SKIP LOCKED and an additional due-state upsert CAS; settlement
matches application/entry/actor/token/generation/exact unexpired lease. A stale
process cannot change a successor's schedule. A terminal write remains immutable
evidence, not execution or scheduling authority.

The loop checks every five seconds with at most two concurrent observations,
matching the dedicated status client's capacity. A slot is acquired before
claiming a lease; excess local calls cannot consume leases. A local Busy result
after claim is requeued like progress, without increasing the failure counter;
each observation has a 60-second budget within a 90-second persisted lease.
Progress schedules the next read 30 seconds later. Transport/authority/validation
failures remain unknown and retry after 15,30,60,120,240, then at most 300 seconds;
they never become publish_failed or consume repair/execution budgets. There is
no finite status-read exhaustion that could abandon a later successful publish.
Disabled original publishers are not selected; reactivation resumes retained work.

The worker first recovers any valid local terminal receipt before peer I/O, then
uses the guarded Cloud status reader and immutable terminal writer. Terminal
rows are excluded from future claims even if the response or scheduling
settlement was lost. Shutdown cancels and waits for active observations; bounded
settlement can outlive request cancellation. Logs are static and contain no
raw peer errors, credentials or private source/target payloads. GET history
remains purely read-only and independent from the worker.

Tests cover status-only HTTPS, in-progress/terminal outcomes, unknown responses,
authority/scope mismatch, cancelled calls, lost-write recovery without Cloud,
bounded concurrency and shutdown; real PostgreSQL CI covers concurrent claims,
restart/expiry takeover, generation fencing, persistent backoff, migration
lifecycle, disabled actor filtering, offline/transferred Bot recovery and no
legacy publication writes. The JWT/HTTPS/PG history contract now runs the real
reconciler before reading the result; its Cloud/Runtime ports remain synthetic.

## Verification and remaining work

### Private local terminal history

`GET /api/skillhub/publications/entries/{id}/description-release-history`
requires both `CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED` and the new independent
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_HISTORY_ENABLED` (default off).
Capabilities advertise `descriptionReleaseHistoryReadsEnabled` only when both
gates and the historical reader adapter are available. This does not enable
publishing, background reconciliation or paid calls.

The route shares active-human Bearer authentication, original-publisher entry
checks, private/no-store headers, a five-second deadline and the existing actor,
entry and concurrency limits. Observation/receipt/observation checks validate
adapter output and reject changed history or disabled actors. It never calls
Cloud, creates a receipt, issues a permit, resets repair rounds, clears a fence
or falls back to the legacy commit path. Bot offline/transfer does not erase
history, and the new Owner does not inherit it.

The response has `schemaVersion`, `entryRequestId` and
`descriptionReleaseHistory`. Its stage is `not_recorded`, `published` or
`publish_failed`. `not_recorded` means a valid retained permit exists but no
local terminal receipt has been recorded; it does NOT claim that Cloud is
queued, running, failed or successful. Missing/invalid release-chain evidence
returns an unavailable error, not a fabricated execution state. A terminal view
adds `recordedAt`, plus either result `{skillId,version,publishedAt}` or a static
allowlisted failure code. Package IDs, checksums, application/permit IDs, target,
hashes, source selectors and private bytes never reach the response.

The real JWT/HTTPS/PostgreSQL history contract reconstructs the adapter and
handler after persisting a terminal, verifies transfer/offline access isolation,
disabled original-publisher rejection and unchanged publication row counts.
Cloud result/derivation/report and Runtime source are synthetic test ports; it
is not real Cloud completion or paid-model/user publishing acceptance.

### Browser terminal-history consumer

The existing publication report dialog now hosts a separate display-only
repaired-copy result section, shared by workspace reports and linked repair
conversations. Opening the dialog first checks capabilities; only explicit
`descriptionReleaseHistoryReadsEnabled: true` with schemaVersion 1 allows the
local history GET. Explicit older/disabled capability responses keep the section
absent. A failed or timed-out capability probe instead shows static uncertainty
and an explicit retry button; retry probes capability again before any history
read. It never treats unknown capability as enabled. Browser
entry references remain disposable hints, not publication or file authority.

Only validated `published` / `publish_failed` local history is terminal.
`not_recorded` is shown as no final result yet, never as queued/running/success.
Unknown history is reread 30 seconds after each response while the dialog is
open and the document visible. Terminal results stop polling; errors drop stale
evidence, show static uncertainty and require explicit retry, with no alternate
endpoint, mutation, model call, legacy commit or publish replay. A complete read
(capability plus history body) has a 15-second browser deadline; header requests
also have bounded timeouts. Calls do not overlap. Hiding pauses future reads;
returning visible resumes unknown reads. Closing/unmounting, changing entry or
auth invalidation cancels requests and suppresses late responses; terminal
history is not persisted in browser storage.

The section does not replace the original validation report or imply automatic
repair has been enabled. It does not initiate description confirmation, release
intent, admission or execution. Existing production gates are unchanged/off.
Tests cover strict response shape/entry binding, private-field rejection, real
API GET/auth/cancellation, unknown/terminal/error transitions, stale responses,
auth changes, response-body deadline, hidden-tab behavior and shared dialog use.

Domain tests cover exact task/draft/application/report binding and earlier-round
progress. Coordinator tests cover recovery at committed checkpoints, stable
replay, source/authority changes, invalid bytes, context cancellation and pointer
isolation. PostgreSQL tests cover missing acceptance, restart recovery, current
authority, unchanged history/budget and cancellation.

The existing mandatory JWT/HTTPS/PostgreSQL publication contract now continues
after real exact human confirmation. It loses replies after real application and
acceptance commits, reconstructs the coordinator wrapper, verifies recovery and
checks task-authority changes during copy recovery. Cloud derivation/report/copy
and Runtime source are synthetic ports. These are not real Cloud/Runtime/PG or
paid-model end-to-end tests. Local database skips are not PostgreSQL passes.

The derived-candidate release/commit protocol, terminal reconciliation and local
browser history are now internal/default-off building blocks, not a complete
user publishing loop. The shared report dialog also consumes the separate exact
description confirmation contract (see skillhub-publication-description-confirmation.md),
without initiating repair, applying bytes or authorizing release. The report
also has a linked-conversation task-registration consumer (see
skillhub-publication-repair-admission.md): it recovers or explicitly admits the
same durable task, without starting repair or resetting its budget. M3 still
requires later-round private candidate ancestry/derivation, uncertain paid-call
reconciliation and both publication entry points' full trusted continuation.
Production wiring/enabling and user acceptance
remain later steps; Chinese presentation content and author images remain after
the publishing-repair loop.

# Restricted repair model coordinator

`runPublicationRepairModel` consumes an already-claimed repair round and the
private model-call journal. It is internal and has **no production call site,
worker, HTTP/RPC route or browser capability**. A separate real HTTP adapter
exists but is not registered. Production
configuration is unchanged. An admitted task still does not automatically run.

## Bounded lane and current authority

The first implementation accepts only round one for the original retained
candidate and complete rejection report. Later rounds fail closed until private
candidate ancestry is implemented; they must not reuse the original source as
if it were the previous round's derived copy. No round is claimed/refunded or
finished by this operation.

The new PostgreSQL execution reader transactionally checks original active
human, current Bot owner/body/enabled state, private task/topic membership and
the exact current live unfinished lease. This differs from history readers,
which can legitimately return evidence after transfer or cancellation.
Execution reads allocate no dispatch fence, model request or repair round.

The coordinator freezes task/lease identity and brackets material preparation,
dispatch and result handling with this current-authority reader plus a trusted
Runtime source verifier. Nested report/index/output pointers passed to ports
cannot change its baseline or returned private result. Locks are released
before network/model operations: these are point-in-time checks, not distributed
atomic authorization or protection against an ownership change during a call.

The complete attempt is bounded to 60 seconds or five seconds before lease
expiry, whichever comes first; an earlier parent deadline applies. No local
lease extension, automatic request retry or background work is introduced.

## Material, model request and payer binding

Only trusted service ports prepare material. `PrepareMaterial` must read and
verify the exact retained private candidate bytes/hash from the supplied receipt,
then produce at most 1 MiB of valid UTF-8 model-readable text without NULs. It
must not truncate silently, read a different version or mutate the live Skill.
The separate [private material client](skillhub-publication-repair-material.md)
can now read exact original Cloud wire bytes and strictly project supported
source-only text file candidates with semantic hash verification. The internal
`newPublicationRepairModelPorts` composition connects its `readOriginal` peer
and the validation-report reader to this `PrepareMaterial` port; the composition
does not construct clients or load credentials. It has no production call site
or worker/provider registration, and the live Runtime verifier remains an
explicit injected port. The function signature alone is not proof of a real
retained-source read. The coordinator independently matches the complete
report and normalized issues to the admitted round.

The lane refuses package-safety issues and blocking issues other than missing
runtime descriptions requiring confirmation. Before reserving a dispatch it
also requires one supported description selector (identical duplicates are
allowed; distinct entrypoints/locations fail closed). It drafts only, never treats the
model's text or the report's recommendation as approval. The fixed system
instruction treats source/report text as untrusted data, forbids tools and
functional/identity/permission changes and asks for a plain-text description
draft. Exact human confirmation remains separate; an instruction to the model
is not a security boundary. The restricted consumer below independently binds
the same selector to retained output before proposing anything.

The immutable request digest includes original payer, provider/model/output
token cap, policy version, exact system prompt/source/report, full original
receipt/topic/task identity and round input. Lease worker/token/generation are
excluded so takeover can recover the original result without rebinding a new
request. The output token limit is 1..8192. There are no arbitrary URLs,
provider options, model tools or caller-supplied prompt templates. Normal JSON
serialization omits private source/prompt; a provider adapter must map
the explicit typed fields, not blindly marshal the whole internal request.

`InvokeOnce` is an injected service port, not a public tool. The separate
[internal HTTP adapter](skillhub-publication-repair-model-provider.md) now maps
the request to the original human's existing relay key and one bounded,
nonretrying Anthropic-compatible HTTP attempt. It has no production call site.
Relay-internal paid retries/fallbacks and actual accounting remain independent
rollout prerequisites, not properties proved by the injected port or local
client. These tests perform **no paid call**.

## Durable fence and unknown outcomes

Only a successfully committed journal Start with `dispatch=true` permits one
invocation of `InvokeOnce`. Missing/malformed/lost Start results never invoke
the provider. The coordinator verifies the exact call/request/task/round/lease
binding and current source/authority immediately before calling the port.
Concurrent replay, restart and lease takeover consume a retained output, or
return static `publication.repair_model_result_uncertain`; they never invoke
again with a missing output. Changed payer/model/settings/prompt/material/report
or round identity cannot obtain another dispatch grant.

Provider errors, invalid/oversize results and cancelled/late calls are uncertain,
not new dispatch opportunities. Private provider errors are not returned. After
current source/authority checks, valid text is retained once under the original
dispatch lease. A lost completion reply triggers only a journal **read**, never
a second provider request or completion retry. The result must match original
call/creation/lease/request and the exact local text. Recovery still performs
current execution/source checks before handing any output to its next consumer.

The coordinator returns only a private, non-JSON-serialized result. It does not
propose/confirm a draft, parse or apply a patch, edit files, publish, or change
task state. A future task controller must map uncertainty to the existing
`model_result_uncertain` stop; this coordinator does not forge a stopped ledger
checkpoint after loss of authority. The journal grants at most one dispatch
authorization, not exactly-once provider execution/billing.

## Retained model output to human-confirmable description

`retainPublicationModelDescription` is a separate internal, unwired consumer.
Its narrow store interface contains no model dispatch, round claim, human
confirmation, candidate writer or publisher. It accepts the private retained
result from the coordinator, not browser/model-supplied text or a chosen path.
It rereads the immutable model journal and matches the exact output, call,
request, task/round evidence and dispatch lease (including original dispatch
generation on takeover). A complete verified report supplies the selector;
the model supplies **only text**. Text is preserved byte-for-byte, not trimmed,
extracted from JSON/Markdown, rendered or interpreted as instructions. The
existing proposal validator requires nonblank UTF-8, at most 8192 bytes and
supported controls/selector shape. Invalid or oversized retained output stops;
there is no model retry to make it fit.

The consumer checks current private task authority, exact first-round task and
lease, original candidate/report evidence and a trusted source verifier before
one proposal attempt. Proposal storage transactionally rechecks current
authority and lease and atomically retains the exact draft plus the
`waiting_confirmation` checkpoint. This is **not human confirmation** and writes
no candidate/live file. A successful response is frozen and read back before
return; an errored/unknown proposal response permits only read recovery, never
a second proposal/model invocation in that attempt. Missing/inconsistent
recovery produces static `publication.model_description_uncertain` without
private write errors. A malformed nil success is also uncertain, not approval.

Recovery checks for an existing draft **before** requesting a pending execution
lease: the proposal has already finished that lease's round. The draft GET
independently rechecks current active publisher/Bot/body/private membership;
historical task/model reads alone do not authorize it. It validates exact draft
text/key/evidence/task/round/current lease, verifies source, rereads the model
journal, then rechecks current-authority draft GET. It can retain an already
human-confirmed draft without creating or changing its confirmation. Fresh
proposal requires a live pending lease; existing draft recovery does not
restart a round merely because the lease later expires. Point-in-time checks
are not distributed atomic authorization across Runtime and database.

A trusted controller can reconstruct the private result and original lease
from retained journal/task history after restart; neither is a process-local
grant. The consumer still rereads the journal and requires the independent
current-authority gates and trusted source verification. Browser/model input
must never be promoted into this private type or used as verified report data.

The internal `publicationRepairDescriptionSourceVerifier` now supports both
pending execution and waiting/confirmed draft recovery against the same original
retained source. Pending execution still requires a live unfinished lease.
Read-only recovery instead brackets task history with independent CURRENT draft
authority reads, freezes the exact draft/confirmation state, and uses the
dedicated Runtime source RPC against the original capture. Expired finished
leases never become executable again. Source removal/change, lost authority or
confirmation changing in flight conservatively stops the read. No source/provider
adapter or task controller is registered in production by this slice. An unknown
outcome still needs controller stop/recovery policy; it is not inferred to mean
no charge, no commit or permission to retry.
The exact confirmation UI and subsequent private-copy application remain
separate human/trusted steps.

## First-round description attempt

`runPublicationRepairDescriptionAttempt` internally composes the coordinator and
description consumer. Its total deadline is 90 seconds (or the earlier parent
deadline); the nested model call retains its 60-second/lease-minus-five-seconds
bound. It consumes an already-claimed first round, never claims another round,
and has no worker, route, environment loader or production registration.

It checks CURRENT draft authority first. With no draft, it requests the live
execution lease and uses the journal-fenced coordinator. With a retained draft,
it reconstructs the exact original lease/task/private result from durable
history after CURRENT authority and source checks, without preparing model
material, requesting an execution lease or invoking a provider. It rereads the
trusted report, binds its complete receipt/evidence, rechecks source, calls the
restricted consumer, then independently reads back the exact draft including
confirmation state. A report read failure after a retained model result can be
recovered without another dispatch. Lost completion/proposal replies and
concurrent callers likewise do not authorize another model request.

Returning `waiting_confirmation` is not approval, a file change or publication.
An existing human confirmation is preserved, never created by recovery. This
helper does not finish uncertain/stopped checkpoints or implement the controller
loop, safe automatic formatting, later rounds or publication continuation.

`runPublicationRepairFirstRoundAttempt` wraps this helper with one conservative
terminal transition: only `publication.repair_model_result_uncertain` is written
as the existing `stopped/model_result_uncertain` checkpoint. A successful write
returns a distinct internal error that still matches the uncertain sentinel and
confirms the stop is durable. If the write reply is lost, the helper reads back
the task and returns that confirmed-stop result only when the exact lease,
checkpoint and stopped state match; otherwise it returns the ordinary
`publication.repair_model_result_uncertain` error, and persistence remains
unconfirmed. Before stopping, it also reads the immutable model-call journal:
an absent dispatch fence is a recoverable pre-dispatch failure; a journal read
failure remains nonterminal; and a retained output re-enters read-only draft
recovery without another model dispatch. Only a valid existing fence without a
retained output is terminalized as an unknown model result.
Temporary source/report failures and uncertain draft persistence are deliberately
not terminalized, because the latter may be recoverable through the next
read-only draft attempt. This helper still consumes an already-claimed lease
and is not a worker/controller loop or production call site.

## Verification and remaining work

Local unit tests cover request identity/private bytes, unsafe material and report
policy, all authority checkpoints, pointer isolation, malformed store responses,
provider uncertainty, committed response loss, concurrent dispatch and lease
takeover without paid replay. Required CI includes their race run.

PostgreSQL tests distinguish execution reads from retained history under expiry,
takeover, finish, cancellation, other actors and membership loss. The required
JWT/HTTPS/PostgreSQL publication contract now runs this coordinator with real
current-task/journal writes, loses a committed output reply, reconstructs and
recovers exactly one synthetic invocation, rejects model rebinding, then feeds
that untrusted draft through the restricted consumer into the existing
separately confirmed-description path. The consumer contract loses a real
committed proposal reply through the description attempt and reconstructs that
attempt after the pending round has finished;
only current-authority reads recover the same unconfirmed text/key and one
waiting round. Its unit tests cover selector/text bounds, authority loss,
changed journal/report/task evidence, cancellation, uncertain write recovery,
pointer isolation, replay and concurrent consumers. The existing required
`TestPublicationRepairModel` race selection includes the consumer tests. A
separate required source-paired contract also runs the live task/lease verifier
inside the coordinator: it checks the pinned XiaoBa Runtime before and after
dispatch grant and immediately before a single synthetic provider invocation;
removing the source after grant is denied without invocation. The required
Runtime+Cloud/PostgreSQL contract now also uses the same exact Runtime-captured
candidate for real Cloud rejection/report/material reads and invokes the live
Runtime verifier at coordinator fences. Its provider and dispatch journal remain
synthetic. The same mandatory runner separately preserves the original exotic
Unicode/JSON-escaping Cloud vectors with a synthetic Runtime verifier. It sets
`CATSCO_REQUIRE_REPAIR_MATERIAL_CONTRACT=1` for both modes and additionally
`CATSCO_REQUIRE_REPAIR_RUNTIME_CLOUD_CONTRACT=1` for the real Runtime mode,
rejecting missing Runtime root/Node settings and every skip. Neither paired
contract is a paid-model test or real-user end-to-end
acceptance. Missing local PG DSN skips are not database passes.

The mandatory pinned Runtime source runner also requires waiting/confirmed
expired-draft recovery, removed-source denial and revoked-authority denial with
synthetic draft/task authority (six suites, nineteen subcases overall). Separate
local TLS adapter tests compose a synthetic key/model HTTP server with the
attempt and synthetic persistence/source/report ports; one invocation survives
a lost proposal reply and reconstructed recovery. These are distinct contracts:
they do not prove a single all-real Runtime/Cloud/PG/paid-provider composition.

The internal material/source adapters and model HTTP adapter are not yet
registered together in a production executor. Deployment-level relay no-retry
and payer accounting validation, automatic safe format executor and task
controller, multiround retained ancestry, both publication entry points'
complete trusted continuation and controlled rollout remain. Chinese marketplace
manuals and author-managed images are still after the repair/publish loop.

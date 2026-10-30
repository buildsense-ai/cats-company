# Restricted repair model coordinator

`runPublicationRepairModel` consumes an already-claimed repair round and the
private model-call journal. It is internal and has **no registered call site,
worker, HTTP/RPC route, browser capability or real provider adapter**. Production
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
This slice does not implement that Runtime/Cloud reader; the function signature
is not proof of a real retained-source read. The coordinator independently
matches the complete report and normalized issues to the admitted round.

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
serialization omits private source/prompt; a future provider adapter must map
the explicit typed fields, not blindly marshal the whole internal request.

`InvokeOnce` is an injected service port, not a public tool. A future adapter
must independently resolve the server-owned provider configuration, bill only
the original human payer, enforce deadline/output bounds and disable automatic
SDK/transport retries and tool execution. This slice performs **no paid call**
and does not claim that an injected port enforces provider billing semantics.

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

A future real `VerifySource` adapter must support both pending execution and
waiting/confirmed draft recovery against the same original retained source;
it must not require an unfinished round for read-only recovery. No such real
source/provider adapter or task controller is registered by this slice. An
unknown outcome still needs controller stop/recovery policy; it is not inferred
to mean no charge, no commit or permission to retry. The exact confirmation UI
and subsequent private-copy application remain separate human/trusted steps.

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
committed proposal reply and reconstructs after the pending round has finished;
only current-authority reads recover the same unconfirmed text/key and one
waiting round. Its unit tests cover selector/text bounds, authority loss,
changed journal/report/task evidence, cancellation, uncertain write recovery,
pointer isolation, replay and concurrent consumers. The existing required
`TestPublicationRepairModel` race selection includes the consumer tests.
Runtime source/material/provider remain synthetic ports; this is not paid-model
or real user/Cloud/Runtime end-to-end acceptance. Missing local PG DSN skips are
not database passes.

Real material/source/provider adapters, automatic safe format executor and task
controller, multiround retained ancestry, both publication entry points'
complete trusted continuation and controlled rollout remain. Chinese marketplace
manuals and author-managed images are still after the repair/publish loop.

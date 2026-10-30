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
runtime descriptions requiring confirmation. It drafts only, never treats the
model's text or the report's recommendation as approval. The fixed system
instruction treats source/report text as untrusted data, forbids tools and
functional/identity/permission changes and asks for a plain-text description
draft. Later proposal parsing/selector binding and exact human confirmation
remain separate; an instruction to the model is not a security boundary.

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
that untrusted draft into the existing separately confirmed-description path.
Runtime source/material/provider remain synthetic ports; this is not paid-model
or real user/Cloud/Runtime end-to-end acceptance. Missing local PG DSN skips are
not database passes.

Real material/source/provider adapters, restricted draft consumer/automatic safe
format executor, multiround retained ancestry, both publication entry points'
complete trusted continuation and controlled rollout remain. Chinese marketplace
manuals and author-managed images are still after the repair/publish loop.

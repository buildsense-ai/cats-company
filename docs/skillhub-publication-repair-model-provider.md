# Restricted repair model HTTP adapter (internal, default off)

`publicationRepairModelProvider.invokeOnce` implements the coordinator's model
port using the existing CatsCo relay's Anthropic-compatible protocol. This is
an HTTP adapter, **not** the automatic repair controller or a new public tool.
There is no environment loader, Hub registration, production constructor call,
worker, route, paid test, or feature-gate change. Merely merging this code does
not send private Skills anywhere or charge a user.

## Trusted composition and original payer

A future trusted controller must supply independently configured HTTPS relay
and relay-admin origins, an admin secret, one exact provider/model/token config,
the real task store, retained report/material readers and Runtime verifier.
The only supported provider ID in this slice is `anthropic-relay`. It is a
transport profile, not an inferred model vendor. No default model is selected,
and switching an existing Bot's model configuration is unrelated to this port.

Only `runPublicationRepairModel` may grant a dispatch after its durable journal
fence and current task/lease/Runtime checks. The HTTP adapter checks the exact
request config, policy, system instruction, task identity and report/evidence,
but does not independently grant task authority or reconstruct it from source
text. Its package-private input must never be populated from a browser/model.

For that original human payer, it POSTs the existing
`/internal/users/<payer UID>/key/reveal` route on the pinned admin origin. It
requires a configured, active key with ID and a bounded nonempty credential.
It never creates/rotates a key, uses the Bot/new owner/platform account, falls
back to a service key, or caches keys. An unavailable/revoked/missing key stops
before the model request. The admin credential is never sent to the model
origin or serialized in configuration. Errors are static and disclose no
candidate, key, URL, provider reply or upstream error text.

The model request uses the resolved payer key as `x-api-key` on the separately
pinned `/anthropic/v1/messages` endpoint. This delegates quota enforcement and
usage accounting to the existing relay under that human's credential; it is
not proof that a particular production relay has billed correctly. No separate
local charge, refund or successful-billing assertion is introduced.

## One local attempt, bounded private input/output

Each adapter invocation has one combined 60-second deadline including key
resolution, shortened by the coordinator/parent deadline. At most two active
invocations per adapter are admitted, with slots held through response reading.
The owned transport has TLS verification, no proxy/cookie jar, no redirects,
compression, keepalive reuse, SDK retry, application retry or idempotency header.
Empty key-reveal POSTs use `http.NoBody` (zero length, no chunked framing);
nonempty model POSTs carry their exact content length. Both deliberately have
no `GetBody` replay callback. All HTTP failures,
429/5xx, incomplete replies, deadline/cancellation and malformed responses stop
without another key/model request in that invocation.

The adapter maps explicit fields rather than serializing the private internal
request. A fixed system instruction and one untrusted user-data envelope contain
the complete retained source projection and verified report. Payer, task/topic
IDs, receipt, leases and credentials are excluded from the model prompt. Only
model, max tokens, system, messages and `stream:false` are sent. No tools, tool
choice, arbitrary URLs/headers/options or instructions from the Skill can add
protocol fields. Untrusted text remains untrusted; prompting is not a sandbox.

Admin response bytes are capped at 16 KiB and model response bytes at 128 KiB.
Both require uncompressed JSON. Duplicate keys, ambiguous casing of consumed
keys, trailing JSON and invalid UTF-8 fail. A model reply must match the configured
model, `type:message`, `role:assistant` and `stop_reason:end_turn`, with exactly
one plain-text block. Tool/thinking/extra blocks, truncation, errors, unpaired
surrogates, NUL/control bytes, blank or >8192-byte drafts fail. Accepted text is
preserved byte-for-byte, not trimmed/extracted from JSON or Markdown, and still
requires the separate retained-draft consumer and explicit human confirmation.
There are no filesystem or publication calls in the adapter.

## Important rollout prerequisite: relay retries are separate

This client guarantees at most one local HTTP model attempt per coordinator
dispatch. It **cannot disable or attest** retry/fallback/routing behavior inside
an external relay or provider. There is intentionally no invented retry header
or boolean pretending to prove that deployment behavior. Before production
registration, verify an appropriate relay lane has no automatic paid replay or
cross-model fallback, preserves the payer binding, and supports this exact
request/response protocol. Until that is verified, do not describe this as an
end-to-end at-most-one provider execution/billing guarantee or enable it for
users. No live relay was contacted to test those properties in this slice.

The journal still grants at most one **dispatch authorization**. Losing a
provider reply leaves the outcome uncertain. Reconstructing the coordinator
and HTTP adapter does not permit redispatch: recover retained output or stop.
Losing an already-committed journal completion reply uses read-only recovery,
not a second model request. A provider failure is not proof that no charge
occurred, and another repair round is not an automatic retry mechanism.

## Verification and next boundary

`TestPublicationRepairModelProvider*` is included by the existing mandatory
`^TestPublicationRepairModel` race job. Tests use real HTTPS with a test CA for
key/model routes, not a real model or real credentials. They cover exact wire
mapping, original payer routing, separate credentials, strict config and response
checks, unsafe/tool output, redirects, lost provider reply, bounded concurrency,
deadlines, and reconstructed coordinator replay with retained/uncertain output.
The coordinator store in these tests is synthetic. The separately required
Runtime+Cloud/PostgreSQL contract remains unchanged; it still uses a synthetic
model invocation/journal. These are complementary tests, not a combined live
provider/Runtime/Cloud end-to-end acceptance.

Remaining: validate the deployment's no-retry/payer relay lane; compose real
store/source/material/provider ports in a controlled executor; implement safe
format repair, bounded multiround retained ancestry and both publication-entry
continuations; then controlled rollout and actual user acceptance. Chinese
marketplace manuals and optional author-managed images remain after that loop.

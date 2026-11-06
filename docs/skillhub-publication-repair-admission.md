# Human publication repair admission

This adds trustworthy admission to the existing three-round repair ledger. It is
not an execution endpoint. The linked repair-conversation browser handoff below
now consumes its safe read and explicit registration contracts.
No model, candidate write, ordinary file/shell/Skill tool, system prompt or
publication authorization changes in this step.

## Default off

Both `CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED` and
`CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED` must be explicitly enabled.
The latter defaults to false. Existing publication peer settings and PostgreSQL
are required; missing support fails closed. This change does not enable flags in
production or change Bot deployments.

The authenticated human commit handler accepts:

- `POST /api/skillhub/publications/commit/entries/{entry}/repair-task` with exactly
  `{"schemaVersion":1,"topicId":"grp_123"}`.
- `GET` on the same path to recover the existing task's safe projection, with no
  body or query. This is a read only: no new task, Cloud call or budget spending.

Bearer authentication uses the existing active-human JWT checks; Bot tokens,
cookie-only requests, unknown/duplicate fields, noncanonical topics, extra
evidence, queries and encoded bodies are refused. Existing per-human/per-entry
rate limits, concurrency slots, response privacy and timeouts apply.

## Trust and atomicity

POST reads the original publisher's current workspace publication context and
retained receipt, calls only the pinned Cloud report API, validates the exact
receipt and candidate, and rejects passed or truncated reports. It rechecks the
entry identity/source/receipt/authority after peer I/O. No database locks are
held across the network call. The browser cannot submit candidate/report/issue
hashes, repair policy, a worker identity, a lease or model output.

The server computes a digest of the exact verified report and a separate sorted,
deduplicated issue identity digest. Issue identities contain code, category,
blocking status, bounded location and repair policy, not report IDs or display
messages. Cosmetic report changes cannot create apparent repair progress.

In the create transaction, PostgreSQL locks the publication entry, rechecks its
original active human, workspace entrypoint, Bot owner/body/enabled state, exact
receipt, cancellation and commit intent. It also checks the canonical group
topic, group owner and `agent_task` kind, topic owner/type, and exact membership
(the original human in the owner role and the original Bot, nobody else).
The group is locked `FOR UPDATE`, blocking new FK-backed member inserts; current
member rows and the topic are locked to fence removals and edits. These checks
also run for claim/reclaim/replay and finish, not just initial task creation.
Concurrent group/entry operations may time out and fail closed; there is no
unlocked fallback.

An identical admission returns the retained task without spending a round.
Changing topic or report evidence conflicts, including after a lost response;
GET recovers the original association. There is no rebind/reset operation. A
later task membership change invalidates execution writes; it does not delete
the original human's private ledger history or refund spent rounds.

## User-visible projection and remaining work

Replies expose only schema/entry/Bot/topic/task identifiers, state, rounds used,
the three-round limit and `automaticRepairEnabled:false`. No internal receipt,
report/candidate hashes, lease token, worker identity or report is serialized by
this endpoint. GET requires current publication authority; lower-level private
ledger history remains readable by its original active publisher after transfer
or cancellation, but that is not execution authority.

Admission alone does not approve a description, make a format issue automatic,
spend a round, start a worker, call a paid model or publish anything.
[Exact human draft confirmation](skillhub-publication-description-confirmation.md)
is separately retained; it still does not execute or resume a repair. Restricted
execution and browser/original-chat continuation remain subsequent work. The
internal [model-call journal](skillhub-publication-repair-model-call.md) retains
one immutable dispatch fence and optional private text result per round, but
does not register or invoke an executor/provider. The runtime must
still recheck the precise retained source before any repair; this API's stored
report is not permission to read or edit live files.

## Verification

### Linked repair-conversation registration

The existing same-Bot repair-conversation report dialog can explicitly register
that conversation with this ledger. It first reads schema-v1 capabilities and
requires `repairTaskAdmissionUiEnabled: true`, independently default-off via
`CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_UI_ENABLED`. Discovery also requires
reads, human-commit and repair-admission gates plus entry, repair, commit-context
and commit-store adapters. This capability is only discovery; it does not replace
the human endpoint's independent gates, peer readiness or live authority checks.
Production configuration is unchanged. No executor, task event or model call is
registered by enabling this UI.

The conversation panel checks group ID/kind/owner and exactly the original human
and one original Bot before handing disposable Bot/topic references to the
shared report dialog. The server remains authoritative: references, membership
read by the browser and displayed reports grant no repair or file access. The
workspace report without an already-linked conversation shows no registration
section and issues no repair-task request; its existing new-conversation action
is unchanged. The browser never creates/disbands a group as part of registration.

After explicit capability opt-in, GET recovers the safe retained task. Only the
exact `404 publication.repair_not_found` permits showing a registration button;
generic 404, authority errors and transport/shape failures remain uncertain.
The button sends only schemaVersion/topicId to the existing POST after a human
click, with a synchronous double-click latch. No candidate/report/issue digests,
text, worker ID, lease or generic automatic-repair assertion is submitted.

Strict projection binds entry/Bot and validates task/topic identifiers, allowed
ledger states, coherent 0..3 spent rounds and the exact three-round limit with
`automaticRepairEnabled:false`. Existing tasks preserve their task ID, state and
spent rounds. A task retained for another conversation is shown as already bound;
there is no rebind, reset or additional POST action. A POST replay can return an
existing task with spent rounds; the UI does not treat this as a fresh zero-round
task. A passed validation checkpoint is not publication success; a running ledger
checkpoint is not proof that an executor is still alive.

Uncertain POST results expose only explicit GET recovery, never automatic retry,
fallback or replacement task creation. A lost committed reply can therefore
recover the same task without resetting the budget. Errors use static text, not
raw upstream details. No polling or new browser persistence is added. GET plus
capability/JSON consumption has a 15-second deadline and seven-second header
timeouts; explicit POST has a 60-second header and 65-second whole-JSON deadline,
allowing the existing handler's 55-second Cloud-report budget. Auth, entry, Bot,
topic changes and unmount abort/discard late responses. Browser cancellation
does not promise to undo a committed server-side registration; GET is recovery.

Unit tests cover all discovery-gate combinations, strict scope/state projection,
explicit click/double-click, lost-response GET recovery, existing rounds, other
topic binding, errors, deadlines and lifecycle races. Integration with the real
report-dialog and conversation panel preserves the original rejection report.
Local Chromium checks load real dialog/CSS with synthetic ports and block real
API/model requests. They are not production or paid-model E2E acceptance. The
existing mandatory JWT/HTTPS/PostgreSQL contract below verifies actual admission
commit, response loss, reconstruction and zero-round recovery.

Unit tests cover human authentication, strict request decoding, default-off and
unsupported stores, rate/concurrency limits, private projection, report binding,
post-network authority changes, normalized issue hashing and exact replay.
Real PostgreSQL tests cover invalid/changed task ownership/type/membership/topic,
no partial task on rejection, retained budgets and row/FK locking of concurrent
member inserts/removals and kind changes. The existing required JWT/HTTPS/PG
contract verifies admission survives a lost response and reconstructed handler,
replays the same zero-round task, and rejects rebinding and changed task kind.
Its Cloud rejection is explicitly synthetic, not full live repair acceptance.

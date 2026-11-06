# Exact description draft confirmation

This retains one immutable description proposal and exact human confirmation
for a paused repair round. It is not an executor, model call or file writer.
The shared report-dialog UI below consumes this existing contract. No executor,
ordinary model-tool permission or prompt changes are included.

## Default-off human contract

The existing human-commit and repair-admission switches must both be enabled:
`CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED` and
`CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED`. Missing PostgreSQL/store
support fails closed. No new production switch is enabled by the migration.

- `GET /api/skillhub/publications/commit/entries/{entry}/repair-description`
  has no body/query and returns the stored draft without generating or approving
  anything. It does not read Cloud or contact a model.
- `POST` on that path takes exactly
  `{"schemaVersion":1,"confirmationKey":"<64 lowercase hex characters>"}`.
  It confirms only the retained proposal named by the key. It cannot submit new
  text, hashes, selectors, a publisher or a generic `confirmed:true` assertion.

Both require the original active human's Bearer JWT and current Bot
owner/body/enabled, uncancelled workspace entry, no publication intent and the
private same-Bot task's current ownership/type/topic/exact two-member authority.
Bot tokens, cookie-only requests, friends and later Bot owners cannot approve.
The ordinary human handler's strict decoding, limits, deadlines, no-store
privacy and static errors apply. See the
[main route reference](skillhub-publication-entries.md#human-publication-confirmation).

## Trusted proposal and exact binding

There is no HTTP/RPC/model proposal endpoint or registered producer. The future
executor must read the pinned peer report and precisely revalidate source before
calling the internal verified-report adapter. It requires a complete rejection
whose exact and normalized evidence matches the claimed round. The selected
issue must be blocking `metadata.description_missing`, category `format`, with
`confirmation_required` and the exact supported selector. Other blocking
functional/identity/compatibility issues and any package-safety issue stop it.
Report prose never becomes executable instructions or approval.

Only `description` in `SKILL.md` (with exact zero-based entry index) or
`skill.json` (without an entry index) is supported. Missing/ambiguous selectors,
README descriptions and arbitrary paths fail closed. The selector identifies
an immutable candidate entry, not an operating-system path. Text is bounded to
8 KiB of valid UTF-8 and must be nonblank; unsafe control characters are refused.
The original text is retained verbatim: no trimming, translation, truncation or
line-ending normalization occurs after confirmation.

The confirmation key binds task identity (including original actor/Bot/topic/
receipt), task ID, round number and immutable round-input digest, exact candidate/
report/normalized-issue evidence, selector and exact proposed text. It is a
content binding, not a bearer token or authority by itself. Changing any bound
fact invalidates approval. The public projection exposes schema/entry/Bot/topic/
task identifiers, round, safe selector, text, confirmation key, draft state and
`automaticRepairEnabled:false`; no receipt, internal digest, lease or worker is
projected. Clients must render draft text as untrusted text, not HTML.

## Atomicity, replay and stopping

Migration 000052 and mirrored startup DDL add append-only drafts and
confirmations. Proposing rechecks current authority and the exact current lease,
then retains the draft and `waiting_confirmation` checkpoint in one transaction.
Stale/reclaimed/expired workers cannot replace it. Different text/selectors
conflict rather than overwriting an awaiting or confirmed proposal.

Reading and confirming also lock/recheck current entry and private task
authority. Confirmation is a separate immutable row by the original actor.
Group/FK/member locks fence concurrent membership changes; the same entry lock
serializes cancellation. Identical proposals and confirmations are idempotent
across lost responses, adapter restart and schema startup. Nonempty down
migration refuses to erase either kind of evidence; empty rollback removes child
tables first. Database guards reject replacement/deletion and forged actor/key/
lease/round/task bindings.

Confirmation does **not** unlock `ClaimSkillHubPublicationRepairRound`, reset the
three-round budget, spend a fourth round, change the round's immutable result,
repeat an uncertain paid call, modify a live Skill or authorize publication.
The task still has its one `waiting_confirmation` checkpoint, while the draft
projection independently says `confirmed`. Future restricted application must
consume the exact retained approval, revalidate live source/current authority,
write only a private candidate copy and checkpoint its outcome without replaying
model work. The separate [description application contract](skillhub-publication-description-application.md)
now retains a durable application identity/result and an internal coordinator;
production derivation/retention adapters and resumption, durable model-call
reconciliation and browser/original-chat continuation remain unimplemented.

## Verification

### Shared browser confirmation

The workspace report and linked repair-conversation report use the same exact
draft component. It requires explicit schema-v1 capability
`descriptionConfirmationUiEnabled: true` before reading. Discovery requires
reads, human-commit, repair-admission and the independent default-off
`CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_CONFIRMATION_UI_ENABLED`, plus entry,
repair and description store support. The capability is discovery only: the
human endpoint still independently rechecks its gates, peer readiness and current
original-human/private-task authority for every GET/POST. This UI flag does not
create drafts, admit a repair task, enable a model call or authorize publishing.

Draft responses are strictly projected and bound to the entry, Bot, private
topic, task, round, exact selector, exact UTF-8 text and confirmation key. Text is
rendered verbatim as untrusted plain text, never Markdown/HTML, without trimming
or translation. Only a human click submits the existing exact-key POST. There
is no `confirmed:true`, text/path/hash/receipt submission, automatic approval,
file write, repair continuation, release intent, commit or legacy fallback.

A synchronous click latch prevents duplicate concurrent submissions. A failed,
timed-out or mismatched POST is uncertain, not success or definite failure. The
draft action is removed and only explicit GET recovery is offered; a committed
approval with a lost reply can then be recovered without resubmitting. Capability
and read failures also show static retry text; explicit disabled/older capability
omits the section. Missing task/draft returns a no-draft message. No background
polling or browser persistence is added. Header requests are bounded to seven
seconds, and the complete read/confirmation including JSON has a 15-second
deadline. Entry/auth changes and unmount abort and discard late responses.

Browser unit tests cover strict shape/byte bounds, safe verbatim rendering,
explicit approval, double clicks, lost-response GET recovery, substituted
responses, default-off discovery, errors/deadlines and identity/lifecycle races.
Shared-dialog integration covers linked repair conversations; local Chromium
checks use the real dialog/CSS with synthetic APIs and blocked real network IO.
Neither is real Cloud/Runtime/paid-model production E2E acceptance.

Unit tests cover selectors/text bounds, exact text/line-ending/evidence binding,
JWT/default-off/strict transport, private projection, changed authority and
verified-report policy. Real PostgreSQL tests cover retained restart/replay,
wrong actor/key, fenced workers, draft/checkpoint atomic failure, concurrent
confirmation, authority mutations, immutable SQL guards and migration lifecycle.
The existing required JWT/HTTPS/PostgreSQL contract now records a synthetic
verified draft, loses the HTTP response after real approval commit, reconstructs
the handler and recovers the same approval and budget through GET/POST. Model
text/report are synthetic; no real model/file repair E2E is claimed. Tests that
lack `CATS_PG_TEST_DSN` skip locally and require actual CI execution as evidence.

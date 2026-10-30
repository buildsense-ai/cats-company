# Private repair model-call journal

This internal PostgreSQL journal is the dispatch/recovery prerequisite for the
restricted publication repair executor. It adds no model provider, executor,
HTTP route, RPC, browser control, automatic invocation or new production gate.
Existing production gates remain unchanged. An admitted repair task still does
not start automatic repair.

## At-most-one dispatch authorization

After claiming one of the existing three repair rounds, a trusted executor may
call `StartSkillHubPublicationRepairModelCall`. It must compute `RequestHash`
over the **complete immutable request**, including original publisher/payer,
provider/model/settings, repair-policy version, exact prompt, retained private
candidate and verified report. Credentials must never be included in this record.
This API does not verify that request hash against a provider; the future
executor owns that binding. Browser/model input is not trusted evidence.

The entry lock serializes task authority, cancellation and concurrent starts.
The store checks current original active human, Bot owner/body/enabled state,
exact private same-Bot task/topic/membership and the current unfinished live
repair lease. It commits one immutable dispatch fence per entry/round. Only the
first successful call returns `dispatch=true`; every replay returns false.
An error, cancellation or lost response grants no dispatch permission. Only a
caller actually receiving true may attempt one provider request, after current
Runtime source/authority checks. It must disable provider/transport automatic
retries that could issue another paid request. No database lock is held across
the provider request, and the journal is not distributed atomic authorization.

Crashing after the fence but before sending is intentionally conservative:
recovery cannot prove no charge occurred. An empty output is **unknown**, not
"not sent". Recovery reads a saved output, or stops `model_result_uncertain`.
It never obtains a fresh dispatch grant by replay, restarting, changing request
hash, taking over an expired lease or selecting another worker. This provides
at-most-one dispatch authorization, not exactly-once provider execution/billing.

## Private result recovery and fencing

The original dispatch lease can retain one bounded UTF-8 text result (64 KiB)
while it is still the current live unfinished round. This is **untrusted text**,
not an executable patch/tool call, approved runtime description or safe format
change. Completion retains its SHA-256 and timestamp. Exact completion replay
recovers the same output; changing output conflicts. Ordinary JSON marshaling
does not serialize the private model text.

Expired or replaced workers cannot inject late output. The original dispatch
lease is immutable even after round takeover. A replacement worker may recover
already-retained output but may not complete the original dispatch or send a
new request. Its later consumer must independently recheck current task/source
authority, request binding and repair policy. Model output still cannot modify
runtime `description` without the separate exact human confirmation.

The read-only history method is available only to the original active human,
including after cancellation/transfer. New Bot owners, the Bot itself and other
users cannot read the text. History reads are not execution permission. There
is no public read adapter, prompt insertion or logging of private output.

Migration 000066 and schema startup create the same journal. Database guards
refuse dispatch replacement/deletion, output reset, stale lease writes, missing
dispatch completion, oversize/partial results and round/authority mismatches.
Down migration refuses a nonempty journal. It neither spends/resets another
repair round nor changes task state, original Skill files or publication facts.

## Verification and remaining work

Store tests cover input/output validation and private-text serialization.
PostgreSQL tests cover committed-response loss and reconstruction, one winner
among concurrent starts, output replay, expired/taken-over leases, authority
loss, privacy and direct SQL guards. Required CI runs these against PostgreSQL
with `-race -v`; a local missing-DSN skip is not database evidence.

The internal [restricted model coordinator](skillhub-publication-repair-model-execution.md)
now consumes this journal with current execution/source checks, strict request
binding and read-only completion recovery. It registers no real provider or
worker and does not apply the output. The real executor/provider adapter, private candidate ancestry across
multiple rounds, trusted continuation for both publication entry points and
controlled end-to-end rollout remain subsequent work. This journal itself never
calls or bills a model. Chinese marketplace manuals and author-managed images
remain after the publishing-repair loop.
